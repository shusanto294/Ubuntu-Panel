<script setup>
import { computed, onMounted, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import ReverseDnsNotice from '@/Components/ReverseDnsNotice.vue';
import EmailTabs from '@/Components/EmailTabs.vue';
import MailDnsHealth from '@/Components/MailDnsHealth.vue';
import Pagination from '@/Components/Pagination.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import RequiredApps from '@/Components/RequiredApps.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { usePagination } from '@/Composables/usePagination';
import { Head, Link } from '@inertiajs/vue3';

// A domain is a thing you set up once — records, DKIM, a certificate — and a
// mailbox is a thing you make every week. Reading past the first to count the
// second was the wrong way round, so they are two pages; this is the first.
const props = defineProps({
    domains: Array,
    counts: Object,
    mailConfigured: Boolean,
    requirements: { type: Array, default: () => [] },
    mailHostname: String,
    reverseDns: { type: Object, default: null },
    roundcubeInstalled: Boolean,
    activeTask: { type: Object, default: null },
});

const busy = computed(
    () =>
        props.domains.some(
            (domain) =>
                isBusyStatus(domain.status) ||
                domain.accounts.some((account) => isBusyStatus(account.status)),
        ) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['domains', 'counts', 'activeTask']);

// Whether each domain's mail records are right, as the public DNS has them.
// Asked for once the page is up, so a slow resolver never holds the list.
const health = ref({});
const healthLoading = ref(false);
const openHealth = ref(null); // domain id
const rechecking = ref(false);

const loadHealth = async (domainId = null, fresh = false) => {
    if (domainId) rechecking.value = true;
    else healthLoading.value = true;
    try {
        const params = {};
        if (domainId) params.domain = domainId;
        if (fresh) params.fresh = 1;
        const { data } = await window.axios.get(route('email.dns-health'), { params });
        health.value = { ...health.value, ...(data.health ?? {}) };
    } catch {
        // The ticks stay as they were; the popup says nothing was checked.
    } finally {
        healthLoading.value = false;
        rechecking.value = false;
    }
};

onMounted(() => {
    if (props.mailConfigured && props.domains.length) loadHealth();
});

const healthOf = (domain) => health.value[String(domain.id)] ?? null;

// A domain added while the page is open gets checked too.
watch(
    () => props.domains.map((d) => d.id).join(','),
    () => {
        if (!healthLoading.value && props.domains.some((d) => !healthOf(d))) loadHealth();
    },
);

const showHealth = (domain) => {
    openHealth.value = domain.id;
    if (!healthOf(domain)) loadHealth(domain.id);
};

// Filtered here rather than asked of the server: the whole list is already on
// the page, and a search that answers as you type is the point of it.
const search = ref('');

const filtered = computed(() => {
    const term = search.value.trim().toLowerCase();
    if (!term) return props.domains;

    return props.domains.filter(
        (item) =>
            (item.domain ?? '').toLowerCase().includes(term) ||
            (item.webmail_host ?? '').toLowerCase().includes(term),
    );
});

const { page, pages, total, perPage, visible: visibleDomains } = usePagination(
    filtered,
);
</script>

<template>
    <Head title="Mail domains" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Email</h2>
                    <p class="text-sm text-slate-500">
                        {{ domains.length }}
                        {{ domains.length === 1 ? 'domain' : 'domains' }}
                        <span v-if="mailHostname">· {{ mailHostname }}</span>
                    </p>
                </div>
                <!--
                    Test delivery is hidden for now rather than removed: the
                    endpoint and the conversation it holds with the server are
                    still there, and it goes back on the page when it is worth
                    trusting again.
                -->
                <div v-if="mailConfigured" class="flex flex-wrap items-center gap-2">
                    <Link
                        :href="route('email.domains.create')"
                        class="rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 shadow-sm transition hover:bg-slate-50"
                    >
                        Add domain
                    </Link>
                    <Link
                        :href="route('email.accounts.new')"
                        class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700"
                    >
                        Add mailbox
                    </Link>
                </div>
            </div>
        </template>

        <div v-if="activeTask" class="mb-6">
            <TaskStatus :task="activeTask" />
        </div>

        <RequiredApps
            v-if="!mailConfigured"
            :apps="requirements"
            :need="['mysql', 'mail']"
            note="Postfix, Dovecot and OpenDKIM go on together, with mailboxes stored in MariaDB and ports 25, 465, 587, 993 and 995 opened. This page unlocks itself when they land."
        />

        <template v-else>
            <EmailTabs current="domains" :counts="counts" />

            <ReverseDnsNotice :status="reverseDns" class="mb-6" />

            <input
                v-if="domains.length"
                v-model="search"
                type="search"
                placeholder="Search domains"
                class="mb-4 block w-full rounded-lg border-slate-300 text-sm shadow-sm"
            />

            <div
                class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
            >
                <div class="overflow-x-auto">
                    <table class="min-w-full divide-y divide-slate-200">
                        <thead class="bg-slate-50">
                            <tr
                                class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                            >
                                <th class="px-3 py-3 sm:px-5">Domain</th>
                                <th class="hidden px-3 py-3 sm:px-5 sm:table-cell">Mailboxes</th>
                                <th class="px-3 py-3 sm:px-5">DNS</th>
                                <th class="hidden px-3 py-3 sm:px-5 sm:table-cell">Status</th>
                                <th class="px-3 py-3 sm:px-5"></th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100 text-sm">
                            <template v-for="domain in visibleDomains" :key="domain.id">
                                <tr>
                                    <td class="break-all px-3 py-3 sm:px-5">
                                        <Link
                                            :href="route('email.domains.show', domain.id)"
                                            class="font-medium text-slate-800 hover:text-brand-600"
                                            >{{ domain.domain }}</Link
                                        >
                                        <p class="text-xs text-slate-500">
                                            {{ domain.webmail_host }}
                                        </p>
                                        <div class="mt-1.5 sm:hidden"><StatusBadge :status="domain.status" /></div>
                                    </td>
                                    <td class="hidden px-3 py-3 sm:px-5 text-slate-600 sm:table-cell">
                                        {{ domain.accounts.length }}
                                    </td>
                                    <td class="whitespace-nowrap px-3 py-3 sm:px-5">
                                        <button
                                            type="button"
                                            class="inline-flex items-center gap-2 rounded-lg px-2 py-1 text-xs font-medium ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                                            :title="
                                                healthOf(domain)
                                                    ? healthOf(domain).ok
                                                        ? 'Every required mail record is set. Click for details.'
                                                        : 'Some mail records are missing or wrong. Click for details.'
                                                    : 'Checking the mail records'
                                            "
                                            @click="showHealth(domain)"
                                        >
                                            <span
                                                v-if="healthOf(domain)"
                                                class="flex h-5 w-5 items-center justify-center rounded-full text-[11px] font-bold text-white"
                                                :class="healthOf(domain).ok ? 'bg-emerald-500' : 'bg-rose-500'"
                                                :aria-label="healthOf(domain).ok ? 'All set' : 'Needs fixing'"
                                                >{{ healthOf(domain).ok ? '✓' : '✕' }}</span
                                            >
                                            <span
                                                v-else
                                                class="h-5 w-5 animate-pulse rounded-full bg-slate-200"
                                                aria-label="Checking"
                                            />
                                            <svg
                                                class="h-4 w-4 text-slate-500"
                                                viewBox="0 0 24 24"
                                                fill="none"
                                                stroke="currentColor"
                                                stroke-width="1.8"
                                                stroke-linecap="round"
                                                stroke-linejoin="round"
                                                aria-hidden="true"
                                            >
                                                <circle cx="12" cy="12" r="9" />
                                                <path d="M3 12h18M12 3c2.5 2.6 3.8 5.6 3.8 9s-1.3 6.4-3.8 9c-2.5-2.6-3.8-5.6-3.8-9S9.5 5.6 12 3z" />
                                            </svg>
                                            <span v-if="healthOf(domain)" class="hidden text-slate-600 sm:inline">
                                                {{ healthOf(domain).passed }}/{{ healthOf(domain).total }}
                                            </span>
                                        </button>
                                    </td>
                                    <td class="hidden px-3 py-3 sm:px-5 sm:table-cell">
                                        <StatusBadge :status="domain.status" />
                                    </td>
                                    <td
                                        class="whitespace-nowrap px-3 py-3 sm:px-5 text-right [&>*]:block [&>*]:py-0.5 sm:[&>*]:inline sm:[&>*]:py-0"
                                    >
                                        <a
                                            v-if="roundcubeInstalled"
                                            :href="domain.webmail_url"
                                            target="_blank"
                                            rel="noopener"
                                            class="text-brand-600 hover:underline sm:mr-3"
                                            >Webmail</a
                                        >
                                        <Link
                                            :href="route('email.domains.show', domain.id)"
                                            class="text-brand-600 hover:underline"
                                            >Manage</Link
                                        >
                                    </td>
                                </tr>
                                <tr v-if="domain.last_error">
                                    <td
                                        colspan="5"
                                        class="bg-rose-50 px-3 py-2 sm:px-5 text-xs text-rose-700"
                                    >
                                        {{ domain.last_error }}
                                    </td>
                                </tr>
                            </template>
                            <tr v-if="domains.length && !filtered.length">
                                <td
                                    colspan="5"
                                    class="px-5 py-12 text-center text-sm text-slate-500"
                                >
                                    No domains match “{{ search }}”.
                                </td>
                            </tr>
                            <tr v-if="!domains.length">
                                <td
                                    colspan="5"
                                    class="px-5 py-16 text-center text-sm text-slate-500"
                                >
                                    No mail domains yet.
                                    <Link
                                        :href="route('email.domains.create')"
                                        class="text-brand-600 hover:underline"
                                        >Add one</Link
                                    >
                                    to start creating addresses.
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>
                <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
            </div>

            <MailDnsHealth
                :show="openHealth !== null"
                :health="openHealth !== null ? health[String(openHealth)] ?? null : null"
                :checking="rechecking"
                @close="openHealth = null"
                @recheck="loadHealth(openHealth, true)"
            />
        </template>
    </AuthenticatedLayout>
</template>
