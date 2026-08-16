<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import TextInput from '@/Components/TextInput.vue';
import VersionCard from '@/Components/VersionCard.vue';
import { useLiveRefresh } from '@/Composables/useLiveRefresh';
import { Head, router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    system: Object,
    update: Object,
    panel: Object,
    defaults: Object,
    phpVersions: Array,
    nodeVersions: Array,
    activeTask: Object,
    latestTask: Object,
});

const domainForm = useForm({
    domain: props.panel.domain ?? '',
});

// The port lives on this form rather than on one of its own, even though it is
// rendered in the card above: /system/settings is one endpoint that saves the
// panel's own values together, and a second form posting to it would send the
// other three back as blanks.
const defaultsForm = useForm({
    php_version: props.defaults.php_version,
    node_version: props.defaults.node_version,
    mail_hostname: props.defaults.mail_hostname ?? '',
    panel_port: props.panel.port,
});

const onCustomDomain = computed(() => Boolean(props.panel.domain));

// Not read off the URL any more: a hostname's address is https whether or not
// the certificate behind it is a real one, because 443 answers with the
// self-signed pair until certbot has run. This is the flag for which of the two
// it is.
const onRealCertificate = computed(() => Boolean(props.panel.tls));

// The hostname goes live before the certificate does, so this page has to keep
// itself in step with a task that finishes in two visible stages. `awaiting`
// covers the gap between the form posting and the worker picking the job up:
// there is no task to watch for the first second or so, and without it the
// console would only appear on a manual reload.
const awaiting = ref(false);
const domainRunning = computed(
    () => awaiting.value || props.activeTask?.status === 'running',
);

useLiveRefresh(domainRunning, ['panel', 'activeTask', 'latestTask']);

const submitDomain = () =>
    domainForm.post(route('system.domain'), {
        preserveScroll: true,
        onSuccess: () => {
            awaiting.value = true;
            setTimeout(() => (awaiting.value = false), 15000);
        },
    });
const submitDefaults = () => defaultsForm.patch(route('system.settings'), { preserveScroll: true });
</script>

<template>
    <Head title="Settings" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">Settings</h2>
                <p class="text-sm text-slate-500">{{ system.hostname }}</p>
            </div>
        </template>

        <!--
            Above the columns rather than inside the card that started it.
            Setting a hostname rewrites the vhost and reloads nginx, and while
            that runs the card underneath is a form you should not be filling
            in again — so the progress belongs where every other page puts it,
            full width under the header, rather than wedged between a warning
            and the field it is about.
        -->
        <div v-if="activeTask ?? latestTask" class="mb-6">
            <TaskStatus :task="activeTask ?? latestTask" />
        </div>

        <div class="grid gap-6 lg:grid-cols-2">
            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-900">Panel address</h3>
                <p class="mt-1 text-sm text-slate-500">
                    The panel currently answers on
                    <a
                        :href="panel.url"
                        class="font-mono text-brand-600 hover:underline"
                        >{{ panel.url }}</a
                    >.
                </p>

                <p v-if="onCustomDomain" class="mt-1 text-sm text-slate-500">
                    <span class="font-mono">{{ panel.domain }}:{{ panel.port }}</span>
                    reaches it too. This server's bare address does not, on any
                    port: 80 and 443 belong to the sites it hosts, and
                    <span class="font-mono">{{ panel.public_ip }}:{{ panel.port }}</span>
                    now gets the same "no site is configured" 404, so somebody who
                    scans the IP is never shown this login page.
                </p>

                <p v-else class="mt-1 text-sm text-slate-500">
                    Nothing answers on this server's bare address: ports 80 and 443
                    belong to the sites it hosts, so somebody who types the IP in a
                    browser is not shown this login page.
                </p>

                <div
                    v-if="!onCustomDomain"
                    class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-xs text-amber-800"
                >
                    You are on the IP address over plain HTTP, so anything typed
                    here — passwords included — crosses the network in the clear.
                    Point a hostname at
                    <span class="font-mono">{{ panel.public_ip }}</span> with an A
                    record, then set it below. The panel then answers on the
                    hostname itself, with no port to remember.
                    <template v-if="panel.certbot">
                        A Let's Encrypt certificate is requested in the same run.
                    </template>
                </div>

                <div
                    v-else-if="!onRealCertificate"
                    class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-xs text-amber-800"
                >
                    <span class="font-mono">{{ panel.domain }}</span> is live, but no
                    Let's Encrypt certificate has been issued for it yet — it is
                    served with a self-signed one, which your browser warns about
                    and an assistant connecting over MCP refuses outright. Behind a
                    CDN that terminates TLS, neither notices.
                    <template v-if="!panel.certbot">
                        Certbot is not installed, so none was requested. Install it
                        from Apps, then set the hostname again.
                    </template>
                    <template v-else>
                        Check the log for why, make sure the hostname resolves to
                        <span class="font-mono">{{ panel.public_ip }}</span> and that
                        ports 80 and 443 reach this server, then set the hostname
                        again to retry.
                    </template>
                </div>

                <div
                    v-if="!panel.certbot"
                    class="mt-4 rounded-xl bg-slate-50 px-4 py-3 text-xs text-slate-600"
                >
                    Certbot is not installed, so a hostname gets a self-signed
                    certificate rather than a trusted one. Install Certbot from Apps
                    first if you want HTTPS without a browser warning.
                </div>

                <form @submit.prevent="submitDomain" class="mt-4 space-y-4">
                    <div>
                        <InputLabel for="domain" value="Panel hostname" />
                        <TextInput
                            id="domain"
                            v-model="domainForm.domain"
                            type="text"
                            class="mt-1 block w-full"
                            placeholder="panel.example.com"
                        />
                        <InputError class="mt-2" :message="domainForm.errors.domain" />
                        <p class="mt-1 text-xs text-slate-500">
                            Must already resolve to this server, or issuing the
                            certificate fails. Served on 80 and 443 under this name
                            alone — a CDN in front of it is fine. Expiry warnings go
                            to your account address.
                        </p>
                        <p class="mt-1 text-xs text-slate-500">
                            Setting a hostname closes the IP address as a way in.
                            If the name ever stops resolving, tunnel to the panel
                            instead:
                            <span class="font-mono"
                                >ssh -L 8080:127.0.0.1:{{ panel.bind_port }}
                                root@{{ panel.public_ip }}</span
                            >, then open
                            <span class="font-mono">http://127.0.0.1:8080</span>.
                        </p>
                    </div>

                    <PrimaryButton :disabled="domainForm.processing">
                        {{ onCustomDomain ? 'Change hostname' : 'Use this hostname' }}
                    </PrimaryButton>

                    <p class="text-xs text-slate-500">
                        You will be logged out of this address when it switches —
                        log back in at the new one.
                    </p>
                </form>

                <form
                    @submit.prevent="submitDefaults"
                    class="mt-6 space-y-4 border-t border-slate-200 pt-6"
                >
                    <div>
                        <InputLabel for="panel_port" value="Panel port" />
                        <TextInput
                            id="panel_port"
                            v-model="defaultsForm.panel_port"
                            type="number"
                            min="1"
                            max="65535"
                            class="mt-1 block w-full"
                        />
                        <InputError
                            class="mt-2"
                            :message="defaultsForm.errors.panel_port"
                        />
                        <p class="mt-1 text-xs text-slate-500">
                            Where the panel answers on this server's IP address, and
                            the way in if a hostname stops resolving. Not 80 or
                            443 — those serve your sites. Change it and the panel
                            moves immediately:
                            open the new address before closing this tab, and if
                            your host has a firewall of its own, a security group or
                            a network rule, let the port through there as well.
                        </p>
                    </div>

                    <PrimaryButton :disabled="defaultsForm.processing">
                        Save port
                    </PrimaryButton>
                </form>
            </div>

            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-900">Defaults for new sites</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Applied when you create a site; each site can still override
                    them.
                </p>

                <form @submit.prevent="submitDefaults" class="mt-4 space-y-4">
                    <div>
                        <InputLabel for="php_version" value="PHP version" />
                        <select
                            id="php_version"
                            v-model="defaultsForm.php_version"
                            class="mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        >
                            <option v-for="v in phpVersions" :key="v" :value="v">
                                PHP {{ v }}
                            </option>
                        </select>
                        <InputError class="mt-2" :message="defaultsForm.errors.php_version" />
                    </div>

                    <div>
                        <InputLabel for="node_version" value="Node.js version" />
                        <select
                            id="node_version"
                            v-model="defaultsForm.node_version"
                            class="mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        >
                            <option v-for="v in nodeVersions" :key="v" :value="v">
                                Node {{ v }}
                            </option>
                        </select>
                        <InputError class="mt-2" :message="defaultsForm.errors.node_version" />
                    </div>

                    <div>
                        <InputLabel for="mail_hostname" value="Mail hostname" />
                        <TextInput
                            id="mail_hostname"
                            v-model="defaultsForm.mail_hostname"
                            type="text"
                            class="mt-1 block w-full"
                            placeholder="mail.example.com"
                        />
                        <InputError class="mt-2" :message="defaultsForm.errors.mail_hostname" />
                    </div>

                    <PrimaryButton :disabled="defaultsForm.processing">Save</PrimaryButton>
                </form>
            </div>

            <VersionCard :update="update" />

        </div>
    </AuthenticatedLayout>
</template>
