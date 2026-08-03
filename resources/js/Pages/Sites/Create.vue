<script setup>
import { computed, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import TextInput from '@/Components/TextInput.vue';
import Checkbox from '@/Components/Checkbox.vue';
import { Head, Link, useForm } from '@inertiajs/vue3';

const props = defineProps({
    installedServices: Array,
    pendingServices: Array,
    phpVersion: String,
    nodeVersion: String,
    publicIp: String,
    cloudflareAccounts: Array,
    phpVersions: Array,
    dnsTypes: Array,
    siteTypes: Array,
    sitesRoot: String,
});

const form = useForm({
    type: 'wordpress',
    domain: '',
    aliases: [],
    php_version: props.phpVersions[0],
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
    manage_dns: props.cloudflareAccounts.length > 0,
    cloudflare_account_id: props.cloudflareAccounts[0]?.id ?? null,
    dns_type: 'A',
    dns_content: '',
    dns_proxied: true,
});

const aliasInput = ref('');

const typeConfig = computed(() =>
    props.siteTypes.find((t) => t.key === form.type),
);

const isProxied = computed(() => !!typeConfig.value?.proxied);
const isPhp = computed(() => typeConfig.value?.runtime === 'php');

// What this machine is missing for the selected site type.
const requirement = computed(() => {
    const installed = props.installedServices ?? [];
    const pending = props.pendingServices ?? [];

    const need = (key, label) => {
        if (installed.includes(key)) return null;
        return pending.includes(key)
            ? `${label} is still installing. It will be ready in a moment.`
            : `${label} is not installed. Add it from the Software page first.`;
    };

    if (isProxied.value) return need('node', 'Node.js');
    if (form.type === 'wordpress')
        return need('mysql', 'MariaDB') ?? need('wpcli', 'WP-CLI');
    if (form.type === 'laravel') return need('mysql', 'MariaDB');
    return null;
});

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
            <div class="rounded-xl border border-slate-200 bg-white p-6">
                <h3 class="font-semibold text-slate-800">What are you hosting?</h3>

                <div class="mt-4 grid gap-3 sm:grid-cols-3">
                    <button
                        v-for="type in siteTypes"
                        :key="type.key"
                        type="button"
                        @click="form.type = type.key"
                        class="rounded-lg border p-4 text-left transition"
                        :class="
                            form.type === type.key
                                ? 'border-orange-400 bg-orange-50 ring-1 ring-orange-300'
                                : 'border-slate-200 hover:border-slate-300'
                        "
                    >
                        <span class="block font-medium text-slate-800">{{
                            type.label
                        }}</span>
                        <span class="mt-1 block text-xs text-slate-500">
                            {{
                                type.proxied
                                    ? 'systemd service behind nginx'
                                    : type.runtime === 'php'
                                      ? 'PHP-FPM via nginx'
                                      : 'served straight from disk'
                            }}
                            <template v-if="type.database"> · database</template>
                        </span>
                    </button>
                </div>
                <InputError class="mt-2" :message="form.errors.type" />
            </div>

            <!-- Domain -->
            <div class="rounded-xl border border-slate-200 bg-white p-6">
                <h3 class="font-semibold text-slate-800">Where it lives</h3>

                <div class="mt-4 grid gap-6 sm:grid-cols-2">
                    <div v-if="requirement" class="sm:col-span-2">
                        <p class="rounded-md bg-amber-50 px-3 py-2 text-xs text-amber-800">
                            {{ requirement }}
                            <Link :href="route('services.index')" class="underline"
                                >Open the Software page</Link
                            >
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
                                class="shrink-0 rounded-md border border-slate-300 px-3 text-sm text-slate-700 hover:bg-slate-50"
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
                            class="mt-1 block w-full rounded-md border-slate-300 shadow-sm focus:border-orange-500 focus:ring-orange-500"
                        >
                            <option v-for="v in phpVersions" :key="v" :value="v">
                                PHP {{ v }}
                            </option>
                        </select>
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
            <div class="rounded-xl border border-slate-200 bg-white p-6">
                <h3 class="font-semibold text-slate-800">Source code</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Leave the repository blank to get a working starter:
                    {{
                        form.type === 'wordpress'
                            ? 'WordPress is downloaded and installed for you'
                            : form.type === 'laravel'
                              ? 'a fresh Laravel application is created'
                              : isProxied
                                ? 'a minimal Node server is scaffolded'
                                : 'a placeholder page is written'
                    }}.
                </p>

                <div
                    v-if="form.type !== 'wordpress'"
                    class="mt-4 grid gap-6 sm:grid-cols-2"
                >
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

                <div v-if="isProxied" class="mt-4 grid gap-6 sm:grid-cols-2">
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
                    v-if="form.type === 'wordpress'"
                    class="mt-4 grid gap-6 sm:grid-cols-2"
                >
                    <div>
                        <InputLabel for="wp_title" value="Site title" />
                        <TextInput
                            id="wp_title"
                            v-model="form.wp_title"
                            class="mt-1 block w-full"
                            :placeholder="form.domain || 'My site'"
                        />
                    </div>
                    <div>
                        <InputLabel for="wp_admin_user" value="Admin username" />
                        <TextInput
                            id="wp_admin_user"
                            v-model="form.wp_admin_user"
                            class="mt-1 block w-full"
                        />
                    </div>
                    <div>
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
                    <div>
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
                    <p class="text-xs text-slate-500 sm:col-span-2">
                        A MariaDB database and user are created automatically
                        and written into wp-config.php.
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

            <!-- Cloudflare DNS -->
            <div class="rounded-xl border border-slate-200 bg-white p-6">
                <div class="flex items-center justify-between">
                    <h3 class="font-semibold text-slate-800">Cloudflare DNS</h3>
                    <label class="flex items-center gap-2">
                        <Checkbox
                            v-model:checked="form.manage_dns"
                            :disabled="!cloudflareAccounts.length"
                        />
                        <span class="text-sm text-slate-700"
                            >Create DNS records automatically</span
                        >
                    </label>
                </div>

                <p
                    v-if="!cloudflareAccounts.length"
                    class="mt-3 text-sm text-slate-500"
                >
                    No Cloudflare account connected.
                    <Link
                        :href="route('cloudflare.index')"
                        class="text-orange-600 hover:underline"
                        >Connect one</Link
                    >
                    to have records created and deleted with the site.
                </p>

                <div
                    v-if="form.manage_dns"
                    class="mt-4 grid gap-6 sm:grid-cols-2"
                >
                    <div>
                        <InputLabel
                            for="cloudflare_account_id"
                            value="Cloudflare account"
                        />
                        <select
                            id="cloudflare_account_id"
                            v-model="form.cloudflare_account_id"
                            class="mt-1 block w-full rounded-md border-slate-300 shadow-sm focus:border-orange-500 focus:ring-orange-500"
                        >
                            <option
                                v-for="account in cloudflareAccounts"
                                :key="account.id"
                                :value="account.id"
                            >
                                {{ account.label }}
                            </option>
                        </select>
                        <InputError
                            class="mt-2"
                            :message="form.errors.cloudflare_account_id"
                        />
                    </div>

                    <div>
                        <InputLabel for="dns_type" value="Record type" />
                        <select
                            id="dns_type"
                            v-model="form.dns_type"
                            class="mt-1 block w-full rounded-md border-slate-300 shadow-sm focus:border-orange-500 focus:ring-orange-500"
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

                    <div class="flex items-end">
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
                    :disabled="form.processing || !!requirement"
                    class="rounded-md bg-orange-500 px-5 py-2.5 text-sm font-medium text-white hover:bg-orange-600 disabled:opacity-50"
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
