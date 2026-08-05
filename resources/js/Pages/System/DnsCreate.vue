<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, Link, useForm } from '@inertiajs/vue3';

const props = defineProps({
    providers: { type: Array, default: () => [] },
});

const form = useForm({
    provider: props.providers[0]?.key ?? 'cloudflare',
    label: '',
    api_token: '',
    api_secret: '',
});

// Whatever the provider, the panel needs the same thing from you — a token it
// can use — so only the labels and the "where do I get one" note change.
const selected = computed(
    () => props.providers.find((p) => p.key === form.provider) ?? props.providers[0],
);

const submit = () => form.post(route('dns.store'));
</script>

<template>
    <Head title="Connect a DNS provider" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">
                    Connect a provider
                </h2>
                <p class="text-sm text-slate-500">
                    The credential is tried before it is saved, and stored
                    encrypted. It is never sent back to the browser.
                </p>
            </div>
        </template>

        <form
            @submit.prevent="submit"
            class="max-w-2xl space-y-6 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div>
                <InputLabel for="provider" value="Provider" />
                <select
                    id="provider"
                    v-model="form.provider"
                    class="mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500"
                >
                    <option
                        v-for="provider in providers"
                        :key="provider.key"
                        :value="provider.key"
                    >
                        {{ provider.label }}
                    </option>
                </select>
                <InputError class="mt-2" :message="form.errors.provider" />

                <p v-if="selected" class="mt-2 text-xs text-slate-500">
                    {{ selected.help }}
                    <a
                        :href="selected.url"
                        target="_blank"
                        rel="noopener"
                        class="text-brand-600 hover:underline"
                        >Get one</a
                    >.
                </p>
            </div>

            <div>
                <InputLabel for="label" value="Name" />
                <TextInput
                    id="label"
                    v-model="form.label"
                    type="text"
                    class="mt-1 block w-full"
                    placeholder="Personal"
                />
                <InputError class="mt-2" :message="form.errors.label" />
                <p class="mt-1 text-xs text-slate-500">
                    Only so you can tell them apart when you have more than one.
                </p>
            </div>

            <div>
                <InputLabel
                    for="api_token"
                    :value="selected?.token_label ?? 'API token'"
                />
                <TextInput
                    id="api_token"
                    v-model="form.api_token"
                    type="password"
                    autocomplete="off"
                    class="mt-1 block w-full font-mono"
                />
                <InputError class="mt-2" :message="form.errors.api_token" />
            </div>

            <div v-if="selected?.secret_label">
                <InputLabel for="api_secret" :value="selected.secret_label" />
                <TextInput
                    id="api_secret"
                    v-model="form.api_secret"
                    type="password"
                    autocomplete="off"
                    class="mt-1 block w-full font-mono"
                />
                <InputError class="mt-2" :message="form.errors.api_secret" />
            </div>

            <div class="flex items-center gap-3 border-t border-slate-100 pt-6">
                <PrimaryButton :disabled="form.processing">
                    {{ form.processing ? 'Checking…' : 'Connect' }}
                </PrimaryButton>
                <Link
                    :href="route('dns.index')"
                    class="text-sm text-slate-500 hover:text-slate-900"
                >
                    Cancel
                </Link>
            </div>
        </form>
    </AuthenticatedLayout>
</template>
