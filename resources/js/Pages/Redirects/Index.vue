<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import { Head, Link } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';
import { ago, card, field, smallButton } from '@/Components/Security/ui';

/*
 * Redirects Cloudflare answers at its edge, for any domain on a connected
 * Cloudflare account — a site here or not. Pick a domain, see its redirects,
 * add one. Adding makes the hostname resolve through Cloudflare when it does
 * not already, since Cloudflare can only redirect a request it sees.
 */
const props = defineProps({
    accounts: { type: Array, default: () => [] },
    codes: { type: Array, default: () => [301, 302, 307, 308] },
});

const CODE_LABEL = {
    301: '301 · Permanent',
    302: '302 · Temporary',
    307: '307 · Temporary, keeps the method',
    308: '308 · Permanent, keeps the method',
};

const STORAGE_KEY = 'panel:redirects-zone';

const zones = ref([]); // { account, account_label, id, name }
const zoneErrors = ref([]);
const loadingZones = ref(false);
const zoneId = ref('');
const zoneFilter = ref('');

const list = ref([]);
const fetchedAt = ref(0);
const loading = ref(false);
const listError = ref('');

const message = ref('');
const notes = ref([]);

const zone = computed(() => zones.value.find((z) => z.id === zoneId.value) ?? null);
const visibleZones = computed(() => {
    const q = zoneFilter.value.trim().toLowerCase();
    return q ? zones.value.filter((z) => z.name.includes(q) || z.id === zoneId.value) : zones.value;
});
const multipleAccounts = computed(() => new Set(zones.value.map((z) => z.account)).size > 1);

const failure = (e, fallback) => e?.response?.data?.error ?? fallback;

const loadZones = async (refresh = false) => {
    loadingZones.value = true;
    try {
        const { data } = await window.axios.get(route('edge.zones') + (refresh ? '?refresh=1' : ''));
        const all = [];
        const errors = [];
        for (const account of data.accounts ?? []) {
            if (account.error) errors.push(`${account.label}: ${account.error}`);
            for (const z of account.zones ?? []) {
                all.push({ account: account.id, account_label: account.label, id: z.id, name: z.name });
            }
        }
        all.sort((a, b) => a.name.localeCompare(b.name));
        zones.value = all;
        zoneErrors.value = errors;

        if (!all.some((z) => z.id === zoneId.value)) {
            let remembered = '';
            try {
                remembered = window.localStorage.getItem(STORAGE_KEY) ?? '';
            } catch {
                // Private windows may refuse; start from the first domain.
            }
            zoneId.value = all.some((z) => z.id === remembered) ? remembered : (all[0]?.id ?? '');
        }
    } catch (e) {
        zoneErrors.value = [failure(e, 'Could not list the Cloudflare domains.')];
    } finally {
        loadingZones.value = false;
    }
};

const take = (data) => {
    list.value = data.redirects ?? [];
    fetchedAt.value = data.fetched_at ?? 0;
};

const loadRedirects = async (refresh = false) => {
    if (!zone.value) return;
    const asked = zone.value.id;
    loading.value = true;
    listError.value = '';
    try {
        const { data } = await window.axios.get(route('redirects.list', zone.value.account), {
            params: { zone_id: zone.value.id, zone_name: zone.value.name, ...(refresh ? { refresh: 1 } : {}) },
        });
        if (asked === zoneId.value) take(data);
    } catch (e) {
        if (asked === zoneId.value) {
            list.value = [];
            listError.value = failure(e, 'Cloudflare did not answer.');
        }
    } finally {
        loading.value = false;
    }
};

watch(zoneId, (id) => {
    message.value = '';
    notes.value = [];
    closeForm();
    list.value = [];
    try {
        if (id) window.localStorage.setItem(STORAGE_KEY, id);
    } catch {
        // Only a convenience.
    }
    loadRedirects();
});

onMounted(() => {
    if (props.accounts.length) loadZones();
});

// ------------------------------------------------------------------ form

const blank = () => ({
    sub: '',
    include_www: false,
    path_mode: 'all',
    path: '',
    target: 'https://',
    status_code: 301,
    keep_path: false,
    keep_query: true,
    proxy: false,
    enabled: true,
});

const form = reactive(blank());
const formOpen = ref(false);
const editing = ref(null); // the redirect being edited, or null
const formError = ref('');
const needsProxy = ref(false);
const saving = ref(false);

const host = computed(() => {
    const sub = form.sub.trim().toLowerCase().replace(/\.+$/, '');
    return sub ? `${sub}.${zone.value?.name ?? ''}` : (zone.value?.name ?? '');
});

const openNew = () => {
    Object.assign(form, blank());
    editing.value = null;
    formError.value = '';
    needsProxy.value = false;
    formOpen.value = true;
};

const openEdit = (r) => {
    const first = r.hosts[0] ?? zone.value.name;
    const www = r.hosts.length === 2 && r.hosts[1] === `www.${first}`;
    Object.assign(form, blank(), {
        sub: first === zone.value.name ? '' : first.slice(0, -(zone.value.name.length + 1)),
        include_www: www,
        path_mode: r.path_mode || 'all',
        path: r.path_mode === 'prefix' ? `${r.path}*` : r.path,
        target: r.target,
        status_code: r.status_code,
        keep_path: r.keep_path,
        keep_query: r.keep_query,
        enabled: r.enabled,
    });
    editing.value = r;
    formError.value = '';
    needsProxy.value = false;
    formOpen.value = true;
};

function closeForm() {
    formOpen.value = false;
    editing.value = null;
}

const report = (data) => {
    take(data);
    message.value = data.message ?? '';
    notes.value = data.notes ?? [];
};

const submit = async () => {
    if (!zone.value) return;
    formError.value = '';
    saving.value = true;

    const body = {
        zone_id: zone.value.id,
        zone_name: zone.value.name,
        host: host.value,
        include_www: form.include_www,
        path_mode: form.path_mode,
        path: form.path,
        target: form.target.trim(),
        status_code: Number(form.status_code),
        keep_path: form.path_mode !== 'exact' && form.keep_path,
        keep_query: form.keep_query,
        proxy: form.proxy,
        enabled: form.enabled,
    };

    try {
        const { data } = editing.value
            ? await window.axios.patch(route('redirects.update', { account: zone.value.account, rule: editing.value.id }), body)
            : await window.axios.post(route('redirects.store', zone.value.account), body);
        report(data);
        closeForm();
    } catch (e) {
        formError.value = failure(e, 'Cloudflare did not take the redirect.');
        needsProxy.value = needsProxy.value || !!e?.response?.data?.needs_proxy;
    } finally {
        saving.value = false;
    }
};

const busy = ref({});

const toggle = async (r) => {
    busy.value = { ...busy.value, [r.id]: true };
    try {
        const { data } = await window.axios.patch(route('redirects.update', { account: zone.value.account, rule: r.id }), {
            zone_id: zone.value.id,
            zone_name: zone.value.name,
            only: 'enabled',
            enabled: !r.enabled,
        });
        report(data);
    } catch (e) {
        message.value = '';
        listError.value = failure(e, 'Cloudflare refused the change.');
    } finally {
        busy.value = { ...busy.value, [r.id]: false };
    }
};

const remove = async (r) => {
    const ok = await confirmDanger(
        'Delete this redirect?',
        `${from(r)} will stop redirecting to ${r.target}. The DNS record for it stays.`,
    );
    if (!ok) return;

    busy.value = { ...busy.value, [r.id]: true };
    try {
        const { data } = await window.axios.delete(route('redirects.destroy', { account: zone.value.account, rule: r.id }), {
            params: { zone_id: zone.value.id, zone_name: zone.value.name },
        });
        report(data);
    } catch (e) {
        listError.value = failure(e, 'Cloudflare refused the change.');
    } finally {
        busy.value = { ...busy.value, [r.id]: false };
    }
};

const from = (r) => {
    if (!r.managed) return r.description || 'Rule made in Cloudflare';
    const hosts = r.hosts.join(', ');
    if (r.path_mode === 'exact') return `${hosts}${r.path}`;
    if (r.path_mode === 'prefix') return `${hosts}${r.path}*`;
    return hosts;
};

const example = computed(() => {
    const target = form.target.trim().replace(/\/+$/, '') || 'https://…';
    const path = form.path_mode === 'prefix' ? `${(form.path || '/old/').replace(/\*$/, '').replace(/\/?$/, '/')}page` : form.path_mode === 'exact' ? form.path || '/old' : '/page';
    const to = form.path_mode !== 'exact' && form.keep_path ? `${target}${path}` : form.target.trim() || 'https://…';
    return `${host.value}${path}?ref=x  →  ${to}${form.keep_query ? '?ref=x' : ''}`;
});

const radio = 'flex items-start gap-2 rounded-lg px-3 py-2 text-sm ring-1 transition cursor-pointer';
const on = 'bg-brand-50 ring-brand-300';
const off = 'bg-white ring-slate-200 hover:bg-slate-50';
</script>

<template>
    <Head title="Redirects" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Redirects</h2>
                    <p class="text-sm text-slate-500">
                        Send a domain, or a path on it, to another address. Cloudflare answers the visit itself, so
                        the domain needs no site here — only to be on a connected Cloudflare account.
                    </p>
                </div>
                <button
                    v-if="zone"
                    type="button"
                    class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700"
                    @click="openNew"
                >
                    Add a redirect
                </button>
            </div>
        </template>

        <!-- No Cloudflare credential: nothing to redirect with. -->
        <div v-if="!accounts.length" :class="card">
            <h3 class="font-semibold text-slate-900">Connect Cloudflare first</h3>
            <p class="mt-1 text-sm text-slate-600">
                Redirects are made in Cloudflare, for the domains it holds. Connect a Cloudflare API token with
                Zone · DNS · Edit and Zone · Single Redirect · Edit, and every domain on that account can be
                redirected from here.
            </p>
            <Link
                :href="route('dns.create')"
                class="mt-4 inline-block rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700"
            >
                Connect a provider
            </Link>
        </div>

        <div v-else class="space-y-6">
            <!-- Domain picker -->
            <div :class="card">
                <div class="flex flex-wrap items-end gap-3">
                    <label class="min-w-0 flex-1 basis-64">
                        <span class="text-xs font-medium text-slate-700">Domain</span>
                        <select v-model="zoneId" :class="field" :disabled="loadingZones && !zones.length">
                            <option v-if="!zones.length" value="">
                                {{ loadingZones ? 'Reading your Cloudflare domains…' : 'No domains found' }}
                            </option>
                            <option v-for="z in visibleZones" :key="z.id" :value="z.id">
                                {{ z.name }}{{ multipleAccounts ? ` — ${z.account_label}` : '' }}
                            </option>
                        </select>
                    </label>
                    <label v-if="zones.length > 10" class="basis-48">
                        <span class="text-xs font-medium text-slate-700">Filter</span>
                        <input v-model="zoneFilter" type="search" placeholder="example.com" :class="field" />
                    </label>
                    <button type="button" :class="smallButton" :disabled="loadingZones" @click="loadZones(true)">
                        {{ loadingZones ? 'Reading…' : 'Reload domains' }}
                    </button>
                </div>
                <p v-for="e in zoneErrors" :key="e" class="mt-2 text-xs text-rose-600">{{ e }}</p>
            </div>

            <p v-if="message" class="rounded-xl bg-emerald-50 px-4 py-3 text-sm text-emerald-800 ring-1 ring-emerald-200">
                {{ message }}
                <span v-for="n in notes" :key="n" class="mt-1 block text-xs text-emerald-700">{{ n }}</span>
            </p>

            <!-- Add / edit -->
            <form v-if="formOpen && zone" :class="card" class="space-y-5" @submit.prevent="submit">
                <div class="flex items-center justify-between gap-3">
                    <h3 class="font-semibold text-slate-900">{{ editing ? 'Edit redirect' : 'New redirect' }}</h3>
                    <button type="button" class="text-sm text-slate-500 hover:underline" @click="closeForm">Cancel</button>
                </div>

                <div>
                    <span class="text-xs font-medium text-slate-700">From</span>
                    <div class="mt-1 flex items-stretch overflow-hidden rounded-lg border border-slate-300 shadow-sm focus-within:border-brand-500 focus-within:ring-1 focus-within:ring-brand-500">
                        <input
                            v-model="form.sub"
                            type="text"
                            placeholder="(the domain itself)"
                            class="min-w-0 flex-1 border-0 text-right focus:ring-0"
                            autocapitalize="off"
                            spellcheck="false"
                        />
                        <span class="flex items-center bg-slate-50 px-3 text-sm text-slate-600">.{{ zone.name }}</span>
                    </div>
                    <p class="mt-1 text-xs text-slate-500">
                        Leave it empty for {{ zone.name }}, or type a subdomain such as <code>mail</code> or <code>shop</code>.
                    </p>
                    <label v-if="!host.startsWith('www.')" class="mt-2 flex items-center gap-2 text-sm text-slate-700">
                        <input v-model="form.include_www" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                        Also redirect www.{{ host }}
                    </label>
                </div>

                <fieldset>
                    <legend class="text-xs font-medium text-slate-700">Which addresses</legend>
                    <div class="mt-1 grid gap-2 sm:grid-cols-3">
                        <label :class="[radio, form.path_mode === 'all' ? on : off]">
                            <input v-model="form.path_mode" type="radio" value="all" class="mt-0.5 text-brand-600" />
                            <span>
                                <span class="font-medium text-slate-800">Every page</span>
                                <span class="block text-xs text-slate-500">The whole hostname.</span>
                            </span>
                        </label>
                        <label :class="[radio, form.path_mode === 'exact' ? on : off]">
                            <input v-model="form.path_mode" type="radio" value="exact" class="mt-0.5 text-brand-600" />
                            <span>
                                <span class="font-medium text-slate-800">One page</span>
                                <span class="block text-xs text-slate-500">Exactly this path.</span>
                            </span>
                        </label>
                        <label :class="[radio, form.path_mode === 'prefix' ? on : off]">
                            <input v-model="form.path_mode" type="radio" value="prefix" class="mt-0.5 text-brand-600" />
                            <span>
                                <span class="font-medium text-slate-800">A folder</span>
                                <span class="block text-xs text-slate-500">Everything under a path.</span>
                            </span>
                        </label>
                    </div>
                    <input
                        v-if="form.path_mode !== 'all'"
                        v-model="form.path"
                        type="text"
                        :placeholder="form.path_mode === 'exact' ? '/old-page' : '/blog/'"
                        :class="field"
                        spellcheck="false"
                    />
                </fieldset>

                <label class="block">
                    <span class="text-xs font-medium text-slate-700">To</span>
                    <input
                        v-model="form.target"
                        type="url"
                        required
                        placeholder="https://example.com/"
                        :class="field"
                        spellcheck="false"
                    />
                </label>

                <div class="grid gap-4 sm:grid-cols-2">
                    <label class="block">
                        <span class="text-xs font-medium text-slate-700">Type</span>
                        <select v-model.number="form.status_code" :class="field">
                            <option v-for="code in codes" :key="code" :value="code">{{ CODE_LABEL[code] ?? code }}</option>
                        </select>
                    </label>
                    <div class="space-y-2 pt-5 text-sm text-slate-700">
                        <label v-if="form.path_mode !== 'exact'" class="flex items-center gap-2">
                            <input v-model="form.keep_path" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                            Add the visitor's path to the destination
                        </label>
                        <label class="flex items-center gap-2">
                            <input v-model="form.keep_query" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                            Keep the query string (?utm=…)
                        </label>
                    </div>
                </div>

                <p class="rounded-lg bg-slate-50 px-3 py-2 font-mono text-xs break-all text-slate-600">{{ example }}</p>

                <div v-if="formError" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 ring-1 ring-rose-200">
                    {{ formError }}
                </div>
                <label v-if="needsProxy" class="flex items-start gap-2 rounded-lg bg-amber-50 px-3 py-2 text-sm text-amber-900 ring-1 ring-amber-200">
                    <input v-model="form.proxy" type="checkbox" class="mt-0.5 rounded border-amber-400 text-brand-600" />
                    <span>
                        <span class="font-medium">Proxy it through Cloudflare</span>
                        <span class="block text-xs">
                            Switches the orange cloud on for this hostname's DNS records, so Cloudflare can answer the
                            redirect.
                        </span>
                    </span>
                </label>

                <div class="flex justify-end gap-2">
                    <button
                        type="button"
                        class="rounded-xl px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                        @click="closeForm"
                    >
                        Cancel
                    </button>
                    <button
                        type="submit"
                        :disabled="saving"
                        class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700 disabled:opacity-60"
                    >
                        {{ saving ? 'Saving…' : editing ? 'Save redirect' : 'Add redirect' }}
                    </button>
                </div>
            </form>

            <!-- The list -->
            <div v-if="zone" :class="card" class="!p-0">
                <div class="flex flex-wrap items-center justify-between gap-2 border-b border-slate-100 px-5 py-4 sm:px-6">
                    <div>
                        <h3 class="font-semibold text-slate-900">Redirects on {{ zone.name }}</h3>
                        <p class="text-xs text-slate-500">
                            <template v-if="fetchedAt">Read from Cloudflare {{ ago(fetchedAt) }}.</template>
                            Cloudflare runs them top to bottom; the first that matches wins.
                        </p>
                    </div>
                    <button type="button" :class="smallButton" :disabled="loading" @click="loadRedirects(true)">
                        {{ loading ? 'Reading…' : 'Refresh' }}
                    </button>
                </div>

                <p v-if="listError" class="mx-5 mt-4 rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 ring-1 ring-rose-200 sm:mx-6">
                    {{ listError }}
                </p>

                <p v-if="loading && !list.length" class="px-5 py-8 text-center text-sm text-slate-400 sm:px-6">Reading Cloudflare…</p>
                <div v-else-if="!list.length && !listError" class="px-5 py-10 text-center sm:px-6">
                    <p class="text-sm text-slate-600">No redirects on {{ zone.name }} yet.</p>
                    <button type="button" class="mt-2 text-sm font-medium text-brand-600 hover:underline" @click="openNew">
                        Add the first one
                    </button>
                </div>

                <ul v-else class="divide-y divide-slate-100">
                    <li
                        v-for="r in list"
                        :key="r.id"
                        class="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-4 sm:px-6"
                        :class="{ 'opacity-60': !r.enabled }"
                    >
                        <div class="min-w-0 flex-1 basis-72">
                            <p class="flex flex-wrap items-center gap-x-2 text-sm">
                                <span class="font-medium break-all text-slate-900">{{ from(r) }}</span>
                                <span class="text-slate-400">→</span>
                                <span class="break-all text-slate-700">{{ r.target }}<span v-if="r.keep_path" class="text-slate-400">/…</span></span>
                            </p>
                            <p class="mt-1 flex flex-wrap gap-1.5 text-[11px]">
                                <span class="rounded bg-slate-100 px-1.5 py-0.5 text-slate-600">{{ r.status_code }}</span>
                                <span v-if="r.keep_path" class="rounded bg-slate-100 px-1.5 py-0.5 text-slate-600">keeps path</span>
                                <span v-if="r.keep_query" class="rounded bg-slate-100 px-1.5 py-0.5 text-slate-600">keeps query</span>
                                <span v-if="!r.enabled" class="rounded bg-amber-50 px-1.5 py-0.5 text-amber-700">off</span>
                                <span v-if="!r.managed" class="rounded bg-sky-50 px-1.5 py-0.5 text-sky-700" :title="r.expression">
                                    made in Cloudflare
                                </span>
                            </p>
                            <p v-if="!r.managed" class="mt-1 font-mono text-[11px] break-all text-slate-400">{{ r.expression }}</p>
                        </div>
                        <div class="flex items-center gap-2">
                            <button type="button" :class="smallButton" :disabled="busy[r.id]" @click="toggle(r)">
                                {{ r.enabled ? 'Switch off' : 'Switch on' }}
                            </button>
                            <button v-if="r.managed" type="button" :class="smallButton" :disabled="busy[r.id]" @click="openEdit(r)">
                                Edit
                            </button>
                            <button
                                type="button"
                                :class="smallButton"
                                class="text-rose-600"
                                :disabled="busy[r.id]"
                                @click="remove(r)"
                            >
                                Delete
                            </button>
                        </div>
                    </li>
                </ul>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
