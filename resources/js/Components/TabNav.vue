<script setup>
defineProps({
    tabs: { type: Array, required: true },
    modelValue: { type: String, required: true },
});

defineEmits(['update:modelValue']);
</script>

<template>
    <div class="border-b border-slate-200">
        <nav class="-mb-px flex flex-wrap gap-x-6 gap-y-1" aria-label="Sections">
            <button
                v-for="tab in tabs"
                :key="tab.key"
                type="button"
                @click="$emit('update:modelValue', tab.key)"
                class="flex items-center gap-2 border-b-2 px-1 py-3 text-sm font-medium transition"
                :class="
                    modelValue === tab.key
                        ? 'border-orange-500 text-slate-900'
                        : 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700'
                "
            >
                {{ tab.label }}
                <span
                    v-if="tab.badge !== undefined && tab.badge !== null"
                    class="rounded-full px-2 py-0.5 text-xs font-medium"
                    :class="
                        tab.badgeTone === 'alert'
                            ? 'bg-rose-100 text-rose-700'
                            : tab.badgeTone === 'busy'
                              ? 'bg-orange-100 text-orange-700'
                              : 'bg-slate-100 text-slate-600'
                    "
                >
                    {{ tab.badge }}
                </span>
            </button>
        </nav>
    </div>
</template>
