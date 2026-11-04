<script setup>
import { computed, ref, watch } from 'vue';
import Pagination from '@/Components/Pagination.vue';
import { confirmDanger } from '@/dialog';
import { router } from '@inertiajs/vue3';

/*
 * The files the machine's own programs write — nginx, PHP-FPM, MySQL,
 * fail2ban, the system, and each site's — a page at a time from the end,
 * optionally searched. The page only ever names a file by its key; the server
 * holds the list of paths.
 */
const props = defineProps({
    serverLogs: { type: Array, default: () => [] },
    serverLog: { type: Object, default: null },
    pagination: { type: Object, default: () => ({ page: 1, pages: 1, total: 0, per_page: 20 }) },
    search: { type: String, default: '' },
});

const source = ref(props.serverLog?.key ?? '');
const query = ref(props.search);
const loading = ref(false);

watch(
    () => props.serverLog?.key,
    (key) => (source.value = key ?? ''),
);

const groups = computed(() => {
    const byGroup = new Map();
    for (const log of props.serverLogs) {
        if (!byGroup.has(log.group)) byGroup.set(log.group, []);
        byGroup.get(log.group).push(log);
    }
    return [...byGroup.entries()];
});

const size = (n) => {
    if (!n) return 'empty';
    const units = ['B', 'KB', 'MB', 'GB'];
    const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
    return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`;
};

const totalSize = computed(() => props.serverLogs.reduce((sum, log) => sum + (log.size ?? 0), 0));

const load = (page = 1) => {
    const params = { view: 'files', source: source.value };
    if (query.value.trim()) params.q = query.value.trim();
    if (page > 1) params.page = String(page);

    router.get(route('logs.index'), params, {
        preserveScroll: true,
        preserveState: true,
        only: ['serverLog', 'serverLogs', 'filePagination', 'search'],
        onStart: () => (loading.value = true),
        onFinish: () => (loading.value = false),
    });
};

const clearSearch = () => {
    query.value = '';
    load();
};

const clearFile = async (key, label) => {
    const text =
        key === 'all'
            ? `Every log file listed here (${props.serverLogs.length}, ${size(totalSize.value)}) is emptied, with the rotated copies kept beside it. It cannot be undone.`
            : `${label} is emptied, with the rotated copies kept beside it. It cannot be undone.`;

    if (!(await confirmDanger(key === 'all' ? 'Empty every log file?' : 'Empty this log file?', text, 'Empty'))) return;

    router.post(route('logs.files.clear'), { source: key }, { preserveScroll: true });
};

// Each line coloured by how bad it says it is, so the one error among a
// thousand notices is findable by eye.
const severity = (line) => {
    if (/\b(emerg|alert|crit|fatal|error|failed|denied|ban |oom|killed process)\b/i.test(line)) return 'text-rose-300';
    if (/\b(warn|warning|notice|max_children|refused|invalid user)\b/i.test(line)) return 'text-amber-200';
    return 'text-slate-200';
};

const shown = computed(() => (props.serverLog?.contents ? props.serverLog.contents.split('\n') : []));
</script>

<template>
    <div class="space-y-4">
        <form
            class="flex flex-wrap items-end gap-3 rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5"
            @submit.prevent="load()"
        >
            <label class="block min-w-[12rem] flex-1 text-sm sm:min-w-[16rem]">
                <span class="text-xs font-medium text-slate-700">Log file</span>
                <select v-model="source" class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm" @change="load()">
                    <optgroup v-for="[group, logs] in groups" :key="group" :label="group">
                        <option v-for="log in logs" :key="log.key" :value="log.key">
                            {{ log.label }} — {{ size(log.size) }}
                        </option>
                    </optgroup>
                </select>
            </label>
            <label class="block min-w-[12rem] flex-1 text-sm sm:min-w-[14rem]">
                <span class="text-xs font-medium text-slate-700">Only lines containing</span>
                <input
                    v-model="query"
                    type="search"
                    placeholder="an IP, a URL, “error”, “max_children”…"
                    maxlength="200"
                    class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm"
                />
            </label>
            <button
                type="submit"
                :disabled="loading"
                class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
            >
                {{ loading ? 'Reading…' : 'Show' }}
            </button>
            <button
                v-if="serverLogs.length"
                type="button"
                class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-rose-600 ring-1 ring-slate-900/10 transition hover:bg-rose-50"
                @click="clearFile('all')"
            >
                Empty every log file
            </button>
        </form>

        <div v-if="!serverLogs.length" class="rounded-2xl bg-white px-5 py-12 text-center text-sm text-slate-500 shadow-sm ring-1 ring-slate-900/5">
            No log files were found on this machine.
        </div>

        <div v-else-if="serverLog" class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
            <div class="flex flex-wrap items-center justify-between gap-2 p-5">
                <div class="min-w-0">
                    <p class="break-words font-medium text-slate-900">{{ serverLog.group }} · {{ serverLog.label }}</p>
                    <p class="break-all font-mono text-xs text-slate-500">{{ serverLog.path }} · {{ size(serverLog.size) }}</p>
                </div>
                <div class="flex flex-wrap items-center gap-3 text-xs text-slate-500">
                    <span v-if="search" class="break-all">
                        {{ pagination.total.toLocaleString() }} lines match “{{ search }}”
                        <button type="button" class="ml-1 font-medium text-brand-600 hover:underline" @click="clearSearch">Clear</button>
                    </span>
                    <span v-else>{{ pagination.total.toLocaleString() }} lines · page 1 is the newest</span>
                    <button
                        type="button"
                        class="rounded-lg px-2.5 py-1 font-medium text-rose-600 ring-1 ring-slate-900/10 transition hover:bg-rose-50"
                        @click="clearFile(serverLog.key, serverLog.label)"
                    >
                        Empty this file
                    </button>
                </div>
            </div>
            <div class="px-5">
                <pre
                    v-if="shown.length"
                    class="max-h-[70vh] overflow-auto rounded-xl bg-slate-900 px-4 py-3 font-mono text-xs leading-relaxed"
                ><span v-for="(line, i) in shown" :key="i" :class="severity(line)">{{ line }}
</span></pre>
                <p v-else class="pb-5 text-sm text-slate-500">
                    {{ search ? 'No line in this log matches.' : 'Nothing in this log yet.' }}
                </p>
                <p v-if="serverLog.group.startsWith('Site')" class="mt-3 text-xs text-slate-500">
                    PHP's own errors and warnings for a site land in its nginx error log, as
                    “FastCGI sent in stderr: PHP message: …”.
                </p>
            </div>
            <Pagination
                class="mt-4"
                :model-value="pagination.page"
                :pages="pagination.pages"
                :total="pagination.total"
                :per-page="pagination.per_page"
                @update:model-value="load"
            />
        </div>
    </div>
</template>
