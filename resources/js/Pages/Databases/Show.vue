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

// Blank means "generate one", which is the better answer anyway: the reason to
// change a password is usually that the old one got out, and a password
// somebody chooses in that moment is the old one with a digit on the end.
const password = ref('');
const changing = ref(false);

const changePassword = async () => {
    const go = await confirmDanger(
        `Change the password for ${credentials.value.username}?`,
        'Anything connecting with the old one stops working until it is updated — ' +
            'sites the panel deployed included.',
        'Change it',
    );

    if (!go) return;

    changing.value = true;

    router.post(
        route('databases.password', props.database.id),
        { password: password.value },
        {
            preserveScroll: true,
            onFinish: () => {
                changing.value = false;
                password.value = '';
            },
        },
    );
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
                <h3 class="font-semibold text-slate-800">Change the password</h3>
                <p class="mt-2 text-sm text-slate-500">
                    For when the old one has been somewhere it should not have
                    been. The database and everything in it is untouched.
                </p>

                <input
                    v-model="password"
                    type="text"
                    autocomplete="off"
                    placeholder="Leave blank to generate one"
                    class="mt-4 w-full rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                />

                <button
                    @click="changePassword"
                    :disabled="changing"
                    class="mt-3 w-full rounded-xl bg-slate-900 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-slate-700 disabled:opacity-50"
                >
                    {{ changing ? 'Changing…' : 'Change password' }}
                </button>

                <p class="mt-3 text-xs text-slate-500">
                    Anything still using the old password stops working until it
                    is updated — including sites the panel deployed against this
                    database.
                </p>
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
