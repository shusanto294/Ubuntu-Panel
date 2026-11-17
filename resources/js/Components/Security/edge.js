/*
 * What the Cloudflare tab's parts share: the names of Cloudflare's actions
 * and modes, the permission each part of the API needs, and the builder that
 * turns "URI path · equals · /wp-login.php" into the rule language Cloudflare
 * reads — (http.request.uri.path eq "/wp-login.php").
 */

export const ACTION_LABEL = {
    block: 'Block',
    managed_challenge: 'Managed challenge',
    js_challenge: 'JS challenge',
    challenge: 'Interactive challenge',
    skip: 'Skip the remaining rules',
    log: 'Log only (Enterprise)',
    whitelist: 'Allow',
};

export const ACTION_TONE = {
    block: 'bg-rose-50 text-rose-700 ring-rose-200',
    managed_challenge: 'bg-amber-50 text-amber-700 ring-amber-200',
    js_challenge: 'bg-amber-50 text-amber-700 ring-amber-200',
    challenge: 'bg-amber-50 text-amber-700 ring-amber-200',
    skip: 'bg-emerald-50 text-emerald-700 ring-emerald-200',
    whitelist: 'bg-emerald-50 text-emerald-700 ring-emerald-200',
    log: 'bg-slate-50 text-slate-600 ring-slate-200',
};

export const PHASE_ACTIONS = {
    custom: ['block', 'managed_challenge', 'js_challenge', 'challenge', 'skip', 'log'],
    ratelimit: ['block', 'managed_challenge', 'js_challenge', 'challenge', 'log'],
};

export const ACCESS_MODES = ['block', 'managed_challenge', 'js_challenge', 'challenge', 'whitelist'];

export const ACCESS_TARGETS = [
    { key: 'ip', label: 'IPv4 address', placeholder: '203.0.113.9' },
    { key: 'ip6', label: 'IPv6 address', placeholder: '2001:db8::1' },
    { key: 'ip_range', label: 'IP range', placeholder: '203.0.113.0/24' },
    { key: 'asn', label: 'ASN (network)', placeholder: 'AS14061' },
    { key: 'country', label: 'Country', placeholder: 'CN — or T1 for Tor' },
];

export const SECURITY_LEVELS = [
    { key: 'essentially_off', label: 'Essentially off', help: 'Challenges only the most grievous offenders.' },
    { key: 'low', label: 'Low', help: 'Challenges only the most threatening visitors.' },
    { key: 'medium', label: 'Medium', help: 'Cloudflare’s default: challenges moderately threatening visitors.' },
    { key: 'high', label: 'High', help: 'Challenges everyone who has shown threatening behaviour in the last 14 days.' },
    {
        key: 'under_attack',
        label: 'I’m under attack',
        help: 'Every visitor sees Cloudflare’s check before the site. For a flood only — it also stops APIs and webhooks.',
    },
];

export const RATE_PERIODS = [10, 60, 120, 300, 600, 3600];
export const RATE_TIMEOUTS = [10, 60, 120, 300, 600, 3600, 86400];

export const seconds = (n) =>
    n >= 86400 ? `${n / 86400} day` : n >= 3600 ? `${n / 3600} hour` : n >= 60 ? `${n / 60} min` : `${n} s`;

// The permission each part of the page needs on the API token.
export const PERMISSION = {
    custom: 'Zone · Zone WAF · Edit',
    ratelimit: 'Zone · Zone WAF · Edit',
    access: 'Zone · Firewall Services · Edit',
    level: 'Zone · Zone Settings · Edit',
};

export const isPermissionError = (message) =>
    /authenticat|permission|unauthori[sz]ed|forbidden|not entitled|10000|9109/i.test(message ?? '');

// ---------------------------------------------------------------- builder

export const FIELDS = [
    { key: 'http.request.uri.path', label: 'URI path', kind: 'string', placeholder: '/wp-login.php' },
    { key: 'http.request.uri', label: 'URI with query', kind: 'string', placeholder: '/search?q=' },
    { key: 'http.request.uri.query', label: 'Query string', kind: 'string', placeholder: 'author=' },
    { key: 'http.host', label: 'Hostname', kind: 'string', placeholder: 'shop.example.com' },
    { key: 'ip.src', label: 'IP address', kind: 'ip', placeholder: '203.0.113.9 or 203.0.113.0/24' },
    { key: 'ip.src.country', label: 'Country', kind: 'country', placeholder: 'CN' },
    { key: 'ip.src.continent', label: 'Continent', kind: 'country', placeholder: 'AS, EU, NA, SA, AF, OC, AN or T1' },
    { key: 'ip.src.asnum', label: 'ASN (network)', kind: 'number', placeholder: '14061' },
    { key: 'http.user_agent', label: 'User agent', kind: 'string', placeholder: 'python-requests' },
    { key: 'http.request.method', label: 'Request method', kind: 'method', placeholder: 'POST' },
    { key: 'http.referer', label: 'Referer', kind: 'string', placeholder: 'spam.example' },
    { key: 'cf.client.bot', label: 'Known bot (Google, Bing…)', kind: 'bool' },
];

const OPERATORS = {
    string: ['eq', 'ne', 'contains', 'not_contains', 'starts_with', 'ends_with', 'wildcard', 'in', 'not_in', 'matches'],
    method: ['eq', 'ne', 'in', 'not_in'],
    ip: ['eq', 'ne', 'in', 'not_in'],
    country: ['eq', 'ne', 'in', 'not_in'],
    number: ['eq', 'ne', 'in', 'not_in', 'gt', 'lt'],
    bool: ['is_true', 'is_false'],
};

export const OPERATOR_LABEL = {
    eq: 'equals',
    ne: 'does not equal',
    contains: 'contains',
    not_contains: 'does not contain',
    starts_with: 'starts with',
    ends_with: 'ends with',
    wildcard: 'wildcard (*)',
    in: 'is one of',
    not_in: 'is not one of',
    matches: 'matches regex (Business+)',
    gt: 'greater than',
    lt: 'less than',
    is_true: 'is true',
    is_false: 'is false',
};

export const fieldOf = (key) => FIELDS.find((f) => f.key === key) ?? FIELDS[0];

export const operatorsFor = (key) => OPERATORS[fieldOf(key).kind];

export const newCondition = (field = 'http.request.uri.path') => ({
    field,
    operator: operatorsFor(field)[0],
    value: '',
});

// A list operator takes several values: by comma, space or a new line.
const values = (text) =>
    String(text ?? '')
        .split(/[\s,]+/)
        .map((v) => v.trim())
        .filter(Boolean);

const literal = (kind, value) => {
    switch (kind) {
        case 'ip':
            return value.trim();
        case 'number':
            return String(parseInt(String(value).replace(/^as/i, ''), 10) || 0);
        case 'country':
        case 'method':
            return JSON.stringify(value.trim().toUpperCase());
        default:
            return JSON.stringify(value);
    }
};

/** One condition in Cloudflare's rule language, or '' while it is unfinished. */
export const conditionExpression = (c) => {
    const field = fieldOf(c.field);
    const f = field.key;

    if (field.kind === 'bool') return c.operator === 'is_false' ? `not ${f}` : f;

    if (c.operator === 'in' || c.operator === 'not_in') {
        const list = values(c.value).map((v) => literal(field.kind, v));
        if (!list.length) return '';
        const set = `${f} in {${list.join(' ')}}`;
        return c.operator === 'not_in' ? `not ${set}` : set;
    }

    const raw = field.kind === 'string' ? c.value ?? '' : String(c.value ?? '').trim();
    if (raw === '') return '';
    const v = literal(field.kind, raw);

    switch (c.operator) {
        case 'not_contains':
            return `not ${f} contains ${v}`;
        case 'starts_with':
        case 'ends_with':
            return `${c.operator}(${f}, ${v})`;
        default:
            return `${f} ${c.operator} ${v}`;
    }
};

/** The whole rule: every finished condition, joined by "and" or "or". */
export const buildExpression = (conditions, joiner = 'and') => {
    const parts = conditions.map(conditionExpression).filter(Boolean);
    if (!parts.length) return '';
    return parts.map((p) => `(${p})`).join(` ${joiner} `);
};

// --------------------------------------------------------------- presets

export const presets = (phase, myIp) => {
    const notMe = myIp ? ` and not ip.src in {${myIp}}` : '';

    if (phase === 'ratelimit') {
        return [
            {
                label: 'Slow down login attempts',
                description: 'Rate limit · logins',
                expression: '(http.request.uri.path eq "/wp-login.php") or (http.request.uri.path eq "/xmlrpc.php")',
                action: 'block',
                period: 10,
                requests_per_period: 5,
                mitigation_timeout: 10,
            },
            {
                label: 'Stop floods on the whole site',
                description: 'Rate limit · every request',
                expression: 'true',
                action: 'managed_challenge',
                period: 10,
                requests_per_period: 100,
                mitigation_timeout: 10,
            },
        ];
    }

    return [
        {
            label: 'Challenge WordPress logins',
            description: 'Challenge wp-login and wp-admin',
            expression: `((http.request.uri.path eq "/wp-login.php") or (starts_with(http.request.uri.path, "/wp-admin") and not http.request.uri.path eq "/wp-admin/admin-ajax.php"))${notMe}`,
            action: 'managed_challenge',
        },
        {
            label: 'Block XML-RPC',
            description: 'Block xmlrpc.php',
            expression: '(http.request.uri.path eq "/xmlrpc.php")',
            action: 'block',
        },
        {
            label: 'Block secret files',
            description: 'Block .env, .git and dumps',
            expression:
                '(http.request.uri.path contains "/.env") or (http.request.uri.path contains "/.git") or (ends_with(http.request.uri.path, ".sql")) or (ends_with(http.request.uri.path, ".bak"))',
            action: 'block',
        },
        {
            label: 'Challenge empty user agents',
            description: 'Challenge requests without a user agent',
            expression: '(http.user_agent eq "")',
            action: 'managed_challenge',
        },
        {
            label: 'Let known bots through',
            description: 'Allow verified search engine bots',
            expression: '(cf.client.bot)',
            action: 'skip',
        },
    ];
};
