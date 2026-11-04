<script setup>
import { computed, reactive, ref } from 'vue';
import InputError from '@/Components/InputError.vue';
import { confirmAction } from '@/dialog';
import { card, field, when } from './ui';
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
    { value: 300, label: '5 minutes' },
    { value: 600, label: '10 minutes' },
    { value: 3600, label: '1 hour' },
    { value: 86400, label: '1 day' },
];

const SECTIONS = [
    {
        title: 'Websites',
        text: 'What reaches the sites. A banned address is refused by the firewall on the web ports, and by nginx itself — which is what stops it on a site behind Cloudflare, where its requests arrive from Cloudflare’s addresses. For that, turn on real visitor addresses.',
        features: ['web_login', 'rate_login', 'web_scan', 'rate_flood', 'real_ip', 'edge_only'],
    },
    { title: 'Mail', text: 'Password guessing against mailboxes.', features: ['mail'] },
    { title: 'Server', text: 'The machine as a whole.', features: ['recidive', 'kernel'] },
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

const busy = ref('');
const lastKey = ref('');

const enabled = (key) => (typeof s.value[key] === 'object' ? s.value[key].enabled : Boolean(s.value[key]));

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
        { ...(forms[key] ?? {}), feature: key, enabled: on },
        {
            preserveScroll: true,
            onStart: () => (busy.value = key),
            onFinish: () => (busy.value = ''),
        },
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
        <div :class="card" class="flex flex-wrap items-center justify-between gap-3 !py-4">
            <p class="text-sm text-slate-700">
                <span class="text-lg font-semibold text-slate-900">{{ onCount }} of {{ Object.keys(FEATURES).length }}</span>
                protections are on.
                <span class="text-slate-500">A card with a green border and a green <b>On</b> badge is protecting the server now.</span>
            </p>
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
                <div v-for="key in section.features" :key="key" :class="[card, STATE[state(key)].card]" class="flex flex-col">
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
                    <p class="mt-1 text-sm text-slate-500">{{ FEATURES[key].text }}</p>

                    <!-- A jail: how many, over how long, banned for how long. -->
                    <div v-if="FEATURES[key].kind === 'jail'" class="mt-4 grid gap-3 sm:grid-cols-3">
                        <label class="block text-xs font-medium text-slate-700">
                            Ban after
                            <input v-model.number="forms[key].max_retry" type="number" min="1" max="1000" :class="field" class="text-sm" />
                            <span class="font-normal text-slate-400">{{ FEATURES[key].unit }}</span>
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

                    <div class="mt-auto flex flex-wrap gap-2 pt-4">
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
                            :disabled="busy !== '' || Boolean(blocked(key))"
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
