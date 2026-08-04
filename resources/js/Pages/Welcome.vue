<script setup>
import { Head, Link } from '@inertiajs/vue3';

defineProps({
    canLogin: Boolean,
});

const features = [
    {
        title: 'Connect servers over SSH',
        body: 'Add any Ubuntu box with a private key or password. Credentials are encrypted at rest and never leave your panel.',
    },
    {
        title: 'Provision the stack',
        body: 'One click installs nginx, PHP-FPM, composer, certbot and a firewall, then keeps the server status in view.',
    },
    {
        title: 'Create and delete sites',
        body: 'Sites get a directory, an nginx vhost and an optional Let’s Encrypt certificate. Deleting cleans all of it up.',
    },
    {
        title: 'Cloudflare DNS on autopilot',
        body: 'Connect a Cloudflare API token and DNS records are created with each site and removed when the site is deleted.',
    },
];
</script>

<template>
    <Head title="Ubuntu Panel" />

    <div class="min-h-screen bg-slate-950 text-slate-100">
        <header class="mx-auto flex max-w-6xl items-center justify-between px-6 py-6">
            <div class="flex items-center gap-3">
                <span
                    class="flex h-9 w-9 items-center justify-center rounded-lg bg-orange-500 text-sm font-bold text-white"
                    >UP</span
                >
                <span class="font-semibold">Ubuntu Panel</span>
            </div>
            <nav v-if="canLogin" class="flex items-center gap-3 text-sm">
                <Link
                    v-if="$page.props.auth.user"
                    :href="route('dashboard')"
                    class="rounded-md bg-orange-500 px-4 py-2 font-medium text-white hover:bg-orange-600"
                >
                    Dashboard
                </Link>
                <Link
                    v-else
                    :href="route('login')"
                    class="rounded-md bg-orange-500 px-4 py-2 font-medium text-white hover:bg-orange-600"
                    >Log in</Link
                >
            </nav>
        </header>

        <main class="mx-auto max-w-6xl px-6 pb-24">
            <section class="py-20">
                <h1 class="max-w-3xl text-4xl font-bold leading-tight sm:text-5xl">
                    Your servers, your sites, your DNS —
                    <span class="text-orange-500">one panel</span>.
                </h1>
                <p class="mt-6 max-w-2xl text-lg text-slate-400">
                    Ubuntu Panel connects to your servers over SSH, provisions a
                    LEMP stack, and creates or removes sites together with their
                    Cloudflare DNS records.
                </p>
                <div v-if="canLogin" class="mt-10 flex gap-3">
                    <Link
                        :href="route('login')"
                        class="rounded-md bg-orange-500 px-6 py-3 font-medium text-white hover:bg-orange-600"
                        >Log in to your panel</Link
                    >
                </div>
            </section>

            <section class="grid gap-6 sm:grid-cols-2">
                <div
                    v-for="feature in features"
                    :key="feature.title"
                    class="rounded-xl border border-slate-800 bg-slate-900 p-6"
                >
                    <h2 class="font-semibold text-white">{{ feature.title }}</h2>
                    <p class="mt-2 text-sm text-slate-400">{{ feature.body }}</p>
                </div>
            </section>
        </main>
    </div>
</template>
