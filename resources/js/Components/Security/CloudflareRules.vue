<script setup>
import { computed, onMounted, ref } from 'vue';
import { Link } from '@inertiajs/vue3';
import EdgeAccessRules from './EdgeAccessRules.vue';
import EdgeRules from './EdgeRules.vue';
import NotFoundGuard from './NotFoundGuard.vue';
import ZoneErrors from './ZoneErrors.vue';
import { PERMISSION, SECURITY_LEVELS } from './edge';
import { card, field } from './ui';
import { confirmAction } from '@/dialog';

/*
 * Security → Cloudflare: the rules Cloudflare enforces in front of the
 * server — the security level, WAF custom rules, rate limiting rules and IP
 * access rules — read from Cloudflare and changed there, without opening its
 * dashboard.
 *
 * It opens on every zone that holds a site on this server, across every
 * Cloudflare account connected, with the same rule in several zones shown
 * once: adding, changing and deleting then happen in all of them together.
 * One zone can be picked to see and order its rules alone.
 *
 * Only Cloudflare: it proxies the requests to the sites it resolves. The other
 * DNS hosts the panel writes to answer DNS queries and never see a request,
 * so there is nothing of theirs to manage here.
 */
const props = defineProps({
    accounts: { type: Array, default: () => [] },
    myIp: { type: String, default: '' },
    siteDomains: { type: Array, default: () => [] },
});

const params = new URLSearchParams(window.location.search);

const ALL = 'sites';

const scope = ref(''); // ALL, or a zone id
const loading = ref(false);
const error = ref('');
const loaded = ref([]); // each zone's rules, as edge.all answers
const status = ref(null); // { text, failed, details }
let statusTimer = null;

const say = (text, failed = false, details = []) => {
    status.value = { text, failed, details };
    clearTimeout(statusTimer);
    // A list of zones that failed stays until it has been read.
    if (!details.length) statusTimer = setTimeout(() => (status.value = null), failed ? 12000 : 5000);
};

// ------------------------------------------------- every account's zones

// Every zone on every Cloudflare account connected, each marked when a site
// on this server is in it.
const allZones = ref([]);
const loadingZones = ref(true);

const hasSite = (zoneName) => props.siteDomains.some((d) => d === zoneName || d.endsWith(`.${zoneName}`));

const zoneErrors = ref([]);

const loadAllZones = async () => {
    loadingZones.value = true;
    zoneErrors.value = [];
    const lists = await Promise.all(
        props.accounts.map(async (account) => {
            try {
                const { data: body } = await window.axios.get(route('dns.zones', account.id));
                return (body.zones ?? []).map((z) => ({
                    account: account.id,
                    account_label: account.label,
                    id: z.id,
                    name: z.name,
                    has_site: hasSite(z.name),
                }));
            } catch (e) {
                zoneErrors.value.push({ zone: account.label, error: e?.response?.data?.error ?? 'Could not read its zones.' });
                return [];
            }
        }),
    );
    allZones.value = lists.flat().sort((a, b) => b.has_site - a.has_site || a.name.localeCompare(b.name));
    loadingZones.value = false;
};

const siteZones = computed(() => allZones.value.filter((z) => z.has_site));
const otherZones = computed(() => allZones.value.filter((z) => !z.has_site));
const multiAccount = computed(() => props.accounts.length > 1);

const single = computed(() => scope.value !== ALL);
const zone = computed(() => (single.value ? allZones.value.find((z) => z.id === scope.value) ?? null : null));
const current = computed(() => (zone.value ? { account: zone.value.account, id: zone.value.id, name: zone.value.name } : null));

const scopeZones = computed(() => (single.value ? (zone.value ? [zone.value] : []) : siteZones.value));
const asTarget = (z) => ({ account: z.account, zone_id: z.id, zone_name: z.name });

// The zone or "all" in the address bar, so a reload or a link lands on it.
const remember = () => {
    const url = new URL(window.location.href);
    url.searchParams.set('tab', 'cloudflare');
    url.searchParams.delete('account');
    url.searchParams.set('zone', zone.value ? zone.value.name : 'all');
    window.history.replaceState(window.history.state, '', url);
};

const load = async () => {
    const targets = scopeZones.value.map(asTarget);
    if (!targets.length) {
        loaded.value = [];
        return;
    }

    loading.value = true;
    error.value = '';

    try {
        const { data: body } = await window.axios.post(route('edge.all'), { targets });
        loaded.value = body.zones ?? [];
    } catch (e) {
        error.value = e?.response?.data?.error ?? 'Could not read the rules from Cloudflare.';
    } finally {
        loading.value = false;
    }
};

const choose = (value) => {
    scope.value = value;
    loaded.value = [];
    remember();
    load();
};

onMounted(async () => {
    if (!props.accounts.length) return;
    await loadAllZones();

    const wanted = params.get('zone');
    const named = allZones.value.find((z) => z.name === wanted);
    const first = named?.id ?? (siteZones.value.length ? ALL : allZones.value[0]?.id ?? '');
    if (!first) {
        error.value = zoneErrors.value.length ? '' : 'The Cloudflare credentials connected have no zones Cloudflare will show them.';
        return;
    }
    choose(first);
});

// ------------------------------------------- the same rule in several zones

const target = (z) => ({ account: z.account, zone_id: z.zone_id, zone_name: z.zone_name });

const errorsOf = (key) => loaded.value.filter((z) => z.errors?.[key]).map((z) => ({ zone: z.zone_name, error: z.errors[key] }));
const readable = (key) => loaded.value.filter((z) => !z.errors?.[key]);

// A rule is the same in two zones when its name, expression, action and
// counting are; whether it is on can differ.
const ruleGroups = (phase) => {
    const groups = new Map();
    for (const z of readable(phase)) {
        for (const rule of z[phase]) {
            const limit = rule.ratelimit
                ? [rule.ratelimit.period, rule.ratelimit.requests_per_period, rule.ratelimit.mitigation_timeout].join('/')
                : '';
            const key = [phase, (rule.description ?? '').toLowerCase(), rule.expression, rule.action, limit].join('\u0000');
            if (!groups.has(key)) groups.set(key, { key, rule, copies: [] });
            groups.get(key).copies.push({ ...target(z), rule_id: rule.id, enabled: rule.enabled });
        }
    }

    const zones = readable(phase);
    return [...groups.values()].map((g) => ({
        ...g,
        enabledCount: g.copies.filter((c) => c.enabled).length,
        missing: zones.filter((z) => !g.copies.some((c) => c.zone_id === z.zone_id)).map(target),
    }));
};

// An access rule is the same when it names the same thing and does the same.
// The account's own rules show in each of its zones, and are one rule.
const accessGroups = computed(() => {
    const groups = new Map();
    for (const z of readable('access')) {
        for (const rule of z.access) {
            const readonly = rule.scope !== 'zone';
            const key = readonly ? `account\u0000${rule.id}` : ['zone', rule.target, rule.value.toLowerCase(), rule.mode].join('\u0000');
            if (!groups.has(key)) groups.set(key, { key, rule, copies: [], readonly });
            groups.get(key).copies.push({ ...target(z), rule_id: rule.id });
        }
    }

    const zones = readable('access');
    return [...groups.values()]
        .map((g) => ({
            ...g,
            missing: g.readonly ? [] : zones.filter((z) => !g.copies.some((c) => c.zone_id === z.zone_id)).map(target),
        }))
        .sort((a, b) => a.readonly - b.readonly);
});

const custom = computed(() => ruleGroups('custom'));
const ratelimit = computed(() => ruleGroups('ratelimit'));

const count = computed(() => custom.value.length + ratelimit.value.length + accessGroups.value.length);

// --------------------------------------------------------- security level

const savingLevel = ref(false);

const levels = computed(() => {
    const byLevel = new Map();
    for (const z of readable('level')) byLevel.set(z.level, [...(byLevel.get(z.level) ?? []), z.zone_name]);
    return [...byLevel.entries()].map(([level, zones]) => ({ level, zones }));
});

const level = computed(() => (levels.value.length === 1 ? levels.value[0].level : ''));
const levelLabel = (key) => SECURITY_LEVELS.find((l) => l.key === key)?.label ?? key;
const levelHelp = computed(() => SECURITY_LEVELS.find((l) => l.key === level.value)?.help ?? '');

const setLevel = async (value) => {
    if (!value || value === level.value) return;

    const zones = readable('level');
    const where = zones.length === 1 ? zones[0].zone_name : `all ${zones.length} zones`;

    if (
        value === 'under_attack' &&
        !(await confirmAction(
            'Turn on Under Attack mode?',
            `Every visitor to ${where} will wait for Cloudflare's browser check before the site loads, and APIs, webhooks and uptime monitors will be stopped by it. Meant for while a flood lasts.`,
            'Turn it on',
        ))
    )
        return;

    savingLevel.value = true;
    try {
        const { data: body } = await window.axios.post(route('edge.bulk.level'), { level: value, targets: zones.map(target) });
        const failures = (body.outcomes ?? []).filter((o) => o.status === 'failed');
        say(body.message, failures.length > 0, failures);
        load();
    } catch (e) {
        say(e?.response?.data?.error ?? 'Cloudflare refused the change.', true);
    } finally {
        savingLevel.value = false;
    }
};
</script>

<template>
    <div class="space-y-6">
        <!-- No Cloudflare credential -->
        <div v-if="!accounts.length" :class="card">
            <h3 class="font-semibold text-slate-900">Cloudflare security rules</h3>
            <p class="mt-1 max-w-2xl text-sm text-slate-500">
                Connect a Cloudflare account on the DNS page and its zones' firewall rules, rate limits, IP access rules
                and security level can be managed here. The token needs Zone · DNS · Edit for the records, and for the
                rules {{ PERMISSION.custom }}, {{ PERMISSION.access }} and {{ PERMISSION.level }}.
            </p>
            <p class="mt-2 max-w-2xl text-xs text-slate-400">
                DigitalOcean, Linode, Vultr, Hetzner DNS and Porkbun only answer DNS queries — requests never pass through
                them — so they have no security rules to manage.
            </p>
            <Link
                :href="route('dns.create')"
                class="mt-4 inline-block rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700"
            >
                Connect Cloudflare
            </Link>
        </div>

        <template v-else>
            <!-- Every site domain at once, whichever zone is picked below -->
            <NotFoundGuard />

            <!-- Which zones -->
            <div :class="card" class="flex flex-wrap items-end gap-4">
                <label class="block min-w-64 text-sm">
                    <span class="text-xs font-medium text-slate-700">Showing</span>
                    <select :value="scope" :disabled="loadingZones || !allZones.length" :class="field" @change="choose($event.target.value)">
                        <option v-if="loadingZones" value="">Reading zones…</option>
                        <option v-if="siteZones.length" value="sites">
                            All site domains ({{ siteZones.length }} zone{{ siteZones.length === 1 ? '' : 's' }})
                        </option>
                        <optgroup v-if="siteZones.length" label="One site domain">
                            <option v-for="z in siteZones" :key="z.id" :value="z.id">
                                {{ z.name }}{{ multiAccount ? ` — ${z.account_label}` : '' }}
                            </option>
                        </optgroup>
                        <optgroup v-if="otherZones.length" label="Other zones on Cloudflare">
                            <option v-for="z in otherZones" :key="z.id" :value="z.id">
                                {{ z.name }}{{ multiAccount ? ` — ${z.account_label}` : '' }}
                            </option>
                        </optgroup>
                    </select>
                </label>
                <button
                    type="button"
                    :disabled="loading || !scope"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                    @click="load"
                >
                    {{ loading ? 'Reading…' : 'Reload' }}
                </button>
                <a
                    v-if="zone"
                    :href="`https://dash.cloudflare.com/?to=/:account/${zone.name}/security`"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="ml-auto text-xs font-medium text-slate-500 hover:text-brand-600 hover:underline"
                >
                    Open in Cloudflare ↗
                </a>
                <p v-if="loaded.length" class="w-full text-xs text-slate-500">
                    <template v-if="single">{{ count }} rule{{ count === 1 ? '' : 's' }} on {{ zone?.name }}, as Cloudflare has them now.</template>
                    <template v-else>
                        {{ count }} rule{{ count === 1 ? '' : 's' }} across {{ loaded.length }} site domain{{ loaded.length === 1 ? '' : 's' }}:
                        {{ loaded.map((z) => z.zone_name).join(', ') }}. A rule on several of them is shown once, and
                        changes to it go to each.
                    </template>
                </p>
            </div>

            <ZoneErrors :errors="zoneErrors" />

            <div
                v-if="status"
                class="rounded-xl px-4 py-2.5 text-sm ring-1"
                :class="status.failed ? 'bg-rose-50 text-rose-700 ring-rose-200' : 'bg-emerald-50 text-emerald-700 ring-emerald-200'"
                role="status"
            >
                {{ status.text }}
                <ul v-if="status.details?.length" class="mt-2 space-y-1 text-xs">
                    <li v-for="d in status.details" :key="d.zone">
                        <strong>{{ d.zone }}</strong>: {{ d.error }}
                    </li>
                </ul>
                <button
                    v-if="status.details?.length"
                    type="button"
                    class="mt-2 text-xs font-medium underline"
                    @click="status = null"
                >
                    Dismiss
                </button>
            </div>

            <p v-if="error" class="rounded-xl bg-rose-50 px-4 py-3 text-sm text-rose-700 ring-1 ring-rose-200">{{ error }}</p>

            <div v-if="(loading || loadingZones) && !loaded.length" :class="card" class="py-10 text-center text-sm text-slate-500">
                {{ loadingZones ? 'Reading the zones on Cloudflare…' : `Reading the rules of ${scopeZones.length} zone${scopeZones.length === 1 ? '' : 's'} from Cloudflare…` }}
            </div>

            <template v-if="loaded.length">
                <!-- Security level -->
                <section :class="card">
                    <div class="flex flex-wrap items-start justify-between gap-4">
                        <div class="max-w-2xl">
                            <h3 class="font-semibold text-slate-900">Security level</h3>
                            <p class="text-sm text-slate-500">
                                How suspicious a visitor's address has to look, by Cloudflare's own reputation data, before
                                it is challenged.{{ single ? '' : ' Choosing one sets it on every zone shown.' }}
                            </p>
                        </div>
                        <select
                            v-if="levels.length"
                            :value="level"
                            :disabled="savingLevel"
                            class="rounded-lg border-slate-300 text-sm shadow-sm"
                            :class="level === 'under_attack' ? 'text-rose-700' : ''"
                            @change="setLevel($event.target.value)"
                        >
                            <option v-if="!level" value="" disabled>Differs by zone</option>
                            <option v-for="l in SECURITY_LEVELS" :key="l.key" :value="l.key">{{ l.label }}</option>
                        </select>
                    </div>
                    <p v-if="level" class="mt-2 text-xs" :class="level === 'under_attack' ? 'text-rose-700' : 'text-slate-500'">
                        {{ levelHelp }}
                    </p>
                    <ul v-else-if="levels.length" class="mt-2 space-y-0.5 text-xs text-slate-600">
                        <li v-for="l in levels" :key="l.level">
                            <strong :class="l.level === 'under_attack' ? 'text-rose-700' : ''">{{ levelLabel(l.level) }}</strong>
                            on {{ l.zones.join(', ') }}
                        </li>
                    </ul>
                    <ZoneErrors :errors="errorsOf('level')" :permission="PERMISSION.level" />
                </section>

                <EdgeRules
                    phase="custom"
                    :groups="custom"
                    :errors="errorsOf('custom')"
                    :zone-count="readable('custom').length"
                    :single="single"
                    :my-ip="myIp"
                    :all-zones="allZones"
                    :current="current"
                    :loading-zones="loadingZones"
                    @changed="load"
                    @message="say"
                />

                <EdgeRules
                    phase="ratelimit"
                    :groups="ratelimit"
                    :errors="errorsOf('ratelimit')"
                    :zone-count="readable('ratelimit').length"
                    :single="single"
                    :my-ip="myIp"
                    :all-zones="allZones"
                    :current="current"
                    :loading-zones="loadingZones"
                    @changed="load"
                    @message="say"
                />

                <EdgeAccessRules
                    :groups="accessGroups"
                    :errors="errorsOf('access')"
                    :zone-count="readable('access').length"
                    :single="single"
                    :my-ip="myIp"
                    :all-zones="allZones"
                    :current="current"
                    :loading-zones="loadingZones"
                    @changed="load"
                    @message="say"
                />
            </template>
        </template>
    </div>
</template>
