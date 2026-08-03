<script setup>
import { computed, ref } from 'vue';
import { router } from '@inertiajs/vue3';

const props = defineProps({
    services: { type: Array, default: () => [] },
});

const selected = ref([]);
const busy = ref(false);

const groups = [
    { key: 'core', label: 'Web stack' },
    { key: 'database', label: 'Databases' },
    { key: 'runtime', label: 'Runtimes and tools' },
    { key: 'mail', label: 'Mail' },
];

const grouped = computed(() =>
    groups
        .map((group) => ({
            ...group,
            items: props.services.filter((s) => s.group === group.key),
        }))
        .filter((group) => group.items.length),
);

const missing = computed(() =>
    props.services.filter((s) => s.status === 'not_installed'),
);

const statusStyles = {
    installed: 'bg-emerald-50 text-emerald-700 ring-emerald-200',
    installing: 'bg-orange-50 text-orange-700 ring-orange-200',
    queued: 'bg-amber-50 text-amber-700 ring-amber-200',
    failed: 'bg-rose-50 text-rose-700 ring-rose-200',
    not_installed: 'bg-slate-100 text-slate-600 ring-slate-200',
};

const statusLabels = {
    installed: 'installed',
    installing: 'installing',
    queued: 'queued',
    failed: 'failed',
    not_installed: 'not installed',
};

// A queued item that has not started yet can be pushed to the front.
const startNow = (service) => {
    busy.value = true;
    router.post(
        route('services.install', service.key),
        {},
        { preserveScroll: true, onFinish: () => (busy.value = false) },
    );
};

const install = (service) => {
    busy.value = true;
    router.post(
        route('services.install', service.key),
        {},
        { preserveScroll: true, onFinish: () => (busy.value = false) },
    );
};

const retry = (service) => {
    busy.value = true;
    router.post(
        route('services.retry', service.key),
        {},
        { preserveScroll: true, onFinish: () => (busy.value = false) },
    );
};

const reinstall = (service) => {
    if (!confirm(`Reinstall ${service.label}? Existing packages are left in place.`))
        return;

    busy.value = true;
    router.post(
        route('services.install', service.key),
        { force: true },
        { preserveScroll: true, onFinish: () => (busy.value = false) },
    );
};

const installSelected = () => {
    if (!selected.value.length) return;

    busy.value = true;
    router.post(
        route('services.install-many'),
        { services: selected.value },
        {
            preserveScroll: true,
            onSuccess: () => (selected.value = []),
            onFinish: () => (busy.value = false),
        },
    );
};

const installAllMissing = () => {
    selected.value = missing.value.map((s) => s.key);
    installSelected();
};

const detect = () => {
    busy.value = true;
    router.post(
        route('services.detect'),
        {},
        { preserveScroll: true, onFinish: () => (busy.value = false) },
    );
};

const toggle = (key) => {
    selected.value = selected.value.includes(key)
        ? selected.value.filter((k) => k !== key)
        : [...selected.value, key];
};
</script>

<template>
    <div class="overflow-hidden rounded-xl border border-slate-200 bg-white">
        <div
            class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-5 py-4"
        >
            <div>
                <h3 class="font-semibold text-slate-800">Services</h3>
                <p class="text-xs text-slate-500">
                    Queued items install together in a single apt transaction.
                    Anything already on the server is detected and skipped.
                </p>
            </div>
            <div class="flex flex-wrap gap-2">
                <button
                    @click="detect"
                    :disabled="busy || false"
                    class="rounded-md border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50 disabled:opacity-50"
                >
                    Refresh from server
                </button>
                <button
                    v-if="missing.length"
                    @click="installAllMissing"
                    :disabled="busy || false"
                    class="rounded-md bg-slate-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-slate-700 disabled:opacity-50"
                >
                    Install all missing ({{ missing.length }})
                </button>
                <button
                    v-if="selected.length"
                    @click="installSelected"
                    :disabled="busy || false"
                    class="rounded-md bg-orange-500 px-3 py-1.5 text-sm font-medium text-white hover:bg-orange-600 disabled:opacity-50"
                >
                    Install selected ({{ selected.length }})
                </button>
            </div>
        </div>

        <div v-for="group in grouped" :key="group.key">
            <p
                class="bg-slate-50 px-5 py-2 text-xs font-semibold uppercase tracking-wide text-slate-500"
            >
                {{ group.label }}
            </p>

            <ul class="divide-y divide-slate-100">
                <li
                    v-for="service in group.items"
                    :key="service.key"
                    class="flex flex-wrap items-center gap-3 px-5 py-3"
                >
                    <input
                        v-if="service.status === 'not_installed'"
                        type="checkbox"
                        :checked="selected.includes(service.key)"
                        @change="toggle(service.key)"
                        class="rounded border-slate-300 text-orange-500 focus:ring-orange-500"
                    />
                    <span v-else class="w-4"></span>

                    <div class="min-w-0 flex-1">
                        <p class="flex items-center gap-2 text-sm font-medium text-slate-800">
                            {{ service.label }}
                            <span
                                v-if="service.core"
                                class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-slate-500"
                                >required</span
                            >
                        </p>
                        <p class="truncate text-xs text-slate-500">
                            {{ service.version || service.description }}
                        </p>
                        <p
                            v-if="service.last_error"
                            class="mt-1 text-xs text-rose-600"
                        >
                            {{ service.last_error }}
                        </p>
                    </div>

                    <span
                        class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset"
                        :class="statusStyles[service.status]"
                    >
                        <span
                            v-if="service.status === 'installing'"
                            class="h-1.5 w-1.5 animate-pulse rounded-full bg-orange-500"
                        />
                        {{ statusLabels[service.status] }}
                    </span>

                    <div class="w-28 text-right">
                        <button
                            v-if="service.status === 'not_installed'"
                            @click="install(service)"
                            :disabled="busy || false"
                            class="text-sm text-orange-600 hover:underline disabled:opacity-50"
                        >
                            Install
                        </button>
                        <button
                            v-else-if="service.status === 'failed'"
                            @click="retry(service)"
                            :disabled="busy || false"
                            class="text-sm text-rose-600 hover:underline disabled:opacity-50"
                        >
                            Retry
                        </button>
                        <button
                            v-else-if="service.status === 'installed'"
                            @click="reinstall(service)"
                            :disabled="busy || false"
                            class="text-sm text-slate-500 hover:underline disabled:opacity-50"
                        >
                            Reinstall
                        </button>
                        <button
                            v-else-if="service.status === 'queued'"
                            @click="startNow(service)"
                            :disabled="busy || false"
                            class="text-sm text-orange-600 hover:underline disabled:opacity-50"
                        >
                            Install now
                        </button>
                        <span v-else class="text-xs text-orange-500">
                            installing…
                        </span>
                    </div>
                </li>
            </ul>
        </div>
    </div>
</template>
