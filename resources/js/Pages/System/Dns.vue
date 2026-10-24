<script setup>
import { computed } from 'vue';
import { useLiveRefresh } from '@/Composables/useLiveRefresh';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import DnsAccounts from '@/Components/DnsAccounts.vue';
import DnsTabs from '@/Components/DnsTabs.vue';
import DnsRecords from '@/Components/DnsRecords.vue';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    accounts: { type: Array, default: () => [] },
    providers: { type: Array, default: () => [] },
    recordTypes: { type: Array, default: () => [] },
    nameserver: {
        type: Object,
        default: () => ({ installed: false, ready: false, zones: 0 }),
    },
});

// Nothing on this page is queued, but Inertia still serves it from cache on a
// back navigation — so a credential added or removed from another tab, or the
// page you came back to after deleting one, would be the old list.
useLiveRefresh(computed(() => false), ['accounts']);
</script>

<template>
    <Head title="DNS" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">DNS</h2>
                    <p class="text-sm text-slate-500">
                        {{ accounts.length }}
                        {{ accounts.length === 1 ? 'provider' : 'providers' }}
                        connected — records are created with each site and
                        removed when the site is deleted.
                    </p>
                </div>
                <Link
                    :href="route('dns.create')"
                    class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700"
                >
                    Connect a provider
                </Link>
            </div>
        </template>

        <DnsTabs
            current="providers"
            :counts="{ providers: accounts.length, zones: nameserver.zones }"
        />

        <DnsAccounts :accounts="accounts" :nameserver="nameserver" />

        <!--
            One record table per connected credential, so a provider's zones
            are managed here rather than in its own dashboard.
        -->
        <div v-if="accounts.length" class="mt-6 space-y-6">
            <!-- The panel's own zones are edited on their own page, where the
                 setup they depend on is explained; a second copy of the table
                 here would be the same records in two places. -->
            <DnsRecords
                v-for="account in accounts.filter((a) => !a.local)"
                :key="account.id"
                :account="account"
                :record-types="recordTypes"
            />
        </div>
    </AuthenticatedLayout>
</template>
