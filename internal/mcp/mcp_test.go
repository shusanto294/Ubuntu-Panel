package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/mcp"
)

// server is a registry with one tool of each shape: one that works, and one
// that refuses.
func server(t *testing.T) *mcp.Server {
	t.Helper()

	s := mcp.NewServer("Test Panel", "1.2.3", "Instructions.",
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	s.Add(
		&mcp.Tool{
			Name:     "echo",
			Title:    "Echo",
			ReadOnly: true,
			Properties: map[string]mcp.Property{
				"text":  {Description: "What to say back."},
				"times": {Type: "integer", Description: "How often."},
			},
			Required: []string{"text"},
			Run: func(ctx context.Context, args mcp.Args) (any, error) {
				return map[string]any{
					"text":  args.String("text"),
					"times": args.Int("times"),
					"loud":  args.Bool("loud", false),
				}, nil
			},
		},
		&mcp.Tool{
			Name: "refuse",
			Run: func(ctx context.Context, args mcp.Args) (any, error) {
				return nil, errors.New("not on this machine")
			},
		},
		&mcp.Tool{
			Name: "explode",
			Run: func(ctx context.Context, args mcp.Args) (any, error) {
				panic("a tool that could not cope")
			},
		},
	)

	return s
}

// call sends one JSON-RPC body and reads the answer back.
func call(t *testing.T, s *mcp.Server, body string) (int, map[string]any) {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	s.ServeHTTP(recorder, request)

	answer := map[string]any{}

	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatalf("the answer was not a JSON object: %v\n%s", err, recorder.Body.String())
		}
	}

	return recorder.Code, answer
}

// result digs the tool result out of a JSON-RPC answer.
func result(t *testing.T, answer map[string]any) map[string]any {
	t.Helper()

	value, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("the answer carried no result: %v", answer)
	}

	return value
}

// text is the one content block every tool result carries.
func text(t *testing.T, tool map[string]any) string {
	t.Helper()

	content, ok := tool["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("the tool result had no content: %v", tool)
	}

	block, _ := content[0].(map[string]any)
	value, _ := block["text"].(string)

	return value
}

func TestInitializeAnswersTheVersionTheClientAsked(t *testing.T) {
	s := server(t)

	status, answer := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2024-11-05","capabilities":{}}}`)

	if status != http.StatusOK {
		t.Fatalf("initialize answered %d", status)
	}

	body := result(t, answer)

	if body["protocolVersion"] != "2024-11-05" {
		t.Errorf("the negotiated version is %v; the client asked for 2024-11-05", body["protocolVersion"])
	}

	info, _ := body["serverInfo"].(map[string]any)

	if info["name"] != "Test Panel" || info["version"] != "1.2.3" {
		t.Errorf("the server named itself %v", info)
	}

	if _, ok := body["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("the server did not advertise tools")
	}
}

func TestInitializeFallsBackToTheCurrentVersion(t *testing.T) {
	s := server(t)

	_, answer := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"1999-01-01"}}`)

	if got := result(t, answer)["protocolVersion"]; got != mcp.Protocol {
		t.Errorf("an unknown version negotiated to %v; it should be %s", got, mcp.Protocol)
	}
}

func TestToolsListDescribesEverySchema(t *testing.T) {
	s := server(t)

	_, answer := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	tools, _ := result(t, answer)["tools"].([]any)

	if len(tools) != 3 {
		t.Fatalf("the list has %d tools; three were registered", len(tools))
	}

	first, _ := tools[0].(map[string]any)

	// Sorted by name, so `echo` comes first.
	if first["name"] != "echo" {
		t.Fatalf("the list is not sorted by name: %v", first["name"])
	}

	schema, _ := first["inputSchema"].(map[string]any)

	if schema["type"] != "object" {
		t.Errorf("the input schema is not an object: %v", schema)
	}

	properties, _ := schema["properties"].(map[string]any)
	times, _ := properties["times"].(map[string]any)

	if times["type"] != "integer" {
		t.Errorf("`times` is typed %v; it is an integer", times["type"])
	}

	required, _ := schema["required"].([]any)

	if len(required) != 1 || required[0] != "text" {
		t.Errorf("the required list is %v; it should name text alone", required)
	}
}

func TestToolCallReturnsItsResultAsJSONText(t *testing.T) {
	s := server(t)

	_, answer := call(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call",
		"params":{"name":"echo","arguments":{"text":"hello","times":"3","loud":true}}}`)

	tool := result(t, answer)

	if tool["isError"] != false {
		t.Errorf("a tool that worked was reported as an error: %v", tool)
	}

	decoded := map[string]any{}

	if err := json.Unmarshal([]byte(text(t, tool)), &decoded); err != nil {
		t.Fatalf("the content block was not JSON: %v", err)
	}

	// A number sent as a string is still a number: assistants send both.
	if decoded["times"] != float64(3) {
		t.Errorf(`"3" read back as %v`, decoded["times"])
	}

	if decoded["loud"] != true || decoded["text"] != "hello" {
		t.Errorf("the arguments did not survive the call: %v", decoded)
	}
}

func TestAToolThatRefusesIsAFailedResultNotAProtocolError(t *testing.T) {
	s := server(t)

	_, answer := call(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call",
		"params":{"name":"refuse","arguments":{}}}`)

	if _, ok := answer["error"]; ok {
		t.Fatalf("a refusal came back as a protocol error: %v", answer)
	}

	tool := result(t, answer)

	if tool["isError"] != true {
		t.Errorf("the failure was not marked: %v", tool)
	}

	if !strings.Contains(text(t, tool), "not on this machine") {
		t.Errorf("the reason was lost: %q", text(t, tool))
	}
}

func TestAToolThatPanicsDoesNotTakeTheServerWithIt(t *testing.T) {
	s := server(t)

	_, answer := call(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call",
		"params":{"name":"explode","arguments":{}}}`)

	tool := result(t, answer)

	if tool["isError"] != true {
		t.Errorf("the panic was not reported as a failure: %v", tool)
	}
}

func TestAnUnknownToolIsInvalidParams(t *testing.T) {
	s := server(t)

	_, answer := call(t, s, `{"jsonrpc":"2.0","id":6,"method":"tools/call",
		"params":{"name":"nothing","arguments":{}}}`)

	failure, ok := answer["error"].(map[string]any)
	if !ok {
		t.Fatalf("an unknown tool was accepted: %v", answer)
	}

	if failure["code"] != float64(-32602) {
		t.Errorf("the error code is %v; it should be -32602", failure["code"])
	}
}

func TestAnUnknownMethodIsMethodNotFound(t *testing.T) {
	s := server(t)

	_, answer := call(t, s, `{"jsonrpc":"2.0","id":7,"method":"sampling/createMessage"}`)

	failure, _ := answer["error"].(map[string]any)

	if failure == nil || failure["code"] != float64(-32601) {
		t.Errorf("an unimplemented method answered %v", answer)
	}
}

func TestANotificationIsAcknowledgedWithNoBody(t *testing.T) {
	s := server(t)

	status, answer := call(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	if status != http.StatusAccepted {
		t.Errorf("a notification answered %d; it should be 202", status)
	}

	if len(answer) != 0 {
		t.Errorf("a notification was answered with a body: %v", answer)
	}
}

func TestABatchIsAnsweredWithOneAnswerPerCall(t *testing.T) {
	s := server(t)

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},
		  {"jsonrpc":"2.0","method":"notifications/initialized"},
		  {"jsonrpc":"2.0","id":2,"method":"tools/list"}]`))
	recorder := httptest.NewRecorder()

	s.ServeHTTP(recorder, request)

	var answers []map[string]any

	if err := json.Unmarshal(recorder.Body.Bytes(), &answers); err != nil {
		t.Fatalf("the batch answer was not an array: %v\n%s", err, recorder.Body.String())
	}

	if len(answers) != 2 {
		t.Fatalf("the batch of three (one a notification) was answered %d times", len(answers))
	}
}

func TestGetIsRefusedSoAClientStopsAskingForAStream(t *testing.T) {
	s := server(t)

	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET answered %d; it should be 405", recorder.Code)
	}
}

func TestABodyThatIsNotJSONIsAParseError(t *testing.T) {
	s := server(t)

	_, answer := call(t, s, `{not json`)

	failure, _ := answer["error"].(map[string]any)

	if failure == nil || failure["code"] != float64(-32700) {
		t.Errorf("a broken body answered %v", answer)
	}
}
