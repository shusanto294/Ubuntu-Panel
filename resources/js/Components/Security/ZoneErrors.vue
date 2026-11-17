<script setup>
import { computed } from 'vue';
import { isPermissionError } from './edge';

/*
 * The zones a part of the Cloudflare tab could not be read from, and why —
 * once when every zone says the same thing, zone by zone otherwise.
 */
const props = defineProps({
    errors: { type: Array, default: () => [] }, // { zone, error }
    permission: { type: String, default: '' },
});

const grouped = computed(() => {
    const byError = new Map();
    for (const { zone, error } of props.errors) {
        byError.set(error, [...(byError.get(error) ?? []), zone]);
    }
    return [...byError.entries()].map(([error, zones]) => ({ error, zones }));
});

const needsPermission = computed(() => props.errors.some((e) => isPermissionError(e.error)));
</script>

<template>
    <div v-if="errors.length" class="mt-4 space-y-1 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800 ring-1 ring-amber-200">
        <p v-for="g in grouped" :key="g.error">
            <strong>{{ g.zones.join(', ') }}</strong>: {{ g.error }}
        </p>
        <p v-if="needsPermission && permission" class="text-xs">
            The API token needs <strong>{{ permission }}</strong> — edit it in Cloudflare under My Profile → API Tokens,
            then reload this tab.
        </p>
    </div>
</template>
