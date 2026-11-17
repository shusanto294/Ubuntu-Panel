<script setup>
import { computed, onMounted, ref, watch } from 'vue';
import { Link } from '@inertiajs/vue3';
import EdgeAccessRules from './EdgeAccessRules.vue';
import EdgeRules from './EdgeRules.vue';
import { PERMISSION, SECURITY_LEVELS, isPermissionError } from './edge';
import { card, field } from './ui';
import { confirmAction } from '@/dialog';

/*
 * Security → Cloudflare: the rules Cloudflare enforces in front of the
 * server, for a zone on a connected DNS credential — the security level, WAF
 * custom rules, rate limiting rules and IP access rules — read from Cloudflare
 * and changed there, without opening its dashboard.
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

const accountId = ref(Number(params.get('account')) || props.accounts[0]?.id || null);
const zones = ref([]);
const zoneId = ref('');
const loadingZones = ref(false);
const loading = ref(false);
const error = ref('');
const data = ref(null);
const status = ref(null); // { text, failed, details }
let statusTimer = null;

const zone = computed(() => zones.value.find((z) => z.id === zoneId.value) ?? null);

const say = (text, failed = false, details = []) => {
    status.value = { text, failed, details };
    clearTimeout(statusTimer);
    // A list of zones that failed stays until it has been read.
    if (!details.length) statusTimer = setTimeout(() => (status.value = null), failed ? 12000 : 5000);
};

// ------------------------------------------------- every account's zones

// What "add to every site" picks from: the zones on every Cloudflare account
// connected, each marked when a site on this server is in it.
const allZones = ref([]);
const loadingAllZones = ref(false);

const hasSite = (zoneName) =>
    props.siteDomains.some((d) => d === zoneName || d.endsWith(`.${zoneName}`));

const loadAllZones = async () => {
    loadingAllZones.value = true;
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
            } catch {
                return [];
            }
        }),
    );
    allZones.value = lists.flat().sort((a, b) => a.name.localeCompare(b.name));
    loadingAllZones.value = false;
};

const current = computed(() => (zone.value ? { account: accountId.value, id: zone.value.id, name: zone.value.name } : null));

// The zone in the address bar, so a reload or a link lands on the same one.
const remember = () => {
    const url = new URL(window.location.href);
    url.searchParams.set('tab', 'cloudflare');
    if (accountId.value) url.searchParams.set('account', accountId.value);
    if (zone.value) url.searchParams.set('zone', zone.value.name);
    window.history.replaceState(window.history.state, '', url);
};

const loadZones = async () => {
    if (!accountId.value) return;

    loadingZones.value = true;
    error.value = '';
    zones.value = [];
    data.value = null;

    try {
        const { data: body } = await window.axios.get(route('dns.zones', accountId.value));
        zones.value = body.zones ?? [];
        const wanted = params.get('zone');
        zoneId.value = (zones.value.find((z) => z.name === wanted) ?? zones.value[0])?.id ?? '';
        if (!zones.value.length) error.value = 'This credential has no zones Cloudflare will show it.';
    } catch (e) {
        error.value = e?.response?.data?.error ?? 'Could not read the zones from Cloudflare.';
    } finally {
        loadingZones.value = false;
    }
};

const load = async () => {
    if (!accountId.value || !zoneId.value) return;

    loading.value = true;
    error.value = '';

    try {
        const { data: body } = await window.axios.get(route('edge.index', accountId.value), {
            params: { zone_id: zoneId.value },
        });
        data.value = body;
    } catch (e) {
        error.value = e?.response?.data?.error ?? 'Could not read the rules from Cloudflare.';
    } finally {
        loading.value = false;
    }
};

watch(accountId, loadZones);
watch(zoneId, () => {
    data.value = null;
    remember();
    load();
});

onMounted(() => {
    loadZones();
    if (props.accounts.length) loadAllZones();
});

// --------------------------------------------------------- security level

const savingLevel = ref(false);

const setLevel = async (level) => {
    if (level === data.value?.level) return;

    if (
        level === 'under_attack' &&
        !(await confirmAction(
            'Turn on Under Attack mode?',
            `Every visitor to ${zone.value?.name} will wait for Cloudflare's browser check before the site loads, and APIs, webhooks and uptime monitors will be stopped by it. Meant for while a flood lasts.`,
            'Turn it on',
        ))
    )
        return;

    savingLevel.value = true;
    try {
        const { data: body } = await window.axios.post(route('edge.level', accountId.value), {
            zone_id: zoneId.value,
            level,
        });
        data.value.level = level;
        say(body.message);
    } catch (e) {
        say(e?.response?.data?.error ?? 'Cloudflare refused the change.', true);
    } finally {
        savingLevel.value = false;
    }
};

const levelHelp = computed(() => SECURITY_LEVELS.find((l) => l.key === data.value?.level)?.help ?? '');

const count = computed(() =>
    data.value ? data.value.custom.length + data.value.ratelimit.length + data.value.access.length : 0,
);
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
            <!-- Which zone -->
            <div :class="card" class="flex flex-wrap items-end gap-4">
                <label v-if="accounts.length > 1" class="block text-sm">
                    <span class="text-xs font-medium text-slate-700">Cloudflare account</span>
                    <select v-model.number="accountId" :class="field">
                        <option v-for="a in accounts" :key="a.id" :value="a.id">{{ a.label }}</option>
                    </select>
                </label>
                <label class="block min-w-56 text-sm">
                    <span class="text-xs font-medium text-slate-700">Zone</span>
                    <select v-model="zoneId" :disabled="loadingZones || !zones.length" :class="field">
                        <option v-if="loadingZones" value="">Reading zones…</option>
                        <option v-for="z in zones" :key="z.id" :value="z.id">{{ z.name }}</option>
                    </select>
                </label>
                <button
                    type="button"
                    :disabled="loading || !zoneId"
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
                <p v-if="data" class="w-full text-xs text-slate-500">
                    {{ count }} rule{{ count === 1 ? '' : 's' }} on {{ zone?.name }}, as Cloudflare has them now.
                </p>
            </div>

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

            <div v-if="loading && !data" :class="card" class="py-10 text-center text-sm text-slate-500">
                Reading the rules from Cloudflare…
            </div>

            <template v-if="data">
                <!-- Security level -->
                <section :class="card">
                    <div class="flex flex-wrap items-start justify-between gap-4">
                        <div class="max-w-2xl">
                            <h3 class="font-semibold text-slate-900">Security level</h3>
                            <p class="text-sm text-slate-500">
                                How suspicious a visitor's address has to look, by Cloudflare's own reputation data, before
                                it is challenged.
                            </p>
                        </div>
                        <select
                            v-if="!data.errors.level"
                            :value="data.level"
                            :disabled="savingLevel"
                            class="rounded-lg border-slate-300 text-sm shadow-sm"
                            :class="data.level === 'under_attack' ? 'text-rose-700' : ''"
                            @change="setLevel($event.target.value)"
                        >
                            <option v-for="l in SECURITY_LEVELS" :key="l.key" :value="l.key">{{ l.label }}</option>
                        </select>
                    </div>
                    <p v-if="!data.errors.level" class="mt-2 text-xs" :class="data.level === 'under_attack' ? 'text-rose-700' : 'text-slate-500'">
                        {{ levelHelp }}
                    </p>
                    <div v-else class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800 ring-1 ring-amber-200">
                        <p>{{ data.errors.level }}</p>
                        <p v-if="isPermissionError(data.errors.level)" class="mt-1 text-xs">
                            The API token needs <strong>{{ PERMISSION.level }}</strong>.
                        </p>
                    </div>
                </section>

                <EdgeRules
                    :account-id="accountId"
                    :zone-id="zoneId"
                    phase="custom"
                    :rules="data.custom"
                    :error="data.errors.custom"
                    :my-ip="myIp"
                    :all-zones="allZones"
                    :current="current"
                    :loading-zones="loadingAllZones"
                    @changed="load"
                    @message="say"
                />

                <EdgeRules
                    :account-id="accountId"
                    :zone-id="zoneId"
                    phase="ratelimit"
                    :rules="data.ratelimit"
                    :error="data.errors.ratelimit"
                    :my-ip="myIp"
                    :all-zones="allZones"
                    :current="current"
                    :loading-zones="loadingAllZones"
                    @changed="load"
                    @message="say"
                />

                <EdgeAccessRules
                    :account-id="accountId"
                    :zone-id="zoneId"
                    :rules="data.access"
                    :error="data.errors.access"
                    :my-ip="myIp"
                    :all-zones="allZones"
                    :current="current"
                    :loading-zones="loadingAllZones"
                    @changed="load"
                    @message="say"
                />
            </template>
        </template>
    </div>
</template>
