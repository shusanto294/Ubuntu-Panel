<script setup>
import { onMounted, ref, watch } from 'vue';
import Dropdown from '@/Components/Dropdown.vue';
import DropdownLink from '@/Components/DropdownLink.vue';
import SidebarLink from '@/Components/SidebarLink.vue';
import FlashMessages from '@/Components/FlashMessages.vue';
import { Link } from '@inertiajs/vue3';

// A white menu column against a soft grey page: the brand mark at the top,
// destinations grouped under quiet uppercase headings, and the current row
// marked by a tinted rounded pill rather than a hard block. The menu collapses
// to an icon rail on a wide screen and slides in over the content on a narrow
// one.
const collapsed = ref(false);
const showingMobileMenu = ref(false);

const STORAGE_KEY = 'panel:sidebar-collapsed';

// Read after mount rather than at setup: this component is server-rendered on
// the first response, where there is no localStorage to ask.
onMounted(() => {
    collapsed.value = window.localStorage.getItem(STORAGE_KEY) === '1';
});

watch(collapsed, (value) => {
    window.localStorage.setItem(STORAGE_KEY, value ? '1' : '0');
});

const icons = {
    dashboard:
        'M3.75 6A2.25 2.25 0 016 3.75h2.25A2.25 2.25 0 0110.5 6v2.25a2.25 2.25 0 01-2.25 2.25H6a2.25 2.25 0 01-2.25-2.25V6zM3.75 15.75A2.25 2.25 0 016 13.5h2.25a2.25 2.25 0 012.25 2.25V18a2.25 2.25 0 01-2.25 2.25H6A2.25 2.25 0 013.75 18v-2.25zM13.5 6a2.25 2.25 0 012.25-2.25H18A2.25 2.25 0 0120.25 6v2.25A2.25 2.25 0 0118 10.5h-2.25a2.25 2.25 0 01-2.25-2.25V6zM13.5 15.75a2.25 2.25 0 012.25-2.25H18a2.25 2.25 0 012.25 2.25V18A2.25 2.25 0 0118 20.25h-2.25A2.25 2.25 0 0113.5 18v-2.25z',
    sites: 'M12 21a9.004 9.004 0 008.716-6.747M12 21a9.004 9.004 0 01-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 017.843 4.582M12 3a8.997 8.997 0 00-7.843 4.582m15.686 0A11.953 11.953 0 0112 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0121 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0112 16.5c-3.162 0-6.133-.815-8.716-2.247m0 0A9.015 9.015 0 013 12c0-1.605.42-3.113 1.157-4.418',
    databases:
        'M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75',
    email: 'M21.75 6.75v10.5a2.25 2.25 0 01-2.25 2.25h-15a2.25 2.25 0 01-2.25-2.25V6.75m19.5 0A2.25 2.25 0 0019.5 4.5h-15a2.25 2.25 0 00-2.25 2.25m19.5 0v.243a2.25 2.25 0 01-1.07 1.916l-7.5 4.615a2.25 2.25 0 01-2.36 0L3.32 8.91a2.25 2.25 0 01-1.07-1.916V6.75',
    terminal:
        'M6.75 7.5l3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0021 18V6a2.25 2.25 0 00-2.25-2.25H5.25A2.25 2.25 0 003 6v12a2.25 2.25 0 002.25 2.25z',
    services:
        'M6 6.878V6a2.25 2.25 0 012.25-2.25h7.5A2.25 2.25 0 0118 6v.878m-12 0c.235-.083.487-.128.75-.128h10.5c.263 0 .515.045.75.128m-12 0A2.25 2.25 0 004.5 9v.878m13.5-3A2.25 2.25 0 0119.5 9v.878m0 0a2.246 2.246 0 00-.75-.128H5.25c-.263 0-.515.045-.75.128m15 0A2.25 2.25 0 0121 12v6a2.25 2.25 0 01-2.25 2.25H5.25A2.25 2.25 0 013 18v-6c0-.98.626-1.813 1.5-2.122M16.5 15a.75.75 0 11-1.5 0 .75.75 0 011.5 0zm-3 0a.75.75 0 11-1.5 0 .75.75 0 011.5 0z',
    dns: 'M12 21a9.004 9.004 0 008.716-6.747M12 21a9.004 9.004 0 01-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 017.843 4.582M12 3a8.997 8.997 0 00-7.843 4.582m15.686 0A11.953 11.953 0 0112 10.5c-2.998 0-5.74-1.1-7.843-2.918',
    settings:
        'M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.324.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 011.37.49l1.296 2.247a1.125 1.125 0 01-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a6.759 6.759 0 010 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.431l-1.298 2.247a1.125 1.125 0 01-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.57 6.57 0 01-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.02-.397-1.11-.94l-.213-1.28c-.062-.375-.312-.687-.644-.87a6.52 6.52 0 01-.22-.128c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 01-1.369-.49l-1.297-2.247a1.125 1.125 0 01.26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 010-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 01-.26-1.43l1.297-2.248a1.125 1.125 0 011.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
    profile:
        'M17.982 18.725A7.488 7.488 0 0012 15.75a7.488 7.488 0 00-5.982 2.975m11.963 0a9 9 0 10-11.963 0m11.963 0A8.966 8.966 0 0112 21a8.966 8.966 0 01-5.982-2.275M15 9.75a3 3 0 11-6 0 3 3 0 016 0z',
};

// Two groups, the way the reference dashboard splits them: the places you
// work, then the machine and the account behind them.
const sections = [
    {
        heading: 'Main menu',
        links: [
            { name: 'Dashboard', route: 'dashboard', pattern: 'dashboard', icon: icons.dashboard },
            { name: 'Sites', route: 'sites.index', pattern: 'sites.*', icon: icons.sites },
            { name: 'Databases', route: 'databases.index', pattern: 'databases.*', icon: icons.databases },
            { name: 'Email', route: 'email.index', pattern: 'email.*', icon: icons.email },
            { name: 'Terminal', route: 'terminal', pattern: 'terminal', icon: icons.terminal },
        ],
    },
    {
        heading: 'System',
        links: [
            { name: 'Services', route: 'services.index', pattern: 'services.index', icon: icons.services },
            { name: 'DNS', route: 'dns.index', pattern: 'dns.index', icon: icons.dns },
            { name: 'Settings', route: 'settings', pattern: 'settings', icon: icons.settings },
            { name: 'Profile', route: 'profile.edit', pattern: 'profile.edit', icon: icons.profile },
        ],
    },
];
</script>

<template>
    <div class="min-h-screen bg-slate-50">
        <!-- Backdrop for the slide-in menu; only ever reachable below lg. -->
        <div
            v-if="showingMobileMenu"
            class="fixed inset-0 z-30 bg-slate-900/40 lg:hidden"
            @click="showingMobileMenu = false"
        />

        <!-- Menu column -->
        <aside
            class="fixed inset-y-0 left-0 z-40 flex flex-col border-e border-slate-200 bg-white transition-[width,transform] duration-150 lg:translate-x-0"
            :class="[
                collapsed ? 'w-20' : 'w-64',
                showingMobileMenu ? 'translate-x-0' : '-translate-x-full',
            ]"
        >
            <Link
                :href="route('dashboard')"
                class="flex h-20 items-center gap-3 px-6"
                :class="collapsed ? 'justify-center px-0' : ''"
            >
                <span
                    class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-brand-600 text-sm font-bold text-white"
                >
                    UP
                </span>
                <span v-if="!collapsed" class="leading-tight">
                    <span class="block font-semibold text-slate-900">
                        Ubuntu Panel
                    </span>
                    <span class="block text-xs text-slate-400">
                        Server control
                    </span>
                </span>
            </Link>

            <nav
                class="flex-1 space-y-6 overflow-y-auto px-3 pb-4"
                @click="showingMobileMenu = false"
            >
                <div v-for="section in sections" :key="section.heading">
                    <p
                        v-if="!collapsed"
                        class="px-3 pb-2 text-[11px] font-semibold uppercase tracking-wider text-slate-400"
                    >
                        {{ section.heading }}
                    </p>
                    <div v-else class="mx-4 mb-2 border-t border-slate-200" />

                    <div class="space-y-1">
                        <SidebarLink
                            v-for="link in section.links"
                            :key="link.route"
                            :href="route(link.route)"
                            :icon="link.icon"
                            :label="link.name"
                            :active="route().current(link.pattern)"
                            :collapsed="collapsed"
                        />
                    </div>
                </div>
            </nav>

            <button
                type="button"
                class="hidden items-center gap-3 border-t border-slate-200 py-4 text-xs font-medium text-slate-400 transition hover:text-slate-700 lg:flex"
                :class="collapsed ? 'justify-center' : 'px-6'"
                @click="collapsed = !collapsed"
            >
                <svg
                    class="h-4 w-4 shrink-0 transition-transform"
                    :class="collapsed ? 'rotate-180' : ''"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                    viewBox="0 0 24 24"
                >
                    <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        d="M15.75 19.5L8.25 12l7.5-7.5"
                    />
                </svg>
                <span v-if="!collapsed">Collapse menu</span>
            </button>
        </aside>

        <!-- Content column -->
        <div
            class="transition-[padding] duration-150"
            :class="collapsed ? 'lg:ps-20' : 'lg:ps-64'"
        >
            <header
                class="sticky top-0 z-20 flex min-h-20 items-center gap-4 border-b border-slate-200 bg-slate-50/90 px-4 py-4 backdrop-blur sm:px-6 lg:px-8"
            >
                <button
                    type="button"
                    class="-ms-1 rounded-lg p-2 text-slate-500 transition hover:bg-white hover:text-slate-900 lg:hidden"
                    aria-label="Toggle menu"
                    @click="showingMobileMenu = !showingMobileMenu"
                >
                    <svg
                        class="h-5 w-5"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="2"
                        viewBox="0 0 24 24"
                    >
                        <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            d="M4 6h16M4 12h16M4 18h16"
                        />
                    </svg>
                </button>

                <div class="min-w-0 flex-1">
                    <slot name="header" />
                </div>

                <Dropdown align="right" width="48">
                    <template #trigger>
                        <button
                            type="button"
                            class="inline-flex shrink-0 items-center gap-2 rounded-full bg-white py-1.5 pe-3 ps-1.5 text-sm font-medium text-slate-700 shadow-sm ring-1 ring-slate-900/5 transition hover:text-slate-900 focus:outline-none"
                        >
                            <span
                                class="flex h-7 w-7 items-center justify-center rounded-full bg-brand-50 text-xs font-semibold uppercase text-brand-700"
                            >
                                {{ $page.props.auth.user.name.charAt(0) }}
                            </span>
                            <span class="hidden sm:inline">
                                {{ $page.props.auth.user.name }}
                            </span>
                            <svg
                                class="h-4 w-4 text-slate-400"
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
            </header>

            <main class="px-4 py-6 sm:px-6 lg:px-8">
                <FlashMessages />
                <slot />
            </main>
        </div>
    </div>
</template>
