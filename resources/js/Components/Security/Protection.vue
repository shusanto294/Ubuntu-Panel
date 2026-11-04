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
        text: 'What reaches the sites. Bans apply to the web ports only. For a site behind Cloudflare a ban cannot stop the visitor — their requests arrive from Cloudflare — so the rate limits, with real visitor addresses on, are what protect it.',
        features: ['web_login', 'rate_login', 'web_scan', 'rate_flood', 'real_ip'],
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
        text: 'Bans an address asking for files only an attacker looks for — .env, .git, phpunit, backup.sql, web shells — and getting refused.',
        jails: ['panel-web-scan'],
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
        text: 'Caps how many PHP or app requests one address can make. Images, CSS and JS are never limited, nor are the panel, Nextcloud and n8n. Set it well above what a real visitor does.',
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

// ------------------------------------------------------------------ scan

const scan = computed(() => props.protection.scan);
const scanning = computed(() => props.protection.scan_running);

const scanNow = () =>
    router.post(route('security.scan'), {}, { preserveScroll: true, onStart: () => (busy.value = 'scan-now'), onFinish: () => (busy.value = '') });

const findingFilter = ref('all');

const findings = computed(() =>
    (scan.value?.findings ?? []).filter((f) => findingFilter.value === 'all' || f.kind === findingFilter.value),
);

const findingKinds = computed(() => {
    const counts = {};
    for (const f of scan.value?.findings ?? []) counts[f.kind] = (counts[f.kind] ?? 0) + 1;
    return counts;
});

const reload = () => router.reload({ only: ['protection'], preserveScroll: true });
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
                — your address is <span class="font-mono">{{ myIp }}</span></span>.
        </p>

        <section v-for="section in SECTIONS" :key="section.title">
            <h3 class="text-sm font-semibold uppercase tracking-wide text-slate-500">{{ section.title }}</h3>
            <p class="mt-0.5 text-sm text-slate-500">{{ section.text }}</p>

            <div class="mt-3 grid gap-4 lg:grid-cols-2">
                <div v-for="key in section.features" :key="key" :class="[card, STATE[state(key)].card]" class="flex flex-col">
                    <div class="flex items-start justify-between gap-3">
                        <h4 class="flex flex-wrap items-center gap-2 font-semibold text-slate-900">
                            {{ FEATURES[key].title }}
                            <span
                                v-if="key === 'real_ip' && protection.edge_sites?.length && !enabled(key)"
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

        <!-- The malware scan. -->
        <section>
            <h3 class="text-sm font-semibold uppercase tracking-wide text-slate-500">Malware scan</h3>
            <p class="mt-0.5 text-sm text-slate-500">
                Looks through every PHP site for WordPress files that differ from wordpress.org’s, PHP files in
                uploads, and code shaped like a web shell or backdoor. It reports; it never changes or deletes
                anything. It runs at the lowest CPU and disk priority.
            </p>

            <div :class="[card, s.scan ? STATE.on.card : '']" class="mt-3">
                <p v-if="failure('scan')" class="mb-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-800">
                    Could not turn it {{ failure('scan').on ? 'on' : 'off' }}: {{ failure('scan').message }}
                </p>
                <div class="flex flex-wrap items-start justify-between gap-4">
                    <div>
                        <h4 class="flex items-center gap-2 font-semibold text-slate-900">
                            Scan every night
                            <span
                                class="inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-semibold"
                                :class="STATE[s.scan ? 'on' : 'off'].badge"
                            >
                                <span class="h-2 w-2 rounded-full" :class="STATE[s.scan ? 'on' : 'off'].dot" />
                                {{ STATE[s.scan ? 'on' : 'off'].label }}
                            </span>
                        </h4>
                        <p class="mt-1 text-sm text-slate-500">
                            Between 2 and 6 in the morning, server time. New findings are emailed if the malware
                            alert is on (Alerts tab).
                        </p>
                        <p v-if="!ready.wp_cli" class="mt-1 text-xs text-amber-700">
                            wp-cli is not installed, so WordPress files are not compared with wordpress.org; the
                            other checks still run.
                        </p>
                    </div>
                    <div class="flex flex-wrap gap-2">
                        <button
                            type="button"
                            class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                            :disabled="busy !== '' || scanning"
                            @click="scanNow"
                        >
                            {{ scanning ? 'Scanning…' : 'Scan now' }}
                        </button>
                        <button
                            v-if="s.scan"
                            type="button"
                            class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                            :disabled="busy !== ''"
                            @click="submit('scan', false)"
                        >
                            Turn off
                        </button>
                        <button
                            v-else
                            type="button"
                            class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                            :disabled="busy !== ''"
                            @click="submit('scan', true)"
                        >
                            Turn on
                        </button>
                    </div>
                </div>

                <div class="mt-5 border-t border-slate-100 pt-4">
                    <p v-if="scanning" class="text-sm text-slate-600">
                        A scan is running.
                        <button type="button" class="font-medium text-brand-600 hover:underline" @click="reload">Check again</button>
                    </p>
                    <p v-else-if="!scan" class="text-sm text-slate-500">No scan has run yet.</p>
                    <template v-else>
                        <p class="text-sm text-slate-600">
                            Last scan {{ when(scan.finished_at) }} · {{ scan.sites }} site{{ scan.sites === 1 ? '' : 's' }} ·
                            <span :class="scan.findings.length ? 'font-medium text-rose-600' : 'text-emerald-700'">
                                {{ scan.findings.length ? `${scan.findings.length} file(s) to review` : 'nothing found' }}
                            </span>
                        </p>
                        <p v-for="error in scan.errors" :key="error" class="mt-1 text-xs text-amber-700">{{ error }}</p>

                        <div v-if="scan.findings.length" class="mt-3">
                            <div class="flex flex-wrap gap-2">
                                <button
                                    type="button"
                                    class="rounded-lg px-3 py-1 text-xs font-medium ring-1 transition"
                                    :class="findingFilter === 'all' ? 'bg-slate-900 text-white ring-slate-900' : 'bg-white text-slate-600 ring-slate-200'"
                                    @click="findingFilter = 'all'"
                                >
                                    All · {{ scan.findings.length }}
                                </button>
                                <button
                                    v-for="(count, kind) in findingKinds"
                                    :key="kind"
                                    type="button"
                                    class="rounded-lg px-3 py-1 text-xs font-medium ring-1 transition"
                                    :class="findingFilter === kind ? 'bg-slate-900 text-white ring-slate-900' : 'bg-white text-slate-600 ring-slate-200'"
                                    @click="findingFilter = kind"
                                >
                                    {{ protection.finding_labels[kind] ?? kind }} · {{ count }}
                                </button>
                            </div>
                            <div class="mt-3 max-h-[28rem] overflow-auto rounded-xl ring-1 ring-slate-200">
                                <table class="w-full text-sm">
                                    <thead class="sticky top-0 bg-slate-50 text-left text-xs uppercase text-slate-400">
                                        <tr>
                                            <th class="px-4 py-2">Site</th>
                                            <th class="px-4 py-2">File</th>
                                            <th class="px-4 py-2">Why</th>
                                        </tr>
                                    </thead>
                                    <tbody class="divide-y divide-slate-100">
                                        <tr v-for="f in findings" :key="f.site + f.kind + f.path" class="align-top">
                                            <td class="whitespace-nowrap px-4 py-2 text-xs text-slate-500">{{ f.site }}</td>
                                            <td class="break-all px-4 py-2 font-mono text-xs text-slate-900">{{ f.path }}</td>
                                            <td class="px-4 py-2 text-xs text-slate-600">
                                                <span class="font-medium">{{ protection.finding_labels[f.kind] ?? f.kind }}.</span>
                                                {{ f.detail }}
                                            </td>
                                        </tr>
                                    </tbody>
                                </table>
                            </div>
                            <p class="mt-2 text-xs text-slate-500">
                                A finding is something to look at, not proof: commercial plugins sometimes obfuscate
                                their code, and a premium plugin cannot be compared with wordpress.org. Open the file
                                in the site’s file manager before deleting anything.
                            </p>
                        </div>
                    </template>
                </div>
            </div>
        </section>
    </div>
</template>
