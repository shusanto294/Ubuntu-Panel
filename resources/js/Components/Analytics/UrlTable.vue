<script setup>
import { THREAT_LABEL, number } from './format';

/*
 * URLs and how many times each was asked for, most first — every one, pages,
 * files and a scanner's guesses alike, with how many of the answers were
 * refused or failed.
 */
defineProps({
    rows: { type: Array, default: null }, // null while the logs are read
    unmatched: { type: String, default: '' },
    offset: { type: Number, default: 0 }, // the rank of the first row, less one
    skeleton: { type: Number, default: 8 },
});

const host = (site, unmatched) => (site === unmatched ? 'server IP' : site);
</script>

<template>
    <div v-if="!rows" class="space-y-3 p-5">
        <div v-for="i in skeleton" :key="i" class="h-5 animate-pulse rounded bg-slate-100" />
    </div>
    <p v-else-if="!rows.length" class="px-5 py-8 text-center text-sm text-slate-500">Nothing was requested in this time.</p>
    <ol v-else>
        <li
            v-for="(p, i) in rows"
            :key="p.site + p.path"
            class="flex items-center justify-between gap-3 border-t border-slate-100 px-5 py-2.5 text-sm first:border-t-0"
        >
            <div class="flex min-w-0 items-baseline gap-2.5">
                <span class="w-7 shrink-0 text-right text-xs tabular-nums text-slate-400">{{ offset + i + 1 }}</span>
                <a
                    v-if="p.url"
                    :href="p.url"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="min-w-0 truncate text-slate-900 hover:text-brand-700 hover:underline"
                    :title="host(p.site, unmatched) + p.path"
                >
                    <span class="text-slate-500">{{ host(p.site, unmatched) }}</span>{{ p.path }}
                </a>
                <span v-else class="min-w-0 truncate text-slate-900" :title="host(p.site, unmatched) + p.path">
                    <span class="text-slate-500">{{ host(p.site, unmatched) }}</span>{{ p.path }}
                </span>
            </div>
            <div class="flex shrink-0 items-center gap-2">
                <span v-if="p.threat" class="rounded-md bg-rose-50 px-1.5 py-0.5 text-[11px] font-semibold text-rose-700">
                    {{ THREAT_LABEL[p.threat] ?? 'Attack' }}
                </span>
                <span
                    v-else-if="p.failed"
                    class="rounded-md bg-amber-50 px-1.5 py-0.5 text-[11px] font-semibold text-amber-700"
                    :title="`${number(p.failed)} of ${number(p.requests)} answered 4xx or 5xx`"
                >
                    {{ p.failed === p.requests ? 'All refused' : `${number(p.failed)} refused` }}
                </span>
                <span class="w-14 text-right font-semibold tabular-nums text-slate-900">{{ number(p.requests) }}</span>
            </div>
        </li>
    </ol>
</template>
