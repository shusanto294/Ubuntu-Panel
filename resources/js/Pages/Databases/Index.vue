<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, Link, router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    databases: Array,
    availableEngines: Array,
    engines: Object,
});

const form = useForm({
    engine: props.availableEngines?.[0] ?? 'mysql',
    name: '',
    username: '',
    password: '',
});

const revealed = ref({});

const engineOptions = computed(() => props.availableEngines ?? []);

const submit = () =>
    form.post(route('databases.store'), {
        preserveScroll: true,
        onSuccess: () => form.reset('name', 'username', 'password'),
    });

const destroy = (database) => {
    if (
        confirm(
            `Drop ${database.name}? All data in it is lost.`,
        )
    ) {
        router.delete(route('databases.destroy', database.id), {
            preserveScroll: true,
        });
    }
};

const reveal = async (database) => {
    const { data } = await window.axios.get(
        route('databases.credentials', database.id),
    );
    revealed.value = { ...revealed.value, [database.id]: data };
};
</script>

<template>
    <Head title="Databases" />

    <AuthenticatedLayout>
        <template #header>
            <h2 class="text-xl font-semibold text-slate-800">Databases</h2>
        </template>

        <div class="grid gap-6 lg:grid-cols-3">
            <!-- Create form -->
            <div class="rounded-xl border border-slate-200 bg-white p-6">
                <h3 class="font-semibold text-slate-800">Create a database</h3>

                <form @submit.prevent="submit" class="mt-4 space-y-4">
                    <div>
                        <InputLabel for="engine" value="Engine" />
                        <select
                            id="engine"
                            v-model="form.engine"
                            class="mt-1 block w-full rounded-md border-slate-300 shadow-sm focus:border-orange-500 focus:ring-orange-500"
                        >
                            <option
                                v-for="engine in engineOptions"
                                :key="engine"
                                :value="engine"
                            >
                                {{ engines[engine] }}
                            </option>
                        </select>
                        <p
                            v-if="!engineOptions.length"
                            class="mt-1 text-xs text-amber-700"
                        >
                            No database engine is installed yet — add MariaDB,
                            PostgreSQL or MongoDB from
                            <Link
                                :href="route('settings', { tab: 'services' })"
                                class="underline"
                                >Settings → Services</Link
                            >.
                        </p>
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
                    </div>

                    <div>
                        <InputLabel
                            for="username"
                            value="Username (optional)"
                        />
                        <TextInput
                            id="username"
                            v-model="form.username"
                            class="mt-1 block w-full"
                            placeholder="generated from the name"
                        />
                        <InputError
                            class="mt-2"
                            :message="form.errors.username"
                        />
                    </div>

                    <div>
                        <InputLabel
                            for="password"
                            value="Password (optional)"
                        />
                        <TextInput
                            id="password"
                            type="password"
                            v-model="form.password"
                            class="mt-1 block w-full"
                            placeholder="generated if left blank"
                            autocomplete="new-password"
                        />
                        <InputError
                            class="mt-2"
                            :message="form.errors.password"
                        />
                    </div>

                    <button
                        :disabled="form.processing || !engineOptions.length"
                        class="w-full rounded-md bg-orange-500 px-4 py-2 text-sm font-medium text-white hover:bg-orange-600 disabled:opacity-50"
                    >
                        Create database
                    </button>
                </form>
            </div>

            <!-- List -->
            <div class="lg:col-span-2">
                <div
                    class="overflow-hidden rounded-xl border border-slate-200 bg-white"
                >
                    <table class="min-w-full divide-y divide-slate-200">
                        <thead class="bg-slate-50">
                            <tr
                                class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                            >
                                <th class="px-5 py-3">Name</th>
                                <th class="px-5 py-3">Engine</th>
                                <th class="px-5 py-3">Status</th>
                                <th class="px-5 py-3"></th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100 text-sm">
                            <template
                                v-for="database in databases"
                                :key="database.id"
                            >
                                <tr>
                                    <td class="px-5 py-3">
                                        <p class="font-medium text-slate-800">
                                            {{ database.name }}
                                        </p>
                                        <p class="text-xs text-slate-500">
                                            user: {{ database.username }}
                                            <span
                                                v-if="database.managed_by_site"
                                                class="ml-1 text-orange-600"
                                                >· created for a site</span
                                            >
                                        </p>
                                    </td>
                                    <td class="px-5 py-3 text-slate-600">
                                        {{ database.engine_label }}
                                    </td>
                                    <td class="px-5 py-3">
                                        <StatusBadge :status="database.status" />
                                    </td>
                                    <td
                                        class="px-5 py-3 text-right whitespace-nowrap"
                                    >
                                        <button
                                            @click="reveal(database)"
                                            class="text-orange-600 hover:underline"
                                        >
                                            Credentials
                                        </button>
                                        <button
                                            @click="destroy(database)"
                                            class="ml-3 text-rose-600 hover:underline"
                                        >
                                            Drop
                                        </button>
                                    </td>
                                </tr>
                                <tr v-if="revealed[database.id]">
                                    <td colspan="5" class="bg-slate-900 px-5 py-3">
                                        <pre
                                            class="overflow-x-auto font-mono text-xs text-slate-200"
                                            >host: {{ revealed[database.id].host }}
port: {{ revealed[database.id].port }}
database: {{ revealed[database.id].database }}
username: {{ revealed[database.id].username }}
password: {{ revealed[database.id].password }}</pre
                                        >
                                    </td>
                                </tr>
                                <tr v-if="database.last_error">
                                    <td
                                        colspan="5"
                                        class="bg-rose-50 px-5 py-2 text-xs text-rose-700"
                                    >
                                        {{ database.last_error }}
                                    </td>
                                </tr>
                            </template>
                            <tr v-if="!databases.length">
                                <td
                                    colspan="5"
                                    class="px-5 py-12 text-center text-sm text-slate-500"
                                >
                                    No databases yet. WordPress and Laravel
                                    sites create theirs automatically.
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
