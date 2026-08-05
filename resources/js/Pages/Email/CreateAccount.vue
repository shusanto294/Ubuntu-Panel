<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Checkbox from '@/Components/Checkbox.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, Link, useForm } from '@inertiajs/vue3';

const props = defineProps({
    domain: Object,
});

const form = useForm({
    local_part: '',
    password: '',
    quota_mb: 2048,
});

// 0 is what Dovecot reads as "no limit", so unlimited is the same field with
// the number taken away rather than a second one to keep in step with it.
const unlimited = computed({
    get: () => Number(form.quota_mb) === 0,
    set: (on) => (form.quota_mb = on ? 0 : 2048),
});

const submit = () => form.post(route('email.accounts.store', props.domain.id));
</script>

<template>
    <Head :title="`New mailbox on ${domain.domain}`" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">New mailbox</h2>
                <p class="text-sm text-slate-500">
                    on {{ domain.domain }} — reachable over IMAP and SMTP as
                    soon as it is created
                </p>
            </div>
        </template>

        <form
            @submit.prevent="submit"
            class="max-w-2xl space-y-6 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div>
                <InputLabel for="local_part" value="Address" />
                <div class="mt-1 flex items-center">
                    <TextInput
                        id="local_part"
                        v-model="form.local_part"
                        class="block w-full rounded-r-none"
                        placeholder="info"
                    />
                    <span
                        class="rounded-r-lg border border-l-0 border-slate-300 bg-slate-50 px-3 py-2 text-sm text-slate-500"
                        >@{{ domain.domain }}</span
                    >
                </div>
                <InputError class="mt-2" :message="form.errors.local_part" />
            </div>

            <div>
                <InputLabel for="password" value="Password" />
                <TextInput
                    id="password"
                    type="password"
                    v-model="form.password"
                    class="mt-1 block w-full"
                    autocomplete="new-password"
                />
                <InputError class="mt-2" :message="form.errors.password" />
                <p class="mt-1 text-xs text-slate-500">
                    At least 10 characters. This is what the mail client signs
                    in with, alongside the full address as the username.
                </p>
            </div>

            <div>
                <InputLabel for="quota_mb" value="Mailbox size" />

                <label class="mt-2 flex items-center gap-2">
                    <Checkbox v-model:checked="unlimited" />
                    <span class="text-sm text-slate-700">
                        Unlimited — let this mailbox use whatever disk there is
                    </span>
                </label>

                <TextInput
                    v-if="!unlimited"
                    id="quota_mb"
                    type="number"
                    min="1"
                    v-model="form.quota_mb"
                    class="mt-3 block w-full sm:max-w-xs"
                />
                <InputError class="mt-2" :message="form.errors.quota_mb" />
                <p v-if="!unlimited" class="mt-1 text-xs text-slate-500">
                    In megabytes. Delivery to a full mailbox is rejected, so the
                    sender finds out rather than the mail disappearing.
                </p>
            </div>

            <div
                class="rounded-xl bg-slate-50 p-4 text-xs text-slate-600 ring-1 ring-slate-900/5"
            >
                <p class="font-semibold text-slate-700">Mail client settings</p>
                <p class="mt-1">
                    IMAP {{ domain.client_settings.imap.host }}:{{
                        domain.client_settings.imap.port
                    }}
                    ({{ domain.client_settings.imap.security }}) · SMTP
                    {{ domain.client_settings.smtp.host }}:{{
                        domain.client_settings.smtp.port
                    }}
                    ({{ domain.client_settings.smtp.security }})
                </p>
            </div>

            <div class="flex items-center gap-3 border-t border-slate-100 pt-6">
                <PrimaryButton :disabled="form.processing">
                    Create mailbox
                </PrimaryButton>
                <Link
                    :href="route('email.index')"
                    class="text-sm text-slate-500 hover:text-slate-900"
                >
                    Cancel
                </Link>
            </div>
        </form>
    </AuthenticatedLayout>
</template>
