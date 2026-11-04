<script setup>
import { ref } from 'vue';
import Checkbox from '@/Components/Checkbox.vue';
import InputError from '@/Components/InputError.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import { confirmAction, confirmDanger } from '@/dialog';
import { card, field } from './ui';
import { router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    hardening: Object,
    service: Object,
});

// SSH itself, on or off — either lasts until it is changed here. Off stops
// and masks sshd, so the port closes and stays closed through reboots and
// package upgrades, and disconnects everybody logged in.
const switching = ref(false);

const setService = (data) =>
    router.post(route('security.ssh-service'), data, {
        preserveScroll: true,
        onStart: () => (switching.value = true),
        onFinish: () => (switching.value = false),
    });

const turnOff = async () => {
    if (
        !(await confirmDanger(
            'Turn SSH off?',
            'Everyone connected over SSH or SFTP is disconnected now, and nobody — including site SFTP accounts — can connect until you turn it back on here. ' +
                'It stays off through reboots. If the panel itself ever stops, your server provider\'s web console is the only way in.',
            'Turn SSH off',
        ))
    )
        return;

    setService({ action: 'off' });
};

const turnOn = () => setService({ action: 'on' });

const form = useForm({
    permit_root_login: props.hardening.permit_root_login || 'prohibit-password',
    password_authentication: props.hardening.password_authentication,
    max_auth_tries: props.hardening.max_auth_tries || 3,
});

const save = async () => {
    if (
        !form.password_authentication &&
        props.hardening.password_authentication &&
        !(await confirmAction(
            'Turn SSH passwords off?',
            `Only keys will log in after this. Keys are installed for: ${props.hardening.authorized_keys.join(', ')}. Keep this terminal open and test a new connection first.`,
            'Keys only',
        ))
    )
        return;

    form.post(route('security.hardening'), { preserveScroll: true });
};
</script>

<template>
    <div :class="card" class="mb-6">
        <div class="flex flex-wrap items-start justify-between gap-4">
            <div class="min-w-0">
                <h3 class="flex items-center gap-2 font-semibold text-slate-900">
                    SSH access
                    <span
                        class="rounded-full px-2 py-0.5 text-xs font-medium"
                        :class="service.running ? 'bg-emerald-50 text-emerald-700' : 'bg-rose-50 text-rose-700'"
                    >
                        {{ service.running ? 'on' : 'off' }}
                    </span>
                </h3>
                <p class="mt-1 max-w-2xl text-sm text-slate-500">
                    <template v-if="service.running">
                        sshd is listening on port {{ (hardening.ports ?? []).join(', ') || '22' }}.
                        Turn it off and the port closes entirely — nothing to guess, scan or
                        attack — and everyone connected is disconnected, until you turn it back
                        on here.
                    </template>
                    <template v-else>
                        SSH stays off — through reboots and upgrades — until you turn it on here.
                        Nothing is listening; the port is closed.
                    </template>
                </p>
            </div>

            <button
                v-if="service.running"
                type="button"
                class="rounded-xl bg-rose-600 px-4 py-2 text-sm font-medium text-white hover:bg-rose-700 disabled:opacity-50"
                :disabled="switching"
                @click="turnOff"
            >
                Turn off
            </button>
            <PrimaryButton v-else type="button" :disabled="switching" @click="turnOn">Turn on</PrimaryButton>
        </div>
    </div>

    <div class="grid gap-6 lg:grid-cols-2">
        <div :class="card">
            <h3 class="font-semibold text-slate-900">SSH settings</h3>
            <p class="mt-1 text-sm text-slate-500">
                Written to <span class="font-mono">/etc/ssh/sshd_config.d/00-ubuntu-panel.conf</span>,
                checked with <span class="font-mono">sshd -t</span> before sshd reloads. Open
                sessions stay connected.
            </p>

            <p v-if="!hardening.readable" class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800">
                Could not read sshd's configuration on this machine.
            </p>

            <form class="mt-4 space-y-5" @submit.prevent="save">
                <div>
                    <label class="block text-sm font-medium text-slate-700">Root login</label>
                    <select v-model="form.permit_root_login" :class="field">
                        <option value="no">Never</option>
                        <option value="prohibit-password">Keys only (recommended)</option>
                        <option value="yes">Password or key</option>
                    </select>
                    <InputError class="mt-2" :message="form.errors.permit_root_login" />
                </div>

                <div>
                    <label class="flex items-start gap-3">
                        <Checkbox v-model:checked="form.password_authentication" class="mt-0.5" />
                        <span>
                            <span class="block text-sm font-medium text-slate-700">Allow password logins</span>
                            <span class="block text-xs text-slate-500">
                                Off means keys only for every account — passwords cannot be
                                guessed if they are never asked for.
                            </span>
                        </span>
                    </label>
                    <InputError class="mt-2" :message="form.errors.password_authentication" />
                </div>

                <div>
                    <label class="block text-sm font-medium text-slate-700">Attempts per connection</label>
                    <input v-model.number="form.max_auth_tries" type="number" min="1" max="10" :class="field" />
                    <InputError class="mt-2" :message="form.errors.max_auth_tries" />
                </div>

                <PrimaryButton :disabled="form.processing || !hardening.readable">Apply</PrimaryButton>
            </form>
        </div>

        <div :class="card">
            <h3 class="font-semibold text-slate-900">What sshd is running with</h3>
            <dl class="mt-4 space-y-3 text-sm">
                <div class="flex justify-between">
                    <dt class="text-slate-500">Port</dt>
                    <dd class="font-mono">{{ (hardening.ports ?? []).join(', ') || '—' }}</dd>
                </div>
                <div class="flex justify-between">
                    <dt class="text-slate-500">Root login</dt>
                    <dd class="font-mono">{{ hardening.permit_root_login || '—' }}</dd>
                </div>
                <div class="flex justify-between">
                    <dt class="text-slate-500">Passwords</dt>
                    <dd class="font-mono">{{ !hardening.readable ? '—' : hardening.password_authentication ? 'yes' : 'no' }}</dd>
                </div>
                <div class="flex justify-between">
                    <dt class="text-slate-500">Max attempts per connection</dt>
                    <dd class="font-mono">{{ hardening.max_auth_tries || '—' }}</dd>
                </div>
                <div class="flex justify-between gap-4">
                    <dt class="text-slate-500">Accounts with SSH keys</dt>
                    <dd class="text-right font-mono">{{ hardening.authorized_keys.join(', ') || 'none' }}</dd>
                </div>
            </dl>
        </div>
    </div>
</template>
