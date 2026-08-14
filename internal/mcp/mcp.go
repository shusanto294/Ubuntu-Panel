// Package mcp is the panel's Model Context Protocol server: the JSON-RPC
// machinery an AI assistant talks to, and the tool registry the web package
// fills with what the panel can do.
//
// The transport is Streamable HTTP — one POST per request, answered with one
// JSON body. There is no SSE stream and no session id: every tool call the
// panel offers either finishes in milliseconds or hands the work to the queue
// and returns a task to poll, so there is nothing for a long-lived stream to
// carry, and a stateless endpoint is one that survives the panel restarting
// under an assistant that is halfway through a deployment.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
)

// Protocol is the revision this server speaks when a client does not ask for
// one it knows.
const Protocol = "2025-06-18"

// supported is every revision this server answers for, newest first. The
// differences between them are in parts of the protocol the panel does not use
// — sampling, roots, SSE resumption — so a client asking for an older one is
// told yes rather than being made to upgrade.
var supported = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// maxBody caps a request. A tool call is a small JSON object; an unbounded read
// is how a crafted body becomes the panel's memory usage.
const maxBody = 4 << 20 // 4 MiB — a file written through write_file lives here.

// JSON-RPC error codes, from the specification.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Args is one tool call's arguments, read through accessors so a tool never has
// to care whether a number arrived as a number or as a string — assistants send
// both, and a site id typed as "12" is not a reason to fail a deployment.
type Args map[string]any

// String is a trimmed string field.
func (a Args) String(key string) string {
	switch value := a[key].(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return trimFloat(value)
	default:
		return ""
	}
}

// StringOr falls back when the field is missing or blank.
func (a Args) StringOr(key, fallback string) string {
	if value := a.String(key); value != "" {
		return value
	}

	return fallback
}

// Int is a whole number, or zero.
func (a Args) Int(key string) int64 {
	switch value := a[key].(type) {
	case float64:
		return int64(value)
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)

		return parsed
	default:
		return 0
	}
}

// Bool reads a flag, falling back when it was not sent.
func (a Args) Bool(key string, fallback bool) bool {
	switch value := a[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	case float64:
		return value != 0
	}

	return fallback
}

// List is a repeated field. A single string is one item rather than an error:
// an assistant asked for one alias sends one alias.
func (a Args) List(key string) []string {
	switch value := a[key].(type) {
	case []any:
		list := make([]string, 0, len(value))

		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				list = append(list, strings.TrimSpace(text))
			}
		}

		return list
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}

		return []string{strings.TrimSpace(value)}
	}

	return nil
}

// Has reports whether the field was sent at all, which is how a tool tells "set
// it to false" from "leave it alone".
func (a Args) Has(key string) bool {
	value, ok := a[key]

	return ok && value != nil
}

func trimFloat(value float64) string {
	if value == float64(int64(value)) {
		return strconv.FormatInt(int64(value), 10)
	}

	return strconv.FormatFloat(value, 'g', -1, 64)
}

// Property is one field of a tool's input schema. The schema is what the
// assistant reads instead of documentation, so every field carries a sentence
// saying what it is for.
type Property struct {
	Type        string // string | integer | boolean | array | object
	Description string
	Enum        []string
	Items       string // element type, for an array
	Default     any
}

func (p Property) schema() map[string]any {
	kind := p.Type
	if kind == "" {
		kind = "string"
	}

	schema := map[string]any{"type": kind, "description": p.Description}

	if len(p.Enum) > 0 {
		schema["enum"] = p.Enum
	}

	if kind == "array" {
		items := p.Items
		if items == "" {
			items = "string"
		}

		schema["items"] = map[string]any{"type": items}
	}

	if p.Default != nil {
		schema["default"] = p.Default
	}

	return schema
}

// Tool is one thing the panel can be asked to do.
type Tool struct {
	Name        string
	Title       string
	Description string
	Properties  map[string]Property
	Required    []string

	// ReadOnly and Destructive are the hints a client shows before it runs
	// something, and what an assistant configured to ask first asks about.
	ReadOnly    bool
	Destructive bool

	// Capability is what this tool needs permission for, in whatever names the
	// caller uses. The registry does nothing with it beyond handing it to Allow:
	// which capabilities exist, and who has them, is the panel's business.
	Capability string

	// Run does the work. Returning an error is an ordinary outcome — "no such
	// site", "MariaDB is not installed" — and is handed back as a failed tool
	// result the assistant can read and act on, not as a protocol error.
	Run func(ctx context.Context, args Args) (any, error)
}

func (t *Tool) describe() map[string]any {
	properties := map[string]any{}

	for name, property := range t.Properties {
		properties[name] = property.schema()
	}

	required := t.Required
	if required == nil {
		required = []string{}
	}

	return map[string]any{
		"name":        t.Name,
		"title":       t.Title,
		"description": t.Description,
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		},
		"annotations": map[string]any{
			"title":           t.Title,
			"readOnlyHint":    t.ReadOnly,
			"destructiveHint": t.Destructive,
		},
	}
}

// Server holds the tool registry and answers the protocol.
type Server struct {
	Name         string
	Version      string
	Instructions string
	Log          *slog.Logger

	// Allow, when set, decides whether a tool is offered and whether a call to
	// one runs. Both, rather than either: a client lists the tools once and
	// keeps them, so a permission taken away after that is only real if the
	// call is refused too.
	Allow func(ctx context.Context, tool *Tool) bool

	// NameFor, when set, is asked for the name to introduce this server by.
	// Name is the fallback. It is a function because the name is the operator's
	// to change — somebody with three of these connected needs to be able to
	// tell which is which — and a client reads it once, on connection.
	NameFor func(ctx context.Context) string

	tools []*Tool
	index map[string]*Tool
}

// allowed is Allow with the default: everything, for a server whose caller has
// no permissions to apply.
func (s *Server) allowed(ctx context.Context, tool *Tool) bool {
	return s.Allow == nil || s.Allow(ctx, tool)
}

// NewServer prepares an empty server. Instructions are handed to the assistant
// on connection: a short paragraph about what this server is in front of, which
// is what stops it guessing.
func NewServer(name, version, instructions string, log *slog.Logger) *Server {
	return &Server{
		Name:         name,
		Version:      version,
		Instructions: instructions,
		Log:          log,
		index:        map[string]*Tool{},
	}
}

// Add registers tools. A duplicate name replaces the earlier one, so a caller
// building the list in pieces cannot end up with two.
func (s *Server) Add(tools ...*Tool) {
	for _, tool := range tools {
		if tool == nil || tool.Name == "" {
			continue
		}

		if _, seen := s.index[tool.Name]; !seen {
			s.tools = append(s.tools, tool)
		} else {
			for i, existing := range s.tools {
				if existing.Name == tool.Name {
					s.tools[i] = tool
				}
			}
		}

		s.index[tool.Name] = tool
	}

	sort.Slice(s.tools, func(a, b int) bool { return s.tools[a].Name < s.tools[b].Name })
}

// request is one JSON-RPC call. A request with no id is a notification, which
// is answered with no body at all.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (r request) isNotification() bool { return len(r.ID) == 0 || string(r.ID) == "null" }

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func fail(code int, message string) *rpcError { return &rpcError{Code: code, Message: message} }

// ServeHTTP answers the Streamable HTTP transport.
//
// POST is the whole of it. A GET would open the server-to-client stream, which
// this server does not have; the specification's answer for that is 405, and a
// client that sees one stops asking.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		// Ending a session there is nothing to end.
		w.WriteHeader(http.StatusNoContent)

		return
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "This MCP endpoint only accepts POST.", http.StatusMethodNotAllowed)

		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		s.write(w, response(nil, nil, fail(codeParse, "the request body could not be read")))

		return
	}

	trimmed := strings.TrimSpace(string(body))

	// A batch, which the older revisions allow. Notifications inside it
	// contribute nothing to the answer, and a batch that was all notifications
	// is answered with no body.
	if strings.HasPrefix(trimmed, "[") {
		var batch []request

		if err := json.Unmarshal([]byte(trimmed), &batch); err != nil {
			s.write(w, response(nil, nil, fail(codeParse, "the request body was not valid JSON")))

			return
		}

		answers := []any{}

		for _, call := range batch {
			if call.isNotification() {
				s.dispatch(r.Context(), call)

				continue
			}

			result, failure := s.dispatch(r.Context(), call)
			answers = append(answers, response(call.ID, result, failure))
		}

		if len(answers) == 0 {
			w.WriteHeader(http.StatusAccepted)

			return
		}

		s.write(w, answers)

		return
	}

	var call request

	if err := json.Unmarshal([]byte(trimmed), &call); err != nil {
		s.write(w, response(nil, nil, fail(codeParse, "the request body was not valid JSON")))

		return
	}

	if call.Method == "" {
		s.write(w, response(call.ID, nil, fail(codeInvalidRequest, "the request named no method")))

		return
	}

	result, failure := s.dispatch(r.Context(), call)

	// A notification is acknowledged and nothing more: answering one with a
	// body is what makes a strict client close the connection.
	if call.isNotification() {
		w.WriteHeader(http.StatusAccepted)

		return
	}

	s.write(w, response(call.ID, result, failure))
}

func (s *Server) dispatch(ctx context.Context, call request) (any, *rpcError) {
	switch call.Method {
	case "initialize":
		return s.initialize(ctx, call.Params), nil

	case "ping":
		return map[string]any{}, nil

	case "tools/list":
		tools := make([]map[string]any, 0, len(s.tools))

		for _, tool := range s.tools {
			if !s.allowed(ctx, tool) {
				continue
			}

			tools = append(tools, tool.describe())
		}

		return map[string]any{"tools": tools}, nil

	case "tools/call":
		return s.call(ctx, call.Params)

	// Nothing is served under either, but a client that asks should be told so
	// rather than shown a protocol error it has to explain to somebody.
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil

	// Notifications. Nothing to do, and no answer to give.
	case "notifications/initialized", "notifications/cancelled", "notifications/progress":
		return map[string]any{}, nil

	default:
		return nil, fail(codeMethodNotFound, "this server does not implement "+call.Method)
	}
}

func (s *Server) initialize(ctx context.Context, params json.RawMessage) map[string]any {
	var asked struct {
		ProtocolVersion string `json:"protocolVersion"`
	}

	if len(params) > 0 {
		json.Unmarshal(params, &asked)
	}

	version := Protocol

	for _, candidate := range supported {
		if candidate == asked.ProtocolVersion {
			version = candidate

			break
		}
	}

	name := s.Name

	if s.NameFor != nil {
		if chosen := strings.TrimSpace(s.NameFor(ctx)); chosen != "" {
			name = chosen
		}
	}

	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			// listChanged is false: the tool list is compiled in, so it cannot
			// change while a client is connected.
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    name,
			"title":   name,
			"version": s.Version,
		},
		"instructions": s.Instructions,
	}
}

func (s *Server) call(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var asked struct {
		Name      string `json:"name"`
		Arguments Args   `json:"arguments"`
	}

	if len(params) > 0 {
		if err := json.Unmarshal(params, &asked); err != nil {
			return nil, fail(codeInvalidParams, "the tool call could not be read")
		}
	}

	tool, ok := s.index[asked.Name]
	if !ok {
		return nil, fail(codeInvalidParams, "no such tool: "+asked.Name)
	}

	if asked.Arguments == nil {
		asked.Arguments = Args{}
	}

	// A refused tool is a failed result rather than a protocol error, for the
	// same reason a refused argument is: the assistant is meant to read why and
	// tell the person, not to report that the server broke.
	if !s.allowed(ctx, tool) {
		return toolResult("The panel does not allow "+tool.Name+" right now. Whoever owns it can "+
			"turn that on under AI access, in the list of what an assistant is allowed to do.",
			true), nil
	}

	result, err := s.run(ctx, tool, asked.Arguments)

	// A tool that could not do what it was asked answers as a failed result
	// rather than as a protocol error: the assistant is meant to read the
	// reason and try something else, and a JSON-RPC error is not shown to it.
	if err != nil {
		return toolResult(err.Error(), true), nil
	}

	return toolResult(encode(result), false), nil
}

// run calls a tool with a net under it. A panic in one tool must not take the
// panel down: the assistant is told the call failed, and the log has the stack.
func (s *Server) run(ctx context.Context, tool *Tool, args Args) (result any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if s.Log != nil {
				s.Log.Error("an MCP tool panicked",
					"tool", tool.Name, "panic", recovered, "stack", string(debug.Stack()))
			}

			result, err = nil, fmt.Errorf("the panel hit an unexpected error running %s", tool.Name)
		}
	}()

	if s.Log != nil {
		s.Log.Info("mcp tool", "tool", tool.Name)
	}

	return tool.Run(ctx, args)
}

// toolResult is the content block shape every client understands: one text
// part, holding JSON. Structured content is deliberately not sent — it is only
// valid alongside an output schema, and a tool whose result is JSON in a text
// block is read correctly by every client there is.
func toolResult(text string, failed bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": failed,
	}
}

func encode(value any) string {
	if value == nil {
		return "done"
	}

	if text, ok := value.(string); ok {
		return text
	}

	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", value)
	}

	return string(encoded)
}

func response(id json.RawMessage, result any, failure *rpcError) map[string]any {
	body := map[string]any{"jsonrpc": "2.0"}

	if len(id) > 0 {
		body["id"] = id
	} else {
		body["id"] = nil
	}

	if failure != nil {
		body["error"] = failure

		return body
	}

	if result == nil {
		result = map[string]any{}
	}

	body["result"] = result

	return body
}

func (s *Server) write(w http.ResponseWriter, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		http.Error(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"could not encode the response"}}`,
			http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(encoded)
}
