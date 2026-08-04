<script setup>
import { computed, ref } from 'vue';
import DangerButton from '@/Components/DangerButton.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import SecondaryButton from '@/Components/SecondaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { router, useForm } from '@inertiajs/vue3';

/**
 * The credentials the panel writes DNS records with.
 *
 * One row per credential, one form to add another. Whatever the provider, the
 * panel needs the same thing from you — a token it can use — so the form is the
 * same shape and only the labels and the "where do I get one" note change.
 */
const props = defineProps({
    accounts: { type: Array, default: () => [] },
    providers: { type: Array, default: () => [] },
});

const form = useForm({
    provider: props.providers[0]?.key ?? 'cloudflare',
    label: '',
    api_token: '',
    api_secret: '',
});

const selected = computed(
    () => props.providers.find((p) => p.key === form.provider) ?? props.providers[0],
);

const submit = () =>
    form.post(route('dns.store'), {
        preserveScroll: true,
        onSuccess: () => form.reset('label', 'api_token', 'api_secret'),
    });

const verifying = ref(null);

const verify = (account) => {
    verifying.value = account.id;

    router.post(
        route('dns.verify', account.id),
        {},
        { preserveScroll: true, onFinish: () => (verifying.value = null) },
    );
};

const remove = (account) => {
    const warning = account.sites_count
        ? `${account.label} is used by ${account.sites_count} site(s). Their DNS records stay where they are, but the panel will stop managing them. Remove it?`
        : `Remove ${account.label}?`;

    if (window.confirm(warning)) {
        router.delete(route('dns.destroy', account.id), { preserveScroll: true });
    }
};
</script>

<template>
    <div class="space-y-6">
        <div class="rounded-xl border border-slate-200 bg-white">
            <div class="border-b border-slate-200 px-5 py-4">
                <h3 class="font-semibold text-slate-800">DNS credentials</h3>
                <p class="mt-1 text-sm text-slate-500">
                    With one of these connected, creating a site publishes its
                    records and deleting the site takes them away again. Without
                    one, everything still works — you just add the records
                    yourself.
                </p>
            </div>

            <ul class="divide-y divide-slate-100">
                <li
                    v-for="account in accounts"
                    :key="account.id"
                    class="flex flex-wrap items-center justify-between gap-3 px-5 py-4"
                >
                    <div class="min-w-0">
                        <p class="font-medium text-slate-800">
                            {{ account.label }}
                            <span class="ml-2 text-xs font-normal text-slate-500">
                                {{ account.provider_label }}
                            </span>
                        </p>
                        <p class="mt-0.5 text-xs" :class="account.verified_at ? 'text-slate-500' : 'text-amber-600'">
                            <template v-if="account.verified_at">
                                Checked {{ account.verified_at }}
                            </template>
                            <template v-else>
                                Not verified — the credential may have been
                                revoked.
                            </template>
                            <span v-if="account.sites_count">
                                · {{ account.sites_count }} site(s)
                            </span>
                        </p>
                    </div>

                    <div class="flex shrink-0 gap-2">
                        <SecondaryButton
                            type="button"
                            :disabled="verifying === account.id"
                            @click="verify(account)"
                        >
                            {{ verifying === account.id ? 'Checking…' : 'Check' }}
                        </SecondaryButton>
                        <DangerButton type="button" @click="remove(account)">
                            Remove
                        </DangerButton>
                    </div>
                </li>

                <li
                    v-if="!accounts.length"
                    class="px-5 py-10 text-center text-sm text-slate-500"
                >
                    Nothing connected. Add one below, or leave DNS to whoever
                    hosts it and point the records at this server by hand.
                </li>
            </ul>
        </div>

        <div class="rounded-xl border border-slate-200 bg-white p-6">
            <h3 class="font-semibold text-slate-800">Connect a provider</h3>

            <form @submit.prevent="submit" class="mt-4 space-y-4">
                <div>
                    <InputLabel for="provider" value="Provider" />
                    <select
                        id="provider"
                        v-model="form.provider"
                        class="mt-1 block w-full rounded-md border-slate-300 shadow-sm focus:border-orange-500 focus:ring-orange-500"
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
                            class="text-orange-600 hover:underline"
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
                        Only so you can tell them apart when you have more than
                        one.
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

                <PrimaryButton :disabled="form.processing">
                    {{ form.processing ? 'Checking…' : 'Connect' }}
                </PrimaryButton>

                <p class="text-xs text-slate-500">
                    The credential is tried before it is saved, and stored
                    encrypted. It is never sent back to the browser.
                </p>
            </form>
        </div>

        <div class="rounded-xl border border-slate-200 bg-slate-50 p-5 text-sm text-slate-600">
            <p class="font-medium text-slate-700">Somewhere else?</p>
            <p class="mt-1">
                The panel writes records to the providers above. Anywhere else —
                Route 53, Namecheap, GoDaddy, your registrar's own panel — works
                just as well; leave "manage DNS" off when you create a site and
                point an A record at this server yourself. Mail domains show you
                the exact MX, SPF, DKIM and DMARC records to copy.
            </p>
        </div>
    </div>
</template>
