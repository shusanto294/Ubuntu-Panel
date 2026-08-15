<script setup>
import { Link } from '@inertiajs/vue3';

// The two mail lists, as links rather than as a `ref`.
//
// They were tabs on one page, which read the same and behaved differently:
// nothing to bookmark, nothing to send anybody, and no address to put somebody
// back at once the thing they were looking at was deleted. Each is a page now,
// and this is the strip across the top of both.
defineProps({
    current: { type: String, required: true },
    counts: { type: Object, default: () => ({ domains: 0, mailboxes: 0 }) },
});

const tabs = [
    { key: 'domains', name: 'Domains', route: 'email.domains.index' },
    { key: 'mailboxes', name: 'Mailboxes', route: 'email.mailboxes.index' },
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
                <span class="ml-1 text-xs text-slate-400">{{ counts[tab.key] ?? 0 }}</span>
            </Link>
        </nav>
    </div>
</template>
