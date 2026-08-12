<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import AppIcon from '@/Components/AppIcon.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import { Head, Link, router } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

const props = defineProps({
    database: Object,
    users: { type: Array, default: () => [] },
    phpMyAdmin: Boolean,
    phpMyAdminManual: String,
    activeTask: { type: Object, default: null },
});

const credentials = computed(() => props.database.credentials);
const isMySQL = computed(() => props.database.engine === 'mysql');

// Everything in one block, copied by one button. What this page is usually
// open for is sending a developer their connection details, and a field at a
// time is how the port gets left behind.
const details = computed(() => {
    const it = credentials.value;

    const lines = [
        `Database: ${it.database}`,
        `Engine:   ${props.database.engine_label}`,
        `Host:     ${it.host}`,
        `Port:     ${it.port}`,
        `Username: ${it.username}`,
        `Password: ${it.password}`,
    ];

    if (it.auth_db) lines.push(`Auth database: ${it.auth_db}`);
    if (it.uri) lines.push(`Connection string: ${it.uri}`);

    return lines.join('\n');
});

const copied = ref('');

const copy = async (text, which) => {
    try {
        await navigator.clipboard.writeText(text);
        copied.value = which;
    } catch {
        // A browser that refuses the clipboard leaves the text on screen to be
        // selected by hand, which is what it was rendered for anyway.
        copied.value = '';
    }
};

// Users. A database is not one account and a password: it is a database, and
// whoever needs to reach it needs an account of their own — an application, a
// backup job, somebody who should be able to read it and nothing else.
//
// No password is ever shown back from the record for anybody but the account
// the database was created with. The panel does not store the others, so it
// prints each one once when it is set and forgets it.
const username = ref('');
const password = ref('');
const working = ref(false);

const submit = (action, extra = {}) => {
    working.value = true;

    router.post(
        route('databases.users', props.database.id),
        { action, ...extra },
        {
            preserveScroll: true,
            onFinish: () => {
                working.value = false;
                username.value = '';
                password.value = '';
            },
        },
    );
};

const addUser = () => {
    if (!username.value.trim()) return;

    submit('create', { username: username.value, password: password.value });
};

const changePassword = async (user) => {
    const chosen = window.prompt(
        `New password for ${user.name}. Leave blank to have one generated.`,
        '',
    );

    // Cancel is null; an empty string is "generate one", which is a different
    // answer and a deliberate one.
    if (chosen === null) return;

    const go = await confirmDanger(
        `Change the password for ${user.name}?`,
        user.primary
            ? 'Anything connecting with the old one stops working until it is updated — ' +
              'sites the panel deployed included.'
            : 'Anything connecting as this user with the old password stops working.',
        'Change it',
    );

    if (!go) return;

    submit('password', { username: user.name, password: chosen });
};

const removeUser = async (user) => {
    const go = await confirmDanger(
        `Remove ${user.name}?`,
        'The account is dropped. The database and everything in it is untouched.',
        'Remove',
    );

    if (!go) return;

    submit('delete', { username: user.name });
};

const repair = () =>
    router.post(
        route('databases.repair', props.database.id),
        {},
        { preserveScroll: true },
    );

const destroy = async () => {
    const go = await confirmDanger(
        `Drop ${props.database.name}?`,
        'All data in it is lost, and the database user is removed with it.',
        'Drop',
    );

    if (!go) return;

    router.delete(route('databases.destroy', props.database.id));
};
</script>

<template>
    <Head :title="database.name" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div class="flex items-center gap-3">
                    <AppIcon :app-key="database.engine" size="md" />
                    <div>
                        <h2 class="text-xl font-semibold text-slate-900">
                            {{ database.name }}
                        </h2>
                        <p class="text-sm text-slate-500">
                            {{ database.engine_label }}
                            <span v-if="database.managed_by_site" class="text-brand-600"
                                >· created for a site</span
                            >
                        </p>
                    </div>
                </div>
                <div class="flex flex-wrap items-center gap-2">
                    <StatusBadge :status="database.status" />
                    <a
                        v-if="phpMyAdmin && isMySQL"
                        :href="phpMyAdminManual"
                        target="_blank"
                        rel="noopener"
                        class="inline-flex items-center gap-2 rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    >
                        <AppIcon app-key="phpmyadmin" size="sm" bare />
                        Open in phpMyAdmin
                    </a>
                    <Link
                        :href="route('databases.index')"
                        class="rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    >
                        All databases
                    </Link>
                </div>
            </div>
        </template>

        <div v-if="activeTask" class="mb-6">
            <TaskStatus :task="activeTask" />
        </div>

        <div
            v-if="database.last_error"
            class="mb-6 rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800"
        >
            <span class="font-medium">Last error:</span> {{ database.last_error }}
        </div>

        <div class="grid gap-6 lg:grid-cols-3">
            <div
                class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5 lg:col-span-2"
            >
                <div class="flex items-center justify-between gap-3">
                    <h3 class="font-semibold text-slate-800">Connection details</h3>
                    <button
                        @click="copy(details, 'all')"
                        class="text-xs font-medium text-brand-600 hover:underline"
                    >
                        {{ copied === 'all' ? 'Copied' : 'Copy all' }}
                    </button>
                </div>

                <pre
                    class="mt-3 overflow-x-auto rounded-xl bg-slate-900 px-4 py-3 font-mono text-xs leading-relaxed text-slate-200"
                    >{{ details }}</pre
                >

                <!--
                    The address is the server's own, not loopback: both engines
                    the panel opens are listening on every interface, so these
                    credentials work from an application on this machine and
                    from one somewhere else.
                -->
                <p class="mt-3 text-xs text-slate-500">
                    <template v-if="isMySQL || database.engine === 'mongodb'">
                        These work from an application on this server and from
                        one anywhere else — the engine listens on every
                        interface, so the password is the only thing keeping
                        other people out. Change it below if it has been
                        anywhere it should not.
                    </template>
                    <template v-else>
                        PostgreSQL listens on this machine only, so these work
                        from an application on this server.
                    </template>
                </p>
            </div>

            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-800">Users</h3>
                <p class="mt-2 text-sm text-slate-500">
                    Accounts that can reach this database, as the engine reports
                    them — including any made outside the panel.
                </p>

                <ul class="mt-4 divide-y divide-slate-100 text-sm">
                    <li
                        v-for="user in users"
                        :key="user.name"
                        class="flex flex-wrap items-center justify-between gap-2 py-3"
                    >
                        <div class="min-w-0">
                            <p class="break-all font-mono text-slate-800">
                                {{ user.name }}
                            </p>
                            <p v-if="user.primary" class="text-xs text-brand-600">
                                created with the database
                            </p>
                            <p v-else-if="user.hosts" class="text-xs text-slate-400">
                                {{ user.hosts }}
                            </p>
                        </div>
                        <div class="flex shrink-0 items-center gap-3">
                            <button
                                @click="changePassword(user)"
                                :disabled="working"
                                class="text-xs font-medium text-brand-600 hover:underline disabled:opacity-50"
                            >
                                Password
                            </button>
                            <!--
                                The account the database was created with has no
                                Remove: a site's configuration names it, and
                                dropping it from here would take that site down
                                from a page which says nothing about it.
                            -->
                            <button
                                v-if="!user.primary"
                                @click="removeUser(user)"
                                :disabled="working"
                                class="text-xs font-medium text-rose-600 hover:underline disabled:opacity-50"
                            >
                                Remove
                            </button>
                        </div>
                    </li>
                    <li v-if="!users.length" class="py-3 text-slate-500">
                        The engine reported no users for this database.
                    </li>
                </ul>

                <div class="mt-5 border-t border-slate-200 pt-4">
                    <p class="text-sm font-medium text-slate-700">Add a user</p>
                    <input
                        v-model="username"
                        type="text"
                        placeholder="username"
                        class="mt-2 w-full rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                    />
                    <input
                        v-model="password"
                        type="text"
                        autocomplete="off"
                        placeholder="Leave blank to generate a password"
                        class="mt-2 w-full rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                    />
                    <button
                        @click="addUser"
                        :disabled="working || !username.trim()"
                        class="mt-3 w-full rounded-xl bg-slate-900 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-slate-700 disabled:opacity-50"
                    >
                        Add user
                    </button>
                    <p class="mt-3 text-xs text-slate-500">
                        The password is shown once, when it is set. The panel
                        stores it only for the account the database was created
                        with — the one in the connection details.
                    </p>
                </div>
            </div>
        </div>

        <!--
            For a database created before the panel opened MariaDB to the
            network: its account exists for localhost and nowhere else, so the
            details above are correct and a connection from another machine is
            refused with an error about the password.
        -->
        <div
            v-if="isMySQL"
            class="mt-6 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div class="flex flex-wrap items-center justify-between gap-4">
                <div>
                    <h3 class="font-semibold text-slate-800">
                        Cannot connect from another machine?
                    </h3>
                    <p class="mt-1 text-sm text-slate-500">
                        A database created before this panel opened MariaDB to
                        the network has an account that only works on the server
                        itself. This rebuilds it at every address, with the
                        password above. Nothing in the database is touched.
                    </p>
                </div>
                <button
                    @click="repair"
                    class="rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                >
                    Rebuild the account
                </button>
            </div>
        </div>

        <div
            class="mt-6 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <h3 class="font-semibold text-slate-800">Danger zone</h3>
            <p class="mt-2 text-sm text-slate-500">
                Dropping removes the database, everything in it, and the user it
                was created with.
            </p>
            <button
                @click="destroy"
                class="mt-4 rounded-xl bg-rose-600 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-rose-700"
            >
                Drop {{ database.name }}
            </button>
        </div>
    </AuthenticatedLayout>
</template>
