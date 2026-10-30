<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import AppIcon from '@/Components/AppIcon.vue';
import Pagination from '@/Components/Pagination.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { usePagination } from '@/Composables/usePagination';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    databases: Array,
    availableEngines: Array,
    engines: Object,
    phpMyAdmin: Boolean,
    // A path, not a URL: phpMyAdmin is served from the panel's own origin, so
    // it opens on whatever address this page was reached on. This one signs
    // itself in and shows every database; the one on a database's own page
    // stops at the login form instead.
    phpMyAdminAll: { type: String, default: '/phpmyadmin/' },
    mongodb: Boolean,
    activeTask: { type: Object, default: null },
});

const busy = computed(
    () =>
        props.databases.some((database) => isBusyStatus(database.status)) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['databases', 'activeTask']);

const { page, pages, total, perPage, visible: visibleDatabases } = usePagination(
    () => props.databases,
);

// The server-wide MongoDB connection string, for Compass. Fetched when the
// dialog opens rather than shipped with the page: it carries the password to
// every database on the machine.
const compass = ref(null);
const compassLoading = ref(false);
const copied = ref('');

const openCompass = async () => {
    compass.value = { pending: true };
    compassLoading.value = true;

    try {
        const { data } = await window.axios.get(
            route('databases.mongo-connection'),
        );
        compass.value = data;
    } catch {
        compass.value = {
            error: 'The panel could not read MongoDB’s credentials. Reload and try again.',
        };
    } finally {
        compassLoading.value = false;
    }
};

const closeCompass = () => {
    compass.value = null;
    copied.value = '';
    accountOpen.value = false;
};

// Changing the account the string is for. Nothing the panel does logs in as
// it, so it can be renamed and re-passworded without the panel noticing.
const accountOpen = ref(false);
const account = ref({ username: '', password: '' });
const accountErrors = ref({});
const accountSaving = ref(false);
const accountSaved = ref(false);

const editAccount = () => {
    account.value = { username: compass.value.own ? compass.value.username : '', password: '' };
    accountErrors.value = {};
    accountSaved.value = false;
    accountOpen.value = true;
};

const saveAccount = async () => {
    accountSaving.value = true;
    accountErrors.value = {};

    try {
        const { data } = await window.axios.post(route('databases.mongo-account'), account.value);
        compass.value = data;
        copied.value = '';
        accountOpen.value = false;
        accountSaved.value = true;
    } catch (error) {
        accountErrors.value = error.response?.data?.errors ?? {
            username: 'The panel could not change the account. Reload and try again.',
        };
    } finally {
        accountSaving.value = false;
    }
};

const copy = async (text, which) => {
    try {
        await navigator.clipboard.writeText(text);
        copied.value = which;
    } catch {
        // A browser that refuses the clipboard leaves the string on screen to
        // be selected by hand, which is what it was rendered for anyway.
        copied.value = '';
    }
};
</script>

<template>
    <Head title="Databases" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Databases</h2>
                    <p class="text-sm text-slate-500">
                        {{ databases.length }}
                        {{ databases.length === 1 ? 'database' : 'databases' }}
                        on this server
                    </p>
                </div>
                <div class="flex flex-wrap items-center gap-2">
                    <Link
                        :href="route('databases.create')"
                        class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700"
                    >
                        New database
                    </Link>

                    <!--
                        Served from this same origin, so a plain link: whatever
                        address the panel was reached on is the one phpMyAdmin
                        opens on, certificate and all.
                    -->
                    <a
                        v-if="phpMyAdmin"
                        :href="phpMyAdminAll"
                        target="_blank"
                        rel="noopener"
                        class="inline-flex items-center gap-2 rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    >
                        <AppIcon app-key="phpmyadmin" size="sm" bare />
                        phpMyAdmin
                    </a>

                    <button
                        v-if="mongodb"
                        @click="openCompass"
                        class="inline-flex items-center gap-2 rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    >
                        <AppIcon app-key="mongodb" size="sm" bare />
                        MongoDB Compass
                    </button>
                </div>
            </div>
        </template>

        <div v-if="activeTask" class="mb-6">
            <TaskStatus :task="activeTask" />
        </div>

        <!--
            Compass is a desktop application, so there is nothing to link to —
            what it wants is a connection string, and the two ways of getting
            one wrong are the authentication database and the escaping of the
            password. Both are already right in what is shown here.
        -->
        <div
            v-if="compass"
            class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4"
            @click.self="closeCompass"
        >
            <div
                class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-xl ring-1 ring-slate-900/10"
            >
                <div class="flex items-start justify-between gap-4">
                    <div>
                        <h3 class="text-lg font-semibold text-slate-900">
                            Connect with MongoDB Compass
                        </h3>
                        <p class="mt-1 text-sm text-slate-500">
                            Paste this into Compass's connection box. It is an
                            administrator account, so every database on this server
                            is visible through it.
                        </p>
                    </div>
                    <button
                        @click="closeCompass"
                        class="rounded-lg p-1 text-slate-400 transition hover:bg-slate-100 hover:text-slate-600"
                        aria-label="Close"
                    >
                        ✕
                    </button>
                </div>

                <p v-if="compassLoading" class="mt-6 text-sm text-slate-500">
                    Reading the credentials…
                </p>

                <p
                    v-else-if="compass.error"
                    class="mt-6 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800"
                >
                    {{ compass.error }}
                </p>

                <template v-else-if="compass.uri">
                    <!--
                        One string, on the server's own address. It works from
                        Compass on a desktop and from anything running on the
                        server, so there is nothing to choose between.
                    -->
                    <div class="mt-5">
                        <div class="flex items-center justify-between gap-3">
                            <p class="text-sm font-medium text-slate-700">
                                Connection string
                            </p>
                            <button
                                @click="copy(compass.uri, 'uri')"
                                class="text-xs font-medium text-brand-600 hover:underline"
                            >
                                {{ copied === 'uri' ? 'Copied' : 'Copy' }}
                            </button>
                        </div>
                        <pre
                            class="mt-2 overflow-x-auto rounded-xl bg-slate-900 px-4 py-3 font-mono text-xs text-slate-200"
                            >{{ compass.uri }}</pre
                        >
                    </div>

                    <dl
                        class="mt-5 grid grid-cols-2 gap-x-6 gap-y-2 border-t border-slate-200 pt-4 text-sm sm:grid-cols-4"
                    >
                        <div>
                            <dt class="text-slate-500">Host</dt>
                            <dd class="font-mono text-slate-800">
                                {{ compass.host }}
                            </dd>
                        </div>
                        <div>
                            <dt class="text-slate-500">Port</dt>
                            <dd class="font-mono text-slate-800">
                                {{ compass.port }}
                            </dd>
                        </div>
                        <div>
                            <dt class="text-slate-500">User</dt>
                            <dd class="font-mono text-slate-800">
                                {{ compass.username }}
                            </dd>
                        </div>
                        <div>
                            <dt class="text-slate-500">Auth database</dt>
                            <dd class="font-mono text-slate-800">
                                {{ compass.auth_db }}
                            </dd>
                        </div>
                    </dl>

                    <div class="mt-5 border-t border-slate-200 pt-4">
                        <div class="flex flex-wrap items-center justify-between gap-3">
                            <p class="text-sm text-slate-600">
                                <template v-if="accountSaved">
                                    <span class="font-medium text-emerald-700">Saved.</span>
                                    The old username and password no longer work.
                                </template>
                                <template v-else-if="compass.own">
                                    Your own account, with full access to every database.
                                </template>
                                <template v-else>
                                    This is the panel's internal account. Set a username and
                                    password of your own to use here instead.
                                </template>
                            </p>
                            <button
                                v-if="!accountOpen"
                                @click="editAccount"
                                class="rounded-lg px-3 py-1.5 text-xs font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                            >
                                {{ compass.own ? 'Change username or password' : 'Set username and password' }}
                            </button>
                        </div>

                        <form
                            v-if="accountOpen"
                            class="mt-3 grid gap-3 sm:grid-cols-2"
                            @submit.prevent="saveAccount"
                        >
                            <div>
                                <label class="block text-xs font-medium text-slate-700">Username</label>
                                <input
                                    v-model="account.username"
                                    type="text"
                                    autocomplete="off"
                                    placeholder="e.g. admin"
                                    class="mt-1 block w-full rounded-lg border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                                />
                                <p v-if="accountErrors.username" class="mt-1 text-xs text-rose-600">
                                    {{ accountErrors.username }}
                                </p>
                            </div>
                            <div>
                                <label class="block text-xs font-medium text-slate-700">Password</label>
                                <input
                                    v-model="account.password"
                                    type="password"
                                    autocomplete="new-password"
                                    placeholder="Leave blank to generate one"
                                    class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                                />
                                <p v-if="accountErrors.password" class="mt-1 text-xs text-rose-600">
                                    {{ accountErrors.password }}
                                </p>
                            </div>
                            <p class="text-xs text-slate-500 sm:col-span-2">
                                Anything connected with the current string stops working once
                                this is saved. The panel's internal account is not changed.
                            </p>
                            <div class="flex gap-2 sm:col-span-2">
                                <button
                                    type="submit"
                                    :disabled="accountSaving || !account.username"
                                    class="rounded-lg bg-brand-600 px-3 py-1.5 text-xs font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                                >
                                    {{ accountSaving ? 'Saving…' : 'Save' }}
                                </button>
                                <button
                                    type="button"
                                    @click="accountOpen = false"
                                    class="rounded-lg px-3 py-1.5 text-xs font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                                >
                                    Cancel
                                </button>
                            </div>
                        </form>
                    </div>

                    <p
                        class="mt-5 rounded-xl bg-amber-50 px-4 py-3 text-xs text-amber-800"
                    >
                        <span class="font-medium"
                            >This is the key to every database on the
                            server.</span
                        >
                        MongoDB listens on all interfaces so Compass can reach
                        it, which means this password is the only thing between
                        the internet and your data. Do not paste it anywhere it
                        will be kept.
                    </p>
                </template>
            </div>
        </div>

        <div
            class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
        >
            <div class="overflow-x-auto">
                <table class="min-w-full divide-y divide-slate-200">
                    <thead class="bg-slate-50">
                        <tr
                            class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                        >
                            <th class="px-5 py-3">Name</th>
                            <th class="px-5 py-3">Engine</th>
                            <th class="px-5 py-3">Status</th>
                            <th class="px-5 py-3"></th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100 text-sm">
                        <template v-for="database in visibleDatabases" :key="database.id">
                            <tr>
                                <td class="px-5 py-3">
                                    <Link
                                        :href="route('databases.show', database.id)"
                                        class="font-medium text-slate-800 hover:text-brand-600 hover:underline"
                                    >
                                        {{ database.name }}
                                    </Link>
                                    <p class="text-xs text-slate-500">
                                        user: {{ database.username }}
                                        <span
                                            v-if="database.managed_by_site"
                                            class="ml-1 text-brand-600"
                                            >· created for a site</span
                                        >
                                    </p>
                                </td>
                                <td class="px-5 py-3 text-slate-600">
                                    {{ database.engine_label }}
                                </td>
                                <td class="px-5 py-3">
                                    <StatusBadge :status="database.status" />
                                </td>
                                <!--
                                    No Drop here. It sat one careless click
                                    from a database and its contents, in a list
                                    people open to read — it lives on the
                                    database's own page now, under a heading
                                    that says what it is.
                                -->
                                <td class="whitespace-nowrap px-5 py-3 text-right">
                                    <Link
                                        :href="route('databases.show', database.id)"
                                        class="text-brand-600 hover:underline"
                                    >
                                        Manage
                                    </Link>
                                </td>
                            </tr>
                            <tr v-if="database.last_error">
                                <td
                                    colspan="4"
                                    class="bg-rose-50 px-5 py-2 text-xs text-rose-700"
                                >
                                    {{ database.last_error }}
                                </td>
                            </tr>
                        </template>
                        <tr v-if="!databases.length">
                            <td
                                colspan="4"
                                class="px-5 py-16 text-center text-sm text-slate-500"
                            >
                                No databases yet. WordPress and Laravel sites
                                create theirs automatically.
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>
            <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
        </div>
    </AuthenticatedLayout>
</template>
