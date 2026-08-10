<script setup>
/*
 * "This needs MariaDB" — and a button that installs it, here.
 *
 * Sites, Databases and Email all have the same problem: the form is in front
 * of you and the app behind it is not on the machine. Sending someone to the
 * Apps page, finding the right entry, installing it and navigating back is
 * four steps to answer a question the page already knew the answer to.
 *
 * Renders nothing at all when everything it needs is present, so it can be
 * dropped into a page without cluttering the normal case.
 */
import { computed, ref } from 'vue';
import { router } from '@inertiajs/vue3';
import AppIcon from '@/Components/AppIcon.vue';
import { useLiveRefresh } from '@/Composables/useLiveRefresh';

const props = defineProps({
    // Every app the page could need, from service_requirements_json.
    apps: { type: Array, default: () => [] },
    // The keys needed right now — for Sites this narrows with the site type.
    need: { type: Array, default: () => [] },
    // What to say above the list. The default suits most pages.
    title: { type: String, default: '' },
    note: { type: String, default: '' },
    // Inertia props to reload while an install is running.
    refresh: { type: Array, default: () => ['requirements'] },
});

const busy = ref(false);

const required = computed(() =>
    props.need
        .map((key) => props.apps.find((app) => app.key === key))
        .filter(Boolean),
);

const missing = computed(() =>
    required.value.filter((app) => app.status !== 'installed'),
);

const working = computed(() =>
    missing.value.filter((app) => ['queued', 'installing'].includes(app.status)),
);

// Poll while anything is mid-install so the notice clears itself and the form
// underneath comes alive without a manual reload.
useLiveRefresh(
    computed(() => working.value.length > 0),
    props.refresh,
);

const heading = computed(() => {
    if (props.title) return props.title;

    const names = missing.value.map((app) => app.label);

    if (!names.length) return '';
    if (names.length === 1) return `${names[0]} is not installed yet`;

    return `${names.slice(0, -1).join(', ')} and ${names.at(-1)} are not installed yet`;
});

const install = (key) => {
    busy.value = true;
    router.post(
        route('services.install', key),
        {},
        { preserveScroll: true, onFinish: () => (busy.value = false) },
    );
};

const installAll = () => {
    busy.value = true;
    router.post(
        route('services.install-many'),
        { services: missing.value.map((app) => app.key) },
        { preserveScroll: true, onFinish: () => (busy.value = false) },
    );
};

const statusText = (app) => {
    if (app.status === 'installing') return 'installing…';
    if (app.status === 'queued') return 'starting…';
    if (app.status === 'failed') return 'the last attempt failed';

    return app.size_hint ? `${app.size_hint} on disk` : '';
};
</script>

<template>
    <div
        v-if="missing.length"
        class="rounded-2xl bg-amber-50 p-4 ring-1 ring-amber-200"
    >
        <div class="flex items-start gap-3">
            <span
                class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-amber-100 text-amber-700"
            >
                <svg
                    class="h-4 w-4"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.8"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                >
                    <path d="M12 9v4m0 3h.01M10.3 4.3L2.8 17.3A2 2 0 0 0 4.5 20h15a2 2 0 0 0 1.7-2.7L13.7 4.3a2 2 0 0 0-3.4 0z" />
                </svg>
            </span>

            <div class="min-w-0 flex-1">
                <p class="text-sm font-semibold text-amber-900">{{ heading }}</p>
                <p v-if="note" class="mt-0.5 text-xs text-amber-800">{{ note }}</p>
                <p v-else-if="working.length" class="mt-0.5 text-xs text-amber-800">
                    This page will unlock itself when the install finishes.
                </p>
                <p v-else class="mt-0.5 text-xs text-amber-800">
                    Install it here — no need to leave this page.
                </p>

                <ul class="mt-3 space-y-2">
                    <li
                        v-for="app in missing"
                        :key="app.key"
                        class="flex items-center gap-3 rounded-xl bg-white/70 px-3 py-2 ring-1 ring-amber-100"
                    >
                        <AppIcon :app-key="app.key" :label="app.label" size="sm" />

                        <div class="min-w-0 flex-1">
                            <p class="text-sm font-medium text-slate-800">{{ app.label }}</p>
                            <p class="truncate text-xs text-slate-500">
                                {{ statusText(app) || app.description }}
                            </p>
                        </div>

                        <span
                            v-if="['queued', 'installing'].includes(app.status)"
                            class="inline-flex items-center gap-1.5 text-xs font-medium text-amber-700"
                        >
                            <span class="h-1.5 w-1.5 animate-pulse rounded-full bg-amber-500" />
                            working
                        </span>
                        <button
                            v-else
                            @click="install(app.key)"
                            :disabled="busy"
                            class="shrink-0 rounded-lg bg-slate-900 px-3 py-1.5 text-xs font-medium text-white hover:bg-slate-700 disabled:opacity-50"
                        >
                            {{ app.status === 'failed' ? 'Try again' : 'Install' }}
                        </button>
                    </li>
                </ul>

                <button
                    v-if="missing.length > 1 && !working.length"
                    @click="installAll"
                    :disabled="busy"
                    class="mt-3 text-xs font-medium text-amber-900 underline hover:no-underline disabled:opacity-50"
                >
                    Install all {{ missing.length }} together
                </button>
            </div>
        </div>
    </div>
</template>
