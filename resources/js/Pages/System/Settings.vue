<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import VersionCard from '@/Components/VersionCard.vue';
import { Head, useForm } from '@inertiajs/vue3';

const props = defineProps({
    system: Object,
    update: Object,
    panel: Object,
    defaults: Object,
    phpVersions: Array,
    nodeVersions: Array,
});

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

            <VersionCard :update="update" />

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
        </div>
    </AuthenticatedLayout>
</template>
