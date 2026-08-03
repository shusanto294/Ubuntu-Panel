<script setup>
import { computed, ref } from 'vue';

/**
 * What version is installed, and whether a newer one has been published.
 *
 * Updating itself is deliberately a command rather than a button: the update
 * restarts the very services that would be serving the click, so it belongs in
 * a shell where you can watch it finish.
 */
const props = defineProps({
    update: { type: Object, required: true },
});

const state = ref(props.update);
const checking = ref(false);

const current = computed(() => state.value.current ?? {});
const latest = computed(() => state.value.latest ?? {});
const available = computed(() => state.value.available);
const error = computed(() => latest.value.error);

const describe = (version) => {
    if (!version) return 'unknown';
    const parts = [];
    if (version.version) parts.push(version.version);
    if (version.commit) parts.push(version.commit);
    return parts.length ? parts.join(' · ') : 'unknown';
};

const when = (iso) =>
    iso
        ? new Date(iso).toLocaleDateString([], {
              year: 'numeric',
              month: 'short',
              day: 'numeric',
          })
        : '';

const check = async () => {
    checking.value = true;

    try {
        const { data } = await window.axios.get(route('system.updates'));
        state.value = data;
    } catch (e) {
        state.value = {
            ...state.value,
            latest: { ...latest.value, error: 'Could not reach GitHub.' },
        };
    } finally {
        checking.value = false;
    }
};

const command = 'sudo -u ubuntupanel php artisan panel:update';
const copied = ref(false);

const copy = async () => {
    try {
        await navigator.clipboard.writeText(command);
        copied.value = true;
        setTimeout(() => (copied.value = false), 2000);
    } catch (e) {
        // Clipboard is unavailable over plain HTTP; the text is selectable.
    }
};
</script>

<template>
    <div
        class="rounded-xl border bg-white p-5"
        :class="available ? 'border-orange-300' : 'border-slate-200'"
    >
        <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
                <div class="flex items-center gap-2">
                    <p class="text-sm font-semibold text-slate-700">Panel version</p>
                    <span
                        v-if="available"
                        class="rounded-full bg-orange-100 px-2 py-0.5 text-[11px] font-medium text-orange-800"
                    >
                        update available
                    </span>
                    <span
                        v-else-if="!error"
                        class="rounded-full bg-emerald-50 px-2 py-0.5 text-[11px] font-medium text-emerald-700"
                    >
                        up to date
                    </span>
                </div>

                <p class="mt-1 font-mono text-sm text-slate-800">
                    {{ describe(current) }}
                </p>
                <p v-if="current.committed_at" class="text-xs text-slate-500">
                    installed {{ when(current.committed_at) }}
                </p>
            </div>

            <button
                @click="check"
                :disabled="checking"
                class="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-xs font-medium text-slate-700 transition hover:bg-slate-50 disabled:opacity-50"
            >
                {{ checking ? 'Checking…' : 'Check for updates' }}
            </button>
        </div>

        <p v-if="error" class="mt-3 text-xs text-amber-700">
            Could not check for updates: {{ error }}
        </p>

        <div v-else-if="available" class="mt-4 border-t border-slate-100 pt-4">
            <p class="text-xs text-slate-600">
                Latest published:
                <span class="font-mono text-slate-800">{{ describe(latest) }}</span>
                <span v-if="latest.committed_at" class="text-slate-400">
                    · {{ when(latest.committed_at) }}
                </span>
                <a
                    v-if="latest.url"
                    :href="latest.url"
                    target="_blank"
                    rel="noopener"
                    class="ml-1 text-orange-600 hover:underline"
                    >view</a
                >
            </p>

            <p class="mt-3 text-xs text-slate-500">
                Update from a shell — it restarts the panel's own services, so it
                cannot finish inside a web request:
            </p>

            <div
                class="mt-2 flex items-center justify-between gap-3 rounded-md bg-slate-900 px-3 py-2"
            >
                <code class="truncate font-mono text-xs text-slate-100">{{
                    command
                }}</code>
                <button
                    @click="copy"
                    class="shrink-0 text-xs text-slate-400 transition hover:text-white"
                >
                    {{ copied ? 'copied' : 'copy' }}
                </button>
            </div>
        </div>
    </div>
</template>
