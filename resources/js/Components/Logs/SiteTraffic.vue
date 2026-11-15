<script setup>
import { computed, ref, watch } from 'vue';

/*
 * Where the traffic went: each site's requests, and the pages visitors opened
 * most, as links. Read from nginx's access logs with the boxes above, so it
 * covers the same period. Clicking a site narrows the pages to it.
 */
const props = defineProps({
    stats: { type: Object, default: null },
});

const WINDOW_LABEL = { '1h': 'last hour', '24h': 'last 24 hours', '7d': 'last 7 days', '30d': 'last 30 days' };

const number = (n) => (n ?? 0).toLocaleString();

const site = ref('');

// A site that has gone quiet in the new period is no longer something to
// narrow to.
watch(
    () => props.stats?.sites,
    (sites) => {
        if (site.value && !(sites ?? []).some((s) => s.domain === site.value)) site.value = '';
    },
);

const sites = computed(() => props.stats?.sites ?? []);
const pages = computed(() => {
    const all = props.stats?.pages ?? [];
    return (site.value ? all.filter((p) => p.site === site.value) : all).slice(0, 50);
});

const busiest = computed(() => Math.max(1, ...sites.value.map((s) => s.requests)));
const period = computed(() => WINDOW_LABEL[props.stats?.window] ?? '');

const pick = (domain) => (site.value = site.value === domain ? '' : domain);
</script>

<template>
    <div class="grid gap-6 xl:grid-cols-5">
        <!-- Sites -->
        <section class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 xl:col-span-2">
            <div class="border-b border-slate-100 px-5 py-4">
                <h3 class="font-semibold text-slate-900">Sites getting traffic</h3>
                <p class="text-xs text-slate-500">
                    Requests in the {{ period }}<template v-if="sites.length">. Click a site to see its pages.</template>
                </p>
            </div>

            <div v-if="!stats" class="space-y-3 p-5">
                <div v-for="i in 4" :key="i" class="h-6 animate-pulse rounded bg-slate-100" />
            </div>
            <p v-else-if="!sites.length" class="p-5 text-sm text-slate-500">No requests in the {{ period }}.</p>
            <ul v-else class="divide-y divide-slate-100">
                <li v-for="s in sites" :key="s.domain">
                    <button
                        type="button"
                        class="w-full px-5 py-3 text-left transition hover:bg-slate-50"
                        :class="site === s.domain ? 'bg-brand-50/60' : ''"
                        :aria-pressed="site === s.domain"
                        @click="pick(s.domain)"
                    >
                        <span class="flex items-baseline justify-between gap-3">
                            <span class="min-w-0 truncate text-sm font-medium text-slate-800">
                                {{ s.domain }}
                            </span>
                            <span class="shrink-0 text-sm font-semibold tabular-nums text-slate-900">
                                {{ number(s.requests) }}
                            </span>
                        </span>
                        <span class="mt-1.5 block h-1.5 overflow-hidden rounded-full bg-slate-100">
                            <span
                                class="block h-full rounded-full bg-brand-500"
                                :style="{ width: `${Math.max(2, (s.requests / busiest) * 100)}%` }"
                            />
                        </span>
                        <span class="mt-1 flex flex-wrap gap-x-3 text-xs text-slate-500">
                            <span>{{ number(s.ips) }} visitor{{ s.ips === 1 ? '' : 's' }}</span>
                            <span v-if="s.client_errors">{{ number(s.client_errors) }} not found</span>
                            <span v-if="s.server_errors" class="text-rose-600">
                                {{ number(s.server_errors) }} server error{{ s.server_errors === 1 ? '' : 's' }}
                            </span>
                            <a
                                v-if="s.url"
                                :href="s.url"
                                target="_blank"
                                rel="noopener noreferrer"
                                class="font-medium text-brand-600 hover:underline"
                                @click.stop
                            >
                                Open site ↗
                            </a>
                        </span>
                    </button>
                </li>
            </ul>
        </section>

        <!-- Pages -->
        <section class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 xl:col-span-3">
            <div class="flex flex-wrap items-start justify-between gap-2 border-b border-slate-100 px-5 py-4">
                <div>
                    <h3 class="font-semibold text-slate-900">Most visited pages</h3>
                    <p class="text-xs text-slate-500">
                        {{ site ? `On ${site}` : 'On every site' }}, in the {{ period }} — not counting images,
                        scripts, styles or attack probes.
                    </p>
                </div>
                <button
                    v-if="site"
                    type="button"
                    class="rounded-lg px-2.5 py-1 text-xs font-medium text-slate-600 ring-1 ring-slate-200 hover:bg-slate-50"
                    @click="site = ''"
                >
                    All sites
                </button>
            </div>

            <div v-if="!stats" class="space-y-3 p-5">
                <div v-for="i in 6" :key="i" class="h-6 animate-pulse rounded bg-slate-100" />
            </div>
            <p v-else-if="!pages.length" class="p-5 text-sm text-slate-500">No pages were visited in the {{ period }}.</p>
            <div v-else class="overflow-x-auto">
                <table class="w-full text-sm">
                    <thead class="text-left text-xs text-slate-500">
                        <tr>
                            <th class="px-5 py-2 font-medium">Page</th>
                            <th class="px-3 py-2 text-right font-medium">Requests</th>
                            <th class="px-5 py-2 text-right font-medium">Visitors</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="p in pages" :key="p.site + p.path">
                            <td class="max-w-0 px-5 py-2.5">
                                <a
                                    v-if="p.url"
                                    :href="p.url"
                                    target="_blank"
                                    rel="noopener noreferrer"
                                    class="block truncate font-medium text-brand-600 hover:underline"
                                    :title="p.url"
                                >
                                    {{ p.path }}
                                </a>
                                <span v-else class="block truncate font-medium text-slate-800" :title="p.path">
                                    {{ p.path }}
                                </span>
                                <span v-if="!site" class="block truncate text-xs text-slate-400">{{ p.site }}</span>
                            </td>
                            <td class="px-3 py-2.5 text-right font-semibold tabular-nums text-slate-900">
                                {{ number(p.requests) }}
                            </td>
                            <td class="px-5 py-2.5 text-right tabular-nums text-slate-500">{{ number(p.ips) }}</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </section>
    </div>
</template>
