<script setup>
/*
 * The app store: search, filter, install, remove.
 *
 * Search is client-side on purpose. The catalogue is a fixed table of a few
 * dozen entries that ships with the page, so filtering it in the browser is
 * instant and works while an install is running — a round trip per keystroke
 * would be slower and would fight the live refresh.
 */
import { computed, ref } from 'vue';
import { router } from '@inertiajs/vue3';
import AppIcon from '@/Components/AppIcon.vue';
import { confirmAction, confirmDanger } from '@/dialog';

const props = defineProps({
    services: { type: Array, default: () => [] },
    // The running install or removal, if there is one. Its steps carry the app
    // they belong to, which is what turns a static "queued" badge into the
    // step that is actually running for this app right now.
    activeTask: { type: Object, default: null },
});

const search = ref('');
const activeGroup = ref('all');
const selected = ref([]);
const busy = ref(false);

const GROUPS = [
    { key: 'all', label: 'All' },
    { key: 'installed', label: 'Installed' },
    { key: 'core', label: 'Web stack' },
    { key: 'database', label: 'Databases' },
    { key: 'runtime', label: 'Runtimes' },
    { key: 'dns', label: 'DNS' },
    { key: 'mail', label: 'Mail' },
];

// Only the tabs that have something behind them.
const groups = computed(() =>
    GROUPS.filter(
        (group) =>
            group.key === 'all' ||
            (group.key === 'installed'
                ? props.services.some((s) => s.status === 'installed')
                : props.services.some((s) => s.group === group.key)),
    ),
);

const matches = (service, term) =>
    [service.label, service.key, service.description, service.keywords]
        .filter(Boolean)
        .some((field) => field.toLowerCase().includes(term));

const results = computed(() => {
    const term = search.value.trim().toLowerCase();

    return props.services.filter((service) => {
        if (activeGroup.value === 'installed') {
            if (service.status !== 'installed') return false;
        } else if (activeGroup.value !== 'all' && service.group !== activeGroup.value) {
            return false;
        }

        return !term || matches(service, term);
    });
});

const missing = computed(() => props.services.filter((s) => s.status === 'not_installed'));

// What the panel cannot host a site without: nginx, PHP-FPM, Composer,
// Certbot, MariaDB and WP-CLI, on the base packages. Everything else on this
// page is a choice, and "install all missing" swept those up too — including a
// mail server nobody asked for.
const missingRequired = computed(() =>
    props.services.filter((s) => s.core && s.status === 'not_installed'),
);
const installedCount = computed(
    () => props.services.filter((s) => s.status === 'installed').length,
);

const statusStyles = {
    installed: 'bg-emerald-50 text-emerald-700 ring-emerald-200',
    installing: 'bg-brand-50 text-brand-700 ring-brand-200',
    queued: 'bg-amber-50 text-amber-700 ring-amber-200',
    failed: 'bg-rose-50 text-rose-700 ring-rose-200',
    not_installed: 'bg-slate-100 text-slate-600 ring-slate-200',
};

const statusLabels = {
    installed: 'installed',
    installing: 'installing',
    // Not "queued": a worker has it within the second, and the card is about
    // to start reporting its steps.
    queued: 'starting',
    failed: 'failed',
    not_installed: 'not installed',
};

/*
 * Live progress, per app.
 *
 * A batch install is one task whose steps are interleaved across every app in
 * it, each step tagged with the app it belongs to. Picking out one app's steps
 * gives that app its own status and its own percentage — so a card says
 * "Installing the packages" while it is happening, rather than "queued" for
 * four minutes and then "installed".
 */
const stepsByApp = computed(() => {
    const map = {};

    for (const step of props.activeTask?.steps ?? []) {
        if (!step.group) continue;

        (map[step.group] ||= []).push(step);
    }

    return map;
});

const taskRunning = computed(() => props.activeTask?.running === true);

const sharedState = () =>
    taskRunning.value && props.activeTask?.current_step
        ? {
              label: props.activeTask.current_step,
              percent: props.activeTask.progress ?? 0,
              shared: true,
          }
        : null;

const liveState = (service) => {
    const steps = stepsByApp.value[service.key];

    // Some apps are only apt packages, so they contribute no step of their own
    // to a batch. They are still being installed; the task says by what.
    if (!steps || !steps.length) return sharedState();

    const finished = steps.filter((s) =>
        ['success', 'skipped', 'failed'].includes(s.status),
    ).length;
    const running = steps.find((s) => s.status === 'running');
    const percent = Math.round((finished / steps.length) * 100);

    // Steps left but none of this app's is running. Either its turn in the
    // batch has not come, or the shared apt transaction — which belongs to
    // every app in the batch and so is tagged with none of them — is running.
    // The task's own current step is the truthful thing to show for that.
    if (!running) {
        if (finished === steps.length) return { label: 'finishing', percent: 100 };
        if (!taskRunning.value) return null;

        const shared = sharedState();

        if (shared) return { ...shared, percent };

        return { label: finished ? 'waiting for the next step' : 'waiting its turn', percent };
    }

    return { label: running.name, percent, step: `${finished + 1}/${steps.length}` };
};

// What the badge says: the running step when there is one, the row's own
// status otherwise.
const statusText = (service) => {
    const live = liveState(service);

    if (live && ['queued', 'installing'].includes(service.status)) return live.label;

    return statusLabels[service.status] ?? service.status;
};

const post = (url, data = {}, options = {}) => {
    busy.value = true;
    router.post(url, data, {
        preserveScroll: true,
        ...options,
        onFinish: () => (busy.value = false),
    });
};

const install = (service) => post(route('services.install', service.key));
const retry = (service) => post(route('services.retry', service.key));

const reinstall = async (service) => {
    const go = await confirmAction(
        `Reinstall ${service.label}?`,
        'Existing packages are left in place.',
        'Reinstall',
    );

    if (!go) return;

    post(route('services.install', service.key), { force: true });
};

const uninstall = async (service) => {
    if (service.remove_blocked_by) return;

    const go = await confirmDanger(
        `Remove ${service.label}?`,
        'The packages are removed with apt. Configuration and data are left on ' +
            'disk, so installing it again brings it back.',
        'Remove',
    );

    if (!go) return;

    post(route('services.uninstall', service.key));
};

const installSelected = () => {
    if (!selected.value.length) return;

    post(
        route('services.install-many'),
        { services: selected.value },
        { onSuccess: () => (selected.value = []) },
    );
};

const installRequired = () => {
    if (!missingRequired.value.length) return;

    post(
        route('services.install-many'),
        { services: missingRequired.value.map((s) => s.key) },
        { onSuccess: () => (selected.value = []) },
    );
};

const detect = () => post(route('services.detect'));

const toggle = (key) => {
    selected.value = selected.value.includes(key)
        ? selected.value.filter((k) => k !== key)
        : [...selected.value, key];
};
</script>

<template>
    <div class="space-y-4">
        <!-- search and bulk actions -->
        <div class="rounded-2xl bg-white p-4 shadow-sm ring-1 ring-slate-900/5">
            <div class="flex flex-wrap items-center gap-3">
                <div class="relative min-w-0 flex-1">
                    <svg
                        class="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400"
                        viewBox="0 0 24 24"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="1.8"
                        stroke-linecap="round"
                    >
                        <circle cx="11" cy="11" r="7" />
                        <path d="M20 20l-3.5-3.5" />
                    </svg>
                    <input
                        v-model="search"
                        type="search"
                        placeholder="Search apps — try “cache”, “backup”, “docker”"
                        class="w-full rounded-xl border-0 bg-slate-50 py-2 pl-9 pr-3 text-sm text-slate-800 ring-1 ring-inset ring-slate-200 placeholder:text-slate-400 focus:bg-white focus:ring-2 focus:ring-brand-500"
                    />
                </div>

                <button
                    @click="detect"
                    :disabled="busy"
                    class="rounded-xl px-3 py-2 text-sm text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50 disabled:opacity-50"
                >
                    Rescan server
                </button>
                <button
                    v-if="missingRequired.length"
                    @click="installRequired"
                    :disabled="busy"
                    :title="missingRequired.map((a) => a.label).join(', ')"
                    class="rounded-xl bg-slate-900 px-3 py-2 text-sm font-medium text-white hover:bg-slate-700 disabled:opacity-50"
                >
                    Install all required ({{ missingRequired.length }})
                </button>
                <button
                    v-if="selected.length"
                    @click="installSelected"
                    :disabled="busy"
                    class="rounded-xl bg-brand-600 px-3 py-2 text-sm font-medium text-white hover:bg-brand-700 disabled:opacity-50"
                >
                    Install selected ({{ selected.length }})
                </button>
            </div>

            <div class="mt-3 flex flex-wrap gap-1.5">
                <button
                    v-for="group in groups"
                    :key="group.key"
                    @click="activeGroup = group.key"
                    class="rounded-full px-3 py-1 text-xs font-medium transition"
                    :class="
                        activeGroup === group.key
                            ? 'bg-slate-900 text-white'
                            : 'bg-slate-100 text-slate-600 hover:bg-slate-200'
                    "
                >
                    {{ group.label }}
                    <span v-if="group.key === 'installed'" class="opacity-60"
                        >· {{ installedCount }}</span
                    >
                </button>
            </div>
        </div>

        <!-- results -->
        <div
            v-if="results.length"
            class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3"
        >
            <div
                v-for="service in results"
                :key="service.key"
                class="flex flex-col rounded-2xl bg-white p-4 shadow-sm ring-1 ring-slate-900/5"
            >
                <div class="flex items-start gap-3">
                    <AppIcon :app-key="service.key" :label="service.label" size="md" />

                    <div class="min-w-0 flex-1">
                        <div class="flex items-center gap-2">
                            <h3 class="truncate text-sm font-semibold text-slate-800">
                                {{ service.label }}
                            </h3>
                            <span
                                v-if="service.core"
                                class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-slate-500"
                                >required</span
                            >
                        </div>
                        <p class="mt-0.5 line-clamp-2 text-xs text-slate-500">
                            {{ service.description }}
                        </p>
                    </div>

                    <input
                        v-if="service.status === 'not_installed'"
                        type="checkbox"
                        :checked="selected.includes(service.key)"
                        @change="toggle(service.key)"
                        class="mt-1 rounded border-slate-300 text-brand-500 focus:ring-brand-500"
                        :aria-label="`Select ${service.label}`"
                    />
                </div>

                <p
                    v-if="service.status === 'installed' && service.version"
                    class="mt-2 truncate text-xs text-slate-400"
                >
                    {{ service.version }}
                </p>
                <p
                    v-else-if="service.size_hint"
                    class="mt-2 text-xs text-slate-400"
                >
                    {{ service.size_hint }} on disk
                </p>

                <p v-if="service.last_error" class="mt-2 text-xs text-rose-600">
                    {{ service.last_error }}
                </p>

                <!-- What it is doing right now, while it is doing it. -->
                <div v-if="liveState(service) && ['queued', 'installing'].includes(service.status)" class="mt-2">
                    <div class="flex items-center justify-between text-[11px] text-slate-500">
                        <span class="truncate">{{ liveState(service).label }}</span>
                        <span v-if="liveState(service).step" class="ml-2 shrink-0 tabular-nums">
                            {{ liveState(service).step }}
                        </span>
                    </div>
                    <div class="mt-1 h-1 overflow-hidden rounded-full bg-slate-100">
                        <div
                            class="h-full rounded-full bg-brand-500 transition-all duration-500"
                            :style="{ width: `${liveState(service).percent}%` }"
                        />
                    </div>
                </div>

                <div class="mt-3 flex items-center justify-between gap-2 border-t border-slate-100 pt-3">
                    <span
                        class="inline-flex max-w-[60%] items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset"
                        :class="statusStyles[service.status]"
                    >
                        <span
                            v-if="['installing', 'queued'].includes(service.status)"
                            class="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-current"
                        />
                        <span class="truncate">{{ statusText(service) }}</span>
                    </span>

                    <div class="flex items-center gap-3">
                        <button
                            v-if="service.status === 'not_installed'"
                            @click="install(service)"
                            :disabled="busy"
                            class="rounded-lg bg-brand-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-brand-700 disabled:opacity-50"
                        >
                            Install
                        </button>

                        <button
                            v-else-if="service.status === 'failed'"
                            @click="retry(service)"
                            :disabled="busy"
                            class="text-xs font-medium text-rose-600 hover:underline disabled:opacity-50"
                        >
                            Retry
                        </button>

                        <button
                            v-else-if="service.status === 'queued'"
                            @click="install(service)"
                            :disabled="busy"
                            class="text-xs font-medium text-brand-600 hover:underline disabled:opacity-50"
                        >
                            Install now
                        </button>

                        <template v-else-if="service.status === 'installed'">
                            <button
                                @click="reinstall(service)"
                                :disabled="busy"
                                class="text-xs text-slate-500 hover:underline disabled:opacity-50"
                            >
                                Reinstall
                            </button>
                            <button
                                v-if="service.removable"
                                @click="uninstall(service)"
                                :disabled="busy || !!service.remove_blocked_by"
                                :title="service.remove_blocked_by || 'Remove this app'"
                                class="text-xs font-medium text-rose-600 hover:underline disabled:cursor-not-allowed disabled:text-slate-300 disabled:no-underline"
                            >
                                Remove
                            </button>
                        </template>

                        <span v-else class="text-xs text-brand-500">working…</span>
                    </div>
                </div>

                <!--
                  The reason lives next to the disabled button rather than only
                  in a tooltip: "why can I not remove this" is the whole
                  question, and a tooltip does not answer it on a phone.
                -->
                <p
                    v-if="service.status === 'installed' && service.remove_blocked_by"
                    class="mt-2 text-[11px] leading-snug text-slate-400"
                >
                    {{ service.remove_blocked_by }}
                </p>
            </div>
        </div>

        <div
            v-else
            class="rounded-2xl bg-white px-5 py-12 text-center shadow-sm ring-1 ring-slate-900/5"
        >
            <p class="text-sm text-slate-500">
                Nothing matches “{{ search }}”.
            </p>
            <button
                @click="((search = ''), (activeGroup = 'all'))"
                class="mt-2 text-sm text-brand-600 hover:underline"
            >
                Clear the search
            </button>
        </div>
    </div>
</template>
