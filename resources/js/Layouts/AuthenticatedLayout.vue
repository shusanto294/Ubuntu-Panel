<script setup>
import { onMounted, ref, watch } from 'vue';
import Dropdown from '@/Components/Dropdown.vue';
import DropdownLink from '@/Components/DropdownLink.vue';
import SidebarLink from '@/Components/SidebarLink.vue';
import FlashMessages from '@/Components/FlashMessages.vue';
import { Link } from '@inertiajs/vue3';

// Sidebar-and-content, laid out the way the WordPress admin is: a slim dark
// bar across the top, a dark menu pinned down the left, and everything else in
// the light column beside it. The menu collapses to an icon rail on a wide
// screen and slides in over the content on a narrow one.
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
        'M2.25 12l8.954-8.955c.44-.439 1.152-.439 1.591 0L21.75 12M4.5 9.75v10.125c0 .621.504 1.125 1.125 1.125H9.75v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21h4.125c.621 0 1.125-.504 1.125-1.125V9.75M8.25 21h8.25',
    sites: 'M12 21a9.004 9.004 0 008.716-6.747M12 21a9.004 9.004 0 01-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 017.843 4.582M12 3a8.997 8.997 0 00-7.843 4.582m15.686 0A11.953 11.953 0 0112 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0121 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0112 16.5c-3.162 0-6.133-.815-8.716-2.247m0 0A9.015 9.015 0 013 12c0-1.605.42-3.113 1.157-4.418',
    databases:
        'M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75',
    email: 'M21.75 6.75v10.5a2.25 2.25 0 01-2.25 2.25h-15a2.25 2.25 0 01-2.25-2.25V6.75m19.5 0A2.25 2.25 0 0019.5 4.5h-15a2.25 2.25 0 00-2.25 2.25m19.5 0v.243a2.25 2.25 0 01-1.07 1.916l-7.5 4.615a2.25 2.25 0 01-2.36 0L3.32 8.91a2.25 2.25 0 01-1.07-1.916V6.75',
    terminal:
        'M6.75 7.5l3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0021 18V6a2.25 2.25 0 00-2.25-2.25H5.25A2.25 2.25 0 003 6v12a2.25 2.25 0 002.25 2.25z',
    settings:
        'M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.324.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 011.37.49l1.296 2.247a1.125 1.125 0 01-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a6.759 6.759 0 010 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.431l-1.298 2.247a1.125 1.125 0 01-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.57 6.57 0 01-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.02-.397-1.11-.94l-.213-1.28c-.062-.375-.312-.687-.644-.87a6.52 6.52 0 01-.22-.128c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 01-1.369-.49l-1.297-2.247a1.125 1.125 0 01.26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 010-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 01-.26-1.43l1.297-2.248a1.125 1.125 0 011.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
    profile:
        'M17.982 18.725A7.488 7.488 0 0012 15.75a7.488 7.488 0 00-5.982 2.975m11.963 0a9 9 0 10-11.963 0m11.963 0A8.966 8.966 0 0112 21a8.966 8.966 0 01-5.982-2.275M15 9.75a3 3 0 11-6 0 3 3 0 016 0z',
};

const links = [
    { name: 'Dashboard', route: 'dashboard', pattern: 'dashboard', icon: icons.dashboard },
    { name: 'Sites', route: 'sites.index', pattern: 'sites.*', icon: icons.sites },
    { name: 'Databases', route: 'databases.index', pattern: 'databases.*', icon: icons.databases },
    { name: 'Email', route: 'email.index', pattern: 'email.*', icon: icons.email },
    { name: 'Terminal', route: 'terminal', pattern: 'terminal', icon: icons.terminal },
];

// Services and DNS credentials are sections of Settings rather than top-level
// destinations — they are things you set up once, not places you work. Below
// the divider for the same reason WordPress keeps Settings down there.
const secondaryLinks = [
    { name: 'Settings', route: 'settings', pattern: 'settings', icon: icons.settings },
    { name: 'Profile', route: 'profile.edit', pattern: 'profile.edit', icon: icons.profile },
];
</script>

<template>
    <div class="min-h-screen bg-slate-100">
        <!-- Admin bar -->
        <header
            class="fixed inset-x-0 top-0 z-40 flex h-12 items-center gap-2 border-b border-slate-800 bg-slate-900 px-3 text-slate-300"
        >
            <button
                type="button"
                class="rounded p-2 text-slate-400 transition hover:bg-slate-800 hover:text-white lg:hidden"
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

            <Link
                :href="route('dashboard')"
                class="flex items-center gap-2 text-white"
            >
                <span
                    class="flex h-7 w-7 items-center justify-center rounded bg-orange-500 text-xs font-bold"
                >
                    UP
                </span>
                <span class="text-sm font-semibold tracking-wide">
                    Ubuntu Panel
                </span>
            </Link>

            <div class="ms-auto flex items-center">
                <Dropdown align="right" width="48">
                    <template #trigger>
                        <button
                            type="button"
                            class="inline-flex items-center rounded-md px-3 py-1.5 text-sm font-medium text-slate-300 transition hover:bg-slate-800 hover:text-white focus:outline-none"
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
        </header>

        <!-- Backdrop for the slide-in menu; only ever reachable below lg. -->
        <div
            v-if="showingMobileMenu"
            class="fixed inset-0 top-12 z-30 bg-slate-900/50 lg:hidden"
            @click="showingMobileMenu = false"
        />

        <!-- Admin menu -->
        <aside
            class="fixed bottom-0 left-0 top-12 z-30 flex flex-col bg-slate-900 transition-[width,transform] duration-150 lg:translate-x-0"
            :class="[
                collapsed ? 'w-16' : 'w-56',
                showingMobileMenu ? 'translate-x-0' : '-translate-x-full',
            ]"
        >
            <nav
                class="flex-1 overflow-y-auto py-2"
                @click="showingMobileMenu = false"
            >
                <SidebarLink
                    v-for="link in links"
                    :key="link.route"
                    :href="route(link.route)"
                    :icon="link.icon"
                    :label="link.name"
                    :active="route().current(link.pattern)"
                    :collapsed="collapsed"
                />

                <div class="my-2 border-t border-slate-800" />

                <SidebarLink
                    v-for="link in secondaryLinks"
                    :key="link.route"
                    :href="route(link.route)"
                    :icon="link.icon"
                    :label="link.name"
                    :active="route().current(link.pattern)"
                    :collapsed="collapsed"
                />
            </nav>

            <button
                type="button"
                class="hidden items-center gap-3 border-t border-slate-800 py-3 text-xs font-medium text-slate-400 transition hover:bg-slate-800 hover:text-white lg:flex"
                :class="collapsed ? 'justify-center' : 'px-4'"
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
            class="pt-12 transition-[padding] duration-150"
            :class="collapsed ? 'lg:ps-16' : 'lg:ps-56'"
        >
            <header
                v-if="$slots.header"
                class="border-b border-slate-200 bg-white px-4 py-5 sm:px-6 lg:px-8"
            >
                <slot name="header" />
            </header>

            <main class="px-4 py-6 sm:px-6 lg:px-8">
                <FlashMessages />
                <slot />
            </main>
        </div>
    </div>
</template>
