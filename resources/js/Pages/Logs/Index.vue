<script setup>
import { computed, onMounted, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Events from '@/Components/Logs/Events.vue';
import ServerLogs from '@/Components/Logs/ServerLogs.vue';
import StatTiles from '@/Components/Logs/StatTiles.vue';
import { confirmDanger } from '@/dialog';
import { Head, Link, router, useForm } from '@inertiajs/vue3';

/*
 * The Logs page: what the server is doing and what its programs logged (the
 * default), what the panel itself did, and the log files themselves. The
 * tabs sit in the top bar, so the logs get the whole page.
 */
const props = defineProps({
    view: { type: String, default: 'server' },
    retention: { type: Number, default: 90 },
    retentionOptions: { type: Array, default: () => [7, 30, 90, 180, 365] },
    clearSources: { type: Array, default: () => [] },

    // The boxes over the server's list: asked for once the page is up.
    stats: { type: Object, default: null },

    // Everything that happened.
    events: { type: Array, default: () => [] },
    filters: { type: Object, default: () => ({}) },
    pagination: { type: Object, default: () => ({ page: 1, pages: 1, total: 0, per_page: 20 }) },
    counts: { type: Object, default: () => ({}) },
    ranges: { type: Array, default: () => [] },
    sources: { type: Array, default: () => [] },
    sites: { type: Array, default: () => [] },

    // Log files.
    serverLogs: { type: Array, default: () => [] },
    serverLog: { type: Object, default: null },
    filePagination: { type: Object, default: () => ({ page: 1, pages: 1, total: 0, per_page: 20 }) },
    search: { type: String, default: '' },
});

const VIEWS = [
    { key: 'server', label: 'Server logs', href: () => route('logs.index') },
    { key: 'panel', label: 'Panel logs', href: () => `${route('logs.index')}?view=panel` },
    { key: 'files', label: 'Log files', href: () => `${route('logs.index')}?view=files` },
];

// ---------------------------------------------------------------- clearing

const showClear = ref(false);

const clearForm = useForm({ what: 'all', older_than: '0', files: false });

// A file cannot be emptied of only its older lines, and only the server's
// entries came from files.
const canEmptyFiles = computed(
    () => clearForm.older_than === '0' && (clearForm.what === 'all' || clearForm.what.startsWith('server')),
);

const clearLogs = async () => {
    const label = props.clearSources.find((s) => s.key === clearForm.what)?.label ?? clearForm.what;
    const range = clearForm.older_than === '0' ? 'all of it' : `entries older than ${clearForm.older_than} days`;
    const files = canEmptyFiles.value && clearForm.files ? ' The log files on disk they came from are emptied too.' : '';

    if (!(await confirmDanger('Clear logs?', `This deletes ${label.toLowerCase()} — ${range}.${files} It cannot be undone.`, 'Clear')))
        return;

    clearForm
        .transform((data) => ({ ...data, files: canEmptyFiles.value && data.files }))
        .post(route('logs.clear'), {
            preserveScroll: true,
            onSuccess: () => (showClear.value = false),
        });
};

// The boxes read the access logs, which is the slow part: the page shows the
// list first and asks for them after.
onMounted(() => {
    if (props.view === 'server' && !props.stats) {
        router.reload({ only: ['stats'], preserveScroll: true, preserveState: true });
    }
});

const setRetention = (days) => router.post(route('logs.retention'), { days }, { preserveScroll: true });
</script>

<template>
    <Head title="Logs" />

    <AuthenticatedLayout>
        <!-- The tabs and Clear logs live in the top bar: no title, more room. -->
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <nav class="flex flex-wrap gap-1 rounded-xl bg-slate-100 p-1" aria-label="Logs">
                    <Link
                        v-for="v in VIEWS"
                        :key="v.key"
                        :href="v.href()"
                        class="rounded-lg px-4 py-2 text-sm font-medium transition"
                        :class="
                            view === v.key
                                ? 'bg-white text-slate-900 shadow-sm ring-1 ring-slate-900/5'
                                : 'text-slate-600 hover:text-slate-900'
                        "
                    >
                        {{ v.label }}
                    </Link>
                </nav>
                <button
                    type="button"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    @click="showClear = !showClear"
                >
                    {{ showClear ? 'Close' : 'Clear logs' }}
                </button>
            </div>
        </template>

        <!-- Clearing, and how long logs are kept without anybody clearing them. -->
        <div v-if="showClear" class="mb-6 grid gap-6 lg:grid-cols-2">
            <form class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5" @submit.prevent="clearLogs">
                <h3 class="font-semibold text-slate-900">Clear logs now</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Deleted from the panel's database, which is then compacted so the space is given back.
                    Tasks still running are kept. Single log files can also be emptied from the Log files tab.
                </p>
                <div class="mt-4 grid gap-3 sm:grid-cols-2">
                    <label class="block text-sm">
                        <span class="text-xs font-medium text-slate-700">Which</span>
                        <select v-model="clearForm.what" class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm">
                            <option v-for="source in clearSources" :key="source.key" :value="source.key">
                                {{ source.label }}
                            </option>
                        </select>
                    </label>
                    <label class="block text-sm">
                        <span class="text-xs font-medium text-slate-700">How much</span>
                        <select v-model="clearForm.older_than" class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm">
                            <option value="0">All of it</option>
                            <option value="1">Older than 1 day</option>
                            <option value="7">Older than 7 days</option>
                            <option value="30">Older than 30 days</option>
                            <option value="90">Older than 90 days</option>
                        </select>
                    </label>
                </div>
                <label v-if="canEmptyFiles" class="mt-4 flex items-start gap-2 text-sm">
                    <input v-model="clearForm.files" type="checkbox" class="mt-0.5 rounded border-slate-300 text-rose-600" />
                    <span>
                        <span class="font-medium text-slate-700">Also empty the log files on disk</span>
                        <span class="block text-xs text-slate-500">
                            nginx's, PHP-FPM's, the databases', fail2ban's, the system's and each site's — whichever
                            the choice above covers. The traffic boxes and their pages read the access logs, so they start
                            from zero too.
                        </span>
                    </span>
                </label>
                <button
                    type="submit"
                    :disabled="clearForm.processing"
                    class="mt-4 rounded-xl bg-rose-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-rose-700 disabled:opacity-50"
                >
                    {{ clearForm.processing ? 'Clearing…' : 'Clear' }}
                </button>
            </form>

            <div class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-900">Keep logs for</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Anything older is deleted from the panel's database automatically every hour, so it never
                    grows past this. The log files on disk are rotated by the system as usual.
                </p>
                <div class="mt-4 flex flex-wrap gap-2">
                    <button
                        v-for="days in retentionOptions"
                        :key="days"
                        type="button"
                        class="rounded-lg px-3 py-1.5 text-xs font-medium ring-1 transition"
                        :class="
                            days === retention
                                ? 'bg-brand-600 text-white ring-brand-600'
                                : 'bg-white text-slate-600 ring-slate-200 hover:bg-slate-50'
                        "
                        @click="setRetention(days)"
                    >
                        {{ days === 365 ? '1 year' : `${days} days` }}
                    </button>
                </div>
            </div>
        </div>

        <template v-if="view === 'panel'">
            <Events
                view="panel"
                :events="events"
                :filters="filters"
                :pagination="pagination"
                :counts="counts"
                :ranges="ranges"
                :sources="sources"
                :sites="sites"
            />
        </template>

        <ServerLogs
            v-else-if="view === 'files'"
            :server-logs="serverLogs"
            :server-log="serverLog"
            :pagination="filePagination"
            :search="search"
        />

        <div v-else class="space-y-6">
            <StatTiles :stats="stats" :window="filters.range ?? '1h'" />

            <section>
                <Events
                    view="server"
                    :events="events"
                    :filters="filters"
                    :pagination="pagination"
                    :counts="counts"
                    :ranges="ranges"
                    :sources="sources"
                    :sites="sites"
                />
            </section>
        </div>
    </AuthenticatedLayout>
</template>
