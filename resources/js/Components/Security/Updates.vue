<script setup>
import { confirmAction } from '@/dialog';
import { card } from './ui';
import { router } from '@inertiajs/vue3';

const props = defineProps({
    updates: Object,
    busy: Boolean,
});

const setAutomatic = (enable) =>
    router.post(route('security.updates.automatic'), { enable }, { preserveScroll: true });

const install = async () => {
    if (!(await confirmAction('Install security updates now?', 'Services may restart briefly as their packages are updated.', 'Install')))
        return;
    router.post(route('security.updates.install'), {}, { preserveScroll: true });
};
</script>

<template>
    <div class="grid gap-6 lg:grid-cols-2">
        <div :class="card">
            <h3 class="font-semibold text-slate-900">Waiting to be installed</h3>
            <dl class="mt-4 grid grid-cols-2 gap-4">
                <div>
                    <dt class="text-xs text-slate-500">Security updates</dt>
                    <dd class="text-3xl font-semibold" :class="updates.security ? 'text-rose-600' : 'text-emerald-600'">
                        {{ updates.security }}
                    </dd>
                </div>
                <div>
                    <dt class="text-xs text-slate-500">All updates</dt>
                    <dd class="text-3xl font-semibold text-slate-900">{{ updates.pending }}</dd>
                </div>
            </dl>
            <p v-if="updates.reboot_required" class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800">
                A reboot is needed to finish an update that is already installed (usually
                the kernel).
            </p>
            <button
                type="button"
                class="mt-4 rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-brand-700 disabled:opacity-50"
                :disabled="busy || !updates.security"
                @click="install"
            >
                Install security updates
            </button>
        </div>

        <div :class="card">
            <h3 class="flex items-center gap-2 font-semibold text-slate-900">
                Automatic security updates
                <span
                    class="rounded-full px-2 py-0.5 text-xs font-medium"
                    :class="updates.automatic ? 'bg-emerald-50 text-emerald-700' : 'bg-slate-100 text-slate-600'"
                >
                    {{ updates.automatic ? 'on' : 'off' }}
                </span>
            </h3>
            <p class="mt-1 text-sm text-slate-500">
                unattended-upgrades installs packages from Ubuntu's security pocket every day,
                and nothing else — your PHP, Node and database versions are left as they are.
            </p>
            <button
                type="button"
                class="mt-4 rounded-xl px-4 py-2.5 text-sm font-medium ring-1 transition disabled:opacity-50"
                :class="updates.automatic ? 'text-slate-700 ring-slate-900/10 hover:bg-slate-50' : 'bg-brand-600 text-white ring-brand-600 hover:bg-brand-700'"
                :disabled="busy"
                @click="setAutomatic(!updates.automatic)"
            >
                {{ updates.automatic ? 'Turn off' : 'Turn on' }}
            </button>
        </div>
    </div>
</template>
