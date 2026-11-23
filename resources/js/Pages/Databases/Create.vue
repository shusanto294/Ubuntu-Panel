<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import RequiredApps from '@/Components/RequiredApps.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, Link, useForm } from '@inertiajs/vue3';

const props = defineProps({
    availableEngines: Array,
    requirements: { type: Array, default: () => [] },
    engines: Object,
});

// Every engine this machine has, and every one the panel can install — so an
// engine that is not here yet is a choice on this form rather than a trip to
// somewhere else first.
const installable = computed(() => props.requirements.map((app) => app.key));

const engineOptions = computed(() =>
    Object.keys(props.engines ?? {}).filter(
        (engine) =>
            (props.availableEngines ?? []).includes(engine) ||
            installable.value.includes(engine),
    ),
);

const isInstalled = (engine) => (props.availableEngines ?? []).includes(engine);

const form = useForm({
    engine: props.availableEngines?.[0] ?? 'mysql',
    name: '',
    username: '',
    password: '',
});

const submit = () => form.post(route('databases.store'));
</script>

<template>
    <Head title="New database" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">New database</h2>
                <p class="text-sm text-slate-500">
                    Leave the username and password blank and the panel
                    generates both.
                </p>
            </div>
        </template>

        <RequiredApps
            v-if="!isInstalled(form.engine)"
            :apps="requirements"
            :need="[form.engine]"
            :refresh="['requirements', 'availableEngines']"
            class="mb-6 max-w-2xl"
            note="A database needs its engine on this machine first. Install it here; the form unlocks itself when it lands."
        />

        <form
            @submit.prevent="submit"
            class="max-w-2xl space-y-6 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div>
                <InputLabel for="engine" value="Engine" />
                <select
                    id="engine"
                    v-model="form.engine"
                    class="mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500"
                >
                    <option
                        v-for="engine in engineOptions"
                        :key="engine"
                        :value="engine"
                    >
                        {{ engines[engine] }}{{ isInstalled(engine) ? '' : ' — not installed yet' }}
                    </option>
                </select>
                <InputError class="mt-2" :message="form.errors.engine" />
            </div>

            <div>
                <InputLabel for="name" value="Database name" />
                <TextInput
                    id="name"
                    v-model="form.name"
                    class="mt-1 block w-full"
                    placeholder="my_app"
                />
                <InputError class="mt-2" :message="form.errors.name" />
                <p class="mt-1 text-xs text-slate-500">
                    Letters, numbers and underscores.
                </p>
            </div>

            <div>
                <InputLabel for="username" value="Username (optional)" />
                <TextInput
                    id="username"
                    v-model="form.username"
                    class="mt-1 block w-full"
                    placeholder="generated from the name"
                />
                <InputError class="mt-2" :message="form.errors.username" />
            </div>

            <div>
                <InputLabel for="password" value="Password (optional)" />
                <TextInput
                    id="password"
                    type="password"
                    v-model="form.password"
                    class="mt-1 block w-full"
                    placeholder="generated if left blank"
                    autocomplete="new-password"
                />
                <InputError class="mt-2" :message="form.errors.password" />
                <p class="mt-1 text-xs text-slate-500">
                    Whatever is used ends up encrypted, and readable again from
                    the list.
                </p>
            </div>

            <div class="flex items-center gap-3 border-t border-slate-100 pt-6">
                <PrimaryButton :disabled="form.processing || !isInstalled(form.engine)">
                    Create database
                </PrimaryButton>
                <Link
                    :href="route('databases.index')"
                    class="text-sm text-slate-500 hover:text-slate-900"
                >
                    Cancel
                </Link>
            </div>
        </form>
    </AuthenticatedLayout>
</template>
