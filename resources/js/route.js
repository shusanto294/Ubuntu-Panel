/*
 * A `route()` helper compatible with the calls the panel's pages already make.
 *
 * The Laravel build got this from Ziggy, which is a PHP package shipping its
 * own JavaScript. The C panel serialises the same route table into the page
 * (see router_manifest_json), so this reads that table instead — the same
 * `{name: {uri, methods}}` shape, and the same call signatures:
 *
 *     route('dashboard')                  -> /dashboard
 *     route('sites.show', 12)             -> /sites/12
 *     route('sites.show', { site: 12 })   -> /sites/12
 *     route('sites.index', {}, false)     -> /sites            (absolute is ignored;
 *                                                               the panel is same-origin)
 *     route().current()                   -> 'sites.show'
 *     route().current('sites.*')          -> true
 *
 * Unknown names throw rather than returning something plausible: a link that
 * silently points at `/undefined` is much harder to notice than one that fails
 * the moment the page renders.
 */

function routes() {
    return (window.Ziggy && window.Ziggy.routes) || {};
}

/* `/sites/{site}` with `{site: 12}` becomes `/sites/12`. */
function fill(uri, parameters) {
    const query = { ...parameters };
    let path = uri;

    const segments = uri.match(/\{[^}]+\}/g) || [];

    for (const segment of segments) {
        const name = segment.slice(1, -1).replace(/\?$/, '');
        const value = query[name];

        if (value === undefined || value === null) {
            throw new Error(`route(): missing parameter "${name}" for "${uri}"`);
        }

        delete query[name];
        path = path.replace(segment, encodeURIComponent(value));
    }

    const remaining = Object.entries(query).filter(([, v]) => v !== undefined && v !== null);

    if (remaining.length === 0) return path;

    const search = remaining
        .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(v)}`)
        .join('&');

    return `${path}?${search}`;
}

/* `sites.*` matches `sites.show`; an exact name matches itself. */
function matches(pattern, name) {
    if (!pattern) return name;

    const expression = new RegExp(
        '^' + pattern.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*') + '$',
    );

    return expression.test(name);
}

/* Which route is the browser on? Compared against the table by shape, so
 * `/sites/12` resolves to `sites.show`. */
function current(pattern) {
    const path = window.location.pathname;
    let found = null;

    for (const [name, definition] of Object.entries(routes())) {
        const expression = new RegExp(
            '^/' + definition.uri.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\\\{[^}]+\\\}/g, '[^/]+') + '/?$',
        );

        if (!expression.test(path)) continue;

        /* Prefer the most specific match: `/sites/create` is `sites.create`,
         * not `sites.show` with a parameter of "create". */
        if (!found || definition.uri.length > routes()[found].uri.length) found = name;
    }

    if (pattern === undefined) return found;

    return found !== null && matches(pattern, found);
}

export function route(name, parameters, absolute) {
    if (name === undefined) {
        return { current, has: (n) => n in routes(), params: {} };
    }

    const definition = routes()[name];

    if (!definition) throw new Error(`route(): no route named "${name}"`);

    /* A bare scalar fills the single parameter the URI declares. Inertia pages
     * use both forms, often in the same file. */
    let bag = parameters;

    if (bag === undefined || bag === null) {
        bag = {};
    } else if (typeof bag !== 'object') {
        const segments = definition.uri.match(/\{[^}]+\}/g) || [];

        bag = segments.length
            ? { [segments[0].slice(1, -1).replace(/\?$/, '')]: bag }
            : {};
    } else if (bag.id !== undefined) {
        /* `route('sites.show', site)` with a whole model, which is what
         * `route('sites.show', site)` means in the pages that pass one. */
        const segments = definition.uri.match(/\{[^}]+\}/g) || [];

        if (segments.length && bag[segments[0].slice(1, -1)] === undefined) {
            bag = { ...bag, [segments[0].slice(1, -1).replace(/\?$/, '')]: bag.id };
        }
    }

    /* Absolute URLs are deliberately not produced: the panel is reached by IP,
     * by hostname, over TLS or not, and a path is correct in all four. */
    void absolute;

    return '/' + fill(definition.uri, bag);
}

export const RoutePlugin = {
    install(app) {
        app.config.globalProperties.route = route;
        app.provide('route', route);

        /* The pages call a bare `route(...)` inside <script setup> too. */
        window.route = route;
    },
};

export default route;
