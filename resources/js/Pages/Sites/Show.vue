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
});

const deleteFiles = ref(true);
const showSecrets = ref(false);

// A deploy runs on the queue, so the page has to go and look for the result.
const busy = computed(() =>
    ['pending', 'deploying', 'deleting'].includes(props.site.status),
);

// The step list stays up while the work runs, and while the run that failed is
// still the last thing that happened — that is when the steps are worth
// reading, because one of them has the ✕ against it.
const showProgress = computed(
    () =>
        busy.value ||
        props.activeTask?.status === 'running' ||
        ((props.activeTask ?? props.latestTask)?.status === 'failed' &&
            props.site.status !== 'active'),
);

const live = computed(() => props.site.status === 'active' && !busy.value);

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
                        <span class="h-2.5 w-2.5 rounded-full bg-emerald-500" />
                        {{ site.domain }} is live
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
            v-if="site.last_error"
            class="mb-6 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800"
        >
            <span class="font-medium">Last error:</span> {{ site.last_error }}
        </div>

        <div class="grid gap-6 lg:grid-cols-3">
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

            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6">
                <h3 class="font-semibold text-slate-800">
                    {{ site.database ? 'Database & credentials' : 'DNS' }}
                </h3>

                <div v-if="site.database" class="mt-4 space-y-3 text-sm">
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Engine</dt>
                        <dd class="text-slate-800">
                            {{ site.database.engine_label }}
                        </dd>
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
                    <Link
                        :href="route('databases.index')"
                        class="inline-block pt-1 text-xs text-brand-600 hover:underline"
                        >Manage databases</Link
                    >
                </div>

                <div v-else-if="site.manage_dns" class="mt-4 space-y-3 text-sm">
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

                <p v-else class="mt-4 text-sm text-slate-500">
                    DNS is managed manually. Point {{ site.domain }} at
                    this machine yourself.
                </p>

                <!-- The application's own credentials -->
                <div v-if="site.app" class="mt-6 border-t border-slate-200 pt-4">
                    <p class="text-sm font-medium text-slate-800">
                        {{ site.app.label }}
                    </p>
                    <p class="mt-1 text-sm text-slate-600">
                        <a
                            :href="site.app.url"
                            target="_blank"
                            rel="noopener"
                            class="text-brand-600 hover:underline"
                            >{{ site.app.url }}</a
                        >
                    </p>
                    <p v-if="!site.app.token" class="mt-1 text-sm text-slate-600">
                        user: {{ site.app.user }}
                    </p>
                    <p class="mt-1 break-all text-sm text-slate-600">
                        {{ site.app.token ? 'token' : 'password' }}:
                        <span v-if="showSecrets" class="font-mono">{{
                            site.app.password
                        }}</span>
                        <button
                            v-else
                            @click="showSecrets = true"
                            class="text-brand-600 hover:underline"
                        >
                            reveal
                        </button>
                    </p>
                    <p
                        v-if="site.app.websocket"
                        class="mt-1 break-all text-sm text-slate-600"
                    >
                        WebSocket URL:
                        <span class="font-mono">{{ site.app.websocket }}</span>
                    </p>
                    <p v-if="site.app.websocket" class="mt-2 text-xs text-slate-500">
                        Open the site, sign in with these, and the dashboard
                        connects on its own — nginx does the authenticating and
                        tells the gateway who you are, so there is no token to
                        paste. The WebSocket URL above is only needed if you
                        point some other client at the gateway.
                    </p>
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

        <div class="mt-8 rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
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
    </AuthenticatedLayout>
</template>
