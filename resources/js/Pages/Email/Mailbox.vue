<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import TextInput from '@/Components/TextInput.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { Head, Link, router, useForm } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

// One mailbox, and the three things about it that anybody comes here to change:
// whose it is, how much it may hold, and its password — which is set here, and
// deliberately never shown. The panel keeps a copy so a mailbox can be built
// again without anybody retyping it; printing that copy on a page is a password
// left on a shared screen, so a forgotten one is answered with a new one.
const props = defineProps({
    mailbox: Object,
    domain: Object,
    roundcubeInstalled: Boolean,
    activeTask: { type: Object, default: null },
});

const form = useForm({
    name: props.mailbox.name ?? '',
    quota_mb: props.mailbox.quota_mb,
    password: '',
});

const busy = computed(
    () =>
        isBusyStatus(props.mailbox.status) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['mailbox', 'activeTask']);

const submit = () =>
    form.post(route('email.accounts.update', props.mailbox.id), {
        preserveScroll: true,
        onSuccess: () => form.reset('password'),
    });

const repair = () =>
    router.post(
        route('email.accounts.repair', props.mailbox.id),
        {},
        { preserveScroll: true },
    );

const destroy = async () => {
    const go = await confirmDanger(
        `Delete ${props.mailbox.address}?`,
        'The mailbox and every message stored in it are removed.',
    );

    if (!go) return;

    router.delete(route('email.accounts.destroy', props.mailbox.id));
};
</script>

<template>
    <Head :title="mailbox.address" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div class="min-w-0">
                    <Link
                        :href="route('email.domains.show', mailbox.domain_id)"
                        class="text-xs text-slate-500 hover:text-brand-600 hover:underline"
                        >&larr; {{ mailbox.domain }}</Link
                    >
                    <h2 class="break-all text-xl font-semibold text-slate-900">
                        {{ mailbox.address }}
                    </h2>
                    <p class="text-sm text-slate-500">
                        <span v-if="mailbox.name">{{ mailbox.name }} ·</span>
                        {{ mailbox.quota_label }}
                    </p>
                </div>

                <div class="flex shrink-0 items-center gap-3">
                    <StatusBadge :status="mailbox.status" />
                    <a
                        v-if="roundcubeInstalled"
                        :href="mailbox.webmail_url"
                        target="_blank"
                        rel="noopener"
                        class="text-sm text-brand-600 hover:underline"
                        >Open webmail</a
                    >
                    <button
                        @click="repair"
                        class="text-sm text-brand-600 hover:underline"
                    >
                        Repair
                    </button>
                    <button
                        @click="destroy"
                        class="text-sm text-rose-600 hover:underline"
                    >
                        Delete
                    </button>
                </div>
            </div>
        </template>

        <div v-if="activeTask" class="mb-6">
            <TaskStatus :task="activeTask" />
        </div>

        <div
            v-if="mailbox.last_error"
            class="mb-6 rounded-2xl bg-rose-50 px-5 py-4 text-sm text-rose-700 ring-1 ring-rose-200"
        >
            {{ mailbox.last_error }}
        </div>

        <div class="grid gap-6 lg:grid-cols-2">
            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-900">Mailbox</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Saving sets the mailbox up again with these — a few seconds,
                    and the address keeps receiving mail throughout.
                </p>

                <form @submit.prevent="submit" class="mt-4 space-y-4">
                    <div>
                        <InputLabel for="name" value="Name" />
                        <TextInput
                            id="name"
                            v-model="form.name"
                            type="text"
                            class="mt-1 block w-full"
                            placeholder="Sales"
                        />
                        <InputError class="mt-2" :message="form.errors.name" />
                        <p class="mt-1 text-xs text-slate-500">
                            Who the mailbox belongs to. The webmail signs sent
                            mail with it; a desktop client carries its own.
                        </p>
                    </div>

                    <div>
                        <InputLabel for="quota_mb" value="Storage limit (MB)" />
                        <TextInput
                            id="quota_mb"
                            v-model="form.quota_mb"
                            type="number"
                            min="0"
                            max="1024000"
                            class="mt-1 block w-full"
                        />
                        <InputError class="mt-2" :message="form.errors.quota_mb" />
                        <p class="mt-1 text-xs text-slate-500">
                            0 is unlimited. Mail already stored is never deleted
                            by lowering this; the mailbox simply stops accepting
                            more until it is under the limit again.
                        </p>
                    </div>

                    <div>
                        <InputLabel for="password" value="New password" />
                        <TextInput
                            id="password"
                            v-model="form.password"
                            type="text"
                            autocomplete="off"
                            class="mt-1 block w-full font-mono"
                            placeholder="Leave blank to keep the current one"
                        />
                        <InputError class="mt-2" :message="form.errors.password" />
                        <p class="mt-1 text-xs text-slate-500">
                            At least 10 characters. Whoever uses this address has
                            to be told — every mail client signed in with the old
                            one stops fetching mail.
                        </p>
                    </div>

                    <PrimaryButton :disabled="form.processing">Save</PrimaryButton>
                </form>
            </div>

            <div class="space-y-6">
                <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                    <h3 class="font-semibold text-slate-900">Mail client settings</h3>
                    <dl class="mt-3 space-y-1 text-sm text-slate-600">
                        <div class="flex justify-between gap-3">
                            <dt>Username</dt>
                            <dd class="break-all font-mono text-slate-800">
                                {{ mailbox.address }}
                            </dd>
                        </div>
                        <div class="flex justify-between gap-3">
                            <dt>IMAP</dt>
                            <dd class="font-mono text-slate-800">
                                {{ domain.client_settings.imap.host }}:{{
                                    domain.client_settings.imap.port
                                }}
                                ({{ domain.client_settings.imap.security }})
                            </dd>
                        </div>
                        <div class="flex justify-between gap-3">
                            <dt>SMTP</dt>
                            <dd class="font-mono text-slate-800">
                                {{ domain.client_settings.smtp.host }}:{{
                                    domain.client_settings.smtp.port
                                }}
                                ({{ domain.client_settings.smtp.security }})
                            </dd>
                        </div>
                        <div class="flex justify-between gap-3">
                            <dt>or</dt>
                            <dd class="font-mono text-slate-800">
                                {{ domain.client_settings.smtp_ssl.port }} ({{
                                    domain.client_settings.smtp_ssl.security
                                }})
                            </dd>
                        </div>
                        <div class="flex justify-between gap-3">
                            <dt>Created</dt>
                            <dd class="text-slate-800">{{ mailbox.created_at }}</dd>
                        </div>
                    </dl>
                </div>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
