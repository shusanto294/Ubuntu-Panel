<script setup>
import { computed, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import TextInput from '@/Components/TextInput.vue';
import Checkbox from '@/Components/Checkbox.vue';
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
    www: true,
    php_version: props.phpVersion ?? props.phpVersions[0],
    web_directory: '',
    ssl: true,
    repository: '',
    branch: 'main',
    repo_auth: 'none',
    repo_token: '',
    repo_key_id: '',
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

// Whether the domain typed is a root domain — example.com, example.co.uk —
// rather than a host inside one. Only a root domain has a www worth offering,
// which is the same question the panel asks on the server before it adds one.
const registryLabels = [
    'co', 'com', 'net', 'org', 'gov', 'edu', 'ac', 'or', 'ne', 'me', 'in',
    'biz', 'info', 'nom', 'gob', 'mil',
];

const isApexDomain = computed(() => {
    const labels = form.domain.trim().toLowerCase().split('.').filter(Boolean);

    if (labels.length === 2) return true;

    return (
        labels.length === 3 &&
        registryLabels.includes(labels[1]) &&
        labels[2].length <= 3
    );
});

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
        case 'nuxt':
            // Not "a minimal Node server": Nuxt is started from its own build
            // output, so the placeholder has to be a real Nuxt app that the
            // install and build steps then turn into one.
            return 'a starter Nuxt application is created, installed and built';
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

// Not on the machine yet. Creating the site installs them before it deploys,
// so they are only said, not asked for.
const toInstall = computed(() =>
    needed.value
        .map((key) => props.requirements.find((app) => app.key === key))
        .filter((app) => app && app.status !== 'installed')
        .map((app) => app.label),
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

// ------------------------------------------------------------ the repository
//
// Asked as it is typed. A public repository needs nothing more; a private one
// is only discovered by asking, and then — and only then — the form asks how
// the panel should sign in to it. Without this, a private repository was found
// out by the deployment, after the site already existed.

const repo = ref({ status: 'idle', message: '', cloneUrl: '' });
const isPrivate = ref(false);
const deployKey = ref({ publicKey: '', generating: false, error: '' });
const keyCopied = ref(false);

const repoNeeded = computed(() => !isManaged.value && form.repository.trim() !== '');

let checkTimer = null;
let checkSequence = 0;

const checkRepository = async () => {
    clearTimeout(checkTimer);
    const sequence = ++checkSequence;

    if (!repoNeeded.value) {
        repo.value = { status: 'idle', message: '', cloneUrl: '' };
        isPrivate.value = false;
        form.repo_auth = 'none';
        return;
    }

    if (form.repo_auth === 'key' && !form.repo_key_id) {
        repo.value = {
            status: 'waiting',
            message: 'Generate a deploy key and add it to the repository.',
            cloneUrl: '',
        };
        return;
    }

    if (form.repo_auth === 'token' && !form.repo_token.trim()) {
        repo.value = { status: 'waiting', message: 'Paste an access token.', cloneUrl: '' };
        return;
    }

    repo.value = { ...repo.value, status: 'checking' };

    try {
        const { data } = await window.axios.post(route('sites.repository.check'), {
            repository: form.repository,
            branch: form.branch,
            repo_auth: form.repo_auth,
            repo_token: form.repo_token,
            repo_key_id: form.repo_key_id,
        });

        if (sequence !== checkSequence) return;

        let status = 'error';

        if (data.ok && data.found) {
            status = 'ok';
        } else if (data.needs_credentials) {
            // Private. A deploy key is the one to suggest: read-only, one
            // repository, and nothing to copy out of an account's settings.
            isPrivate.value = true;
            if (form.repo_auth === 'none') form.repo_auth = 'key';
            status = 'private';
        }

        repo.value = { status, message: data.message, cloneUrl: data.repository ?? '' };
    } catch (error) {
        if (sequence !== checkSequence) return;

        repo.value = {
            status: 'error',
            message: 'The panel could not check the repository. You can still create the site; it is checked again then.',
            cloneUrl: '',
        };
    }
};

const scheduleCheck = (delay = 700) => {
    clearTimeout(checkTimer);
    checkTimer = setTimeout(checkRepository, delay);
};

// A different repository starts again as a public one: it may well be, and a
// deploy key that is not on it would be refused even if it were.
watch(
    () => form.repository,
    () => {
        isPrivate.value = false;
        form.repo_auth = 'none';
    },
);
watch(() => [form.repository, form.branch, form.type], () => scheduleCheck());
watch(() => form.repo_token, () => form.repo_auth === 'token' && scheduleCheck());
watch(() => form.repo_auth, () => scheduleCheck(0));

const generateDeployKey = async () => {
    deployKey.value = { ...deployKey.value, generating: true, error: '' };

    try {
        const { data } = await window.axios.post(route('sites.repository.key'), {
            domain: form.domain,
        });

        deployKey.value = { publicKey: data.public_key, generating: false, error: '' };
        form.repo_key_id = data.key_id;
        keyCopied.value = false;
        checkRepository();
    } catch (error) {
        deployKey.value = {
            ...deployKey.value,
            generating: false,
            error: error.response?.data?.error ?? 'The panel could not generate a key.',
        };
    }
};

const copyKey = async () => {
    try {
        await navigator.clipboard.writeText(deployKey.value.publicKey);
        keyCopied.value = true;
    } catch {
        keyCopied.value = false;
    }
};

// What stops the form: a repository the panel cannot read yet. Checked again
// on the server, so this is the courtesy and that is the rule.
const repoBlocking = computed(
    () => repoNeeded.value && !['ok', 'error'].includes(repo.value.status),
);

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
                        <p
                            v-if="toInstall.length"
                            class="rounded-xl bg-brand-50 px-4 py-3 text-sm text-brand-900 ring-1 ring-brand-100"
                        >
                            This server does not have {{ toInstall.join(', ') }} yet. Creating the site
                            installs {{ toInstall.length === 1 ? 'it' : 'them' }} first — only what a
                            {{ typeConfig?.label ?? 'site' }} site needs — then deploys it.
                        </p>
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

                    <div v-if="isApexDomain">
                        <label class="flex items-start gap-3">
                            <input
                                v-model="form.www"
                                type="checkbox"
                                class="mt-0.5 rounded border-slate-300 text-brand-600 focus:ring-brand-500"
                            />
                            <span class="min-w-0 text-sm text-slate-700">
                                Also answer on
                                <span class="break-all font-mono">www.{{ form.domain.trim().toLowerCase() }}</span>
                                <span class="mt-0.5 block text-xs text-slate-500">
                                    The record is published with the domain's
                                    and the certificate covers both. Unticked,
                                    a visitor who types the www gets a 404.
                                </span>
                            </span>
                        </label>
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
                                class="inline-flex max-w-full items-center gap-1 break-all rounded-full bg-slate-100 px-2.5 py-0.5 text-xs text-slate-700"
                            >
                                {{ alias }}
                                <button
                                    type="button"
                                    @click="removeAlias(alias)"
                                    class="-my-1 -me-1 px-1 py-1 text-slate-400 hover:text-rose-600"
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
                        <p
                            v-if="!repoNeeded"
                            class="mt-1 text-xs text-slate-500"
                        >
                            Public or private. If it is private, the panel asks
                            how to sign in to it.
                        </p>
                        <p
                            v-else-if="repo.status === 'checking'"
                            class="mt-1 text-xs text-slate-500"
                        >
                            Checking the repository…
                        </p>
                        <p
                            v-else-if="repo.status === 'ok'"
                            class="mt-1 text-xs text-emerald-700"
                        >
                            ✓ {{ repo.message }}
                        </p>
                        <p
                            v-else-if="isPrivate"
                            class="mt-1 text-xs text-amber-700"
                        >
                            This repository is private. Choose how the panel
                            signs in to it below.
                        </p>
                        <p
                            v-else-if="repo.status === 'error' && !isPrivate"
                            class="mt-1 text-xs text-rose-700"
                        >
                            {{ repo.message }}
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

                <!-- Credentials: only for a repository that turned out to be private -->
                <div
                    v-if="repoNeeded && isPrivate"
                    class="mt-5 rounded-xl border border-amber-200 bg-amber-50/50 p-4"
                >
                    <p class="text-sm font-medium text-slate-800">
                        Private repository access
                    </p>

                    <div class="mt-3 flex flex-wrap gap-4">
                        <label class="flex items-center gap-2 text-sm text-slate-700">
                            <input
                                type="radio"
                                value="key"
                                v-model="form.repo_auth"
                                class="text-brand-600 focus:ring-brand-500"
                            />
                            Deploy key (recommended)
                        </label>
                        <label class="flex items-center gap-2 text-sm text-slate-700">
                            <input
                                type="radio"
                                value="token"
                                v-model="form.repo_auth"
                                class="text-brand-600 focus:ring-brand-500"
                            />
                            Access token
                        </label>
                    </div>
                    <InputError class="mt-2" :message="form.errors.repo_auth" />

                    <!-- deploy key -->
                    <div v-if="form.repo_auth === 'key'" class="mt-4">
                        <template v-if="!deployKey.publicKey">
                            <p class="text-xs text-slate-600">
                                The panel makes an SSH key for this site. You add
                                its public half to the repository; it can read that
                                one repository and nothing else.
                            </p>
                            <button
                                type="button"
                                :disabled="deployKey.generating"
                                class="mt-3 rounded-lg bg-slate-800 px-3 py-1.5 text-xs font-medium text-white hover:bg-slate-700 disabled:opacity-50"
                                @click="generateDeployKey"
                            >
                                {{ deployKey.generating ? 'Generating…' : 'Generate deploy key' }}
                            </button>
                            <p v-if="deployKey.error" class="mt-2 text-xs text-rose-700">
                                {{ deployKey.error }}
                            </p>
                        </template>

                        <template v-else>
                            <InputLabel value="Public key — add this to the repository" />
                            <div
                                class="mt-1 break-all rounded-xl bg-slate-900 p-3 font-mono text-xs text-slate-100"
                            >
                                {{ deployKey.publicKey }}
                            </div>
                            <div class="mt-2 flex flex-wrap items-center gap-4">
                                <button
                                    type="button"
                                    class="text-xs font-medium text-brand-600 hover:underline"
                                    @click="copyKey"
                                >
                                    {{ keyCopied ? 'Copied' : 'Copy the public key' }}
                                </button>
                                <button
                                    type="button"
                                    :disabled="repo.status === 'checking'"
                                    class="rounded-lg bg-slate-800 px-3 py-1.5 text-xs font-medium text-white hover:bg-slate-700 disabled:opacity-50"
                                    @click="checkRepository"
                                >
                                    {{ repo.status === 'checking' ? 'Checking…' : "I've added it — check again" }}
                                </button>
                            </div>
                            <p class="mt-3 text-xs text-slate-600">
                                On GitHub: the repository → Settings → Deploy keys →
                                Add deploy key. Paste the key; leave “Allow write
                                access” off. (GitLab: Settings → Repository → Deploy
                                keys.)
                            </p>
                            <p
                                v-if="repo.cloneUrl && repo.cloneUrl !== form.repository.trim()"
                                class="mt-2 text-xs text-slate-500"
                            >
                                A deploy key works over SSH, so the panel will
                                clone <code>{{ repo.cloneUrl }}</code>.
                            </p>
                        </template>
                    </div>

                    <!-- token -->
                    <div v-else-if="form.repo_auth === 'token'" class="mt-4">
                        <InputLabel for="repo_token" value="Access token" />
                        <TextInput
                            id="repo_token"
                            type="password"
                            v-model="form.repo_token"
                            class="mt-1 block w-full"
                            autocomplete="off"
                            placeholder="github_pat_…"
                        />
                        <InputError class="mt-2" :message="form.errors.repo_token" />
                        <p class="mt-1 text-xs text-slate-600">
                            On GitHub: Settings → Developer settings → Fine-grained
                            tokens, with read-only “Contents” access to this
                            repository. Stored encrypted.
                        </p>
                    </div>

                    <p
                        v-if="repo.status === 'ok'"
                        class="mt-3 text-xs font-medium text-emerald-700"
                    >
                        ✓ The panel can read the repository.
                    </p>
                    <p
                        v-else-if="repo.status === 'error'"
                        class="mt-3 text-xs text-rose-700"
                    >
                        {{ repo.message }}
                    </p>
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
                <div class="flex flex-wrap items-center justify-between gap-3">
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

                <!--
                    The credential above is pre-selected, not chosen, and it may
                    hold no zone for the domain being typed — a subdomain of a
                    domain parked at some other registrar is the ordinary case.
                    The panel checks when the site is created and publishes
                    nothing when the answer is no, rather than failing the
                    deployment on its second step. Said here so the answer is
                    not a surprise.
                -->
                <p
                    v-if="form.manage_dns"
                    class="mt-3 text-xs text-slate-500"
                >
                    Records are published at whichever connected credential holds
                    this domain's zone. If none of them does, the site is still
                    built and its page says which record to add by hand — the
                    deployment is not held up by it.
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
                    :disabled="form.processing || repoBlocking"
                    class="rounded-xl bg-brand-600 px-5 py-2.5 text-sm font-medium text-white hover:bg-brand-700 disabled:opacity-50"
                >
                    Create site
                </button>
                <span v-if="repoBlocking" class="text-xs text-slate-500">
                    {{
                        repo.status === 'checking' || repo.status === 'idle'
                            ? 'Waiting for the repository check…'
                            : 'The panel needs to be able to read the repository first.'
                    }}
                </span>
                <Link
                    :href="route('sites.index')"
                    class="text-sm text-slate-600 hover:underline"
                    >Cancel</Link
                >
            </div>
        </form>
    </AuthenticatedLayout>
</template>
