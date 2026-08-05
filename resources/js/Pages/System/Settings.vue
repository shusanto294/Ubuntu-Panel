<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import VersionCard from '@/Components/VersionCard.vue';
import { Head, router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    system: Object,
    update: Object,
    panel: Object,
    defaults: Object,
    phpVersions: Array,
    nodeVersions: Array,
    daemons: { type: Array, default: () => [] },
});

const restarting = ref(null);

const restart = (unit = null) => {
    restarting.value = unit ?? 'all';

    router.post(
        route('system.restart'),
        { unit },
        {
            preserveScroll: true,
            onFinish: () => (restarting.value = null),
        },
    );
};

const domainForm = useForm({
    domain: props.panel.domain ?? '',
    email: '',
});

const defaultsForm = useForm({
    php_version: props.defaults.php_version,
    node_version: props.defaults.node_version,
    mail_hostname: props.defaults.mail_hostname ?? '',
});

const onCustomDomain = computed(() => Boolean(props.panel.domain));

const submitDomain = () => domainForm.post(route('system.domain'), { preserveScroll: true });
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

                <div
                    v-if="!onCustomDomain"
                    class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-xs text-amber-800"
                >
                    You are on the IP address with a self-signed certificate, so
                    browsers warn every time. Point a hostname at
                    <span class="font-mono">{{ panel.public_ip }}</span> with an A
                    record, then set it below — the panel gets a real Let's
                    Encrypt certificate and the warning goes away.
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
                            certificate fails.
                        </p>
                    </div>

                    <div>
                        <InputLabel for="email" value="Let's Encrypt contact (optional)" />
                        <TextInput
                            id="email"
                            v-model="domainForm.email"
                            type="email"
                            class="mt-1 block w-full"
                            placeholder="you@example.com"
                        />
                        <InputError class="mt-2" :message="domainForm.errors.email" />
                        <p class="mt-1 text-xs text-slate-500">
                            Used for expiry warnings if renewal ever stops working.
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

            <!--
                The panel's own workers. Nothing here is serving this request —
                PHP-FPM is, and restarting that would kill the request asking
                for it — so these can be restarted and the answer is the state
                afterwards rather than a promise.
            -->
            <div
                v-if="daemons.length"
                class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5 lg:col-span-2"
            >
                <div class="flex flex-wrap items-start justify-between gap-3">
                    <div>
                        <h3 class="font-semibold text-slate-900">Panel services</h3>
                        <p class="mt-1 text-sm text-slate-500">
                            The background workers the panel runs on this
                            machine. Restarting one is safe — anything queued
                            stays queued.
                        </p>
                    </div>
                    <button
                        type="button"
                        :disabled="restarting !== null"
                        class="rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 shadow-sm ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                        @click="restart()"
                    >
                        {{ restarting === 'all' ? 'Restarting…' : 'Restart all' }}
                    </button>
                </div>

                <ul class="mt-4 divide-y divide-slate-100">
                    <li
                        v-for="daemon in daemons"
                        :key="daemon.unit"
                        class="flex flex-wrap items-center justify-between gap-3 py-3"
                    >
                        <div class="min-w-0">
                            <p class="flex items-center gap-2 text-sm font-medium text-slate-800">
                                <span
                                    class="h-2 w-2 shrink-0 rounded-full"
                                    :class="daemon.active ? 'bg-emerald-500' : 'bg-rose-500'"
                                />
                                {{ daemon.label }}
                                <span
                                    class="text-xs font-normal"
                                    :class="daemon.active ? 'text-slate-500' : 'text-rose-600'"
                                    >{{ daemon.state }}</span
                                >
                            </p>
                            <p class="mt-0.5 text-xs text-slate-500">
                                {{ daemon.what }}
                            </p>
                        </div>
                        <button
                            type="button"
                            :disabled="restarting !== null"
                            class="shrink-0 rounded-xl px-3 py-1.5 text-sm text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                            @click="restart(daemon.unit)"
                        >
                            {{ restarting === daemon.unit ? 'Restarting…' : 'Restart' }}
                        </button>
                    </li>
                </ul>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
