<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { confirmAction } from '@/dialog';

/**
 * What version is installed, whether a newer one has been published, and the
 * way to get it.
 *
 * The update restarts the service that would be serving the click, so it runs
 * in a transient systemd unit of its own and this card follows it from the
 * outside: it polls until a different version answers, and reloads the page
 * onto it. A panel that does not come back is the old one put back by the
 * installer, and the card says so with the end of the installer's log.
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
const last = computed(() => state.value.last ?? null);

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

const whenUnix = (seconds) =>
    seconds
        ? new Date(seconds * 1000).toLocaleString([], {
              day: 'numeric',
              month: 'short',
              hour: '2-digit',
              minute: '2-digit',
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

// ------------------------------------------------------------ automatic

const auto = ref(Boolean(props.update.auto));
const savingAuto = ref(false);

const setAuto = async (enabled) => {
    savingAuto.value = true;

    try {
        const { data } = await window.axios.post(route('system.update.automatic'), { enabled });
        auto.value = data.auto;
    } catch (e) {
        auto.value = !enabled;
    } finally {
        savingAuto.value = false;
    }
};

// --------------------------------------------------------------- update

// idle → running (the installer is at work) → restarting (nothing answers) →
// done (a new version answered; the page reloads) or failed.
const phase = ref('idle');
const problem = ref('');
const startedFrom = ref(null);
let timer = null;
let deadline = 0;

const stopPolling = () => {
    if (timer) clearTimeout(timer);
    timer = null;
};

const poll = async () => {
    try {
        const { data } = await window.axios.get(route('system.update.status'), { timeout: 8000 });

        if (data.version && data.version !== startedFrom.value) {
            phase.value = 'done';
            stopPolling();
            setTimeout(() => window.location.reload(), 1500);
            return;
        }

        if (data.state === 'running') {
            phase.value = 'running';
        } else {
            // Finished, and the same version is answering: it did not take.
            phase.value = 'failed';
            state.value = { ...state.value, last: data.last ?? state.value.last };
            stopPolling();
            return;
        }
    } catch (e) {
        // The panel is down while the new binary goes in — that is expected.
        phase.value = 'restarting';
    }

    if (Date.now() > deadline) {
        phase.value = 'failed';
        problem.value =
            'The panel has not come back after twenty minutes. Check it over SSH: ' +
            'journalctl -u ubuntu-panel-update and /var/log/ubuntu-panel/install.log.';
        stopPolling();
        return;
    }

    timer = setTimeout(poll, 3000);
};

const follow = () => {
    startedFrom.value = current.value.version ?? null;
    deadline = Date.now() + 20 * 60 * 1000;
    phase.value = 'running';
    stopPolling();
    timer = setTimeout(poll, 3000);
};

const update = async () => {
    const go = await confirmAction(
        'Update the panel now?',
        'The newest release is downloaded and installed, and the panel restarts — this ' +
            'page reloads by itself when it is back, usually within a minute. The database ' +
            'is backed up first, and if the new version does not start the old one is put back.',
        'Update',
    );

    if (!go) return;

    problem.value = '';

    try {
        await window.axios.post(route('system.update'));
        follow();
    } catch (e) {
        phase.value = 'failed';
        problem.value = e.response?.data?.error ?? 'The update could not be started.';
    }
};

onMounted(() => {
    // The page is rendered without asking GitHub, so the answer is fetched
    // here once the card is on screen, unless the panel already had one.
    if (state.value.pending) check();

    // An update already under way — started by the clock, or in another tab.
    if (state.value.running) follow();
});

onBeforeUnmount(stopPolling);

const busy = computed(() => ['running', 'restarting', 'done'].includes(phase.value));

// The panel builds this: it knows where it is installed and which user owns
// it, and neither is guaranteed to be the default.
const command = computed(() => state.value.command ?? '');
const copied = ref(false);

const copy = async () => {
    try {
        await navigator.clipboard.writeText(command.value);
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
        :class="available ? 'border-brand-300' : 'border-slate-200'"
    >
        <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
                <div class="flex items-center gap-2">
                    <p class="text-sm font-semibold text-slate-700">Panel version</p>
                    <span
                        v-if="available"
                        class="rounded-full bg-brand-100 px-2 py-0.5 text-[11px] font-medium text-brand-800"
                    >
                        update available
                    </span>
                    <span
                        v-else-if="!error && !state.pending"
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
                :disabled="checking || busy"
                class="rounded-xl bg-white px-3 py-1.5 text-xs font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
            >
                {{ checking ? 'Checking…' : 'Check for updates' }}
            </button>
        </div>

        <!-- An update under way, followed from outside the panel. -->
        <div v-if="busy" class="mt-4 rounded-lg bg-brand-50 px-4 py-3 text-sm text-brand-800">
            <p class="flex items-center gap-2 font-medium">
                <svg
                    v-if="phase !== 'done'"
                    class="h-4 w-4 animate-spin"
                    viewBox="0 0 24 24"
                    fill="none"
                >
                    <circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" class="opacity-25" />
                    <path d="M22 12a10 10 0 0 1-10 10" stroke="currentColor" stroke-width="3" />
                </svg>
                <template v-if="phase === 'running'">Downloading and installing the update…</template>
                <template v-else-if="phase === 'restarting'">The panel is restarting…</template>
                <template v-else>Updated. Reloading…</template>
            </p>
            <p v-if="phase !== 'done'" class="mt-1 text-xs text-brand-700">
                Keep this page open — it reloads by itself when the new version answers.
            </p>
        </div>

        <div
            v-else-if="phase === 'failed'"
            class="mt-4 rounded-lg bg-rose-50 px-4 py-3 text-sm text-rose-800"
        >
            <p class="font-medium">The update did not complete.</p>
            <p class="mt-1 text-xs">
                {{ problem || last?.message || 'The previous version is still running.' }}
            </p>
            <details v-if="!problem && last?.log" class="mt-2 text-xs">
                <summary class="cursor-pointer">The installer's log</summary>
                <pre
                    class="mt-2 max-h-64 overflow-auto rounded-md bg-slate-900 px-3 py-2 font-mono text-[11px] text-slate-200"
                    >{{ last.log }}</pre
                >
            </details>
        </div>

        <p v-else-if="error" class="mt-3 text-xs text-amber-700">
            Could not check for updates: {{ error }}
        </p>

        <div v-if="available && !busy" class="mt-4 border-t border-slate-100 pt-4">
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
                    class="ml-1 text-brand-600 hover:underline"
                    >what's new</a
                >
            </p>

            <button
                @click="update"
                class="mt-3 w-full rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
            >
                Update now
            </button>

            <details class="mt-3 text-xs text-slate-500">
                <summary class="cursor-pointer">Or run it yourself over SSH</summary>
                <div
                    class="mt-2 flex items-center justify-between gap-3 rounded-md bg-slate-900 px-3 py-2"
                >
                    <code class="truncate font-mono text-xs text-slate-100">{{ command }}</code>
                    <button
                        @click="copy"
                        class="shrink-0 text-xs text-slate-400 transition hover:text-white"
                    >
                        {{ copied ? 'copied' : 'copy' }}
                    </button>
                </div>
            </details>
        </div>

        <!-- Automatic updates. Off unless somebody turns them on. -->
        <div class="mt-4 flex items-start justify-between gap-4 border-t border-slate-100 pt-4">
            <div>
                <p class="text-sm font-medium text-slate-700">Update automatically</p>
                <p class="mt-0.5 text-xs text-slate-500">
                    The panel checks for a new release every few hours. With this on, it
                    installs one by itself — never in the middle of a deploy, an install or a
                    backup, and a release that fails to install is not retried for a day.
                </p>
            </div>
            <button
                type="button"
                role="switch"
                :aria-checked="auto"
                :disabled="savingAuto"
                @click="setAuto(!auto)"
                class="relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full transition disabled:opacity-50"
                :class="auto ? 'bg-brand-600' : 'bg-slate-200'"
            >
                <span
                    class="pointer-events-none mt-0.5 inline-block h-5 w-5 rounded-full bg-white shadow transition"
                    :class="auto ? 'translate-x-5' : 'translate-x-0.5'"
                />
            </button>
        </div>

        <!-- How the last update went. -->
        <div
            v-if="last && phase === 'idle'"
            class="mt-4 border-t border-slate-100 pt-4 text-xs"
            :class="last.state === 'failed' ? 'text-rose-700' : 'text-slate-500'"
        >
            <p>
                Last update{{ last.auto ? ' (automatic)' : '' }}, {{ whenUnix(last.at) }}:
                {{ last.message }}
            </p>
            <details v-if="last.log" class="mt-2">
                <summary class="cursor-pointer text-slate-500">The installer's log</summary>
                <pre
                    class="mt-2 max-h-64 overflow-auto rounded-md bg-slate-900 px-3 py-2 font-mono text-[11px] text-slate-200"
                    >{{ last.log }}</pre
                >
            </details>
        </div>
    </div>
</template>
