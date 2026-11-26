<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import { Head, Link } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';
import { ago, card, field, smallButton } from '@/Components/Security/ui';

/*
 * Redirects Cloudflare answers at its edge, for any domain on a connected
 * Cloudflare account — a site here or not. Tick domains and each gets a row
 * below to say where it goes; one ticked domain also shows all its redirects,
 * with a form for a path or subdomain. Every zone's redirects come from the
 * panel's copy (redirects.all), so the list can say which already redirect.
 * Adding makes the hostname resolve through Cloudflare when it does not
 * already, since Cloudflare can only redirect a request it sees.
 */
const props = defineProps({
    accounts: { type: Array, default: () => [] },
    codes: { type: Array, default: () => [301, 302, 307, 308] },
    hosted: { type: Array, default: () => [] }, // hostnames a site here serves
});

const CODE_LABEL = {
    301: '301 · Permanent',
    302: '302 · Temporary',
    307: '307 · Temporary, keeps the method',
    308: '308 · Permanent, keeps the method',
};

const zones = ref([]); // { account, account_label, id, name }
const zoneErrors = ref([]);
const loadingZones = ref(false);
const zoneFilter = ref('');

// zone id → its redirects as the panel's copy has them, or { error }.
const summary = ref({});
const summaryLoading = ref(false);
const picked = ref([]); // zone ids ticked for a bulk add
const hideRedirected = ref(false);

const list = computed(() => summary.value[zoneId.value]?.redirects ?? []);
const fetchedAt = computed(() => summary.value[zoneId.value]?.fetched_at ?? 0);
const loading = ref(false);
const listError = ref('');

const message = ref('');
const notes = ref([]);

// One ticked domain is shown in full; with several, only the rows.
const zone = computed(() => (picked.value.length === 1 ? (zones.value.find((z) => z.id === picked.value[0]) ?? null) : null));
const zoneId = computed(() => zone.value?.id ?? '');
const redirectCount = (z) => summary.value[z.id]?.redirects?.length ?? 0;
const apexRedirect = (z) =>
    (summary.value[z.id]?.redirects ?? []).find((r) => r.managed && r.path_mode === 'all' && r.hosts.includes(z.name));
// A domain with a website here cannot be redirected as a whole (hostedProblem in redirects.go).
const isHosted = (z) => props.hosted.includes(z.name);
const visibleZones = computed(() => {
    const q = zoneFilter.value.trim().toLowerCase();
    return zones.value.filter((z) => (!q || z.name.includes(q)) && !(hideRedirected.value && redirectCount(z)));
});
const allVisiblePicked = computed(
    () =>
        visibleZones.value.some((z) => !isHosted(z)) &&
        visibleZones.value.every((z) => isHosted(z) || picked.value.includes(z.id)),
);
const pickVisible = () => {
    const ids = visibleZones.value.filter((z) => !isHosted(z)).map((z) => z.id);
    picked.value = allVisiblePicked.value
        ? picked.value.filter((id) => !ids.includes(id))
        : [...new Set([...picked.value, ...ids])];
};
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
        picked.value = picked.value.filter((id) => all.some((z) => z.id === id));
    } catch (e) {
        zoneErrors.value = [failure(e, 'Could not list the Cloudflare domains.')];
    } finally {
        loadingZones.value = false;
    }
    loadSummary(refresh);
};

// Reads only the panel's copy; a domain never read before is read once.
const loadSummary = async (refresh = false) => {
    summaryLoading.value = true;
    try {
        const { data } = await window.axios.get(route('redirects.all') + (refresh ? '?refresh=1' : ''));
        summary.value = data.zones ?? {};
    } catch (e) {
        listError.value = failure(e, 'Could not read the redirects.');
    } finally {
        summaryLoading.value = false;
    }
};

const take = (data) => {
    if (data.zone_id) summary.value = { ...summary.value, [data.zone_id]: data };
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
        take(data);
    } catch (e) {
        if (asked === zoneId.value) listError.value = failure(e, 'Cloudflare did not answer.');
    } finally {
        loading.value = false;
    }
};

watch(zoneId, () => {
    listError.value = '';
    closeForm(); // the form belongs to the domain it was opened on
});

// ------------------------------------------------------------- the rows

// zone id → { target, error }: where each ticked domain goes. A domain that
// already redirects as a whole starts with its current destination.
const rows = reactive({});
const bulk = reactive({
    target: 'https://',
    status_code: 301,
    include_www: true,
    keep_path: false,
    keep_query: true,
    proxy: false,
});
const bulkNeedsProxy = ref(false);
const savingAll = ref(false);

const pickedZones = computed(() => picked.value.map((id) => zones.value.find((z) => z.id === id)).filter(Boolean));

watch(picked, (ids) => {
    for (const id of ids) {
        if (rows[id]) continue;
        const z = zones.value.find((zz) => zz.id === id);
        rows[id] = { target: (z && apexRedirect(z)?.target) || '', error: '' };
    }
});

const unpick = (id) => (picked.value = picked.value.filter((p) => p !== id));
const applyToAll = () => pickedZones.value.forEach((z) => (rows[z.id].target = bulk.target.trim()));

const saveAll = async () => {
    savingAll.value = true;
    message.value = '';
    notes.value = [];
    let added = 0;
    let updated = 0;
    let allNotes = [];
    const finished = [];

    // One domain after another, so each answer lands in the copy before the next.
    for (const z of pickedZones.value) {
        const row = rows[z.id];
        const target = row.target.trim();
        row.error = '';
        if (!target || target === 'https://' || isHosted(z)) continue;

        const have = apexRedirect(z);
        if (have && have.target === target && have.status_code === Number(bulk.status_code)) {
            finished.push(z.id);
            continue;
        }

        const body = {
            zone_id: z.id,
            zone_name: z.name,
            host: z.name,
            include_www: bulk.include_www,
            path_mode: 'all',
            path: '',
            target,
            status_code: Number(bulk.status_code),
            keep_path: bulk.keep_path,
            keep_query: bulk.keep_query,
            proxy: bulk.proxy,
            enabled: true,
        };
        try {
            const { data } = have
                ? await window.axios.patch(route('redirects.update', { account: z.account, rule: have.id }), body)
                : await window.axios.post(route('redirects.store', z.account), body);
            take(data);
            have ? updated++ : added++;
            allNotes = allNotes.concat(data.notes ?? []);
            finished.push(z.id);
        } catch (e) {
            row.error = failure(e, 'Cloudflare did not take the redirect.');
            bulkNeedsProxy.value = bulkNeedsProxy.value || !!e?.response?.data?.needs_proxy;
        }
    }
    savingAll.value = false;

    const said = [added && `${added} added`, updated && `${updated} updated`].filter(Boolean).join(', ');
    message.value = said ? `Redirects saved: ${said}.` : '';
    notes.value = allNotes;
    // What is done leaves the list; what failed stays, with why, for a retry.
    if (picked.value.length > 1) picked.value = picked.value.filter((id) => !finished.includes(id));
};

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
const targets = ref([]); // the zones a new redirect goes to
const results = ref([]); // { name, error } from a bulk add

const hostFor = (z) => {
    const sub = form.sub.trim().toLowerCase().replace(/\.+$/, '');
    return sub ? `${sub}.${z?.name ?? ''}` : (z?.name ?? '');
};
const host = computed(() => hostFor(targets.value[0] ?? zone.value));
const suffix = computed(() => (targets.value.length > 1 ? 'each domain' : (targets.value[0] ?? zone.value)?.name));

// The redirect a zone already has for this hostname and path, as the server
// would refuse it (duplicateRedirect in redirects.go).
const formPath = () => {
    if (form.path_mode === 'all') return '';
    const path = form.path.trim();
    return form.path_mode === 'prefix' ? `${path.replace(/\*$/, '').replace(/\/+$/, '')}/` : path;
};
const clash = (z) => {
    const hosts = [hostFor(z)];
    if (form.include_www && !hosts[0].startsWith('www.')) hosts.push(`www.${hosts[0]}`);
    const mode = form.path_mode === 'prefix' && formPath() === '/' ? 'all' : form.path_mode;
    const path = mode === 'all' ? '' : formPath();
    return (summary.value[z.id]?.redirects ?? []).find(
        (r) =>
            r.managed &&
            r.id !== editing.value?.id &&
            r.path_mode === mode &&
            (r.path ?? '') === path &&
            r.hosts.some((h) => hosts.includes(h)),
    );
};
const clashes = computed(() => targets.value.filter((z) => clash(z)));

const openNew = () => {
    targets.value = [zone.value];
    results.value = [];
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
    targets.value = [zone.value];
    results.value = [];
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
    if (!targets.value.length) return;
    formError.value = '';
    saving.value = true;

    const body = (z) => ({
        zone_id: z.id,
        zone_name: z.name,
        host: hostFor(z),
        include_www: form.include_www,
        path_mode: form.path_mode,
        path: form.path,
        target: form.target.trim(),
        status_code: Number(form.status_code),
        keep_path: form.path_mode !== 'exact' && form.keep_path,
        keep_query: form.keep_query,
        proxy: form.proxy,
        enabled: form.enabled,
    });

    if (editing.value) {
        try {
            const { data } = await window.axios.patch(
                route('redirects.update', { account: zone.value.account, rule: editing.value.id }),
                body(zone.value),
            );
            report(data);
            closeForm();
        } catch (e) {
            formError.value = failure(e, 'Cloudflare did not take the redirect.');
            needsProxy.value = needsProxy.value || !!e?.response?.data?.needs_proxy;
        } finally {
            saving.value = false;
        }
        return;
    }

    // One domain after another, so each answer lands in the copy before the next.
    const done = [];
    const failed = [];
    let allNotes = [];
    for (const z of targets.value) {
        if (clash(z)) continue;
        try {
            const { data } = await window.axios.post(route('redirects.store', z.account), body(z));
            take(data);
            done.push(z.name);
            allNotes = allNotes.concat(data.notes ?? []);
        } catch (e) {
            failed.push({ zone: z, error: failure(e, 'Cloudflare did not take the redirect.') });
            needsProxy.value = needsProxy.value || !!e?.response?.data?.needs_proxy;
        }
    }
    saving.value = false;

    const skipped = targets.value.length - done.length - failed.length;
    message.value = done.length
        ? `Redirect added to ${done.length === 1 ? done[0] : `${done.length} domains`}.` +
          (skipped ? ` ${skipped} skipped — they already redirect.` : '')
        : '';
    notes.value = allNotes;
    picked.value = picked.value.filter((id) => !targets.value.some((z) => z.id === id && done.includes(z.name)));

    if (!failed.length) {
        closeForm();
        return;
    }
    // Keep the form open on the ones that failed, so a retry sends only those.
    targets.value = failed.map((f) => f.zone);
    results.value = failed.map((f) => ({ name: f.zone.name, error: f.error }));
    formError.value = failed.length === 1 ? failed[0].error : `${failed.length} domains were refused — see below.`;
};

const busy = ref({});

// The ticked domains that redirect as a whole, for "Remove redirects".
const removable = computed(() => pickedZones.value.filter((z) => apexRedirect(z)));

// Removes each domain's whole-domain redirect, one after another.
const removeRedirects = async (list) => {
    const ok = await confirmDanger(
        list.length === 1 ? `Remove the redirect on ${list[0].name}?` : `Remove the redirects on ${list.length} domains?`,
        `${list.map((z) => z.name).join(', ')} will stop redirecting. Their DNS records stay.`,
    );
    if (!ok) return;

    savingAll.value = true;
    message.value = '';
    notes.value = [];
    let removed = 0;
    for (const z of list) {
        const have = apexRedirect(z);
        if (!have) continue;
        rows[z.id].error = '';
        try {
            const { data } = await window.axios.delete(route('redirects.destroy', { account: z.account, rule: have.id }), {
                params: { zone_id: z.id, zone_name: z.name },
            });
            take(data);
            rows[z.id].target = '';
            removed++;
        } catch (e) {
            rows[z.id].error = failure(e, 'Cloudflare refused the change.');
        }
    }
    savingAll.value = false;
    if (removed) message.value = `Removed ${removed} redirect${removed === 1 ? '' : 's'}.`;
};

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
            <!-- Domain picker: tick domains; each gets a row below. -->
            <div :class="card">
                <label class="block">
                    <span class="text-xs font-medium text-slate-700">Domains</span>
                    <input
                        v-model="zoneFilter"
                        type="search"
                        placeholder="Search your Cloudflare domains"
                        :class="field"
                        class="w-full"
                        autocapitalize="off"
                        spellcheck="false"
                    />
                </label>
                <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-slate-700">
                    <label class="flex cursor-pointer items-center gap-2">
                        <input
                            type="checkbox"
                            class="rounded border-slate-300 text-brand-600"
                            :checked="allVisiblePicked"
                            :disabled="!visibleZones.length"
                            @change="pickVisible"
                        />
                        Select all shown
                    </label>
                    <label class="flex cursor-pointer items-center gap-2">
                        <input v-model="hideRedirected" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                        Hide domains that already redirect
                    </label>
                    <span v-if="picked.length" class="text-slate-500">
                        {{ picked.length }} selected ·
                        <button type="button" class="text-brand-600 hover:underline" @click="picked = []">clear</button>
                    </span>
                    <span v-if="summaryLoading || loadingZones" class="text-xs text-slate-400">Reading Cloudflare…</span>
                    <button
                        v-else
                        type="button"
                        class="ml-auto text-xs text-slate-500 hover:text-brand-600 hover:underline"
                        @click="loadZones(true)"
                    >
                        Read again from Cloudflare
                    </button>
                </div>

                <p v-if="!zones.length" class="mt-3 text-sm text-slate-500">
                    {{ loadingZones ? 'Reading your Cloudflare domains…' : 'No domains found.' }}
                </p>
                <p v-else-if="!visibleZones.length" class="mt-3 text-sm text-slate-500">No domain matches.</p>
                <ul v-else class="mt-3 max-h-80 divide-y divide-slate-100 overflow-y-auto rounded-lg ring-1 ring-slate-200">
                    <li v-for="z in visibleZones" :key="z.id">
                        <label
                            class="flex cursor-pointer items-center gap-3 px-3 py-2 text-sm"
                            :class="picked.includes(z.id) ? 'bg-brand-50' : 'hover:bg-slate-50'"
                        >
                            <input v-model="picked" type="checkbox" :value="z.id" class="rounded border-slate-300 text-brand-600" />
                            <span class="min-w-0 flex-1 truncate">
                                <span class="font-medium text-slate-900">{{ z.name }}</span>
                                <span v-if="multipleAccounts" class="text-xs text-slate-400"> — {{ z.account_label }}</span>
                            </span>
                            <span
                                v-if="isHosted(z)"
                                class="rounded bg-amber-50 px-1.5 py-0.5 text-[11px] text-amber-700"
                                title="Remove the website first to redirect this domain."
                            >
                                website on this panel
                            </span>
                            <span
                                v-else-if="apexRedirect(z)"
                                class="max-w-[50%] truncate rounded bg-emerald-50 px-1.5 py-0.5 text-[11px] text-emerald-700"
                                :title="apexRedirect(z).target"
                            >
                                → {{ apexRedirect(z).target }}
                            </span>
                            <span v-else-if="redirectCount(z)" class="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-600">
                                {{ redirectCount(z) }} redirect{{ redirectCount(z) === 1 ? '' : 's' }}
                            </span>
                            <span
                                v-else-if="summary[z.id]?.error"
                                class="rounded bg-rose-50 px-1.5 py-0.5 text-[11px] text-rose-700"
                                :title="summary[z.id].error"
                            >
                                could not read
                            </span>
                        </label>
                    </li>
                </ul>
                <p v-for="e in zoneErrors" :key="e" class="mt-2 text-xs text-rose-600">{{ e }}</p>
            </div>

            <!-- One row per ticked domain: where its whole domain goes. -->
            <form v-if="pickedZones.length" :class="card" class="space-y-4" @submit.prevent="saveAll">
                <div>
                    <h3 class="font-semibold text-slate-900">
                        Redirect {{ pickedZones.length === 1 ? pickedZones[0].name : `${pickedZones.length} domains` }}
                    </h3>
                    <p class="text-xs text-slate-500">
                        Every page on each domain goes to its address. Leave an address empty to skip that domain.
                    </p>
                </div>

                <div v-if="pickedZones.length > 1" class="flex flex-wrap items-end gap-2">
                    <label class="min-w-0 flex-1 basis-64">
                        <span class="text-xs font-medium text-slate-700">Same address for all</span>
                        <input v-model="bulk.target" type="url" placeholder="https://example.com/" :class="field" spellcheck="false" />
                    </label>
                    <button type="button" :class="smallButton" class="mb-0.5" @click="applyToAll">Apply to all</button>
                </div>

                <ul class="divide-y divide-slate-100 rounded-lg ring-1 ring-slate-200">
                    <li v-for="z in pickedZones" :key="z.id" class="px-3 py-2">
                        <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                            <span class="w-full truncate text-sm font-medium text-slate-900 sm:w-56">{{ z.name }}</span>
                            <span class="hidden text-slate-400 sm:inline">→</span>
                            <input
                                v-model="rows[z.id].target"
                                :disabled="isHosted(z)"
                                type="url"
                                placeholder="https://example.com/"
                                :class="field"
                                class="!mt-0 min-w-0 flex-1 basis-48"
                                spellcheck="false"
                            />
                            <button
                                type="button"
                                class="rounded px-2 text-lg leading-none text-slate-400 hover:text-rose-600"
                                :aria-label="`Remove ${z.name}`"
                                @click="unpick(z.id)"
                            >
                                ×
                            </button>
                        </div>
                        <p v-if="isHosted(z)" class="mt-1 text-[11px] text-amber-700">
                            {{ z.name }} has a website on this panel. Remove the website first (Sites), then add the
                            redirect.
                        </p>
                        <p v-else-if="apexRedirect(z)" class="mt-1 text-[11px] text-emerald-700">
                            Already redirects to {{ apexRedirect(z).target }} — saving a new address changes it.
                            <button
                                type="button"
                                class="ml-1 text-rose-600 hover:underline"
                                :disabled="savingAll"
                                @click="removeRedirects([z])"
                            >
                                Remove redirect
                            </button>
                        </p>
                        <p v-else-if="summary[z.id]?.error" class="mt-1 text-[11px] text-amber-700">
                            Could not read its redirects: {{ summary[z.id].error }}
                        </p>
                        <p v-if="rows[z.id].error" class="mt-1 text-xs text-rose-600">{{ rows[z.id].error }}</p>
                    </li>
                </ul>

                <div class="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm text-slate-700">
                    <label class="flex items-center gap-2">
                        <span class="text-xs font-medium text-slate-700">Type</span>
                        <select v-model.number="bulk.status_code" :class="field" class="!mt-0 !w-auto">
                            <option v-for="code in codes" :key="code" :value="code">{{ CODE_LABEL[code] ?? code }}</option>
                        </select>
                    </label>
                    <label class="flex items-center gap-2">
                        <input v-model="bulk.include_www" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                        Also redirect www
                    </label>
                    <label class="flex items-center gap-2">
                        <input v-model="bulk.keep_path" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                        Add the visitor's path
                    </label>
                    <label class="flex items-center gap-2">
                        <input v-model="bulk.keep_query" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                        Keep the query string
                    </label>
                </div>

                <label v-if="bulkNeedsProxy" class="flex items-start gap-2 rounded-lg bg-amber-50 px-3 py-2 text-sm text-amber-900 ring-1 ring-amber-200">
                    <input v-model="bulk.proxy" type="checkbox" class="mt-0.5 rounded border-amber-400 text-brand-600" />
                    <span>
                        <span class="font-medium">Proxy them through Cloudflare</span>
                        <span class="block text-xs">
                            Switches the orange cloud on for the domains whose records point elsewhere, so Cloudflare can
                            answer the redirect. Domains that receive mail at that hostname are never switched.
                        </span>
                    </span>
                </label>

                <div class="flex flex-wrap justify-end gap-2">
                    <button
                        v-if="removable.length > 1"
                        type="button"
                        :disabled="savingAll"
                        class="rounded-xl px-4 py-2.5 text-sm font-medium text-rose-600 ring-1 ring-rose-200 transition hover:bg-rose-50 disabled:opacity-60"
                        @click="removeRedirects(removable)"
                    >
                        Remove redirects ({{ removable.length }})
                    </button>
                    <button
                        type="submit"
                        :disabled="savingAll"
                        class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700 disabled:opacity-60"
                    >
                        {{ savingAll ? 'Saving…' : 'Save redirects' }}
                    </button>
                </div>
            </form>

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

                <div v-if="targets.length > 1" class="text-sm text-slate-700">
                    Adding to {{ targets.length }} domains:
                    <span class="text-slate-500">{{ targets.map((z) => z.name).join(', ') }}</span>
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
                        <span class="flex items-center bg-slate-50 px-3 text-sm text-slate-600">.{{ suffix }}</span>
                    </div>
                    <p class="mt-1 text-xs text-slate-500">
                        Leave it empty for {{ suffix === 'each domain' ? 'the domain itself' : suffix }}, or type a subdomain such as <code>mail</code> or <code>shop</code>.
                    </p>
                    <label v-if="!host.startsWith('www.')" class="mt-2 flex items-center gap-2 text-sm text-slate-700">
                        <input v-model="form.include_www" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                        Also redirect www.{{ targets.length > 1 ? 'each domain' : host }}
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

                <div v-if="clashes.length" class="rounded-lg bg-amber-50 px-3 py-2 text-sm text-amber-900 ring-1 ring-amber-200">
                    <template v-if="clashes.length === targets.length && targets.length === 1">
                        {{ hostFor(clashes[0]) }} already redirects to {{ clash(clashes[0]).target }} — edit that redirect instead.
                    </template>
                    <template v-else>
                        These already redirect and will be skipped:
                        <span v-for="z in clashes" :key="z.id" class="block text-xs">
                            {{ hostFor(z) }} → {{ clash(z).target }}
                        </span>
                    </template>
                </div>

                <div v-if="formError" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 ring-1 ring-rose-200">
                    {{ formError }}
                    <span v-for="f in results.length > 1 ? results : []" :key="f.name" class="mt-1 block text-xs">
                        {{ f.name }}: {{ f.error }}
                    </span>
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
                        :disabled="saving || clashes.length === targets.length"
                        class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700 disabled:opacity-60"
                    >
                        {{ saving ? 'Saving…' : editing ? 'Save redirect' : targets.length > 1 ? `Add to ${targets.length - clashes.length} domains` : 'Add redirect' }}
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
                    <div class="flex gap-2">
                        <button type="button" :class="smallButton" @click="openNew">Add a path or subdomain</button>
                        <button type="button" :class="smallButton" :disabled="loading" @click="loadRedirects(true)">
                            {{ loading ? 'Reading…' : 'Refresh' }}
                        </button>
                    </div>
                </div>

                <p v-if="listError || summary[zoneId]?.error" class="mx-5 mt-4 rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 ring-1 ring-rose-200 sm:mx-6">
                    {{ listError || summary[zoneId]?.error }}
                </p>

                <p v-if="(loading || summaryLoading) && !list.length" class="px-5 py-8 text-center text-sm text-slate-400 sm:px-6">Reading Cloudflare…</p>
                <div v-else-if="!list.length && !listError && !summary[zoneId]?.error" class="px-5 py-10 text-center sm:px-6">
                    <p class="text-sm text-slate-600">No redirects on {{ zone.name }} yet.</p>
                    <p class="mt-1 text-xs text-slate-500">Give it an address above, or add a path or subdomain.</p>
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
