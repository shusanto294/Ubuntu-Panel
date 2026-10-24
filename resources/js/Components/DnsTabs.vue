<script setup>
import { Link } from '@inertiajs/vue3';

// The two ways to have DNS, as two pages.
//
// Connecting Cloudflare and becoming a nameserver yourself are both "DNS", and
// they are not variations of one job: one is a pasted token, the other is two
// names registered at a registrar and a machine answering on port 53. Each has
// its own page and its own address; this is the strip across the top of both.
defineProps({
    current: { type: String, required: true },
    counts: { type: Object, default: () => ({ providers: 0, zones: 0 }) },
});

const tabs = [
    { key: 'providers', name: 'Providers', route: 'dns.index' },
    { key: 'nameserver', name: 'This server', route: 'dns.nameserver' },
];
</script>

<template>
    <div class="mb-6 border-b border-slate-200">
        <nav class="-mb-px flex flex-wrap gap-6" aria-label="Sections">
            <Link
                v-for="tab in tabs"
                :key="tab.key"
                :href="route(tab.route)"
                class="border-b-2 px-1 pb-3 text-sm font-medium transition"
                :class="
                    current === tab.key
                        ? 'border-brand-600 text-brand-700'
                        : 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700'
                "
                :aria-current="current === tab.key ? 'page' : null"
            >
                {{ tab.name }}
                <span class="ml-1 text-xs text-slate-400">
                    {{ counts[tab.key === 'providers' ? 'providers' : 'zones'] ?? 0 }}
                </span>
            </Link>
        </nav>
    </div>
</template>
