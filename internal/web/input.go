package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
)

// maxBody caps what a request may send. Inertia posts small JSON objects; an
// unbounded read is how a crafted body becomes the panel's memory usage.
const maxBody = 1 << 20 // 1 MiB

// input reads a request body into a flat map, so a handler asks for a field
// the same way whether the frontend sent JSON or a form posted it.
//
// Inertia sends JSON for everything except file uploads, which arrive as
// multipart. Both are read here.
func (a *App) input(r *http.Request) (map[string]string, error) {
	values := map[string]string{}

	contentType := r.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "application/json") {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			return nil, fmt.Errorf("reading the request body: %w", err)
		}

		if len(body) == 0 {
			return values, nil
		}

		var decoded map[string]any

		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, errors.New("the request body was not valid JSON")
		}

		for key, value := range decoded {
			// Nested objects are not flattened — nothing the panel posts is
			// nested — and anything that is not a string is rendered the way
			// it would read back.
			switch typed := value.(type) {
			case nil:
				values[key] = ""
			case string:
				values[key] = typed
			case bool:
				values[key] = fmt.Sprintf("%t", typed)
			case float64:
				values[key] = trimFloat(typed)
			default:
				encoded, _ := json.Marshal(typed)
				values[key] = string(encoded)
			}
		}

		return values, nil
	}

	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)

	if err := r.ParseMultipartForm(maxBody); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		if err := r.ParseForm(); err != nil {
			return nil, fmt.Errorf("reading the form: %w", err)
		}
	}

	for key := range r.Form {
		values[key] = r.Form.Get(key)
	}

	return values, nil
}

// trimFloat writes a JSON number the way it was most likely written: 12 rather
// than 12.000000, so an id round-trips as an id.
func trimFloat(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}

	return fmt.Sprintf("%g", value)
}

func (a *App) serverError(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.Log.Error(what, "path", r.URL.Path, "error", err)

	if inertia.IsInertia(r) {
		// Never a bare JSON body: the client refuses anything that is not a
		// page and shows an error the user cannot act on.
		inertia.Location(w, r, inertia.RefererPath(r, "/"))
		return
	}

	http.Error(w, "The panel hit an unexpected error.", http.StatusInternalServerError)
}

func (a *App) badRequest(w http.ResponseWriter, r *http.Request, err error) {
	a.Log.Warn("bad request", "path", r.URL.Path, "error", err)

	if inertia.IsInertia(r) {
		inertia.Location(w, r, inertia.RefererPath(r, "/"))
		return
	}

	http.Error(w, "That request could not be read.", http.StatusBadRequest)
}

func isAsset(path string) bool {
	return strings.HasPrefix(path, "/build/") ||
		path == "/favicon.ico" ||
		path == "/robots.txt"
}
