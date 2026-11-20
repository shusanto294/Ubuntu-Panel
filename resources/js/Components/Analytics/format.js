import { onBeforeUnmount, onMounted, ref } from 'vue';

/*
 * What the Analytics pages share: numbers, "4 seconds ago", and the colour
 * and name of a status code.
 */

export const number = (n) => (n ?? 0).toLocaleString();

export const PERIOD_LABEL = { '1h': 'last hour', '24h': 'last 24 hours', '7d': 'last 7 days', '30d': 'last 30 days' };
export const PERIOD_SHORT = { '1h': '1 hour', '24h': '24 hours', '7d': '7 days', '30d': '30 days' };
export const PERIOD_MINUTES = { '1h': 60, '24h': 1440, '7d': 10080, '30d': 43200 };

// The current time, ticking, so "12 seconds ago" keeps up without a reload.
export const useNow = (every = 5000) => {
    const now = ref(Date.now() / 1000);
    let timer = null;
    onMounted(() => (timer = setInterval(() => (now.value = Date.now() / 1000), every)));
    onBeforeUnmount(() => clearInterval(timer));
    return now;
};

export const ago = (at, now) => {
    if (!at || at < 0) return '—';
    const s = Math.max(0, Math.round(now - at));
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)} min ago`;
    if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
    return `${Math.floor(s / 86400)} d ago`;
};

export const CODE_LABEL = {
    200: 'OK',
    201: 'Created',
    204: 'No content',
    206: 'Partial content',
    301: 'Redirect',
    302: 'Redirect',
    303: 'See other',
    304: 'Not modified',
    307: 'Redirect',
    308: 'Redirect',
    400: 'Bad request',
    401: 'Unauthorised',
    403: 'Forbidden',
    404: 'Not found',
    405: 'Method not allowed',
    408: 'Timed out',
    413: 'Too large',
    429: 'Too many requests',
    444: 'Dropped',
    499: 'Visitor gave up',
    500: 'Server error',
    502: 'Bad gateway',
    503: 'Unavailable',
    504: 'Gateway timeout',
};

// The badge a status code is shown in.
export const statusTone = (code) =>
    ({
        2: 'bg-emerald-50 text-emerald-700',
        3: 'bg-sky-50 text-sky-700',
        4: 'bg-amber-50 text-amber-700',
        5: 'bg-rose-50 text-rose-700',
    })[String(code)[0]] ?? 'bg-slate-100 text-slate-600';

// The bar a class of status codes is drawn in.
export const CLASS_BAR = { '2xx': 'bg-emerald-600', '3xx': 'bg-sky-500', '4xx': 'bg-amber-400', '5xx': 'bg-rose-600' };

export const THREAT_LABEL = {
    'wp-login': 'Login attack',
    xmlrpc: 'XML-RPC attack',
    login: 'Login attack',
    probe: 'Scanner',
};

// What a part of the window is called: "Today, 22:23 – 22:53" for a bar of
// the chart, or the window itself when it is all of it.
export const spanLabel = (from, to, window) => {
    if (!from || !to) return `The ${PERIOD_LABEL[window] ?? window}`;
    const start = new Date(from * 1000);
    const end = new Date(Math.min(to * 1000, Date.now()));
    const time = { hour: '2-digit', minute: '2-digit' };
    const day = { weekday: 'short', month: 'short', day: 'numeric' };
    if (['1h', '24h'].includes(window) && start.toDateString() === end.toDateString()) {
        const date = start.toDateString() === new Date().toDateString() ? 'Today' : start.toLocaleDateString(undefined, day);
        return `${date}, ${start.toLocaleTimeString(undefined, time)} – ${end.toLocaleTimeString(undefined, time)}`;
    }
    return `${start.toLocaleString(undefined, { ...day, ...time })} – ${end.toLocaleString(undefined, { ...day, ...time })}`;
};

// The address of a part of the window on its own page: view is "" for both
// lists, "urls" or "requests" for one of them in full.
export const periodHref = ({ window, site, from, to, view, status, page }) => {
    const query = new URLSearchParams({ window });
    if (site) query.set('site', site);
    if (from && to) {
        query.set('from', from);
        query.set('to', to);
    }
    if (view) query.set('view', view);
    if (status) query.set('status', status);
    if (page > 1) query.set('page', page);
    return `${route('analytics.period')}?${query}`;
};
