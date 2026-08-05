<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskConsole from '@/Components/TaskConsole.vue';
import { Head, Link, router } from '@inertiajs/vue3';

const props = defineProps({
    site: Object,
    logs: Array,
    activeTask: Object,
    latestTask: Object,
});

const openLog = ref(null);
const deleteFiles = ref(true);
const showSecrets = ref(false);

// A deploy runs on the queue, so the page has to go and look for the result.
const busy = computed(() =>
    ['pending', 'deploying', 'deleting'].includes(props.site.status),
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

onMounted(() => busy.value && startPolling());
onBeforeUnmount(stopPolling);
watch(busy, (value) => (value ? startPolling() : stopPolling()));

const post = (name, extra = {}) =>
    router.post(route(name, props.site.id), extra, { preserveScroll: true });

const destroy = () => {
    const parts = ['its nginx vhost'];
    if (props.site.manage_dns)
        parts.push(`${props.site.dns_provider ?? 'DNS'} records`);
    if (props.site.database) parts.push('the site database');
    if (props.site.is_proxied) parts.push('the systemd service');

    if (
        confirm(
            `Delete ${props.site.domain}? This removes ${parts.join(', ')}${
                deleteFiles.value ? ' and all site files' : ''
            }.`,
        )
    ) {
        router.delete(route('sites.destroy', props.site.id), {
            data: { delete_files: deleteFiles.value },
        });
    }
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
                        {{ site.type_label }} on
                        · {{ site.document_root }}
                    </p>
                </div>
                <div class="flex flex-wrap items-center gap-2">
                    <StatusBadge :status="site.status" />
                    <a
                        :href="`http${site.ssl ? 's' : ''}://${site.domain}`"
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
            Waiting for the queue to pick this up. If nothing happens, check that
            <code class="rounded bg-brand-100 px-1">php artisan queue:work</code>
            is running.
        </div>

        <div v-if="activeTask || latestTask" class="mb-8">
            <TaskConsole
                :task="activeTask ?? latestTask"
                :site-id="site.id"
                :watch-latest="busy"
                title="Deployment"
            />
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

                <!-- WordPress admin credentials -->
                <div
                    v-if="site.wordpress"
                    class="mt-6 border-t border-slate-200 pt-4"
                >
                    <p class="text-sm font-medium text-slate-800">
                        WordPress admin
                    </p>
                    <p class="mt-1 text-sm text-slate-600">
                        <a
                            :href="site.wordpress.admin_url"
                            target="_blank"
                            rel="noopener"
                            class="text-brand-600 hover:underline"
                            >{{ site.wordpress.admin_url }}</a
                        >
                    </p>
                    <p class="mt-1 text-sm text-slate-600">
                        user: {{ site.wordpress.admin_user }}
                    </p>
                    <p class="mt-1 text-sm text-slate-600">
                        password:
                        <span v-if="showSecrets" class="font-mono">{{
                            site.wordpress.admin_password
                        }}</span>
                        <button
                            v-else
                            @click="showSecrets = true"
                            class="text-brand-600 hover:underline"
                        >
                            reveal
                        </button>
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
                <li v-for="log in logs" :key="log.id" class="px-5 py-3">
                    <div class="flex items-start justify-between gap-3">
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
                                {{ log.created_at }}
                            </p>
                        </div>
                    </div>
                    <button
                        v-if="log.output"
                        @click="openLog = openLog === log.id ? null : log.id"
                        class="mt-2 text-xs text-brand-600 hover:underline"
                    >
                        {{ openLog === log.id ? 'Hide' : 'Show' }} output
                    </button>
                    <pre
                        v-if="openLog === log.id"
                        class="mt-2 max-h-64 overflow-auto rounded-md bg-slate-900 p-3 text-xs text-slate-100"
                        >{{ log.output }}</pre
                    >
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
