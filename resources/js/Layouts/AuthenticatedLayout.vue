<script setup>
import { ref } from 'vue';
import Dropdown from '@/Components/Dropdown.vue';
import DropdownLink from '@/Components/DropdownLink.vue';
import NavLink from '@/Components/NavLink.vue';
import ResponsiveNavLink from '@/Components/ResponsiveNavLink.vue';
import FlashMessages from '@/Components/FlashMessages.vue';
import { Link } from '@inertiajs/vue3';

const showingNavigationDropdown = ref(false);

const links = [
    { name: 'Dashboard', route: 'dashboard', pattern: 'dashboard' },
    { name: 'Sites', route: 'sites.index', pattern: 'sites.*' },
    { name: 'Databases', route: 'databases.index', pattern: 'databases.*' },
    { name: 'Email', route: 'email.index', pattern: 'email.*' },
    { name: 'Terminal', route: 'terminal', pattern: 'terminal' },
];

// Services and DNS credentials are sections of Settings rather than top-level
// destinations — they are things you set up once, not places you work.
</script>

<template>
    <div class="min-h-screen bg-slate-100">
        <nav class="border-b border-slate-800 bg-slate-900">
            <div class="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
                <div class="flex h-16 justify-between">
                    <div class="flex">
                        <div class="flex shrink-0 items-center">
                            <Link
                                :href="route('dashboard')"
                                class="flex items-center gap-2 text-white"
                            >
                                <span
                                    class="flex h-8 w-8 items-center justify-center rounded-lg bg-orange-500 text-sm font-bold"
                                >
                                    UP
                                </span>
                                <span class="text-sm font-semibold tracking-wide">
                                    Ubuntu Panel
                                </span>
                            </Link>
                        </div>

                        <div class="hidden space-x-6 sm:-my-px sm:ms-10 sm:flex">
                            <NavLink
                                v-for="link in links"
                                :key="link.route"
                                :href="route(link.route)"
                                :active="route().current(link.pattern)"
                                class="!text-slate-300 hover:!border-slate-500 hover:!text-white"
                                :class="
                                    route().current(link.pattern)
                                        ? '!border-orange-500 !text-white'
                                        : ''
                                "
                            >
                                {{ link.name }}
                            </NavLink>
                        </div>
                    </div>

                    <div class="hidden sm:ms-6 sm:flex sm:items-center">
                        <div class="relative ms-3">
                            <Dropdown align="right" width="48">
                                <template #trigger>
                                    <span class="inline-flex rounded-md">
                                        <button
                                            type="button"
                                            class="inline-flex items-center rounded-md border border-transparent px-3 py-2 text-sm font-medium leading-4 text-slate-300 transition hover:text-white focus:outline-none"
                                        >
                                            {{ $page.props.auth.user.name }}
                                            <svg
                                                class="-me-0.5 ms-2 h-4 w-4"
                                                xmlns="http://www.w3.org/2000/svg"
                                                viewBox="0 0 20 20"
                                                fill="currentColor"
                                            >
                                                <path
                                                    fill-rule="evenodd"
                                                    d="M5.293 7.293a1 1 0 011.414 0L10 10.586l3.293-3.293a1 1 0 111.414 1.414l-4 4a1 1 0 01-1.414 0l-4-4a1 1 0 010-1.414z"
                                                    clip-rule="evenodd"
                                                />
                                            </svg>
                                        </button>
                                    </span>
                                </template>

                                <template #content>
                                    <DropdownLink :href="route('profile.edit')">
                                        Profile
                                    </DropdownLink>
                                    <DropdownLink
                                        :href="route('logout')"
                                        method="post"
                                        as="button"
                                    >
                                        Log Out
                                    </DropdownLink>
                                </template>
                            </Dropdown>
                        </div>

                        <Link
                            :href="route('settings')"
                            class="ms-2 rounded-md p-2 transition"
                            :class="
                                route().current('settings')
                                    ? 'bg-slate-800 text-white'
                                    : 'text-slate-400 hover:bg-slate-800 hover:text-white'
                            "
                            title="Settings"
                            aria-label="Settings"
                        >
                            <svg
                                class="h-5 w-5"
                                xmlns="http://www.w3.org/2000/svg"
                                fill="none"
                                viewBox="0 0 24 24"
                                stroke-width="1.6"
                                stroke="currentColor"
                                aria-hidden="true"
                            >
                                <path
                                    stroke-linecap="round"
                                    stroke-linejoin="round"
                                    d="M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.324.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 011.37.49l1.296 2.247a1.125 1.125 0 01-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a6.759 6.759 0 010 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.431l-1.298 2.247a1.125 1.125 0 01-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.57 6.57 0 01-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.02-.397-1.11-.94l-.213-1.28c-.062-.375-.312-.687-.644-.87a6.52 6.52 0 01-.22-.128c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 01-1.369-.49l-1.297-2.247a1.125 1.125 0 01.26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 010-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 01-.26-1.43l1.297-2.248a1.125 1.125 0 011.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28z"
                                />
                                <path
                                    stroke-linecap="round"
                                    stroke-linejoin="round"
                                    d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"
                                />
                            </svg>
                        </Link>
                    </div>

                    <div class="-me-2 flex items-center sm:hidden">
                        <button
                            @click="
                                showingNavigationDropdown =
                                    !showingNavigationDropdown
                            "
                            class="inline-flex items-center justify-center rounded-md p-2 text-slate-400 transition hover:bg-slate-800 hover:text-white focus:outline-none"
                        >
                            <svg
                                class="h-6 w-6"
                                stroke="currentColor"
                                fill="none"
                                viewBox="0 0 24 24"
                            >
                                <path
                                    :class="{
                                        hidden: showingNavigationDropdown,
                                        'inline-flex':
                                            !showingNavigationDropdown,
                                    }"
                                    stroke-linecap="round"
                                    stroke-linejoin="round"
                                    stroke-width="2"
                                    d="M4 6h16M4 12h16M4 18h16"
                                />
                                <path
                                    :class="{
                                        hidden: !showingNavigationDropdown,
                                        'inline-flex':
                                            showingNavigationDropdown,
                                    }"
                                    stroke-linecap="round"
                                    stroke-linejoin="round"
                                    stroke-width="2"
                                    d="M6 18L18 6M6 6l12 12"
                                />
                            </svg>
                        </button>
                    </div>
                </div>
            </div>

            <div
                :class="{
                    block: showingNavigationDropdown,
                    hidden: !showingNavigationDropdown,
                }"
                class="sm:hidden"
            >
                <div class="space-y-1 pb-3 pt-2">
                    <ResponsiveNavLink
                        v-for="link in links"
                        :key="link.route"
                        :href="route(link.route)"
                        :active="route().current(link.pattern)"
                        class="!text-slate-200"
                    >
                        {{ link.name }}
                    </ResponsiveNavLink>
                </div>

                <div class="border-t border-slate-700 pb-1 pt-4">
                    <div class="px-4">
                        <div class="text-base font-medium text-white">
                            {{ $page.props.auth.user.name }}
                        </div>
                        <div class="text-sm font-medium text-slate-400">
                            {{ $page.props.auth.user.email }}
                        </div>
                    </div>

                    <div class="mt-3 space-y-1">
                        <ResponsiveNavLink
                            :href="route('settings')"
                            :active="route().current('settings')"
                            class="!text-slate-200"
                        >
                            Settings
                        </ResponsiveNavLink>
                        <ResponsiveNavLink
                            :href="route('profile.edit')"
                            class="!text-slate-200"
                        >
                            Profile
                        </ResponsiveNavLink>
                        <ResponsiveNavLink
                            :href="route('logout')"
                            method="post"
                            as="button"
                            class="!text-slate-200"
                        >
                            Log Out
                        </ResponsiveNavLink>
                    </div>
                </div>
            </div>
        </nav>

        <header class="border-b border-slate-200 bg-white" v-if="$slots.header">
            <div class="mx-auto max-w-7xl px-4 py-6 sm:px-6 lg:px-8">
                <slot name="header" />
            </div>
        </header>

        <main class="mx-auto max-w-7xl px-4 py-8 sm:px-6 lg:px-8">
            <FlashMessages />
            <slot />
        </main>
    </div>
</template>
