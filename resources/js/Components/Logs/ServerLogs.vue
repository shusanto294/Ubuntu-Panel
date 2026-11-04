<script setup>
import { computed, ref } from 'vue';
import { router } from '@inertiajs/vue3';

/*
 * The files the machine's own programs write — nginx, PHP-FPM, MySQL,
 * fail2ban, the system, and each site's — read from the end, optionally
 * searched. The page only ever names a file by its key; the server holds the
 * list of paths.
 */
const props = defineProps({
    serverLogs: { type: Array, default: () => [] },
    serverLogLines: { type: Array, default: () => [100, 200, 500, 1000, 2000] },
    serverLog: { type: Object, default: null },
    lines: { type: Number, default: 200 },
    search: { type: String, default: '' },
});

const source = ref(props.serverLog?.key ?? '');
const count = ref(props.lines);
const query = ref(props.search);
const loading = ref(false);

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

const load = () => {
    const params = { view: 'files', source: source.value, lines: String(count.value) };
    if (query.value.trim()) params.q = query.value.trim();

    router.get(route('logs.index'), params, {
        preserveScroll: true,
        preserveState: true,
        only: ['serverLog', 'serverLogs', 'lines', 'search'],
        onStart: () => (loading.value = true),
        onFinish: () => (loading.value = false),
    });
};

const clearSearch = () => {
    query.value = '';
    load();
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
            @submit.prevent="load"
        >
            <label class="block min-w-[16rem] flex-1 text-sm">
                <span class="text-xs font-medium text-slate-700">Log</span>
                <select
                    v-model="source"
                    class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm"
                    @change="load"
                >
                    <optgroup v-for="[group, logs] in groups" :key="group" :label="group">
                        <option v-for="log in logs" :key="log.key" :value="log.key">
                            {{ log.label }} — {{ size(log.size) }}
                        </option>
                    </optgroup>
                </select>
            </label>
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">Lines</span>
                <select v-model.number="count" class="mt-1 block rounded-lg border-slate-300 text-sm shadow-sm" @change="load">
                    <option v-for="n in serverLogLines" :key="n" :value="n">Last {{ n.toLocaleString() }}</option>
                </select>
            </label>
            <label class="block min-w-[14rem] flex-1 text-sm">
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
        </form>

        <div v-if="!serverLogs.length" class="rounded-2xl bg-white px-5 py-12 text-center text-sm text-slate-500 shadow-sm ring-1 ring-slate-900/5">
            No log files were found on this machine.
        </div>

        <div v-else-if="serverLog" class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
            <div class="flex flex-wrap items-center justify-between gap-2">
                <div>
                    <p class="font-medium text-slate-900">{{ serverLog.group }} · {{ serverLog.label }}</p>
                    <p class="break-all font-mono text-xs text-slate-500">
                        {{ serverLog.path }} · {{ size(serverLog.size) }}
                    </p>
                </div>
                <p class="text-xs text-slate-500">
                    <template v-if="search">
                        {{ shown.length.toLocaleString() }} matching “{{ search }}” in the last 100,000 lines
                        <button type="button" class="ml-2 font-medium text-brand-600 hover:underline" @click="clearSearch">
                            Clear
                        </button>
                    </template>
                    <template v-else>Last {{ lines.toLocaleString() }} lines, newest at the bottom</template>
                </p>
            </div>
            <pre
                v-if="shown.length"
                class="mt-3 max-h-[70vh] overflow-auto rounded-xl bg-slate-900 px-4 py-3 font-mono text-xs leading-relaxed"
            ><span v-for="(line, i) in shown" :key="i" :class="severity(line)">{{ line }}
</span></pre>
            <p v-else class="mt-3 text-sm text-slate-500">
                {{ search ? 'No line in this log matches.' : 'Nothing in this log yet.' }}
            </p>
            <p v-if="serverLog.group.startsWith('Site')" class="mt-3 text-xs text-slate-500">
                PHP's own errors and warnings for a site land in its nginx error log, as
                “FastCGI sent in stderr: PHP message: …”.
            </p>
        </div>
    </div>
</template>
