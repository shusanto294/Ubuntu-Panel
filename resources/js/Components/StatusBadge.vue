<script setup>
import { computed } from 'vue';

const props = defineProps({
    status: { type: String, default: 'pending' },
});

const styles = {
    online: 'bg-emerald-100 text-emerald-800 ring-emerald-200',
    active: 'bg-emerald-100 text-emerald-800 ring-emerald-200',
    ready: 'bg-emerald-100 text-emerald-800 ring-emerald-200',
    installed: 'bg-emerald-100 text-emerald-800 ring-emerald-200',
    success: 'bg-emerald-100 text-emerald-800 ring-emerald-200',
    preparing: 'bg-orange-100 text-orange-800 ring-orange-200',
    installing: 'bg-orange-100 text-orange-800 ring-orange-200',
    pending: 'bg-amber-100 text-amber-800 ring-amber-200',
    queued: 'bg-amber-100 text-amber-800 ring-amber-200',
    deploying: 'bg-amber-100 text-amber-800 ring-amber-200',
    creating: 'bg-amber-100 text-amber-800 ring-amber-200',
    deleting: 'bg-amber-100 text-amber-800 ring-amber-200',
    running: 'bg-sky-100 text-sky-800 ring-sky-200',
    not_installed: 'bg-slate-100 text-slate-700 ring-slate-200',
    offline: 'bg-rose-100 text-rose-800 ring-rose-200',
    failed: 'bg-rose-100 text-rose-800 ring-rose-200',
};

const classes = computed(
    () => styles[props.status] ?? 'bg-slate-100 text-slate-700 ring-slate-200',
);

const label = computed(() => (props.status ?? '').replace(/_/g, ' '));
</script>

<template>
    <span
        class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium capitalize ring-1 ring-inset"
        :class="classes"
    >
        {{ label }}
    </span>
</template>
