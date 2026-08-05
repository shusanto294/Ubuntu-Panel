<script setup>
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import DnsAccounts from '@/Components/DnsAccounts.vue';
import DnsRecords from '@/Components/DnsRecords.vue';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    accounts: { type: Array, default: () => [] },
    providers: { type: Array, default: () => [] },
    recordTypes: { type: Array, default: () => [] },
});
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

        <DnsAccounts :accounts="accounts" />

        <!--
            One record table per connected credential, so a provider's zones
            are managed here rather than in its own dashboard.
        -->
        <div v-if="accounts.length" class="mt-6 space-y-6">
            <DnsRecords
                v-for="account in accounts"
                :key="account.id"
                :account="account"
                :record-types="recordTypes"
            />
        </div>
    </AuthenticatedLayout>
</template>
