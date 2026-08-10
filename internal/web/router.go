package web

import (
	"net/http"
	"sort"
	"strings"
)

// access says who may reach a route.
type access int

const (
	// public is reachable by anyone: assets, and the root URL that only points
	// somewhere useful. No CSRF check, because there is no session state to
	// protect.
	public access = iota

	// guest is for people who are not signed in. Someone who is gets sent to
	// the dashboard instead of being shown the sign-in form again.
	guest

	// authenticated is everything else.
	authenticated
)

// route is one entry in the table, kept so the frontend can be handed the same
// name-to-URL map its route() helper expects.
type route struct {
	name    string
	uri     string
	methods []string
}

// router registers handlers and remembers their names.
//
// Patterns are Go 1.22 ServeMux patterns, whose {placeholder} syntax happens to
// be exactly what the frontend's route() helper reads — so one string serves
// both, and the two can never drift apart.
type router struct {
	mux    *http.ServeMux
	app    *App
	routes map[string]*route
}

func newRouter(app *App) *router {
	return &router{mux: http.NewServeMux(), app: app, routes: map[string]*route{}}
}

func (rt *router) handle(method, pattern, name string, level access, handler http.HandlerFunc) {
	rt.mux.Handle(method+" "+pattern, rt.app.guard(level, handler))

	if name == "" {
		return
	}

	uri := strings.TrimPrefix(pattern, "/")

	if existing, ok := rt.routes[name]; ok {
		existing.methods = append(existing.methods, method)
		return
	}

	rt.routes[name] = &route{name: name, uri: uri, methods: []string{method}}
}

func (rt *router) get(pattern, name string, level access, handler http.HandlerFunc) {
	rt.handle(http.MethodGet, pattern, name, level, handler)
}

func (rt *router) post(pattern, name string, level access, handler http.HandlerFunc) {
	rt.handle(http.MethodPost, pattern, name, level, handler)
}

func (rt *router) patch(pattern, name string, level access, handler http.HandlerFunc) {
	rt.handle(http.MethodPatch, pattern, name, level, handler)
}

func (rt *router) delete(pattern, name string, level access, handler http.HandlerFunc) {
	rt.handle(http.MethodDelete, pattern, name, level, handler)
}

// manifest is the route table the frontend's route() helper reads, in the
// {name: {uri, methods}} shape it already expects.
func (rt *router) manifest() map[string]any {
	table := make(map[string]any, len(rt.routes))

	for name, entry := range rt.routes {
		methods := append([]string(nil), entry.methods...)
		sort.Strings(methods)

		table[name] = map[string]any{"uri": entry.uri, "methods": methods}
	}

	return table
}
