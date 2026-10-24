<script setup>
import { ref } from 'vue';
import DangerButton from '@/Components/DangerButton.vue';
import SecondaryButton from '@/Components/SecondaryButton.vue';
import { Link, router } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

/**
 * The credentials the panel writes DNS records with — one row each.
 *
 * Connecting a new one is its own page: whatever the provider, the panel needs
 * the same thing from you, but it needs a token pasted in and checked, which is
 * not something to do in a corner of a list.
 */
defineProps({
    accounts: { type: Array, default: () => [] },
    nameserver: {
        type: Object,
        default: () => ({ installed: false, ready: false, zones: 0 }),
    },
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

const remove = async (account) => {
    const detail = account.sites_count
        ? `It is used by ${account.sites_count} site(s). Their DNS records stay where ` +
          'they are, but the panel stops managing them.'
        : 'The panel will no longer be able to publish records with this credential.';

    if (!(await confirmDanger(`Remove ${account.label}?`, detail, 'Remove'))) return;

    router.delete(route('dns.destroy', account.id), { preserveScroll: true });
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
                        <p v-if="account.local" class="mt-0.5 text-xs text-slate-500">
                            This machine answers for
                            {{ nameserver.zones }}
                            {{ nameserver.zones === 1 ? 'zone' : 'zones' }} itself
                            <span v-if="account.sites_count">
                                · {{ account.sites_count }} site(s)
                            </span>
                        </p>
                        <p
                            v-else
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

                    <!--
                        This machine's own nameserver has no credential to check
                        and nothing to revoke: what makes it work is a registrar
                        pointed at this server, which is its own page.
                    -->
                    <div v-if="account.local" class="flex shrink-0 gap-2">
                        <Link
                            :href="route('dns.nameserver')"
                            class="rounded-xl px-4 py-2 text-sm text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                        >
                            Zones and setup
                        </Link>
                    </div>

                    <div v-else class="flex shrink-0 gap-2">
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

        <div class="grid gap-4 lg:grid-cols-2">
            <!--
                The other way round: rather than writing records at somebody
                else's nameserver, be the nameserver. Offered beside the
                credentials because somebody with no account at any of the
                providers above has not run out of options.
            -->
            <div
                class="rounded-2xl bg-white p-5 text-sm text-slate-600 shadow-sm ring-1 ring-slate-900/5"
            >
                <p class="font-medium text-slate-700">Or host the DNS here</p>
                <p class="mt-1">
                    This machine can be the nameserver itself, the way Namecheap
                    and GoDaddy are: point a domain's nameservers at this server
                    and manage its records in the panel, with no account
                    anywhere. It needs two nameserver names registered as glue
                    records at a registrar, which is explained step by step on
                    its own page.
                </p>
                <p class="mt-3">
                    <Link
                        :href="route('dns.nameserver')"
                        class="font-medium text-brand-600 hover:underline"
                    >
                        <template v-if="nameserver.zones">
                            {{ nameserver.zones }} zone(s) hosted here →
                        </template>
                        <template v-else-if="nameserver.ready">
                            Add a zone →
                        </template>
                        <template v-else> Set this server up as a nameserver → </template>
                    </Link>
                </p>
            </div>

            <div class="rounded-2xl bg-slate-50 p-5 text-sm text-slate-600 ring-1 ring-slate-900/5">
                <p class="font-medium text-slate-700">Somewhere else again?</p>
                <p class="mt-1">
                    Anywhere the panel does not write to — Route 53, your
                    registrar's own panel — works just as well; leave "manage
                    DNS" off when you create a site and point an A record at this
                    server yourself. Mail domains show you the exact MX, SPF,
                    DKIM and DMARC records to copy.
                </p>
            </div>
        </div>
    </div>
</template>
