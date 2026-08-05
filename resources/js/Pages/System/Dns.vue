<script setup>
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import DnsAccounts from '@/Components/DnsAccounts.vue';
import { Head } from '@inertiajs/vue3';

const props = defineProps({
    accounts: { type: Array, default: () => [] },
    providers: { type: Array, default: () => [] },
});
</script>

<template>
    <Head title="DNS" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">DNS</h2>
                <p class="text-sm text-slate-500">
                    {{ accounts.length }}
                    {{ accounts.length === 1 ? 'provider' : 'providers' }}
                    connected — records are created with each site and removed
                    when the site is deleted.
                </p>
            </div>
        </template>

        <DnsAccounts :accounts="accounts" :providers="providers" />
    </AuthenticatedLayout>
</template>
