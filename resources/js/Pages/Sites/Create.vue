<script setup>
import { computed, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import TextInput from '@/Components/TextInput.vue';
import Checkbox from '@/Components/Checkbox.vue';
import RequiredApps from '@/Components/RequiredApps.vue';
import AppIcon from '@/Components/AppIcon.vue';
import { Head, Link, useForm } from '@inertiajs/vue3';

const props = defineProps({
    installedServices: Array,
    pendingServices: Array,
    requirements: { type: Array, default: () => [] },
    phpVersion: String,
    nodeVersion: String,
    publicIp: String,
    dnsAccounts: Array,
    phpVersions: Array,
    installedPhpVersions: { type: Array, default: () => [] },
    dnsTypes: Array,
    siteTypes: Array,
    sitesRoot: String,
});

const form = useForm({
    type: 'wordpress',
    domain: '',
    aliases: [],
    php_version: props.phpVersion ?? props.phpVersions[0],
    web_directory: '',
    ssl: true,
    repository: '',
    branch: 'main',
    start_command: '',
    build_command: '',
    wp_title: '',
    wp_admin_user: 'admin',
    wp_admin_email: '',
    wp_admin_password: '',
    manage_dns: props.dnsAccounts.length > 0,
    dns_account_id: props.dnsAccounts[0]?.id ?? null,
    dns_type: 'A',
    dns_content: '',
    dns_proxied: true,
});

const aliasInput = ref('');

// The orange cloud is Cloudflare's alone; every other provider just serves the
// record, so offering the toggle would be offering something that does nothing.
const selectedDnsAccount = computed(() =>
    props.dnsAccounts.find((a) => a.id === form.dns_account_id),
);

watch(
    () => selectedDnsAccount.value?.supports_proxy,
    (supported) => {
        if (!supported) form.dns_proxied = false;
    },
    { immediate: true },
);

const typeConfig = computed(() =>
    props.siteTypes.find((t) => t.key === form.type),
);

const isProxied = computed(() => !!typeConfig.value?.proxied);
const isPhp = computed(() => typeConfig.value?.runtime === 'php');

// An application the panel installs itself: no repository to ask for, and for
// most of them a login to hand back afterwards.
const isManaged = computed(() => !!typeConfig.value?.managed);
const hasAdminAccount = computed(() => !!typeConfig.value?.admin_account);

const wantsAdminUser = computed(() => hasAdminAccount.value);

// What is written where, per type, said before the deployment rather than in
// the console afterwards.
const starterNote = computed(() => {
    switch (form.type) {
        case 'wordpress':
            return 'WordPress is downloaded and installed for you';
        case 'nextcloud':
            return 'Nextcloud is downloaded, installed and pointed at its database';
        case 'n8n':
            return 'n8n is installed and run as a service, with its data under the site directory';
        case 'laravel':
            return 'a fresh Laravel application is created';
        default:
            return isProxied.value
                ? 'a minimal Node server is scaffolded'
                : 'a placeholder page is written';
    }
});

// What the selected site type needs on the machine. nginx and Certbot are on
// every one of them: there is no site without a vhost, and `ssl` is on by
// default.
const needed = computed(() => {
    const keys = ['nginx'];

    if (form.ssl) keys.push('certbot');
    if (isProxied.value) keys.push('node');
    if (isPhp.value) keys.push('php');
    if (typeConfig.value?.database) keys.push('mysql');
    if (form.type === 'laravel') keys.push('composer');
    if (form.type === 'wordpress') keys.push('wpcli');

    return keys;
});

// Still missing, so the submit button knows to stay down.
const requirement = computed(() =>
    needed.value.some(
        (key) =>
            props.requirements.find((app) => app.key === key)?.status !== 'installed',
    ),
);

// The DNS record points here unless told otherwise.
if (!form.dns_content && props.publicIp) form.dns_content = props.publicIp;

watch(
    () => form.type,
    () => {
        form.web_directory = typeConfig.value?.web_directory ?? '';
        form.build_command = typeConfig.value?.build ?? '';
        form.start_command = typeConfig.value?.start ?? '';
    },
    { immediate: true },
);

const addAlias = () => {
    const value = aliasInput.value.trim().toLowerCase();
    if (value && !form.aliases.includes(value)) form.aliases.push(value);
    aliasInput.value = '';
};

const removeAlias = (alias) => {
    form.aliases = form.aliases.filter((a) => a !== alias);
};

const rootPreview = computed(() => {
    const dir = (form.web_directory ?? '').replace(/^\/+|\/+$/g, '');
    return `${props.sitesRoot}/${form.domain || 'example.com'}${dir ? '/' + dir : ''}`;
});

const submit = () => form.post(route('sites.store'));
</script>

<template>
    <Head title="New site" />

    <AuthenticatedLayout>
        <template #header>
            <h2 class="text-xl font-semibold text-slate-800">Create a site</h2>
        </template>

        <form @submit.prevent="submit" class="max-w-4xl space-y-6">
            <!-- Type -->
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6">
                <h3 class="font-semibold text-slate-800">What are you hosting?</h3>

                <div class="mt-4 grid gap-3 sm:grid-cols-3">
                    <button
                        v-for="type in siteTypes"
                        :key="type.key"
                        type="button"
                        @click="form.type = type.key"
                        class="flex items-start gap-3 rounded-lg border p-4 text-left transition"
                        :class="
                            form.type === type.key
                                ? 'border-brand-400 bg-brand-50 ring-1 ring-brand-300'
                                : 'border-slate-200 hover:border-slate-300'
                        "
                    >
                        <AppIcon :app-key="type.key" :label="type.label" size="sm" />
                        <span class="min-w-0">
                            <span class="block font-medium text-slate-800">{{
                                type.label
                            }}</span>
                            <span class="mt-1 block text-xs text-slate-500">
                                {{ type.description }}
                            </span>
                        </span>
                    </button>
                </div>
                <InputError class="mt-2" :message="form.errors.type" />
            </div>

            <!-- Domain -->
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6">
                <h3 class="font-semibold text-slate-800">Where it lives</h3>

                <div class="mt-4 grid gap-6 sm:grid-cols-2">
                    <div class="sm:col-span-2">
                        <RequiredApps
                            :apps="requirements"
                            :need="needed"
                            note="A site of this type needs these. Install them here and the form unlocks itself."
                        />
                    </div>

                    <div>
                        <InputLabel for="domain" value="Domain" />
                        <TextInput
                            id="domain"
                            v-model="form.domain"
                            class="mt-1 block w-full"
                            placeholder="app.example.com"
                        />
                        <InputError class="mt-2" :message="form.errors.domain" />
                    </div>

                    <div>
                        <InputLabel value="Aliases (optional)" />
                        <div class="mt-1 flex gap-2">
                            <TextInput
                                v-model="aliasInput"
                                class="block w-full"
                                placeholder="www.example.com"
                                @keydown.enter.prevent="addAlias"
                            />
                            <button
                                type="button"
                                @click="addAlias"
                                class="shrink-0 rounded-xl ring-1 ring-slate-900/10 px-3 text-sm text-slate-700 hover:bg-slate-50"
                            >
                                Add
                            </button>
                        </div>
                        <div class="mt-2 flex flex-wrap gap-2">
                            <span
                                v-for="alias in form.aliases"
                                :key="alias"
                                class="inline-flex items-center gap-1 rounded-full bg-slate-100 px-2.5 py-0.5 text-xs text-slate-700"
                            >
                                {{ alias }}
                                <button
                                    type="button"
                                    @click="removeAlias(alias)"
                                    class="text-slate-400 hover:text-rose-600"
                                >
                                    ×
                                </button>
                            </span>
                        </div>
                    </div>

                    <div v-if="isPhp">
                        <InputLabel for="php_version" value="PHP version" />
                        <select
                            id="php_version"
                            v-model="form.php_version"
                            class="mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        >
                            <option v-for="v in phpVersions" :key="v" :value="v">
                                PHP {{ v
                                }}{{
                                    installedPhpVersions.includes(v)
                                        ? ''
                                        : ' — not installed yet'
                                }}
                            </option>
                        </select>
                        <p
                            v-if="
                                installedPhpVersions.length &&
                                !installedPhpVersions.includes(form.php_version)
                            "
                            class="mt-1 text-xs text-amber-700"
                        >
                            PHP {{ form.php_version }} is not on this machine
                            yet. The deployment installs it first — that adds a
                            minute or two.
                        </p>
                    </div>

                    <div v-if="!isProxied">
                        <InputLabel for="web_directory" value="Web directory" />
                        <TextInput
                            id="web_directory"
                            v-model="form.web_directory"
                            class="mt-1 block w-full"
                            placeholder="public"
                        />
                        <p class="mt-1 text-xs text-slate-500">
                            Document root: <code>{{ rootPreview }}</code>
                        </p>
                    </div>

                    <div class="flex items-end">
                        <label class="flex items-center gap-2">
                            <Checkbox v-model:checked="form.ssl" />
                            <span class="text-sm text-slate-700">
                                Secure with HTTPS and redirect HTTP to it
                            </span>
                        </label>
                    </div>
                </div>
            </div>

            <!-- Source -->
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6">
                <h3 class="font-semibold text-slate-800">Source code</h3>
                <p class="mt-1 text-sm text-slate-500">
                    <template v-if="isManaged">
                        The panel installs this one:
                        {{ starterNote }}.
                    </template>
                    <template v-else>
                        Leave the repository blank to get a working starter:
                        {{ starterNote }}.
                    </template>
                </p>

                <div v-if="!isManaged" class="mt-4 grid gap-6 sm:grid-cols-2">
                    <div>
                        <InputLabel for="repository" value="Git repository" />
                        <TextInput
                            id="repository"
                            v-model="form.repository"
                            class="mt-1 block w-full"
                            placeholder="https://github.com/you/app.git"
                        />
                        <p class="mt-1 text-xs text-slate-500">
                            Cloned over HTTPS as www-data. Private repositories
                            need a token in the URL.
                        </p>
                        <InputError
                            class="mt-2"
                            :message="form.errors.repository"
                        />
                    </div>

                    <div>
                        <InputLabel for="branch" value="Branch" />
                        <TextInput
                            id="branch"
                            v-model="form.branch"
                            class="mt-1 block w-full"
                        />
                    </div>
                </div>

                <div
                    v-if="isProxied && !isManaged"
                    class="mt-4 grid gap-6 sm:grid-cols-2"
                >
                    <div>
                        <InputLabel for="build_command" value="Build command" />
                        <TextInput
                            id="build_command"
                            v-model="form.build_command"
                            class="mt-1 block w-full"
                            placeholder="npm run build"
                        />
                    </div>
                    <div>
                        <InputLabel for="start_command" value="Start command" />
                        <TextInput
                            id="start_command"
                            v-model="form.start_command"
                            class="mt-1 block w-full"
                            placeholder="npm run start"
                        />
                        <p class="mt-1 text-xs text-slate-500">
                            Runs as a systemd service. The panel assigns a local
                            port and puts nginx in front.
                        </p>
                    </div>
                </div>

                <div
                    v-if="hasAdminAccount"
                    class="mt-4 grid gap-6 sm:grid-cols-2"
                >
                    <div v-if="form.type === 'wordpress'">
                        <InputLabel for="wp_title" value="Site title" />
                        <TextInput
                            id="wp_title"
                            v-model="form.wp_title"
                            class="mt-1 block w-full"
                            placeholder="WordPress Site"
                        />
                    </div>
                    <div v-if="wantsAdminUser">
                        <InputLabel for="wp_admin_user" value="Admin username" />
                        <TextInput
                            id="wp_admin_user"
                            v-model="form.wp_admin_user"
                            class="mt-1 block w-full"
                        />
                    </div>
                    <div v-if="wantsAdminUser">
                        <InputLabel for="wp_admin_email" value="Admin email" />
                        <TextInput
                            id="wp_admin_email"
                            type="email"
                            v-model="form.wp_admin_email"
                            class="mt-1 block w-full"
                            placeholder="your account email"
                        />
                        <InputError
                            class="mt-2"
                            :message="form.errors.wp_admin_email"
                        />
                    </div>
                    <div v-if="hasAdminAccount">
                        <InputLabel
                            for="wp_admin_password"
                            value="Admin password"
                        />
                        <TextInput
                            id="wp_admin_password"
                            type="password"
                            v-model="form.wp_admin_password"
                            class="mt-1 block w-full"
                            placeholder="leave blank to generate one"
                            autocomplete="new-password"
                        />
                        <InputError
                            class="mt-2"
                            :message="form.errors.wp_admin_password"
                        />
                    </div>
                    <p
                        v-if="typeConfig?.database"
                        class="text-xs text-slate-500 sm:col-span-2"
                    >
                        A MariaDB database and user are created automatically
                        and written into the application's own configuration.
                    </p>
                </div>

                <p
                    v-if="form.type === 'laravel'"
                    class="mt-4 text-xs text-slate-500"
                >
                    A MariaDB database is created and written into
                    <code>.env</code>, then key:generate, storage:link and
                    migrate are run.
                </p>
            </div>

            <!-- DNS -->
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6">
                <div class="flex items-center justify-between">
                    <h3 class="font-semibold text-slate-800">DNS</h3>
                    <label class="flex items-center gap-2">
                        <Checkbox
                            v-model:checked="form.manage_dns"
                            :disabled="!dnsAccounts.length"
                        />
                        <span class="text-sm text-slate-700"
                            >Create DNS records automatically</span
                        >
                    </label>
                </div>

                <p
                    v-if="!dnsAccounts.length"
                    class="mt-3 text-sm text-slate-500"
                >
                    No DNS provider connected.
                    <Link
                        :href="route('dns.index')"
                        class="text-brand-600 hover:underline"
                        >Connect one</Link
                    >
                    to have records created and deleted with the site — or leave
                    this off and point an A record at this server yourself:
                    <span class="font-mono text-slate-700"
                        >A {{ form.domain || 'your domain' }} →
                        {{ publicIp || 'this server’s IP' }}</span
                    >. Without it the site is built and serves nothing, because
                    the name reaches no one.
                </p>

                <div
                    v-if="form.manage_dns"
                    class="mt-4 grid gap-6 sm:grid-cols-2"
                >
                    <div>
                        <InputLabel for="dns_account_id" value="DNS provider" />
                        <select
                            id="dns_account_id"
                            v-model="form.dns_account_id"
                            class="mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        >
                            <option
                                v-for="account in dnsAccounts"
                                :key="account.id"
                                :value="account.id"
                            >
                                {{ account.label }} — {{ account.provider_label }}
                            </option>
                        </select>
                        <InputError
                            class="mt-2"
                            :message="form.errors.dns_account_id"
                        />
                    </div>

                    <div>
                        <InputLabel for="dns_type" value="Record type" />
                        <select
                            id="dns_type"
                            v-model="form.dns_type"
                            class="mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        >
                            <option v-for="t in dnsTypes" :key="t" :value="t">
                                {{ t }}
                            </option>
                        </select>
                    </div>

                    <div>
                        <InputLabel for="dns_content" value="Points to" />
                        <TextInput
                            id="dns_content"
                            v-model="form.dns_content"
                            class="mt-1 block w-full"
                            :placeholder="selected?.host"
                        />
                        <p class="mt-1 text-xs text-slate-500">
                            Defaults to this machine's public IP address.
                        </p>
                    </div>

                    <div v-if="selectedDnsAccount?.supports_proxy" class="flex items-end">
                        <label class="flex items-center gap-2">
                            <Checkbox v-model:checked="form.dns_proxied" />
                            <span class="text-sm text-slate-700"
                                >Proxy through Cloudflare (orange cloud)</span
                            >
                        </label>
                    </div>
                </div>

                <p
                    v-if="form.manage_dns && form.ssl && form.dns_proxied"
                    class="mt-4 rounded-md bg-emerald-50 px-3 py-2 text-xs text-emerald-800"
                >
                    The certificate will be issued through Cloudflare DNS
                    validation, so the proxy can stay on.
                </p>
            </div>

            <div class="flex items-center gap-3">
                <button
                    :disabled="form.processing || requirement"
                    class="rounded-xl bg-brand-600 px-5 py-2.5 text-sm font-medium text-white hover:bg-brand-700 disabled:opacity-50"
                >
                    Create site
                </button>
                <Link
                    :href="route('sites.index')"
                    class="text-sm text-slate-600 hover:underline"
                    >Cancel</Link
                >
            </div>
        </form>
    </AuthenticatedLayout>
</template>
