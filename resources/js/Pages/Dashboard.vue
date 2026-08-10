<script setup>
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import { Head } from '@inertiajs/vue3';

defineProps({
    host: String,
    sitesRoot: String,
    database: String,
});
</script>

<template>
    <Head title="Dashboard" />

    <AuthenticatedLayout>
        <template #header>
            <h1 class="text-xl font-semibold text-gray-900">Dashboard</h1>
        </template>

        <div class="space-y-6">
            <div class="rounded-lg border border-gray-200 bg-white p-6 shadow-sm">
                <h2 class="text-base font-semibold text-gray-900">
                    Signed in to {{ host }}
                </h2>

                <p class="mt-2 text-sm text-gray-600">
                    The Go rewrite is serving this page. Sessions, CSRF, the Inertia
                    protocol and the account pages are ported; the rest of the panel is
                    still on the C build.
                </p>

                <dl class="mt-6 grid gap-4 sm:grid-cols-2">
                    <div>
                        <dt class="text-xs font-medium uppercase tracking-wide text-gray-500">Sites root</dt>
                        <dd class="mt-1 font-mono text-sm text-gray-900">{{ sitesRoot }}</dd>
                    </div>
                    <div>
                        <dt class="text-xs font-medium uppercase tracking-wide text-gray-500">Database</dt>
                        <dd class="mt-1 font-mono text-sm text-gray-900">{{ database }}</dd>
                    </div>
                </dl>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
