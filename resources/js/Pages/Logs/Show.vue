<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import TaskConsole from '@/Components/TaskConsole.vue';
import { formatDuration, formatStamp } from '@/time';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    task: { type: Object, default: null },
});

const duration = computed(() =>
    formatDuration(props.task?.started_at, props.task?.finished_at),
);
</script>

<template>
    <Head :title="task ? `Log — ${task.action}` : 'Log'" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">
                        {{ task?.action ?? 'Log' }}
                    </h2>
                    <p class="text-sm text-slate-500">
                        {{ formatStamp(task?.created_at) }}
                        <span v-if="duration">· took {{ duration }}</span>
                    </p>
                </div>
                <Link
                    :href="route('logs.index')"
                    class="rounded-lg bg-white px-3 py-1.5 text-xs font-medium text-slate-600 ring-1 ring-slate-200 transition hover:bg-slate-50"
                >
                    All logs
                </Link>
            </div>
        </template>

        <!--
            The console lives here and nowhere else: this is the page you open
            to read output, so it opens expanded and stays that way.
        -->
        <TaskConsole
            v-if="task"
            :task="task"
            :collapse-when-done="false"
            :reload-on-finish="false"
            title="Run"
        />

        <div
            v-else
            class="rounded-2xl bg-white px-5 py-12 text-center text-sm text-slate-500 shadow-sm ring-1 ring-slate-900/5"
        >
            This log is no longer on the machine.
        </div>
    </AuthenticatedLayout>
</template>
