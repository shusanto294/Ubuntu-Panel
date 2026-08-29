package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// writeJSON answers a fetch() the frontend makes outside the Inertia protocol —
// task polling, DNS zone listings, database credentials.
func (a *App) writeJSON(w http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		a.Log.Error("could not encode a JSON response", "error", err)
		http.Error(w, `{"error":"could not encode the response"}`, http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(encoded)
}

// routeID reads an {id} path segment.
func routeID(r *http.Request, name string) int64 {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		return 0
	}

	return id
}

// notFoundError answers a request for something that is not there. An Inertia
// visit gets a location change rather than a JSON body, which is the one rule
// the protocol will not forgive breaking.
func (a *App) notFoundError(w http.ResponseWriter, r *http.Request, message string) {
	if inertia.IsInertia(r) {
		inertia.Location(w, r, inertia.RefererPath(r, "/dashboard"))

		return
	}

	http.Error(w, message, http.StatusNotFound)
}

// failBack flashes an error and goes back. Used for the "you asked for
// something this machine cannot do" cases, which are ordinary outcomes rather
// than faults.
func (a *App) failBack(w http.ResponseWriter, r *http.Request, message string) {
	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("error", message)
	}

	inertia.Back(w, r, "/dashboard")
}

// okBack is the same for something that worked.
func (a *App) okBack(w http.ResponseWriter, r *http.Request, message string) {
	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("success", message)
	}

	inertia.Back(w, r, "/dashboard")
}

// boolInput reads a checkbox, which arrives as a real boolean over JSON and as
// a string from a form.
func boolInput(input map[string]string, key string, fallback bool) bool {
	value, ok := input[key]
	if !ok || value == "" {
		return fallback
	}

	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}

	return fallback
}

// intInput reads a number.
func intInput(input map[string]string, key string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(input[key]), 10, 64)
	if err != nil {
		return fallback
	}

	return value
}

// stringInput reads a trimmed field, falling back when it is blank.
func stringInput(input map[string]string, key, fallback string) string {
	value := strings.TrimSpace(input[key])
	if value == "" {
		return fallback
	}

	return value
}

// listInput reads a repeated field. Inertia sends arrays as JSON, which the
// input reader hands over as the encoded array.
func listInput(input map[string]string, key string) []string {
	raw := strings.TrimSpace(input[key])
	if raw == "" {
		return nil
	}

	var list []string

	if strings.HasPrefix(raw, "[") {
		json.Unmarshal([]byte(raw), &list)

		return list
	}

	return []string{raw}
}

// validDomain is a domain, strictly: what ends up in an nginx server_name and a
// certbot -d.
func validDomain(text string) bool {
	if text == "" || len(text) > 253 {
		return false
	}

	label, dots := 0, 0

	for i := 0; i < len(text); i++ {
		character := text[i]

		if character == '.' {
			if label == 0 || text[i-1] == '-' {
				return false
			}

			label = 0
			dots++

			continue
		}

		if label == 0 && character == '-' {
			return false
		}

		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '-':
		default:
			return false
		}

		label++

		if label > 63 {
			return false
		}
	}

	return label > 0 && dots > 0
}

// registryLabels are the second-level labels a registry runs rather than a
// person: the `co` of co.uk, the `com` of com.au. They are what tells
// example.co.uk — a domain somebody bought — from api.example.com, a host
// inside one.
//
// A list rather than the public suffix list itself, which is thousands of lines
// that would have to be shipped and kept current for the one question asked of
// it here. Being wrong either way costs a www alias somebody can add or remove
// by hand, so the short list is the right size of answer.
var registryLabels = map[string]bool{
	"co": true, "com": true, "net": true, "org": true, "gov": true, "edu": true,
	"ac": true, "or": true, "ne": true, "me": true, "in": true, "biz": true,
	"info": true, "nom": true, "gob": true, "mil": true,
}

// isApexDomain says whether this is a domain at its root — example.com,
// example.co.uk — rather than a host inside one, like app.example.com.
//
// This is the whole of what decides whether a new site is given its www alias.
// A root domain typed into a browser without the www is the one case where
// visitors reliably type it with the www as well; app.example.com is not.
func isApexDomain(domain string) bool {
	labels := strings.Split(strings.ToLower(strings.TrimSpace(domain)), ".")

	switch len(labels) {
	case 2:
		return true

	case 3:
		// example.co.uk: the middle label belongs to the registry and the last
		// is a country. app.example.com fails both halves.
		return registryLabels[labels[1]] && len(labels[2]) <= 3

	default:
		return false
	}
}

// validIdentifier is letters, digits and underscore. See the databases package
// for why identifiers are validated rather than escaped.
func validIdentifier(text string, max int) bool {
	if text == "" || len(text) > max {
		return false
	}

	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}

	return true
}

// validLocalPart is a mailbox name, kept to what Postfix and Dovecot handle
// without quoting — which is also what people actually use.
func validLocalPart(text string) bool {
	if text == "" || len(text) > 64 {
		return false
	}

	if strings.HasPrefix(text, ".") || strings.HasSuffix(text, ".") {
		return false
	}

	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}

	return true
}
