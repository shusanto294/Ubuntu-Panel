<script setup>
import { computed, reactive, ref } from 'vue';
import InputError from '@/Components/InputError.vue';
import { confirmAction } from '@/dialog';
import { card, field, smallButton, when } from './ui';
import { countryName, flagOf } from '@/countries';
import { router, usePage } from '@inertiajs/vue3';

/*
 * The optional protections, each on or off by itself. Nothing here is on until
 * it is switched on: each one changes what visitors, mail clients or the
 * kernel may do, and that is the owner's call.
 */
const props = defineProps({
    protection: { type: Object, required: true },
    myIp: { type: String, default: '' },
});

const emit = defineEmits(['open']);

const s = computed(() => props.protection.settings);
const ready = computed(() => props.protection.ready ?? {});
const errors = computed(() => usePage().props.errors ?? {});

const BAN_TIMES = [
    { value: 3600, label: '1 hour' },
    { value: 86400, label: '1 day' },
    { value: 7 * 86400, label: '1 week' },
    { value: 30 * 86400, label: '30 days' },
    { value: -1, label: 'Permanently' },
];

const FIND_TIMES = [
    { value: 60, label: '1 minute' },
    { value: 300, label: '5 minutes' },
    { value: 600, label: '10 minutes' },
    { value: 3600, label: '1 hour' },
    { value: 86400, label: '1 day' },
];

const SECTIONS = [
    {
        title: 'Websites',
        text: 'What reaches the sites. A banned address is refused by the firewall on the web ports, and by nginx itself — which is what stops it on a site behind Cloudflare, where its requests arrive from Cloudflare’s addresses. For that, turn on real visitor addresses.',
        features: ['web_login', 'rate_login', 'web_scan', 'tools', 'waf', 'real_ip', 'edge_only'],
    },
    {
        title: 'Floods and DDoS',
        text: 'Layered, cheapest first: limits refuse the excess before PHP, bans move persistent attackers to the firewall, and under attack mode stops floods that come from many addresses at once. A flood big enough to fill the server’s network link can only be absorbed in front of it — put the sites behind Cloudflare and let only Cloudflare in.',
        features: ['rate_flood', 'conn_limit', 'web_flood', 'challenge', 'countries'],
    },
    {
        title: 'Hacked-site protection',
        text: 'For when an attacker gets past everything else through a vulnerable plugin or theme: stop them changing the site, notice when they do, and take away what a web shell needs.',
        features: ['locks', 'integrity', 'php_harden'],
    },
    { title: 'Mail', text: 'Password guessing against mailboxes.', features: ['mail'] },
    {
        title: 'Server',
        text: 'The machine as a whole.',
        features: ['recidive', 'port_scan', 'watch', 'compilers', 'kernel', 'no_ping'],
    },
];

// One plain sentence per card; the full explanation is behind “How it works”.
const SHORT = {
    web_login: 'Bans bots that keep guessing WordPress and other site passwords.',
    rate_login: 'Lets each visitor try to log in only a few times a minute.',
    web_scan: 'Bans bots hunting for .env files, backups and known security holes.',
    tools: 'Blocks and bans hacking tools like sqlmap, nikto and WPScan.',
    waf: 'Blocks SQL injection, XSS and other attacks before they reach your site’s code.',
    real_ip: 'Shows the real visitor instead of Cloudflare, so the right address is banned.',
    edge_only: 'Nobody can reach your sites without going through Cloudflare.',
    rate_flood: 'Caps how fast one visitor can load pages.',
    conn_limit: 'Stops one machine holding hundreds of connections open.',
    web_flood: 'Bans addresses that keep hitting the limits above.',
    challenge: 'Emergency switch: visitors pass a two-second browser check first.',
    countries: 'Block countries you never get real visitors from — or allow only yours.',
    locks: 'Makes a site’s files read-only, so a hacker cannot change them.',
    integrity: 'Emails you when a site’s code changes and the panel did not change it.',
    php_harden: 'Stops PHP running system commands — what a hacker’s web shell needs.',
    mail: 'Bans bots guessing mailbox passwords.',
    recidive: 'Bans an address that keeps coming back for much longer, on every port.',
    port_scan: 'Bans anyone probing the server for open ports.',
    watch: 'Emails you when a new login account, SSH key, cron job or service appears.',
    compilers: 'Only administrators can compile programs on the server.',
    kernel: 'Safer network settings, including SYN-flood protection.',
    no_ping: 'The server stops answering ping.',
};

// What one press of “Turn on recommended” turns on: nothing that changes how a
// real visitor's site behaves.
const RECOMMENDED = [
    'web_login', 'rate_login', 'web_scan', 'tools', 'rate_flood', 'conn_limit', 'web_flood',
    'recidive', 'port_scan', 'watch', 'integrity', 'kernel',
];

const FEATURES = {
    web_login: {
        kind: 'jail',
        title: 'Ban website login brute force',
        text: 'Bans an address that keeps posting to wp-login.php, xmlrpc.php or another site login form. A WordPress login that succeeds does not count.',
        jails: ['panel-web-login'],
        needs: 'fail2ban',
        unit: 'login posts',
    },
    web_scan: {
        kind: 'jail',
        title: 'Ban vulnerability scanners',
        text: 'Bans an address asking for files only an attacker looks for — phpunit, backup.sql, web shells — on the sites and on the server’s bare IP address. One request for a secret file (.env, .git, .aws, SSH keys, wp-config backups) is enough. Floods and sweeps for missing pages are banned too when they reach the server directly; through Cloudflare those are left to Cloudflare. Search engines and AI crawlers following dead links are never banned. You can edit what counts below.',
        jails: ['panel-web-scan', 'panel-web-secret'],
        needs: 'fail2ban',
        unit: 'probes',
    },
    rate_login: {
        kind: 'rate',
        perSecond: false,
        title: 'Rate-limit login pages',
        text: 'nginx answers extra login posts with “429 Too Many Requests” before they reach PHP, so even an attack nobody has banned yet costs almost no CPU.',
        needs: 'nginx',
    },
    rate_flood: {
        kind: 'rate',
        perSecond: true,
        title: 'Limit request floods',
        text: 'Caps how many PHP or app requests one address can make. Images, CSS and JS are never limited, nor are the panel, Nextcloud and n8n, nor search engines, AI crawlers, link previews and uptime monitors. Set it well above what a real visitor does.',
        needs: 'nginx',
        confirm:
            'Every address is limited, real visitors included. The default (20 a second, bursts of 100) is far above what a person browsing does, but a site whose pages fire many AJAX calls may need more. Watch Logs → Website traffic for 429s after turning it on.',
    },
    real_ip: {
        kind: 'switch',
        title: 'Real visitor addresses behind Cloudflare',
        text: 'For sites proxied through Cloudflare: nginx takes the visitor’s address from Cloudflare’s header — trusted only from Cloudflare’s own addresses — so logs, Website traffic and rate limits see the visitor instead of Cloudflare. Cloudflare’s addresses are never banned or rate limited either way.',
        needs: 'nginx',
    },
    edge_only: {
        kind: 'switch',
        title: 'Only Cloudflare reaches the websites',
        text: 'For servers whose sites are all proxied through Cloudflare (orange cloud): the firewall lets only Cloudflare’s addresses reach ports 80 and 443. Nobody can reach the sites around Cloudflare, and bots sweeping IP addresses get nothing from this server. SSH, mail and the panel’s own port are not affected. It will not turn on while a site or the panel is not proxied, since that one would stop answering.',
        confirm: 'Ports 80 and 443 will refuse every address except Cloudflare’s. Any site or hostname that is not proxied through Cloudflare stops answering. Needs the firewall on.',
    },
    mail: {
        kind: 'jail',
        title: 'Ban mail login brute force',
        text: 'Bans an address that keeps failing to log in to a mailbox over SMTP, IMAP or POP3 — the usual way a mailbox is stolen and then used to send spam.',
        jails: ['panel-mail-smtp', 'panel-mail-imap'],
        needs: 'mail',
        unit: 'failed logins',
    },
    recidive: {
        kind: 'jail',
        title: 'Ban repeat offenders for longer',
        text: 'An address that any jail — SSH, website or mail — bans again and again is banned from every port of the server for much longer.',
        jails: ['recidive'],
        needs: 'fail2ban',
        unit: 'bans',
    },
    tools: {
        kind: 'jail',
        title: 'Refuse attack tools',
        text: 'sqlmap, nikto, WPScan, Nuclei, masscan and other attack tools announce themselves in their user agent. nginx closes the connection without an answer, and the address is banned. curl, wget and scripting libraries are never counted: monitors and webhooks use them.',
        jails: ['panel-web-tools'],
        needs: 'fail2ban',
        unit: 'requests',
    },
    waf: {
        kind: 'waf',
        title: 'Web application firewall (WAF)',
        text: 'ModSecurity with the OWASP Core Rule Set inspects every request to the sites and refuses SQL injection, cross-site scripting, file inclusion, remote code execution and protocol attacks before PHP runs — the same kind of WAF commercial panels sell. WordPress’s admin and editor are excluded from the rules that misread them. Start in detection mode, check the sites’ error logs for false alarms, then switch to blocking.',
        jails: ['panel-web-waf'],
        needs: 'nginx',
        unit: 'refused requests',
    },
    conn_limit: {
        kind: 'conn',
        title: 'Cut off slow and held-open connections',
        text: 'Caps how many PHP or app requests one address can have open at once, and closes clients that send their request slowly — a Slowloris attack holds hundreds of connections open from one machine with a trickle of bytes. Images, CSS and JS are not counted, nor the panel, Nextcloud and n8n.',
        needs: 'nginx',
    },
    web_flood: {
        kind: 'jail',
        title: 'Ban addresses that keep flooding',
        text: 'An address that keeps getting “429 Too Many Requests” from the limits above is banned on the firewall, so its next requests cost nothing at all. Works with the request flood limit and the connection limit; search engines and monitors are never banned.',
        jails: ['panel-web-flood'],
        needs: 'fail2ban',
        unit: 'refused requests',
    },
    challenge: {
        kind: 'challenge',
        title: 'Under attack mode',
        text: 'For a flood from thousands of addresses, which per-address limits cannot stop: on the sites you choose, every visitor’s browser runs a two-second check before the first page and then gets a pass for 12 hours, bound to its address. Scripts and bots that do not run JavaScript get a tiny static page from nginx and never reach PHP. Search engines, monitors and the never-ban list are let through. APIs and webhooks on these sites are checked too, so turn it on only while under attack.',
        needs: 'nginx',
        confirm: 'Every visitor to the chosen sites will see a “Checking your browser” page for about two seconds before their first page. API clients and webhooks to those sites (payment callbacks, apps) will be refused while it is on.',
    },
    countries: {
        kind: 'countries',
        title: 'Country rules',
        text: 'Refuse visitors from the countries you list, or let in only those. nginx answers them “403 Forbidden” before PHP. Address ranges come from ipdeny.com and are refreshed weekly. Applies to the websites only — never SSH, mail or the panel, so you cannot lock yourself out — and never to search engines or the never-ban list.',
        needs: 'nginx',
        confirm: 'Visitors from the refused countries will get “403 Forbidden” on every site, including customers using a VPN or travelling.',
    },
    locks: {
        kind: 'locks',
        title: 'Lock site files',
        text: 'A locked site’s code belongs to root and cannot be changed by PHP, so a hacked plugin cannot rewrite index.php or drop a web shell beside it. Uploads, caches and other folders a site writes to are left alone. WordPress cannot update itself, its plugins or its themes while locked: press “Unlock for 30 minutes”, update, and it locks itself again. The panel’s own deploys, restores and PHP changes unlock and relock it for you.',
    },
    integrity: {
        kind: 'switch',
        title: 'Watch for code changes',
        text: 'Once an hour, every PHP site’s code files (PHP, .htaccess, .user.ini) are compared with the last look. Anything added, changed or removed that the panel did not do is emailed to you and listed here — and a PHP file inside an uploads folder, where real sites never keep code, is flagged as a likely web shell. It reports facts and deletes nothing. WordPress updates show up here too; that is expected.',
    },
    php_harden: {
        kind: 'switch',
        title: 'PHP hardening',
        text: 'Turns off the PHP functions that run other programs — exec, system, shell_exec, passthru, proc_open, popen — for every website. A web shell uploaded through a vulnerable plugin then cannot run commands, download malware or read other sites. The command line (WP-CLI, Composer, cron) is not affected. A few plugins use these functions (some backup and image tools); if one stops working, turn this off.',
        confirm: 'Every PHP site loses exec, system, shell_exec, passthru, proc_open and popen. Most sites never use them, but some backup, image-optimising and Laravel queue features do. PHP-FPM reloads; nothing goes down.',
    },
    watch: {
        kind: 'switch',
        title: 'Watch for backdoors',
        text: 'Every five minutes the panel checks the things an intruder adds to keep a way back in: accounts that can log in, sudo rights, cron jobs, SSH keys, system services, and /etc/ld.so.preload (the usual rootkit trick). Anything that appears or changes without the panel is emailed to you and listed here. Changes you make through the panel — SSH users, keys, installing apps — are never reported. It removes nothing.',
    },
    compilers: {
        kind: 'switch',
        title: 'Compilers for administrators only',
        text: 'gcc, g++, cc, as, ld and clang can only be run by root and the panel. An attacker with a shell as a website often compiles an exploit on the server; this stops that step. It survives package upgrades. Node.js packages that compile native code during a deploy will fail while it is on.',
        confirm: 'Only root and the panel will be able to run the compilers. Deploys of Node.js sites whose packages compile native code will fail until you turn this off.',
    },
    port_scan: {
        kind: 'jail',
        title: 'Ban port scanners',
        text: 'An address that knocks on port after closed port is mapping the server for something to attack. It is banned from every port. Reads what the firewall refuses, so it needs the firewall on (its log is turned on for you).',
        jails: ['panel-port-scan'],
        needs: 'firewall',
        unit: 'closed ports tried',
    },
    no_ping: {
        kind: 'switch',
        title: 'Do not answer ping',
        text: 'The server stops answering ICMP echo (ping), so sweeps of the internet that ping first skip it, and ping floods get no reply. Everything else ICMP does — the messages TCP needs to find the path’s packet size — keeps working. Uptime monitors that use ping will report the server down; use an HTTP check instead.',
    },
    kernel: {
        kind: 'switch',
        title: 'Network hardening',
        text: 'SYN-flood protection and a deeper connection backlog; ICMP redirects and source-routed packets refused. Forwarding is untouched, so Docker and VPNs keep working. Turning it off puts the previous values back.',
    },
};

const NEEDS = {
    fail2ban: 'fail2ban is not installed — the SSH protection tab installs it.',
    nginx: 'nginx is not installed.',
    mail: 'The mail server is not installed.',
    firewall: 'The firewall is off — turn it on on the Firewall tab first.',
};

// One small form per protection, seeded from what is saved (or the defaults).
const forms = reactive({});
for (const key of Object.keys(FEATURES)) {
    const saved = s.value[key];
    if (typeof saved !== 'object') {
        forms[key] = {};
        continue;
    }
    // The thresholds only: whether it is on is the button pressed, never the form.
    const { enabled: _ignored, ...values } = { ...props.protection.defaults[key], ...saved };
    forms[key] = values;
}
forms.countries.codes = [...(s.value.countries?.codes ?? [])];
forms.challenge.sites = [...(s.value.challenge?.sites ?? [])];
forms.countries.mode = s.value.countries?.mode || 'block';
forms.waf.block = Boolean(s.value.waf?.block);

// Countries are typed as codes and shown as names.
const countryInput = ref('');
const addCountries = () => {
    for (const code of countryInput.value.toLowerCase().split(/[\s,]+/)) {
        if (/^[a-z]{2}$/.test(code) && !forms.countries.codes.includes(code)) forms.countries.codes.push(code);
    }
    countryInput.value = '';
};
const removeCountry = (code) => (forms.countries.codes = forms.countries.codes.filter((c) => c !== code));
const nameOf = (code) => countryName(code.toUpperCase()) || code.toUpperCase();

const sites = computed(() => props.protection.sites ?? []);
const toggleSite = (domain) => {
    const list = forms.challenge.sites;
    forms.challenge.sites = list.includes(domain) ? list.filter((d) => d !== domain) : [...list, domain];
};

const busy = ref('');
const lastKey = ref('');

const locks = computed(() => props.protection.locks ?? []);
const enabled = (key) => {
    if (key === 'locks') return locks.value.some((l) => l.locked || l.wanted);
    return typeof s.value[key] === 'object' ? s.value[key].enabled : Boolean(s.value[key]);
};

// How it works, per card.
const open = reactive({});

// The recommended set, as far as this server can have it.
const recommended = computed(() =>
    RECOMMENDED.concat(ready.value.mail ? ['mail'] : [], props.protection.edge_sites?.length ? ['real_ip'] : []).filter(
        (key) => !(key === 'port_scan' && !ready.value.firewall) && !(FEATURES[key].needs === 'fail2ban' && !ready.value.fail2ban),
    ),
);
const recommendedOff = computed(() => recommended.value.filter((key) => !enabled(key)));
const turnOnRecommended = async () => {
    if (
        !(await confirmAction(
            'Turn on the recommended protections?',
            `${recommendedOff.value.map((k) => FEATURES[k].title).join(', ')}. None of them changes how your sites work for real visitors, and each can be turned off again here.`,
            'Turn them on',
        ))
    )
        return;
    lastKey.value = 'recommended';
    router.post(
        route('security.protection'),
        { feature: 'recommended' },
        { preserveScroll: true, onStart: () => (busy.value = 'recommended'), onFinish: () => (busy.value = '') },
    );
};

// Site locks.
const lockAction = async (site, action) => {
    if (action === 'lock') {
        const ok = await confirmAction(
            `Lock ${site.domain}?`,
            'Its code becomes read-only to PHP. WordPress, its plugins and themes cannot update until you unlock it — “Unlock for 30 minutes” is there for that. Uploads keep working.',
            'Lock files',
        );
        if (!ok) return;
    }
    lastKey.value = 'locks';
    router.post(
        route('security.locks'),
        { site: site.domain, action },
        { preserveScroll: true, onStart: () => (busy.value = `lock:${site.domain}`), onFinish: () => (busy.value = '') },
    );
};
const minutesLeft = (at) => Math.max(1, Math.round((at * 1000 - Date.now()) / 60000));

// The two change lists.
const clearChanges = (kind) =>
    router.post(
        route('security.changes.clear'),
        { kind },
        { preserveScroll: true, onStart: () => (busy.value = `clear:${kind}`), onFinish: () => (busy.value = '') },
    );
const watchChanges = computed(() => props.protection.watch_changes ?? []);
const codeChanges = computed(() => props.protection.code_changes ?? []);
const codeCount = (c) => c.added.length + c.changed.length + c.removed.length;

// Cloudflare's own Under Attack mode, for sites behind it.
const cfOn = (domain) => Boolean(props.protection.cloudflare_attack?.[domain]);
const cloudflareAttack = (domain, on) =>
    router.post(
        route('security.cloudflare-attack'),
        { site: domain, enabled: on },
        { preserveScroll: true, onStart: () => (busy.value = `cf:${domain}`), onFinish: () => (busy.value = '') },
    );
const scrollTo = (id) => document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });

const blocked = (key) => {
    const needs = FEATURES[key].needs;
    return needs && !ready.value[needs] ? NEEDS[needs] : '';
};

const banned = (key) =>
    (FEATURES[key].jails ?? []).reduce((sum, jail) => sum + (props.protection.jails?.[jail]?.currently_banned ?? 0), 0);

const total = (key) =>
    (FEATURES[key].jails ?? []).reduce((sum, jail) => sum + (props.protection.jails?.[jail]?.total_banned ?? 0), 0);

const running = (key) => (FEATURES[key].jails ?? []).every((jail) => props.protection.jails?.[jail]?.running);

// What the card says about itself, in one word and a colour: on and working,
// on but not doing its job, or off.
const state = (key) => {
    if (!enabled(key)) return 'off';
    if (FEATURES[key].kind === 'jail' && !running(key)) return 'stalled';
    if (key === 'kernel' && !props.protection.kernel_applied) return 'stalled';
    if (key === 'edge_only' && !props.protection.edge_applied) return 'stalled';
    if (key === 'no_ping' && !props.protection.ping_applied) return 'stalled';
    if (key === 'php_harden' && !props.protection.php_applied) return 'stalled';
    if (key === 'waf' && enabled(key) && s.value.waf?.block && s.value.waf?.max_retry > 0 && !running(key)) return 'stalled';
    return 'on';
};

const STATE = {
    on: { label: 'On', badge: 'bg-emerald-600 text-white', dot: 'bg-white', card: 'ring-2 !ring-emerald-500/60' },
    stalled: { label: 'On — not working', badge: 'bg-amber-500 text-white', dot: 'bg-white', card: 'ring-2 !ring-amber-400/70' },
    off: { label: 'Off', badge: 'bg-slate-100 text-slate-600 ring-1 ring-slate-200', dot: 'bg-slate-400', card: '' },
};

// Why the last attempt to switch it did not take, until one does.
const failure = (key) => props.protection.failures?.[key] ?? null;

const onCount = computed(() => Object.keys(FEATURES).filter((key) => enabled(key)).length);

const submit = async (key, on) => {
    const feature = FEATURES[key] ?? {};
    lastKey.value = key;

    if (on && !enabled(key) && feature.confirm && !(await confirmAction(`Turn on: ${feature.title}?`, feature.confirm, 'Turn on')))
        return;

    router.post(
        route('security.protection'),
        // The switch last: the form is seeded from the saved settings, which
        // carry their own `enabled`, and spread after it that saved value won —
        // every Turn on was sent as the off it was replacing.
        { ...payload(key), feature: key, enabled: on },
        {
            preserveScroll: true,
            onStart: () => (busy.value = key),
            onFinish: () => (busy.value = ''),
        },
    );
};

// What a card's form sends: lists as text, the way the server reads them.
const payload = (key) => {
    const form = { ...(forms[key] ?? {}) };
    if (key === 'countries') form.codes = form.codes.join(',');
    if (key === 'challenge') form.sites = form.sites.join(',');
    return form;
};

const installWAF = () => {
    lastKey.value = 'waf';
    router.post(
        route('security.protection'),
        { feature: 'waf_install' },
        { preserveScroll: true, onStart: () => (busy.value = 'waf'), onFinish: () => (busy.value = '') },
    );
};

const labelOf = (list, value) => list.find((o) => o.value === Number(value))?.label ?? `${value}s`;

// The scanner protection's two lists, one pattern a line.
const paths = computed(() => props.protection.paths ?? { secret: [], scan: [], default_secret: [], default_scan: [] });
const pathForm = reactive({
    secret_paths: (paths.value.secret ?? []).join('\n'),
    scan_paths: (paths.value.scan ?? []).join('\n'),
});
const pathsOpen = ref(false);
const lines = (text) => text.split('\n').map((l) => l.trim()).filter(Boolean);
const sameList = (text, list) => {
    const a = lines(text).map((l) => l.toLowerCase()).sort();
    const b = (list ?? []).map((l) => l.toLowerCase()).sort();
    return a.length === b.length && a.every((v, i) => v === b[i]);
};
const pathsChanged = computed(
    () => !sameList(pathForm.secret_paths, paths.value.secret) || !sameList(pathForm.scan_paths, paths.value.scan),
);
const pathsCustom = computed(
    () => !sameList(pathForm.secret_paths, paths.value.default_secret) || !sameList(pathForm.scan_paths, paths.value.default_scan),
);
const pathErrors = computed(() => ['secret_paths', 'scan_paths'].map((k) => errors.value[k]).filter(Boolean));

const restorePaths = (which) => {
    if (which === 'secret') pathForm.secret_paths = (paths.value.default_secret ?? []).join('\n');
    else pathForm.scan_paths = (paths.value.default_scan ?? []).join('\n');
};

const savePaths = () => {
    lastKey.value = 'scan_paths';
    router.post(
        route('security.protection'),
        { feature: 'scan_paths', secret_paths: pathForm.secret_paths, scan_paths: pathForm.scan_paths },
        {
            preserveScroll: true,
            onStart: () => (busy.value = 'scan_paths'),
            onFinish: () => (busy.value = ''),
            onSuccess: () => {
                pathForm.secret_paths = (paths.value.secret ?? []).join('\n');
                pathForm.scan_paths = (paths.value.scan ?? []).join('\n');
            },
        },
    );
};
</script>

<template>
    <div class="space-y-8">
        <div class="grid gap-4 lg:grid-cols-3">
            <div :class="card" class="lg:col-span-2">
                <h3 class="font-semibold text-slate-900">Quick setup</h3>
                <p class="mt-1 text-sm text-slate-600">
                    <span class="text-lg font-semibold text-slate-900">{{ onCount }} of {{ Object.keys(FEATURES).length }}</span>
                    protections are on.
                    <template v-if="recommendedOff.length">
                        {{ recommendedOff.length }} recommended {{ recommendedOff.length === 1 ? 'one is' : 'ones are' }} still off.
                    </template>
                    <template v-else>Every recommended protection is on.</template>
                </p>
                <p class="mt-1 text-xs text-slate-500">
                    The recommended set never changes how your sites work for real visitors. The rest — the WAF, country rules,
                    file locks, PHP hardening — are for you to choose, one card each below.
                </p>
                <button
                    v-if="recommendedOff.length"
                    type="button"
                    class="mt-3 rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-semibold text-white transition hover:bg-brand-700 disabled:opacity-50"
                    :disabled="busy !== ''"
                    @click="turnOnRecommended"
                >
                    {{ busy === 'recommended' ? 'Turning on…' : `Turn on ${recommendedOff.length} recommended protection${recommendedOff.length === 1 ? '' : 's'}` }}
                </button>
            </div>
            <div :class="card" class="border-s-4 border-rose-400">
                <h3 class="font-semibold text-slate-900">Under attack right now?</h3>
                <p class="mt-1 text-sm text-slate-600">
                    Sites slow or down from a flood of traffic? Turn on <b>under attack mode</b> for them. Sites behind Cloudflare can
                    use Cloudflare’s own check instead.
                </p>
                <button
                    type="button"
                    class="mt-3 rounded-xl bg-rose-600 px-4 py-2.5 text-sm font-semibold text-white transition hover:bg-rose-700"
                    @click="scrollTo('card-challenge')"
                >
                    Go to under attack mode
                </button>
            </div>
        </div>

        <p class="max-w-3xl text-sm text-slate-500">
            Every protection here is optional and off until you turn it on. Addresses on the never-ban list
            (SSH protection tab) are never banned or rate limited by any of them<span v-if="myIp">
                — your address is <span class="break-all font-mono">{{ myIp }}</span></span>. Nor are search engines, AI
            crawlers, link previews and uptime monitors for crawling — following dead links, coming back often, crawling fast.
        </p>

        <section v-for="section in SECTIONS" :key="section.title">
            <h3 class="text-sm font-semibold uppercase tracking-wide text-slate-500">{{ section.title }}</h3>
            <p class="mt-0.5 text-sm text-slate-500">{{ section.text }}</p>

            <div class="mt-3 grid gap-4 lg:grid-cols-2">
                <div
                    v-for="key in section.features"
                    :id="`card-${key}`"
                    :key="key"
                    :class="[card, STATE[state(key)].card]"
                    class="flex scroll-mt-4 flex-col"
                >
                    <div class="flex items-start justify-between gap-3">
                        <h4 class="flex min-w-0 flex-wrap items-center gap-2 font-semibold text-slate-900">
                            {{ FEATURES[key].title }}
                            <span
                                v-if="(key === 'real_ip' || key === 'edge_only') && protection.edge_sites?.length && !enabled(key)"
                                class="rounded-full bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-700"
                            >
                                recommended — {{ protection.edge_sites.length }} site{{ protection.edge_sites.length === 1 ? '' : 's' }} behind Cloudflare
                            </span>
                        </h4>
                        <span
                            class="inline-flex shrink-0 items-center gap-1.5 rounded-full px-3 py-1 text-xs font-semibold"
                            :class="STATE[state(key)].badge"
                        >
                            <span class="h-2 w-2 rounded-full" :class="STATE[state(key)].dot" />
                            {{ STATE[state(key)].label }}
                        </span>
                    </div>
                    <p class="mt-1 text-sm text-slate-700">{{ SHORT[key] }}</p>
                    <button
                        type="button"
                        class="mt-1 self-start py-1 text-xs font-medium text-brand-600 hover:underline"
                        :aria-expanded="Boolean(open[key])"
                        @click="open[key] = !open[key]"
                    >
                        {{ open[key] ? 'Hide details' : 'How it works' }}
                    </button>
                    <p v-if="open[key]" class="mt-1 text-sm text-slate-500">{{ FEATURES[key].text }}</p>

                    <!-- A jail: how many, over how long, banned for how long. -->
                    <div v-if="FEATURES[key].kind === 'jail'" class="mt-4 grid gap-3 sm:grid-cols-3">
                        <label class="block text-xs font-medium text-slate-700">
                            Ban after
                            <input v-model.number="forms[key].max_retry" type="number" min="0" max="1000" :class="field" class="text-sm" />
                            <span class="font-normal text-slate-400">
                                {{ forms[key].max_retry <= 1 ? `${FEATURES[key].unit} — banned on the very first one` : FEATURES[key].unit }}
                            </span>
                        </label>
                        <label class="block text-xs font-medium text-slate-700">
                            Within
                            <select v-model.number="forms[key].find_time" :class="field" class="text-sm">
                                <option v-for="o in FIND_TIMES" :key="o.value" :value="o.value">{{ o.label }}</option>
                                <option v-if="!FIND_TIMES.some((o) => o.value === forms[key].find_time)" :value="forms[key].find_time">
                                    {{ labelOf(FIND_TIMES, forms[key].find_time) }}
                                </option>
                            </select>
                        </label>
                        <label class="block text-xs font-medium text-slate-700">
                            Ban for
                            <select v-model.number="forms[key].ban_time" :class="field" class="text-sm">
                                <option v-for="o in BAN_TIMES" :key="o.value" :value="o.value">{{ o.label }}</option>
                                <option v-if="!BAN_TIMES.some((o) => o.value === forms[key].ban_time)" :value="forms[key].ban_time">
                                    {{ labelOf(BAN_TIMES, forms[key].ban_time) }}
                                </option>
                            </select>
                        </label>
                    </div>

                    <!-- A rate limit. -->
                    <div v-else-if="FEATURES[key].kind === 'rate'" class="mt-4 grid gap-3 sm:grid-cols-2">
                        <label class="block text-xs font-medium text-slate-700">
                            Requests per {{ FEATURES[key].perSecond ? 'second' : 'minute' }}, per address
                            <input v-model.number="forms[key].rate" type="number" min="1" :class="field" class="text-sm" />
                        </label>
                        <label class="block text-xs font-medium text-slate-700">
                            Allowed in a burst
                            <input v-model.number="forms[key].burst" type="number" min="0" :class="field" class="text-sm" />
                        </label>
                    </div>

                    <!-- The connection limit. -->
                    <div v-else-if="FEATURES[key].kind === 'conn'" class="mt-4 grid gap-3 sm:grid-cols-2">
                        <label class="block text-xs font-medium text-slate-700">
                            Requests open at once, per address
                            <input v-model.number="forms[key].max" type="number" min="5" max="10000" :class="field" class="text-sm" />
                            <span class="font-normal text-slate-400">Clients slower than 10 s to send their headers are closed.</span>
                        </label>
                    </div>

                    <!-- The WAF: install, mode, bans. -->
                    <div v-else-if="FEATURES[key].kind === 'waf'" class="mt-4 space-y-3">
                        <div v-if="!ready.waf" class="rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-xs text-slate-600">
                            <template v-if="protection.waf_installing">Installing ModSecurity and the Core Rule Set — follow it on the Apps page.</template>
                            <template v-else>
                                Needs ModSecurity and the OWASP Core Rule Set (about 15 MB, from Ubuntu’s packages).
                                <button
                                    type="button"
                                    class="ms-1 font-semibold text-brand-600 hover:underline disabled:opacity-50"
                                    :disabled="busy !== '' || !ready.nginx"
                                    @click="installWAF"
                                >
                                    {{ busy === 'waf' ? 'Queuing…' : 'Install the WAF' }}
                                </button>
                            </template>
                        </div>
                        <fieldset class="grid gap-2 sm:grid-cols-2">
                            <legend class="sr-only">Mode</legend>
                            <label
                                v-for="mode in [
                                    { value: false, label: 'Detect only', hint: 'Logs what it would refuse. Refuses nothing.' },
                                    { value: true, label: 'Block', hint: 'Refuses attacks with 403, and bans repeat attackers.' },
                                ]"
                                :key="String(mode.value)"
                                class="flex cursor-pointer gap-2 rounded-lg border p-3 text-sm"
                                :class="forms.waf.block === mode.value ? 'border-brand-500 bg-brand-50/50' : 'border-slate-200'"
                            >
                                <input v-model="forms.waf.block" type="radio" :value="mode.value" class="mt-0.5 text-brand-600" />
                                <span>
                                    <span class="font-medium text-slate-900">{{ mode.label }}</span>
                                    <span class="block text-xs text-slate-500">{{ mode.hint }}</span>
                                </span>
                            </label>
                        </fieldset>
                        <div v-if="forms.waf.block" class="grid gap-3 sm:grid-cols-3">
                            <label class="block text-xs font-medium text-slate-700">
                                Ban after
                                <input v-model.number="forms.waf.max_retry" type="number" min="0" max="1000" :class="field" class="text-sm" />
                                <span class="font-normal text-slate-400">{{ forms.waf.max_retry === 0 ? 'never banned' : 'refused requests' }}</span>
                            </label>
                            <label class="block text-xs font-medium text-slate-700">
                                Within
                                <select v-model.number="forms.waf.find_time" :class="field" class="text-sm">
                                    <option v-for="o in FIND_TIMES" :key="o.value" :value="o.value">{{ o.label }}</option>
                                </select>
                            </label>
                            <label class="block text-xs font-medium text-slate-700">
                                Ban for
                                <select v-model.number="forms.waf.ban_time" :class="field" class="text-sm">
                                    <option v-for="o in BAN_TIMES" :key="o.value" :value="o.value">{{ o.label }}</option>
                                </select>
                            </label>
                        </div>
                    </div>

                    <!-- Under attack mode: which sites. -->
                    <div v-else-if="FEATURES[key].kind === 'challenge'" class="mt-4">
                        <p class="text-xs font-medium text-slate-700">Sites under attack mode</p>
                        <p v-if="!sites.length" class="mt-1 text-xs text-slate-500">There are no sites yet.</p>
                        <div v-else class="mt-2 flex flex-wrap gap-2">
                            <label
                                v-for="site in sites"
                                :key="site.domain"
                                class="inline-flex max-w-full cursor-pointer items-center gap-2 rounded-lg border px-3 py-2 text-sm"
                                :class="forms.challenge.sites.includes(site.domain) ? 'border-brand-500 bg-brand-50/50' : 'border-slate-200'"
                            >
                                <input
                                    type="checkbox"
                                    class="rounded text-brand-600"
                                    :checked="forms.challenge.sites.includes(site.domain)"
                                    @change="toggleSite(site.domain)"
                                />
                                <span class="truncate">{{ site.domain }}</span>
                                <span v-if="site.edge" class="shrink-0 text-xs text-slate-400">Cloudflare</span>
                            </label>
                        </div>
                        <p v-if="sites.some((x) => x.edge && forms.challenge.sites.includes(x.domain)) && !enabled('real_ip')" class="mt-2 text-xs text-amber-700">
                            A site behind Cloudflare needs real visitor addresses on first — or use Cloudflare’s own Under Attack mode for it.
                        </p>
                    </div>

                    <!-- Country rules. -->
                    <div v-else-if="FEATURES[key].kind === 'countries'" class="mt-4 space-y-3">
                        <fieldset class="flex flex-wrap gap-4 text-sm">
                            <legend class="sr-only">Mode</legend>
                            <label class="inline-flex items-center gap-2">
                                <input v-model="forms.countries.mode" type="radio" value="block" class="text-brand-600" />
                                Refuse these countries
                            </label>
                            <label class="inline-flex items-center gap-2">
                                <input v-model="forms.countries.mode" type="radio" value="allow" class="text-brand-600" />
                                Let in only these countries
                            </label>
                        </fieldset>
                        <div class="flex flex-wrap gap-2">
                            <span
                                v-for="code in forms.countries.codes"
                                :key="code"
                                class="inline-flex items-center gap-1.5 rounded-full bg-slate-100 py-1 pe-1 ps-3 text-xs font-medium text-slate-700"
                            >
                                {{ flagOf(code) }} {{ nameOf(code) }}
                                <button
                                    type="button"
                                    class="rounded-full p-1 text-slate-400 hover:bg-slate-200 hover:text-slate-700"
                                    :aria-label="`Remove ${nameOf(code)}`"
                                    @click="removeCountry(code)"
                                >
                                    ×
                                </button>
                            </span>
                            <span v-if="!forms.countries.codes.length" class="text-xs text-slate-500">No countries yet.</span>
                        </div>
                        <form class="flex gap-2" @submit.prevent="addCountries">
                            <input
                                v-model="countryInput"
                                type="text"
                                :class="field"
                                class="!mt-0 text-sm"
                                placeholder="Two-letter codes, e.g. CN RU KP"
                                autocapitalize="characters"
                                aria-label="Country codes to add"
                            />
                            <button
                                type="submit"
                                class="shrink-0 rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                            >
                                Add
                            </button>
                        </form>
                        <p v-if="forms.countries.mode === 'allow' && myIp" class="text-xs text-slate-500">
                            The panel itself is never refused, so you can always get back in to change this.
                        </p>
                        <p v-if="protection.countries_at && enabled(key)" class="text-xs text-slate-500">
                            Address ranges last downloaded {{ when(Number(protection.countries_at)) }}.
                        </p>
                    </div>

                    <!-- Site locks: one row per site. -->
                    <div v-else-if="FEATURES[key].kind === 'locks'" class="mt-4">
                        <p v-if="!locks.length" class="text-xs text-slate-500">There are no sites yet.</p>
                        <ul v-else class="divide-y divide-slate-100 rounded-lg ring-1 ring-slate-200">
                            <li v-for="site in locks" :key="site.domain" class="flex flex-wrap items-center justify-between gap-2 px-3 py-2.5">
                                <span class="min-w-0">
                                    <span class="block truncate text-sm font-medium text-slate-900">{{ site.domain }}</span>
                                    <span class="text-xs" :class="site.locked ? 'text-emerald-700' : site.relock_at ? 'text-amber-700' : 'text-slate-500'">
                                        <template v-if="site.locked">Locked</template>
                                        <template v-else-if="site.relock_at">Unlocked for updates — locks again in {{ minutesLeft(site.relock_at) }} min</template>
                                        <template v-else-if="!site.lockable">{{ site.why_not }}</template>
                                        <template v-else>Not locked</template>
                                    </span>
                                </span>
                                <span v-if="site.lockable" class="flex shrink-0 flex-wrap gap-2">
                                    <template v-if="site.locked">
                                        <button type="button" :class="smallButton" :disabled="busy !== ''" @click="lockAction(site, 'unlock_temp')">
                                            Unlock for 30 minutes
                                        </button>
                                        <button type="button" :class="smallButton" :disabled="busy !== ''" @click="lockAction(site, 'unlock')">Unlock</button>
                                    </template>
                                    <button
                                        v-else
                                        type="button"
                                        class="rounded-lg bg-slate-900 px-3 py-2 text-xs font-medium text-white hover:bg-slate-800 disabled:opacity-50 sm:py-1.5"
                                        :disabled="busy !== ''"
                                        @click="lockAction(site, 'lock')"
                                    >
                                        {{ busy === `lock:${site.domain}` ? 'Locking…' : site.relock_at ? 'Lock now' : 'Lock files' }}
                                    </button>
                                </span>
                            </li>
                        </ul>
                    </div>

                    <!-- What the backdoor watch found. -->
                    <div v-if="key === 'watch' && enabled('watch')" class="mt-4">
                        <p v-if="!watchChanges.length" class="text-xs text-emerald-700">Nothing has changed since it started watching.</p>
                        <template v-else>
                            <p class="text-xs font-semibold text-rose-700">{{ watchChanges.length }} change{{ watchChanges.length === 1 ? '' : 's' }} to review</p>
                            <ul class="mt-2 max-h-56 space-y-1.5 overflow-y-auto text-xs">
                                <li v-for="(c, i) in watchChanges" :key="i" class="rounded-lg bg-rose-50 px-3 py-2 text-rose-900">
                                    {{ c.summary }} <span class="text-rose-600">· {{ when(c.at) }}</span>
                                </li>
                            </ul>
                            <button
                                type="button"
                                class="mt-2 rounded-lg bg-white px-3 py-2 text-xs font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50 sm:py-1.5"
                                :disabled="busy !== ''"
                                @click="clearChanges('watch')"
                            >
                                These were me — mark as expected
                            </button>
                        </template>
                    </div>

                    <!-- What the code change monitor found. -->
                    <div v-if="key === 'integrity' && enabled('integrity')" class="mt-4">
                        <p v-if="!codeChanges.length" class="text-xs text-emerald-700">No unexpected code changes so far.</p>
                        <template v-else>
                            <ul class="max-h-64 space-y-2 overflow-y-auto text-xs">
                                <li
                                    v-for="(c, i) in codeChanges"
                                    :key="i"
                                    class="rounded-lg px-3 py-2"
                                    :class="c.in_uploads.length ? 'bg-rose-50 text-rose-900' : 'bg-amber-50 text-amber-900'"
                                >
                                    <span class="font-semibold">{{ c.site }}</span> — {{ codeCount(c) }} file{{ codeCount(c) === 1 ? '' : 's' }}
                                    <span class="opacity-75">· {{ when(c.at) }}</span>
                                    <span v-if="c.in_uploads.length" class="block font-semibold">
                                        PHP in an uploads folder — very likely a web shell: {{ c.in_uploads.slice(0, 3).join(', ') }}
                                    </span>
                                    <span class="block break-all opacity-90">
                                        <template v-if="c.added.length">Added: {{ c.added.slice(0, 5).join(', ') }}{{ c.added.length > 5 ? ` and ${c.added.length - 5} more` : '' }}. </template>
                                        <template v-if="c.changed.length">Changed: {{ c.changed.slice(0, 5).join(', ') }}{{ c.changed.length > 5 ? ` and ${c.changed.length - 5} more` : '' }}. </template>
                                        <template v-if="c.removed.length">Removed: {{ c.removed.slice(0, 5).join(', ') }}{{ c.removed.length > 5 ? ` and ${c.removed.length - 5} more` : '' }}.</template>
                                    </span>
                                </li>
                            </ul>
                            <button
                                type="button"
                                class="mt-2 rounded-lg bg-white px-3 py-2 text-xs font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50 sm:py-1.5"
                                :disabled="busy !== ''"
                                @click="clearChanges('code')"
                            >
                                Clear the list
                            </button>
                        </template>
                    </div>

                    <!-- Cloudflare's own Under Attack mode. -->
                    <div v-if="key === 'challenge' && sites.some((x) => x.edge)" class="mt-4 rounded-lg border border-slate-200 bg-slate-50 p-3">
                        <p class="text-xs font-medium text-slate-700">Behind Cloudflare? Its own Under Attack mode stops the flood before it reaches this server.</p>
                        <ul class="mt-2 space-y-1.5">
                            <li v-for="site in sites.filter((x) => x.edge)" :key="site.domain" class="flex items-center justify-between gap-2 text-sm">
                                <span class="truncate">{{ site.domain }}</span>
                                <button
                                    type="button"
                                    class="shrink-0 rounded-lg px-3 py-2 text-xs font-medium sm:py-1.5"
                                    :class="cfOn(site.domain) ? 'bg-rose-600 text-white hover:bg-rose-700' : 'bg-white text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50'"
                                    :disabled="busy !== ''"
                                    @click="cloudflareAttack(site.domain, !cfOn(site.domain))"
                                >
                                    {{ busy === `cf:${site.domain}` ? 'Asking Cloudflare…' : cfOn(site.domain) ? 'Cloudflare: on — turn off' : 'Turn on at Cloudflare' }}
                                </button>
                            </li>
                        </ul>
                    </div>

                    <!-- The scanner protection: what it bans for, editable. -->
                    <div v-if="key === 'web_scan'" class="mt-4 rounded-xl border border-slate-200 bg-slate-50/60">
                        <button
                            type="button"
                            class="flex w-full items-center justify-between gap-3 px-4 py-3 text-start text-sm font-medium text-slate-800"
                            :aria-expanded="pathsOpen"
                            @click="pathsOpen = !pathsOpen"
                        >
                            <span class="min-w-0">
                                What gets banned
                                <span class="block text-xs font-normal text-slate-500 sm:inline">
                                    <span class="hidden sm:inline"> · </span>{{ lines(pathForm.secret_paths).length }} secret paths ·
                                    {{ lines(pathForm.scan_paths).length }} scanner paths<template v-if="pathsCustom"> · edited</template>
                                </span>
                            </span>
                            <svg
                                class="h-4 w-4 shrink-0 text-slate-400 transition"
                                :class="pathsOpen ? 'rotate-180' : ''"
                                viewBox="0 0 20 20"
                                fill="currentColor"
                                aria-hidden="true"
                            >
                                <path
                                    fill-rule="evenodd"
                                    d="M5.23 7.21a.75.75 0 011.06.02L10 11.17l3.71-3.94a.75.75 0 111.08 1.04l-4.25 4.5a.75.75 0 01-1.08 0l-4.25-4.5a.75.75 0 01.02-1.06z"
                                    clip-rule="evenodd"
                                />
                            </svg>
                        </button>

                        <div v-if="pathsOpen" class="space-y-4 border-t border-slate-200 px-4 pb-4 pt-3">
                            <p class="text-xs text-slate-500">
                                One pattern a line, matched anywhere in the requested path (never the query string), ignoring case.
                                <code class="rounded bg-white px-1 font-mono ring-1 ring-slate-200">*</code> matches anything,
                                <code class="rounded bg-white px-1 font-mono ring-1 ring-slate-200">$</code> at the end means the path ends there.
                                Encoded dots and slashes (<span class="font-mono">%2e</span>, <span class="font-mono">%2f</span>) are caught too.
                                A pattern that would match ordinary pages — <span class="font-mono">/</span>, <span class="font-mono">.php</span>,
                                <span class="font-mono">/wp-admin/</span>, sitemaps — is refused.
                            </p>

                            <div class="grid gap-4 md:grid-cols-2">
                                <label class="block text-xs font-medium text-slate-700">
                                    <span class="flex flex-wrap items-baseline justify-between gap-x-2">
                                        Banned on the first request
                                        <button
                                            type="button"
                                            class="py-1 font-normal text-brand-600 hover:underline"
                                            @click="restorePaths('secret')"
                                        >
                                            Restore defaults
                                        </button>
                                    </span>
                                    <span class="block font-normal text-slate-500">
                                        Secrets — whatever the answer. Search engines confirmed by DNS are never banned.
                                    </span>
                                    <textarea
                                        v-model="pathForm.secret_paths"
                                        rows="9"
                                        spellcheck="false"
                                        autocapitalize="off"
                                        autocomplete="off"
                                        :class="field"
                                        class="font-mono text-xs"
                                        placeholder="/.env"
                                    />
                                </label>
                                <label class="block text-xs font-medium text-slate-700">
                                    <span class="flex flex-wrap items-baseline justify-between gap-x-2">
                                        Counted as scanning
                                        <button
                                            type="button"
                                            class="py-1 font-normal text-brand-600 hover:underline"
                                            @click="restorePaths('scan')"
                                        >
                                            Restore defaults
                                        </button>
                                    </span>
                                    <span class="block font-normal text-slate-500">
                                        Only when refused, missing or redirected; banned after the number above. Useful bots are not counted.
                                    </span>
                                    <textarea
                                        v-model="pathForm.scan_paths"
                                        rows="9"
                                        spellcheck="false"
                                        autocapitalize="off"
                                        autocomplete="off"
                                        :class="field"
                                        class="font-mono text-xs"
                                        placeholder="/vendor/phpunit"
                                    />
                                </label>
                            </div>

                            <div v-if="busy === '' && lastKey === 'scan_paths' && pathErrors.length" class="space-y-1">
                                <InputError v-for="message in pathErrors" :key="message" :message="message" />
                            </div>

                            <div class="flex flex-wrap items-center gap-3">
                                <button
                                    type="button"
                                    class="rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white transition hover:bg-slate-800 disabled:opacity-50"
                                    :disabled="busy !== '' || !pathsChanged"
                                    @click="savePaths"
                                >
                                    {{ busy === 'scan_paths' ? 'Saving…' : 'Save paths' }}
                                </button>
                                <span class="text-xs text-slate-500">
                                    {{ enabled('web_scan') ? 'Applied as soon as they are saved.' : 'Used once the protection is turned on.' }}
                                </span>
                            </div>
                        </div>
                    </div>

                    <div v-if="busy === '' && lastKey === key && Object.keys(errors).length" class="mt-2 space-y-1">
                        <InputError v-for="(message, field) in errors" :key="field" :message="message" />
                    </div>

                    <p v-if="FEATURES[key].kind === 'jail' && enabled(key)" class="mt-3 text-xs text-slate-500">
                        <template v-if="running(key)">
                            {{ banned(key) }} banned now · {{ total(key) }} since fail2ban started ·
                            <button type="button" class="font-medium text-brand-600 hover:underline" @click="emit('open', 'bans')">
                                Banned IPs
                            </button>
                        </template>
                        <span v-else class="text-amber-700">fail2ban is not running this jail right now.</span>
                    </p>
                    <p v-if="key === 'edge_only' && enabled(key) && !protection.edge_applied" class="mt-3 text-xs text-amber-700">
                        The firewall is off or opens 80/443 to everyone again; the panel puts the Cloudflare-only rules back within ten minutes.
                    </p>
                    <p v-if="key === 'waf' && enabled(key)" class="mt-3 text-xs text-slate-500">
                        {{ s.waf?.block ? 'Blocking.' : 'Detection only — nothing is refused. What it would refuse is in each site’s error log, as “ModSecurity: Warning”.' }}
                        <template v-if="s.waf?.block && s.waf?.max_retry > 0 && running(key)">
                            {{ banned(key) }} banned now ·
                            <button type="button" class="font-medium text-brand-600 hover:underline" @click="emit('open', 'bans')">Banned IPs</button>
                        </template>
                    </p>
                    <p v-if="key === 'no_ping' && enabled(key) && !protection.ping_applied" class="mt-3 text-xs text-amber-700">
                        The settings file is missing; the panel puts it back when it restarts.
                    </p>
                    <p v-if="key === 'kernel' && enabled(key) && !protection.kernel_applied" class="mt-3 text-xs text-amber-700">
                        The settings file is missing; the panel puts it back when it restarts.
                    </p>

                    <p v-if="blocked(key) && !enabled(key)" class="mt-3 text-xs text-amber-700">{{ blocked(key) }}</p>

                    <div
                        v-if="failure(key) && busy !== key"
                        class="mt-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-800"
                    >
                        <p class="font-semibold">
                            Could not turn it {{ failure(key).on ? 'on' : 'off' }}
                            <span class="font-normal text-rose-600">· {{ when(failure(key).at) }}</span>
                        </p>
                        <p class="mt-0.5 whitespace-pre-line break-words">{{ failure(key).message }}</p>
                        <p class="mt-1 text-rose-600">
                            Nothing was changed, so it is still {{ enabled(key) ? 'on' : 'off' }}. Fix the cause and try again.
                        </p>
                    </div>

                    <div v-if="FEATURES[key].kind !== 'locks'" class="mt-auto flex flex-wrap gap-2 pt-4">
                        <template v-if="enabled(key)">
                            <button
                                v-if="FEATURES[key].kind !== 'switch'"
                                type="button"
                                class="rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white transition hover:bg-slate-800 disabled:opacity-50"
                                :disabled="busy !== ''"
                                @click="submit(key, true)"
                            >
                                {{ busy === key ? 'Saving…' : 'Save' }}
                            </button>
                            <button
                                type="button"
                                class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                                :disabled="busy !== ''"
                                @click="submit(key, false)"
                            >
                                Turn off
                            </button>
                        </template>
                        <button
                            v-else
                            type="button"
                            class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                            :disabled="busy !== '' || Boolean(blocked(key)) || (key === 'waf' && !ready.waf)"
                            @click="submit(key, true)"
                        >
                            {{ busy === key ? 'Turning on…' : 'Turn on' }}
                        </button>
                    </div>
                </div>
            </div>
        </section>
    </div>
</template>
