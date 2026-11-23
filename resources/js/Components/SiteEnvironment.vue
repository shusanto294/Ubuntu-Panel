<script setup>
/*
 * The Environment tab: a Node site's variables — database URLs, signing
 * secrets, API keys — pasted the way a .env file is written.
 *
 * Stored encrypted and kept outside the site's directory, so a redeploy (which
 * clones into an empty directory) does not lose them. They reach the build and
 * the running app through the environment, which outranks any .env file a
 * repository commits.
 */
import { computed, ref } from 'vue';
import InputError from '@/Components/InputError.vue';
import { useForm } from '@inertiajs/vue3';

const props = defineProps({
    site: { type: Object, required: true },
    environment: { type: Object, default: () => ({}) },
});

const form = useForm({
    vars: props.environment?.vars ?? '',
    apply: 'restart',
});

// Hidden until asked for: this is the page somebody screen-shares.
const editing = ref(!props.environment?.vars);

const names = computed(() =>
    (props.environment?.vars ?? '')
        .split('\n')
        .map((line) => line.trim().replace(/^export\s+/, ''))
        .filter((line) => line && !line.startsWith('#') && line.includes('='))
        .map((line) => line.slice(0, line.indexOf('=')).trim()),
);

const failed = computed(() => props.site.status === 'failed');
const hasRepository = computed(() => !!props.site.repository);

const save = (apply) => {
    form.apply = apply;
    form.post(route('sites.env', props.site.id), {
        preserveScroll: true,
        onSuccess: () => {
            if (!form.hasErrors) editing.value = false;
        },
    });
};
</script>

<template>
    <div class="grid gap-6 lg:grid-cols-3">
        <div class="space-y-6 lg:col-span-2">
            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                <div class="flex items-start justify-between gap-4">
                    <div>
                        <h3 class="font-semibold text-slate-800">Environment variables</h3>
                        <p class="mt-1 text-sm text-slate-500">
                            Available to the build and to the running app. Paste
                            them the way a <code>.env</code> file is written, one
                            <code>NAME=value</code> per line.
                        </p>
                    </div>
                    <button
                        v-if="!editing"
                        type="button"
                        class="shrink-0 rounded-lg px-3 py-1.5 text-xs font-medium text-brand-600 ring-1 ring-brand-200 hover:bg-brand-50"
                        @click="editing = true"
                    >
                        Show and edit
                    </button>
                </div>

                <!-- the names only, until asked -->
                <div v-if="!editing" class="mt-4">
                    <ul
                        v-if="names.length"
                        class="divide-y divide-slate-100 rounded-xl ring-1 ring-slate-900/5"
                    >
                        <li
                            v-for="name in names"
                            :key="name"
                            class="flex items-center justify-between px-4 py-2 font-mono text-xs"
                        >
                            <span class="text-slate-800">{{ name }}</span>
                            <span class="text-slate-400">••••••••</span>
                        </li>
                    </ul>
                </div>

                <div v-else class="mt-4">
                    <textarea
                        v-model="form.vars"
                        rows="14"
                        spellcheck="false"
                        autocomplete="off"
                        class="block w-full rounded-xl border-slate-300 font-mono text-xs shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        placeholder="APP_SECRET=…&#10;ENCRYPTION_KEY=…&#10;MONGODB_URI=mongodb://…&#10;NEXT_PUBLIC_SITE_URL=https://example.com"
                    />
                    <InputError class="mt-2" :message="form.errors.vars" />

                    <div class="mt-4 flex flex-wrap items-center gap-3">
                        <button
                            type="button"
                            :disabled="form.processing"
                            class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white hover:bg-brand-700 disabled:opacity-50"
                            @click="save(failed ? 'deploy' : 'restart')"
                        >
                            {{
                                form.processing
                                    ? 'Saving…'
                                    : failed
                                      ? 'Save and deploy again'
                                      : 'Save and restart'
                            }}
                        </button>
                        <button
                            v-if="!failed && hasRepository"
                            type="button"
                            :disabled="form.processing"
                            class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50 disabled:opacity-50"
                            @click="save('rebuild')"
                        >
                            Save and rebuild
                        </button>
                        <button
                            v-if="environment?.vars"
                            type="button"
                            class="text-sm text-slate-500 hover:underline"
                            @click="
                                form.vars = environment.vars;
                                form.clearErrors();
                                editing = false;
                            "
                        >
                            Cancel
                        </button>
                    </div>
                </div>
            </div>
        </div>

        <div class="space-y-4 text-sm text-slate-600">
            <div class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <p class="font-medium text-slate-700">Restart or rebuild?</p>
                <p class="mt-1">
                    A restart is enough for values the app reads while it runs:
                    secrets, database and API settings.
                </p>
                <p class="mt-2">
                    Values the build bakes in — <code>NEXT_PUBLIC_…</code> in
                    Next.js, <code>NUXT_PUBLIC_…</code> in Nuxt, <code>VITE_…</code>
                    — only change with a rebuild.
                </p>
            </div>
            <div class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <p class="font-medium text-slate-700">Where they are kept</p>
                <p class="mt-1">
                    Encrypted in the panel, and in a file only root can read
                    outside the site's directory — so a redeploy does not lose
                    them, and they are not in the repository or a site backup.
                    They override any <code>.env</code> file the repository has.
                </p>
                <p class="mt-2">
                    <code>PORT</code> and <code>HOST</code> are set by the panel
                    so nginx can reach the app.
                </p>
            </div>
        </div>
    </div>
</template>
