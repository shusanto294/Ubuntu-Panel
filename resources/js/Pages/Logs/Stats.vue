<script setup>
import { computed, onMounted, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Traffic from '@/Components/Logs/Traffic.vue';
import { Head, Link, router } from '@inertiajs/vue3';

/*
 * One of the Logs page's boxes, in detail. The page is up at once; the
 * access logs are read after, and the CPU measured on the PHP page only.
 */
const props = defineProps({
    section: { type: String, required: true },
    title: { type: String, required: true },
    window: { type: String, default: '1h' },
    windows: { type: Array, default: () => ['1h', '24h', '7d', '30d'] },
    traffic: { type: Object, default: null },
    trustedIps: { type: Object, default: () => ({}) },
    bannedIps: { type: Array, default: () => [] },
    processes: { type: Object, default: null },
    fpm: { type: Array, default: () => [] },
});

// What each page shows of the traffic view, and how it narrows it.
const LAYOUT = {
    requests: { sections: ['timeline', 'sites', 'urls'] },
    php: { sections: ['sites', 'cpu'] },
    addresses: { sections: ['ips', 'agents'] },
    'not-found': { sections: ['urls', 'ips'], focus: 'not-found' },
    'server-errors': { sections: ['urls'], focus: 'server-errors' },
    suspicious: { sections: ['findings', 'attacks', 'ips', 'urls'], focus: 'suspicious' },
};

const INTRO = {
    requests: 'Every request the sites answered over the period: when they came, which site they went to, and what was asked for.',
    php: 'Requests that reach PHP or an app are the ones that cost CPU; static files are served by nginx for almost nothing. Beside them, what is using the CPU right now.',
    addresses: 'Who is sending the requests, busiest first, and what they say they are.',
    'not-found': 'Requests answered with a 4xx — not found, forbidden, refused. A burst of these from one address is usually something crawling for files that are not there.',
    'server-errors': 'Requests answered with a 5xx — the site or PHP failed. The site’s error log under Log files says why.',
    suspicious: 'Login brute force, XML-RPC, vulnerability scanning and floods, and the addresses doing them. Your own addresses are never counted.',
};

const layout = computed(() => LAYOUT[props.section] ?? LAYOUT.requests);

const loading = ref(true);

onMounted(() => {
    const only = ['traffic', 'trustedIps', 'bannedIps'];
    if (props.section === 'php') only.push('processes', 'fpm');

    router.reload({
        only,
        preserveScroll: true,
        preserveState: true,
        onFinish: () => (loading.value = false),
    });
});

const back = computed(() => `${route('logs.index')}?range=${props.window}`);

const errorsLink = computed(
    () => `${route('logs.index')}?range=${props.window}&source=requests&level=error`,
);
</script>

<template>
    <Head :title="title" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center gap-3">
                <Link :href="back" class="text-sm font-medium text-slate-500 hover:text-slate-900">← Logs</Link>
                <h2 class="text-lg font-semibold text-slate-900">{{ title }}</h2>
            </div>
        </template>

        <p class="mb-4 max-w-3xl text-sm text-slate-500">
            {{ INTRO[section] }}
            <Link v-if="section === 'server-errors'" :href="errorsLink" class="font-medium text-brand-600 hover:underline">
                See each one in the server logs →
            </Link>
        </p>

        <Traffic
            :window="window"
            :windows="windows"
            :traffic="traffic"
            :processes="processes"
            :fpm="fpm"
            :trusted-ips="trustedIps"
            :banned-ips="bannedIps"
            :sections="layout.sections"
            :focus="layout.focus ?? ''"
            :loading="loading"
        />
    </AuthenticatedLayout>
</template>
