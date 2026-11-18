<script setup>
import { ref } from 'vue';
import Modal from '@/Components/Modal.vue';
import { copyText } from '@/clipboard';

/*
 * One mail domain's records as the public DNS has them, against what this
 * server needs for mail to arrive and to be trusted when it leaves. Read from
 * 1.1.1.1, so it is what Gmail sees, whoever hosts the domain's DNS.
 */
const props = defineProps({
    show: Boolean,
    health: { type: Object, default: null },
    checking: Boolean,
});

const emit = defineEmits(['close', 'recheck']);

const copied = ref('');

const copy = async (key, text) => {
    if (await copyText(text)) {
        copied.value = key;
        setTimeout(() => (copied.value = ''), 1500);
    }
};

const checkedAgo = (seconds) => {
    if (!seconds) return '';
    const age = Math.max(0, Math.round(Date.now() / 1000 - seconds));
    if (age < 60) return 'just now';
    if (age < 3600) return `${Math.round(age / 60)} min ago`;
    return `${Math.round(age / 3600)} h ago`;
};
</script>

<template>
    <Modal :show="show" max-width="3xl" @close="emit('close')">
        <div class="p-5 sm:p-6">
            <div class="flex flex-wrap items-start justify-between gap-3">
                <div>
                    <h3 class="text-lg font-semibold text-slate-900">
                        Mail DNS for {{ health?.domain }}
                    </h3>
                    <p class="text-sm text-slate-500">
                        <template v-if="health">
                            {{ health.passed }} of {{ health.total }} required records are right.
                            Checked against public DNS {{ checkedAgo(health.checked_at) }}.
                        </template>
                        <template v-else>Checking…</template>
                    </p>
                </div>
                <button
                    type="button"
                    :disabled="checking"
                    class="rounded-xl bg-white px-3 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                    @click="emit('recheck')"
                >
                    {{ checking ? 'Checking…' : 'Check again' }}
                </button>
            </div>

            <ul v-if="health" class="mt-5 space-y-3">
                <li
                    v-for="check in health.checks"
                    :key="check.key"
                    class="rounded-xl p-4 ring-1"
                    :class="
                        check.ok
                            ? 'bg-white ring-slate-200'
                            : check.required
                              ? 'bg-rose-50/60 ring-rose-200'
                              : 'bg-amber-50/60 ring-amber-200'
                    "
                >
                    <div class="flex items-start gap-3">
                        <span
                            class="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-xs font-bold text-white"
                            :class="check.ok ? 'bg-emerald-500' : check.required ? 'bg-rose-500' : 'bg-amber-400'"
                            :aria-label="check.ok ? 'Set correctly' : 'Needs fixing'"
                        >
                            {{ check.ok ? '✓' : check.required ? '✕' : '!' }}
                        </span>
                        <div class="min-w-0 flex-1">
                            <p class="font-medium text-slate-900">
                                {{ check.label }}
                                <span class="ml-1 rounded bg-slate-100 px-1.5 py-0.5 font-mono text-xs text-slate-600">{{ check.type }}</span>
                                <span v-if="!check.required" class="ml-1 text-xs font-normal text-slate-500">optional</span>
                            </p>
                            <p class="mt-0.5 break-all font-mono text-xs text-slate-500">{{ check.name }}</p>

                            <p v-if="check.hint" class="mt-2 text-sm" :class="check.required ? 'text-rose-700' : 'text-amber-800'">
                                {{ check.hint }}
                            </p>

                            <dl v-if="!check.ok && (check.expected || check.found)" class="mt-2 space-y-1.5 text-xs">
                                <div v-if="check.expected">
                                    <dt class="text-slate-500">
                                        {{ check.type === 'PTR' ? 'Should be' : 'Set it to' }}
                                    </dt>
                                    <dd class="mt-0.5 flex items-start gap-2">
                                        <code class="min-w-0 flex-1 break-all rounded bg-white px-2 py-1 text-slate-800 ring-1 ring-slate-200">{{ check.expected }}</code>
                                        <button
                                            type="button"
                                            class="shrink-0 rounded-lg px-2 py-1 font-medium text-brand-600 ring-1 ring-slate-900/10 hover:bg-slate-50"
                                            @click="copy(check.key, check.expected)"
                                        >
                                            {{ copied === check.key ? 'Copied' : 'Copy' }}
                                        </button>
                                    </dd>
                                </div>
                                <div>
                                    <dt class="text-slate-500">Found now</dt>
                                    <dd class="mt-0.5 break-all font-mono text-slate-600">{{ check.found || 'nothing' }}</dd>
                                </div>
                            </dl>
                            <p v-else-if="check.ok && check.found" class="mt-1 break-all font-mono text-xs text-emerald-700">
                                {{ check.found }}
                            </p>
                        </div>
                    </div>
                </li>
            </ul>

            <p class="mt-5 text-xs text-slate-500">
                A record just added can take a few minutes to show here, and longer if its old value is still cached.
                Reverse DNS is set at your hosting provider, not with the domain's DNS.
            </p>

            <div class="mt-4 flex justify-end">
                <button
                    type="button"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                    @click="emit('close')"
                >
                    Close
                </button>
            </div>
        </div>
    </Modal>
</template>
