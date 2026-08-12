<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import AppIcon from '@/Components/AppIcon.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { Head, Link, router } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

const props = defineProps({
    databases: Array,
    availableEngines: Array,
    engines: Object,
    phpMyAdmin: Boolean,
    // Paths, not URLs: phpMyAdmin is served from the panel's own origin, so it
    // opens on whatever address this page was reached on. The first signs
    // itself in and shows every database; the second stops at the login form,
    // for signing in as one database's own user.
    phpMyAdminAll: { type: String, default: '/phpmyadmin/' },
    phpMyAdminManual: { type: String, default: '/phpmyadmin/' },
    mongodb: Boolean,
    activeTask: { type: Object, default: null },
});

const busy = computed(
    () =>
        props.databases.some((database) => isBusyStatus(database.status)) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['databases', 'activeTask']);


const destroy = async (database) => {
    const go = await confirmDanger(
        `Drop ${database.name}?`,
        'All data in it is lost, and the database user is removed with it.',
        'Drop',
    );

    if (!go) return;

    router.delete(route('databases.destroy', database.id), {
        preserveScroll: true,
    });
};

// The Manage dialog: one database's credentials, and the ways into it.
//
// A dialog rather than a row that unfolds underneath: what it holds is a
// password, and a password that stays open on a page somebody scrolls past is
// a password on a screen somebody walks past.
const managing = ref(null);

const manage = async (database) => {
    managing.value = { database, credentials: null };

    try {
        const { data } = await window.axios.get(
            route('databases.credentials', database.id),
        );

        // Guard against a second click having moved on while this was in
        // flight, which would otherwise fill one database's dialog with
        // another's password.
        if (managing.value?.database.id === database.id)
            managing.value = { database, credentials: data };
    } catch {
        if (managing.value?.database.id === database.id)
            managing.value = {
                database,
                error: 'The panel could not read this database’s credentials.',
            };
    }
};

const closeManage = () => {
    managing.value = null;
    copied.value = '';
};

// Everything about one database as a single block of text.
//
// One block and one button, because the reason somebody opens this dialog is
// usually to send the lot to a developer — and four separate copy buttons is
// four chances to paste three of them and leave the port behind.
const managedDetails = computed(() => {
    if (!managing.value?.credentials) return '';

    const { database } = managing.value;
    const it = managing.value.credentials;

    const lines = [
        `Database: ${it.database}`,
        `Engine:   ${database.engine_label}`,
        `Host:     ${it.host}`,
        `Port:     ${it.port}`,
        `Username: ${it.username}`,
        `Password: ${it.password}`,
    ];

    if (it.auth_db) lines.push(`Auth database: ${it.auth_db}`);
    if (it.uri) lines.push(`Connection string: ${it.uri}`);

    return lines.join('\n');
});

// phpMyAdmin, at its login form, with this database already named — so the
// credentials above are pasted into a page that opens straight onto it.
const phpMyAdminFor = (database) => {
    const separator = props.phpMyAdminManual.includes('?') ? '&' : '?';

    return `${props.phpMyAdminManual}${separator}db=${encodeURIComponent(database.name)}`;
};

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

        <!-- One database: what it is called, who it belongs to, and the way in. -->
        <div
            v-if="managing"
            class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4"
            @click.self="closeManage"
        >
            <div
                class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-xl ring-1 ring-slate-900/10"
            >
                <div class="flex items-start justify-between gap-4">
                    <div>
                        <h3 class="text-lg font-semibold text-slate-900">
                            {{ managing.database.name }}
                        </h3>
                        <p class="mt-1 text-sm text-slate-500">
                            {{ managing.database.engine_label }}
                        </p>
                    </div>
                    <button
                        @click="closeManage"
                        class="rounded-lg p-1 text-slate-400 transition hover:bg-slate-100 hover:text-slate-600"
                        aria-label="Close"
                    >
                        ✕
                    </button>
                </div>

                <p
                    v-if="managing.error"
                    class="mt-6 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800"
                >
                    {{ managing.error }}
                </p>

                <p
                    v-else-if="!managing.credentials"
                    class="mt-6 text-sm text-slate-500"
                >
                    Reading the credentials…
                </p>

                <template v-else>
                    <!--
                        Everything in one block, copied by one button. What
                        this dialog is usually open for is sending a developer
                        their connection details, and a field at a time is how
                        the port gets left behind.
                    -->
                    <div class="mt-5">
                        <div class="flex items-center justify-between gap-3">
                            <p class="text-sm font-medium text-slate-700">
                                Connection details
                            </p>
                            <button
                                @click="copy(managedDetails, 'all')"
                                class="text-xs font-medium text-brand-600 hover:underline"
                            >
                                {{ copied === 'all' ? 'Copied' : 'Copy all' }}
                            </button>
                        </div>
                        <pre
                            class="mt-2 overflow-x-auto rounded-xl bg-slate-900 px-4 py-3 font-mono text-xs leading-relaxed text-slate-200"
                            >{{ managedDetails }}</pre
                        >
                        <p
                            v-if="managing.credentials.uri"
                            class="mt-2 text-xs text-slate-500"
                        >
                            The connection string works from MongoDB Compass on
                            your desktop and from anything running on the
                            server.
                        </p>
                    </div>

                    <!--
                        The same phpMyAdmin the header button opens, stopped at
                        its login form with this database named. The header one
                        is already signed in as an administrator and shows
                        everything; this one is whatever the credentials above
                        can reach, which is this database and nothing else.
                    -->
                    <div
                        v-if="phpMyAdmin && managing.database.engine === 'mysql'"
                        class="mt-5 flex flex-wrap items-center justify-between gap-3 border-t border-slate-200 pt-4"
                    >
                        <p class="text-xs text-slate-500">
                            Opens phpMyAdmin's login form. Sign in with the user
                            and password above to manage this database on its
                            own.
                        </p>
                        <a
                            :href="phpMyAdminFor(managing.database)"
                            target="_blank"
                            rel="noopener"
                            class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700"
                        >
                            Open in phpMyAdmin
                        </a>
                    </div>
                </template>
            </div>
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
                            Paste this into Compass's connection box. It is the
                            panel's own administrator account, so every database
                            on this server is visible through it.
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
                        <template v-for="database in databases" :key="database.id">
                            <tr>
                                <td class="px-5 py-3">
                                    <p class="font-medium text-slate-800">
                                        {{ database.name }}
                                    </p>
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
                                <td class="whitespace-nowrap px-5 py-3 text-right">
                                    <button
                                        @click="manage(database)"
                                        class="text-brand-600 hover:underline"
                                    >
                                        Manage
                                    </button>
                                    <button
                                        @click="destroy(database)"
                                        class="ml-3 text-rose-600 hover:underline"
                                    >
                                        Drop
                                    </button>
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
        </div>
    </AuthenticatedLayout>
</template>
