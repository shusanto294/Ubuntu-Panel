<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskProgress from '@/Components/TaskProgress.vue';
import { formatRelative } from '@/time';
import { Head, Link, router } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

const props = defineProps({
    site: Object,
    logs: Array,
    activeTask: Object,
    latestTask: Object,
    certbot: Boolean,
    resolution: Object,
    phpDirectives: { type: Array, default: () => [] },
    cronJobs: { type: Array, default: () => [] },
    certificate: { type: Object, default: () => ({}) },
});

const deleteFiles = ref(true);
const showSecrets = ref(false);

// Which section is on screen. General first, because the thing somebody opens
// a site for is usually to read it rather than to change it.
const tab = ref('general');

const tabs = computed(() => {
    const entries = [
        { key: 'general', label: 'General' },
        { key: 'settings', label: 'Settings' },
    ];

    if (!props.site.is_proxied) entries.push({ key: 'php', label: 'PHP' });

    entries.push({ key: 'database', label: 'Database' });
    entries.push({ key: 'ssl', label: 'SSL/TLS' });
    entries.push({ key: 'cron', label: 'Cron jobs' });
    entries.push({ key: 'logs', label: 'Logs' });

    return entries;
});

// ---- Settings: the document root, inside the site's own directory.
const webDirectory = ref((props.site.web_directory ?? '').replace(/^\//, ''));

const saveDirectory = () =>
    router.post(
        route('sites.settings', props.site.id),
        { web_directory: webDirectory.value },
        { preserveScroll: true },
    );

// ---- PHP: one field per directive the panel is willing to write.
const phpValues = ref(
    Object.fromEntries(
        props.phpDirectives.map((directive) => [directive.key, directive.value ?? '']),
    ),
);

const savePHP = () =>
    router.post(route('sites.php', props.site.id), phpValues.value, {
        preserveScroll: true,
    });

// ---- Cron: the whole list is posted, so removing a row removes the line.
const jobs = ref(props.cronJobs.map((job) => ({ ...job })));

const addJob = () => jobs.value.push({ schedule: '0 3 * * *', command: '' });
const removeJob = (index) => jobs.value.splice(index, 1);

const saveJobs = () =>
    router.post(
        route('sites.cron', props.site.id),
        { jobs: JSON.stringify(jobs.value) },
        { preserveScroll: true },
    );

let timer = null;

const startPolling = () => {
    if (timer) return;
    timer = setInterval(
        () =>
            router.reload({
                only: ['site', 'logs', 'activeTask', 'latestTask'],
                preserveScroll: true,
            }),
        3000,
    );
};

const stopPolling = () => {
    if (timer) {
        clearInterval(timer);
        timer = null;
    }
};

// The list of logs and the site's own state can both have moved on since this
// page was last rendered into Inertia's cache — a deploy that finished after
// the redirect, a certificate issued on a retry.
const refetch = () =>
    router.reload({
        only: ['site', 'logs', 'activeTask', 'latestTask'],
        preserveScroll: true,
        preserveState: true,
    });

onMounted(() => {
    refetch();

    if (busy.value) startPolling();
});
onBeforeUnmount(stopPolling);
watch(busy, (value) => (value ? startPolling() : stopPolling()));

const post = (name, extra = {}) =>
    router.post(route(name, props.site.id), extra, { preserveScroll: true });

const destroy = async () => {
    const parts = ['its nginx vhost'];
    if (props.site.manage_dns)
        parts.push(`${props.site.dns_provider ?? 'DNS'} records`);
    if (props.site.database) parts.push('the site database');
    if (props.site.is_proxied) parts.push('the systemd service');

    const go = await confirmDanger(
        `Delete ${props.site.domain}?`,
        `This removes ${parts.join(', ')}${
            deleteFiles.value ? ' and all site files' : ''
        }.`,
    );

    if (!go) return;

    router.delete(route('sites.destroy', props.site.id), {
        data: { delete_files: deleteFiles.value },
    });
};
</script>

<template>
    <Head :title="site.domain" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-800">
                        {{ site.domain }}
                    </h2>
                    <p class="text-sm text-slate-500">
                        {{ site.type_label }} · {{ site.document_root }}
                    </p>
                </div>
                <div class="flex flex-wrap items-center gap-2">
                    <StatusBadge :status="site.status" />
                    <a
                        :href="site.url"
                        target="_blank"
                        rel="noopener"
                        class="rounded-xl bg-white ring-1 ring-slate-900/10 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                        >Visit</a
                    >
                    <button
                        v-if="site.repository"
                        @click="post('sites.pull')"
                        class="rounded-xl bg-white ring-1 ring-slate-900/10 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                    >
                        Pull latest
                    </button>
                    <button
                        v-if="site.is_proxied"
                        @click="post('sites.restart')"
                        class="rounded-xl bg-white ring-1 ring-slate-900/10 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                    >
                        Restart app
                    </button>
                    <button
                        v-if="site.manage_dns"
                        @click="post('sites.dns-sync')"
                        class="rounded-xl bg-white ring-1 ring-slate-900/10 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                    >
                        Sync DNS
                    </button>
                    <button
                        @click="post('sites.redeploy')"
                        class="rounded-md bg-slate-900 px-3 py-2 text-sm font-medium text-white hover:bg-slate-700"
                    >
                        Redeploy
                    </button>
                </div>
            </div>
        </template>

        <div
            v-if="busy && !activeTask && !latestTask"
            class="mb-8 flex items-center gap-3 rounded-xl border border-brand-200 bg-brand-50 px-5 py-4 text-sm text-brand-900"
        >
            <span class="h-2 w-2 animate-pulse rounded-full bg-brand-500" />
            Starting. If nothing happens within a minute, the panel's workers are
            not running — check
            <code class="rounded bg-brand-100 px-1">systemctl status ubuntu-panel</code
            >.
        </div>

        <!--
            Building a site takes minutes and every one of them happens out of
            sight: the step list is the page while that is true, and while the
            run that just failed still needs reading.
        -->
        <div v-if="showProgress" class="mb-8">
            <TaskProgress
                :task="activeTask ?? latestTask"
                :title="`Setting up ${site.domain}`"
                :subtitle="site.type_label"
            />
        </div>

        <!-- What you came back for once it lands. -->
        <div
            v-if="live"
            class="mb-8 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div class="flex flex-wrap items-start justify-between gap-4">
                <div class="min-w-0">
                    <p class="flex items-center gap-2 font-semibold text-slate-900">
                        <span
                            class="h-2.5 w-2.5 rounded-full"
                            :class="unreachable ? 'bg-rose-500' : 'bg-emerald-500'"
                        />
                        <template v-if="unreachable">
                            {{ site.domain }} is built, but nothing resolves it
                        </template>
                        <template v-else>{{ site.domain }} is live</template>
                    </p>
                    <a
                        :href="site.url"
                        target="_blank"
                        rel="noopener"
                        class="mt-1 block break-all font-mono text-sm text-brand-600 hover:underline"
                        >{{ site.url }}</a
                    >
                </div>

                <a
                    v-if="site.app"
                    :href="site.app.url"
                    target="_blank"
                    rel="noopener"
                    class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700"
                >
                    Open {{ site.type_label }}
                </a>
            </div>

            <!--
                The deployment checks the application on its port and nginx
                with the site's own Host header, both on this machine, so all
                of it passes on a domain that resolves to nothing. Opening the
                link then gives DNS_PROBE_FINISHED_NXDOMAIN and reads as the
                panel having built a broken site. It has not: the name has
                nowhere to send anyone.
            -->
            <div
                v-if="unreachable"
                class="mt-4 rounded-xl bg-rose-50 px-4 py-3 text-xs text-rose-800"
            >
                <span class="font-medium">{{ site.domain }} does not resolve.</span>
                The site is built and nginx answers for it on this server, but the
                domain points nowhere, so a browser shows
                <code class="rounded bg-rose-100 px-1"
                    >DNS_PROBE_FINISHED_NXDOMAIN</code
                >
                rather than reaching it.
                <template v-if="site.manage_dns">
                    The panel published the record itself, so either it has not
                    spread yet — that takes minutes — or the zone at the provider
                    is not the one serving this name. Check the zone, then
                    <button
                        @click="post('sites.dns-sync')"
                        class="font-medium underline"
                    >
                        Sync DNS</button
                    >.
                </template>
                <template v-else>
                    Add this record at whoever runs
                    <span class="font-mono">{{ site.domain }}</span
                    >'s DNS:
                    <span class="mt-2 block font-mono text-rose-900"
                        >A&nbsp;&nbsp;{{ site.domain }}&nbsp;&nbsp;→&nbsp;&nbsp;{{
                            resolution.expected || 'this server’s IP'
                        }}</span
                    >
                    Nothing needs redeploying afterwards; the site is already
                    waiting for it.
                </template>
            </div>

            <p
                v-if="!site.ssl"
                class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-xs text-amber-800"
            >
                Served over HTTPS with a self-signed certificate, so browsers warn
                and a CDN in front needs its origin mode on "Full" rather than
                "Full (strict)".
                <template v-if="certbot">
                    Certbot is installed, so it tried and did not get one —
                    <Link
                        v-if="latestTask"
                        :href="route('logs.show', latestTask.id)"
                        class="font-medium underline"
                        >the log says why</Link
                    ><span v-else>the log says why</span>. Usually
                    {{ site.domain }} does not resolve to this machine yet, or a
                    CDN answered the challenge itself. Fix that and Redeploy.
                </template>
                <template v-else>
                    Install Certbot from Apps, make sure {{ site.domain }} resolves
                    here, then Redeploy.
                </template>
            </p>

            <p v-if="site.app" class="mt-4 text-xs text-slate-500">
                <template v-if="site.app.token">
                    {{ site.app.label }} — it is below.
                </template>
                <template v-else>
                    {{ site.app.label }}
                    <span class="font-mono text-slate-700">{{
                        site.app.user
                    }}</span>
                    — the password is below.
                </template>
            </p>
        </div>

        <div
            v-if="site.last_error && !busy"
            class="mb-6 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800"
        >
            <span class="font-medium">Last error:</span> {{ site.last_error }}
        </div>

        <!--
            Nothing below the step list while the site is being built. Half of
            it is not true yet — the port, the certificate, the login the
            deployment has not generated — and a Danger zone under a running
            build invites deleting a site mid-deployment.
            
            `busy`, not `showProgress`: a deployment that failed leaves its
            steps on screen with a ✕ against one of them, and that is precisely
            when the Danger zone has to be reachable. Hiding it there left a
            half-built site with no way to remove it.
        -->
        <!--
            What the site is, before what can be done to it — the three facts
            somebody reads off a site page and puts into something else.
        -->
        <dl
            v-if="!busy"
            class="mb-6 grid grid-cols-2 gap-x-8 gap-y-4 border-b border-slate-200 pb-6 text-sm sm:grid-cols-4"
        >
            <div class="min-w-0">
                <dt class="text-xs uppercase tracking-wide text-slate-400">Domain</dt>
                <dd class="mt-1 flex items-center gap-2 truncate font-medium text-slate-800">
                    {{ site.domain }}
                    <a
                        :href="site.url"
                        target="_blank"
                        rel="noopener"
                        class="text-xs font-normal text-brand-600 hover:underline"
                        >open</a
                    >
                </dd>
            </div>
            <div class="min-w-0">
                <dt class="text-xs uppercase tracking-wide text-slate-400">Type</dt>
                <dd class="mt-1 truncate font-medium text-slate-800">
                    {{ site.type_label }}
                </dd>
            </div>
            <div class="min-w-0">
                <dt class="text-xs uppercase tracking-wide text-slate-400">
                    {{ site.is_proxied ? 'App port' : 'PHP' }}
                </dt>
                <dd class="mt-1 truncate font-medium text-slate-800">
                    {{ site.is_proxied ? site.app_port : site.php_version }}
                </dd>
            </div>
            <div class="min-w-0">
                <dt class="text-xs uppercase tracking-wide text-slate-400">
                    IP address
                </dt>
                <dd class="mt-1 truncate font-medium text-slate-800">
                    {{ resolution?.expected || '—' }}
                </dd>
            </div>
        </dl>

        <!--
            One site, several subjects. The page used to be one column of
            everything at once — settings, credentials, DNS, the vhost, the
            history — and finding the one you came for meant reading past the
            other four.
        -->
        <div v-if="!busy" class="mb-6 border-b border-slate-200">
            <nav class="-mb-px flex flex-wrap gap-6" aria-label="Sections">
                <button
                    v-for="entry in tabs"
                    :key="entry.key"
                    @click="tab = entry.key"
                    class="border-b-2 px-1 pb-3 text-sm font-medium transition"
                    :class="
                        tab === entry.key
                            ? 'border-brand-600 text-brand-700'
                            : 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700'
                    "
                >
                    {{ entry.label }}
                </button>
            </nav>
        </div>

        <div v-if="!busy && tab === 'general'" class="grid gap-6 lg:grid-cols-3">
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6">
                <h3 class="font-semibold text-slate-800">Site</h3>
                <dl class="mt-4 space-y-3 text-sm">
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Type</dt>
                        <dd class="text-slate-800">{{ site.type_label }}</dd>
                    </div>
                    <div
                        v-if="site.aliases.length"
                        class="flex justify-between gap-4"
                    >
                        <dt class="text-slate-500">Aliases</dt>
                        <dd class="text-right text-slate-800">
                            {{ site.aliases.join(', ') }}
                        </dd>
                    </div>
                    <div v-if="!site.is_proxied" class="flex justify-between gap-4">
                        <dt class="text-slate-500">PHP</dt>
                        <dd class="text-slate-800">{{ site.php_version }}</dd>
                    </div>
                    <div v-else class="flex justify-between gap-4">
                        <dt class="text-slate-500">App port</dt>
                        <dd class="text-slate-800">{{ site.app_port }}</dd>
                    </div>
                    <div v-if="site.service_name" class="flex justify-between gap-4">
                        <dt class="text-slate-500">Service</dt>
                        <dd class="break-all text-right text-xs text-slate-700">
                            {{ site.service_name }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">SSL</dt>
                        <dd class="text-slate-800">
                            {{ site.ssl ? 'Let’s Encrypt' : 'none' }}
                        </dd>
                    </div>
                    <div v-if="site.repository" class="flex justify-between gap-4">
                        <dt class="text-slate-500">Repository</dt>
                        <dd class="break-all text-right text-xs text-slate-700">
                            {{ site.repository }} ({{ site.branch }})
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Document root</dt>
                        <dd class="break-all text-right text-slate-800">
                            {{ site.document_root }}
                        </dd>
                    </div>
                </dl>
            </div>

            <div
                v-if="site.manage_dns"
                class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6"
            >
                <h3 class="font-semibold text-slate-800">DNS</h3>
                <div v-if="site.manage_dns" class="mt-4 space-y-3 text-sm">
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Account</dt>
                        <dd class="text-slate-800">
                            {{ site.dns_account }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Record</dt>
                        <dd class="text-slate-800">
                            {{ site.dns_type }} to {{ site.dns_content }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Proxied</dt>
                        <dd class="text-slate-800">
                            {{ site.dns_proxied ? 'yes' : 'no' }}
                        </dd>
                    </div>
                </div>
            </div>

            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6">
                <h3 class="font-semibold text-slate-800">Danger zone</h3>
                <p class="mt-2 text-sm text-slate-500">
                    Deleting removes the vhost, the systemd service, the site
                    database created by the panel, and its DNS records.
                </p>
                <label class="mt-4 flex items-center gap-2 text-sm">
                    <input
                        type="checkbox"
                        v-model="deleteFiles"
                        class="rounded border-slate-300 text-brand-500 focus:ring-brand-500"
                    />
                    Also delete files in {{ site.root_path }}
                </label>
                <button
                    @click="destroy"
                    class="mt-4 w-full rounded-md bg-rose-600 px-4 py-2 text-sm font-medium text-white hover:bg-rose-700"
                >
                    Delete this site
                </button>
            </div>
        </div>

        <div v-if="!busy && tab === 'database'" class="grid gap-6 lg:grid-cols-3">
            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5 lg:col-span-2">
                <h3 class="font-semibold text-slate-800">Database</h3>

                <div v-if="site.database" class="mt-4 space-y-3 text-sm">
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Engine</dt>
                        <dd class="text-slate-800">{{ site.database.engine_label }}</dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Database</dt>
                        <dd class="break-all text-right text-slate-800">
                            {{ site.database.name }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">User</dt>
                        <dd class="break-all text-right text-slate-800">
                            {{ site.database.username }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Status</dt>
                        <dd><StatusBadge :status="site.database.status" /></dd>
                    </div>
                </div>

                <p v-else class="mt-4 text-sm text-slate-500">
                    This kind of site does not use a database, and the panel did
                    not create one for it.
                </p>

                <!--
                    The user, the password and the connection details live on
                    the database's own page, which is also where the password is
                    changed. Two places to manage one database is one place too
                    many.
                -->
                <Link
                    v-if="site.database"
                    :href="route('databases.show', site.database.id)"
                    class="mt-5 inline-block rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
                >
                    Credentials and users
                </Link>
                <Link
                    v-else
                    :href="route('databases.index')"
                    class="mt-5 inline-block text-sm text-brand-600 hover:underline"
                >
                    All databases
                </Link>
            </div>
        </div>

        <div
            v-if="!busy && tab === 'settings'"
            class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <h3 class="font-semibold text-slate-800">Root directory</h3>
            <p class="mt-2 text-sm text-slate-500">
                The folder inside the site that nginx serves. A Laravel site
                serves <code>public</code>; a plain HTML site serves the site
                directory itself, which is this box left empty.
            </p>

            <div class="mt-4 flex flex-wrap items-center gap-2">
                <span class="font-mono text-sm text-slate-500">{{ site.root_path }}/</span>
                <input
                    v-model="webDirectory"
                    type="text"
                    placeholder="public"
                    class="w-56 rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                />
                <button
                    @click="saveDirectory"
                    class="rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
                >
                    Save
                </button>
            </div>

            <p class="mt-3 text-xs text-slate-500">
                The folder is created if it is not there, and nginx is reloaded
                — after being asked whether it will take the new configuration,
                and given the old one back if it will not.
            </p>
        </div>

        <div
            v-if="!busy && tab === 'php'"
            class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <h3 class="font-semibold text-slate-800">PHP settings</h3>
            <p class="mt-2 text-sm text-slate-500">
                For this site only, passed to PHP-FPM with every request. Left
                blank, each takes the value PHP itself was configured with.
            </p>

            <div class="mt-5 grid gap-5 sm:grid-cols-2">
                <div v-for="directive in phpDirectives" :key="directive.key">
                    <label class="block text-sm font-medium text-slate-700">
                        {{ directive.label }}
                    </label>
                    <input
                        v-model="phpValues[directive.key]"
                        type="text"
                        :placeholder="directive.default"
                        class="mt-1 w-full rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                    />
                    <p class="mt-1 text-xs text-slate-500">{{ directive.help }}</p>
                </div>
            </div>

            <button
                @click="savePHP"
                class="mt-6 rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
            >
                Apply
            </button>

            <p class="mt-3 text-xs text-slate-500">
                An upload also has to fit inside the maximum POST size, and
                nginx has its own limit on top of both — this panel sets that to
                128 MB for every site.
            </p>
        </div>

        <!--
            What was issued, read off the certificate on disk rather than from
            what the site asked for. The two differ exactly when somebody comes
            to this tab.
        -->
        <div
            v-if="!busy && tab === 'ssl'"
            class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <h3 class="font-semibold text-slate-800">SSL/TLS</h3>

            <dl class="mt-4 space-y-3 text-sm">
                <div class="flex justify-between gap-4">
                    <dt class="text-slate-500">Certificate</dt>
                    <dd class="text-slate-800">
                        {{ certificate.issued ? 'Issued' : 'None yet' }}
                    </dd>
                </div>
                <div v-if="certificate.expires" class="flex justify-between gap-4">
                    <dt class="text-slate-500">Expires</dt>
                    <dd class="text-right text-slate-800">{{ certificate.expires }}</dd>
                </div>
                <div v-if="certificate.issuer" class="flex justify-between gap-4">
                    <dt class="text-slate-500">Issuer</dt>
                    <dd class="break-all text-right text-xs text-slate-700">
                        {{ certificate.issuer }}
                    </dd>
                </div>
                <div class="flex justify-between gap-4">
                    <dt class="text-slate-500">Serving over</dt>
                    <dd class="text-slate-800">{{ site.ssl ? 'HTTPS' : 'HTTP' }}</dd>
                </div>
            </dl>

            <p
                v-if="!certificate.certbot"
                class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-xs text-amber-800"
            >
                Certbot is not installed, so no certificate can be issued.
                Install it from Apps first.
            </p>

            <button
                v-else
                @click="post('sites.certificate')"
                class="mt-5 rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
            >
                {{ certificate.issued ? 'Renew the certificate' : 'Issue a certificate' }}
            </button>

            <p class="mt-3 text-xs text-slate-500">
                Certbot is asked for this site alone — no rebuild, no
                redeployment. It needs the domain to resolve to this server
                first; the Logs tab has the run.
            </p>
        </div>

        <div
            v-if="!busy && tab === 'cron'"
            class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h3 class="font-semibold text-slate-800">Cron jobs</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        Run as <span class="font-mono">www-data</span>, from
                        <span class="font-mono">{{ site.root_path }}</span>.
                    </p>
                </div>
                <button
                    @click="addJob"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                >
                    Add a job
                </button>
            </div>

            <div v-if="jobs.length" class="mt-5 space-y-3">
                <div
                    v-for="(job, index) in jobs"
                    :key="index"
                    class="flex flex-wrap items-start gap-2"
                >
                    <input
                        v-model="job.schedule"
                        type="text"
                        placeholder="0 3 * * *"
                        class="w-40 rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                    />
                    <input
                        v-model="job.command"
                        type="text"
                        placeholder="php artisan schedule:run"
                        class="min-w-0 flex-1 rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                    />
                    <button
                        @click="removeJob(index)"
                        class="px-2 py-2 text-sm text-rose-600 hover:underline"
                    >
                        Remove
                    </button>
                </div>
            </div>

            <p v-else class="mt-5 text-sm text-slate-500">
                Nothing scheduled for this site.
            </p>

            <button
                @click="saveJobs"
                class="mt-6 rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
            >
                Save the schedule
            </button>

            <p class="mt-3 text-xs text-slate-500">
                Five fields — minute, hour, day of month, month, day of week.
                The whole list is written at once, so removing a row here
                removes the line on the machine.
            </p>
        </div>

        <div v-if="!busy && tab === 'logs'">
                <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                <div class="border-b border-slate-200 px-5 py-4">
                    <h3 class="font-semibold text-slate-800">Activity log</h3>
                </div>
                <ul class="divide-y divide-slate-100">
                    <li v-for="log in logs" :key="log.id">
                        <!-- The output itself is on the log's own page now, so this
                             stays a list of what happened rather than a place a
                             thousand lines of npm can unfold in. -->
                        <Link
                            :href="route('logs.show', log.id)"
                            class="flex items-start justify-between gap-3 px-5 py-3 transition hover:bg-slate-50"
                        >
                            <div class="min-w-0">
                                <p class="text-sm font-medium text-slate-800">
                                    {{ log.action }}
                                </p>
                                <p class="text-xs text-slate-500">
                                    {{ log.message }}
                                </p>
                            </div>
                            <div class="shrink-0 text-right">
                                <StatusBadge :status="log.status" />
                                <p class="mt-1 text-xs text-slate-400">
                                    {{ formatRelative(log.created_at) }}
                                </p>
                            </div>
                        </Link>
                    </li>
                    <li
                        v-if="!logs.length"
                        class="px-5 py-8 text-center text-sm text-slate-500"
                    >
                        Nothing logged yet.
                    </li>
                </ul>
            </div>
        </div>

    </AuthenticatedLayout>
</template>
