<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Checkbox from '@/Components/Checkbox.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, Link, useForm } from '@inertiajs/vue3';

// The same form as the one on a domain's own page, with the domain still to be
// chosen. Somebody adding a mailbox has an address in mind — sales@something —
// rather than a domain to open first, and this is the way in from Email.
const props = defineProps({
    domains: { type: Array, default: () => [] },
});

const form = useForm({
    domain_id: props.domains[0]?.id ?? null,
    local_part: '',
    password: '',
    quota_mb: 2048,
});

const domain = computed(
    () => props.domains.find((entry) => entry.id === Number(form.domain_id)) ?? null,
);

// 0 is what Dovecot reads as "no limit", so unlimited is the same field with
// the number taken away rather than a second one to keep in step with it.
const unlimited = computed({
    get: () => Number(form.quota_mb) === 0,
    set: (on) => (form.quota_mb = on ? 0 : 2048),
});

// Posted to the chosen domain's own endpoint — the one the per-domain form
// uses. One place creates a mailbox, whichever page asked for it.
const submit = () => {
    if (!form.domain_id) return;

    form.post(route('email.accounts.store', form.domain_id));
};
</script>

<template>
    <Head title="New mailbox" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">New mailbox</h2>
                <p class="text-sm text-slate-500">
                    On any domain this server takes mail for — reachable over
                    IMAP and SMTP as soon as it is created
                </p>
            </div>
        </template>

        <div
            v-if="!domains.length"
            class="max-w-2xl rounded-2xl bg-white p-6 text-sm text-slate-500 shadow-sm ring-1 ring-slate-900/5"
        >
            There is no mail domain to put an address on yet.
            <Link
                :href="route('email.domains.create')"
                class="font-medium text-brand-600 hover:underline"
                >Add a domain</Link
            >
            first — the records and the certificate belong to it, not to the
            mailbox.
        </div>

        <form
            v-else
            @submit.prevent="submit"
            class="max-w-2xl space-y-6 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div>
                <InputLabel for="domain_id" value="Domain" />
                <select
                    id="domain_id"
                    v-model="form.domain_id"
                    class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                >
                    <option
                        v-for="entry in domains"
                        :key="entry.id"
                        :value="entry.id"
                    >
                        {{ entry.domain }}
                        <template v-if="entry.status !== 'active'">
                            — {{ entry.status }}
                        </template>
                    </option>
                </select>
                <InputError class="mt-2" :message="form.errors.domain_id" />
                <!--
                    A domain still being set up can hold a mailbox: the work is
                    queued either way, and refusing here would mean coming back
                    later to do the thing you already asked for.
                -->
                <p
                    v-if="domain && domain.status !== 'active'"
                    class="mt-1 text-xs text-amber-700"
                >
                    {{ domain.domain }} is {{ domain.status }}. The mailbox will
                    be created, and starts working when the domain does.
                </p>
            </div>

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
                        class="max-w-[45%] truncate rounded-r-lg border border-l-0 border-slate-300 bg-slate-50 px-3 py-2 text-sm text-slate-500"
                        >@{{ domain?.domain }}</span
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
                v-if="domain?.client_settings"
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
                <PrimaryButton :disabled="form.processing || !form.domain_id">
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
