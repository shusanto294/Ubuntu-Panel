<script setup>
import { computed, onMounted, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import IpReport from '@/Components/Logs/IpReport.vue';
import { confirmDanger } from '@/dialog';
import { formatRelative, formatStamp } from '@/time';
import { Head, Link, router } from '@inertiajs/vue3';

/*
 * One notification, with what it is about: the addresses it names — what
 * each did, whether it is banned, and the buttons to ban it — the log lines
 * it counted, or the task that failed. Read after the page is up.
 */
const props = defineProps({
    notification: { type: Object, required: true },
    ips: { type: Array, default: () => [] },
    related: { type: Object, default: null },
});

const n = computed(() => props.notification);
const loading = ref(true);

onMounted(() => {
    router.reload({ only: ['related'], preserveScroll: true, preserveState: true, onFinish: () => (loading.value = false) });
});

const LEVEL = {
    critical: 'bg-rose-600 text-white',
    warning: 'bg-amber-50 text-amber-700 ring-1 ring-amber-200',
    info: 'bg-sky-50 text-sky-700 ring-1 ring-sky-200',
};

const destroy = async () => {
    if (!(await confirmDanger('Delete this notification?', 'It cannot be undone.', 'Delete'))) return;
    router.post(route('notifications.delete'), { ids: String(n.value.id) }, {
        onSuccess: () => router.visit(route('notifications.index')),
    });
};
</script>

<template>
    <Head :title="n.title" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div class="flex min-w-0 items-center gap-3">
                    <Link :href="route('notifications.index')" class="text-sm font-medium text-slate-500 hover:text-slate-900">
                        ← Notifications
                    </Link>
                </div>
                <button
                    type="button"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-rose-600 ring-1 ring-slate-900/10 transition hover:bg-rose-50"
                    @click="destroy"
                >
                    Delete
                </button>
            </div>
        </template>

        <div class="space-y-6">
            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                <div class="flex flex-wrap items-center gap-2">
                    <span class="rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase" :class="LEVEL[n.level] ?? LEVEL.info">
                        {{ n.level }}
                    </span>
                    <span class="text-xs text-slate-500" :title="formatStamp(n.created_at)">{{ formatRelative(n.created_at) }} · {{ formatStamp(n.created_at) }}</span>
                </div>
                <h2 class="mt-2 text-xl font-semibold text-slate-900">{{ n.title }}</h2>
                <p v-if="n.body" class="mt-3 whitespace-pre-line break-words text-sm text-slate-600">{{ n.body }}</p>
                <Link v-if="n.link" :href="n.link" class="mt-4 inline-block text-sm font-medium text-brand-600 hover:underline">
                    Open the related page →
                </Link>
            </div>

            <!-- The addresses it is about. -->
            <section v-if="ips.length" class="space-y-3">
                <h3 class="font-semibold text-slate-900">
                    {{ ips.length === 1 ? 'The address' : `The ${ips.length} addresses` }} — what {{ ips.length === 1 ? 'it' : 'each' }} did
                </h3>
                <div v-if="loading && !related" class="rounded-2xl bg-white px-5 py-10 text-center text-sm text-slate-500 shadow-sm ring-1 ring-slate-900/5">
                    Reading the logs for {{ ips.join(', ') }}…
                </div>
                <IpReport v-for="report in related?.reports ?? []" :key="report.ip" :report="report" />
            </section>

            <!-- The critical lines it counted. -->
            <section v-if="related?.events?.length" class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                <div class="px-5 pt-5">
                    <h3 class="font-semibold text-slate-900">The lines</h3>
                </div>
                <ul class="mt-3 divide-y divide-slate-100">
                    <li v-for="event in related.events" :key="event.id" class="px-5 py-3 text-sm">
                        <p class="text-xs text-slate-500">
                            {{ formatStamp(event.created_at) }} · {{ event.source }}<template v-if="event.site"> · {{ event.site }}</template>
                        </p>
                        <p class="mt-1 whitespace-pre-wrap break-all font-mono text-xs text-slate-800">{{ event.message }}</p>
                    </li>
                </ul>
            </section>

            <!-- The task that failed. -->
            <section v-if="related?.task" class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <div class="flex flex-wrap items-center justify-between gap-2">
                    <h3 class="font-semibold text-slate-900">{{ related.task.action ?? 'The task' }}</h3>
                    <Link :href="route('logs.show', related.task.id)" class="text-sm font-medium text-brand-600 hover:underline">
                        Open the whole output →
                    </Link>
                </div>
                <pre class="mt-3 max-h-96 overflow-auto rounded-xl bg-slate-900 px-4 py-3 font-mono text-xs leading-relaxed text-slate-200">{{ (related.task.output ?? '').split('\n').slice(-60).join('\n') }}</pre>
            </section>
        </div>
    </AuthenticatedLayout>
</template>
