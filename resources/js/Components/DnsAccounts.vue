<script setup>
import { ref } from 'vue';
import DangerButton from '@/Components/DangerButton.vue';
import SecondaryButton from '@/Components/SecondaryButton.vue';
import { Link, router } from '@inertiajs/vue3';

/**
 * The credentials the panel writes DNS records with — one row each.
 *
 * Connecting a new one is its own page: whatever the provider, the panel needs
 * the same thing from you, but it needs a token pasted in and checked, which is
 * not something to do in a corner of a list.
 */
defineProps({
    accounts: { type: Array, default: () => [] },
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
        <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
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
                        <p
                            class="mt-0.5 text-xs"
                            :class="account.verified_at ? 'text-slate-500' : 'text-amber-600'"
                        >
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
                    class="px-5 py-16 text-center text-sm text-slate-500"
                >
                    Nothing connected.
                    <Link
                        :href="route('dns.create')"
                        class="text-brand-600 hover:underline"
                        >Connect a provider</Link
                    >, or leave DNS to whoever hosts it and point the records at
                    this server by hand.
                </li>
            </ul>
        </div>

        <div class="rounded-2xl bg-slate-50 p-5 text-sm text-slate-600 ring-1 ring-slate-900/5">
            <p class="font-medium text-slate-700">Somewhere else?</p>
            <p class="mt-1">
                The panel writes records to the providers it knows. Anywhere
                else — Route 53, Namecheap, GoDaddy, your registrar's own panel
                — works just as well; leave "manage DNS" off when you create a
                site and point an A record at this server yourself. Mail domains
                show you the exact MX, SPF, DKIM and DMARC records to copy.
            </p>
        </div>
    </div>
</template>
