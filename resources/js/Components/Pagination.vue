<script setup>
import { computed } from 'vue';

/*
 * The footer of a paged list: where you are, and the way to the rest. Pairs
 * with usePagination — bind its `page` with v-model and pass the rest through.
 * Says nothing when everything fits on one page.
 */
const props = defineProps({
    pages: { type: Number, required: true },
    total: { type: Number, required: true },
    perPage: { type: Number, required: true },
});

const page = defineModel({ type: Number, required: true });

const first = computed(() => (page.value - 1) * props.perPage + 1);
const last = computed(() => Math.min(page.value * props.perPage, props.total));

// The first, the last, and two either side of the current one, with a gap
// marked wherever pages are skipped.
const numbers = computed(() => {
    const shown = [];

    for (let n = 1; n <= props.pages; n++) {
        if (n === 1 || n === props.pages || Math.abs(n - page.value) <= 2) {
            if (shown.length && n - shown[shown.length - 1] > 1) shown.push(null);
            shown.push(n);
        }
    }

    return shown;
});

const button =
    'min-w-[2rem] rounded-lg px-2.5 py-1.5 font-medium ring-1 transition disabled:cursor-not-allowed disabled:opacity-40';
</script>

<template>
    <div
        v-if="pages > 1"
        class="flex flex-wrap items-center justify-between gap-3 border-t border-slate-100 px-5 py-3 text-xs text-slate-500"
    >
        <span>{{ first }}–{{ last }} of {{ total }}</span>
        <div class="flex flex-wrap items-center gap-1">
            <button
                type="button"
                :class="button"
                class="bg-white text-slate-600 ring-slate-200 hover:bg-slate-50"
                :disabled="page <= 1"
                @click="page--"
            >
                Previous
            </button>
            <template v-for="(n, index) in numbers" :key="n ?? `gap-${index}`">
                <span v-if="n === null" class="px-1 text-slate-400">…</span>
                <button
                    v-else
                    type="button"
                    :class="[
                        button,
                        n === page
                            ? 'bg-brand-600 text-white ring-brand-600'
                            : 'bg-white text-slate-600 ring-slate-200 hover:bg-slate-50',
                    ]"
                    @click="page = n"
                >
                    {{ n }}
                </button>
            </template>
            <button
                type="button"
                :class="button"
                class="bg-white text-slate-600 ring-slate-200 hover:bg-slate-50"
                :disabled="page >= pages"
                @click="page++"
            >
                Next
            </button>
        </div>
    </div>
</template>
