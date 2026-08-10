<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { usePage } from '@inertiajs/vue3';

/**
 * The one-line answer to something you just did, and then gone.
 *
 * These used to stay up for as long as the page did. A flash is consumed by
 * the request that renders it, but the pages here reload themselves in parts
 * while work is running — `only: ['site', 'activeTask']` and so on — and a
 * partial reload does not carry `flash`, so Inertia keeps the value it already
 * had. A sentence about something that finished four minutes ago sits above a
 * page that has moved on, and the only way to clear it is a full reload.
 *
 * So each message is shown for a few seconds and dismissed. The watcher keys
 * on the text rather than on its presence: two identical actions in a row are
 * the same string arriving twice, and the timer has to start again for the
 * second one.
 */
const page = usePage();

const HOLD_MS = 5000;

const useDismissing = (read) => {
    const visible = ref(Boolean(read()));
    let timer = null;

    const clear = () => {
        if (!timer) return;
        clearTimeout(timer);
        timer = null;
    };

    watch(
        () => [read(), page.props.flash],
        () => {
            clear();

            visible.value = Boolean(read());

            if (visible.value) {
                timer = setTimeout(() => (visible.value = false), HOLD_MS);
            }
        },
        { immediate: true },
    );

    onBeforeUnmount(clear);

    return computed(() => (visible.value ? read() : null));
};

const success = useDismissing(() => page.props.flash?.success);
const danger = useDismissing(() => page.props.flash?.danger);
const error = useDismissing(() => page.props.flash?.error);
</script>

<template>
    <div v-if="success || danger || error" class="mb-6 space-y-3">
        <div
            v-if="success"
            class="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800"
        >
            {{ success }}
        </div>
        <div
            v-if="danger"
            class="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800"
        >
            {{ danger }}
        </div>
        <div
            v-if="error"
            class="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800"
        >
            {{ error }}
        </div>
    </div>
</template>
