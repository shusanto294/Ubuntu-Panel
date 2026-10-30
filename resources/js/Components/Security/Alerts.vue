<script setup>
import Checkbox from '@/Components/Checkbox.vue';
import InputError from '@/Components/InputError.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import { card, field } from './ui';
import { Link, useForm } from '@inertiajs/vue3';

const props = defineProps({
    alerts: Object,
    smtpReady: Boolean,
});

const form = useForm({
    email: props.alerts.email,
    panel_login: props.alerts.panel_login,
    panel_lockout: props.alerts.panel_lockout,
    ssh_login: props.alerts.ssh_login,
    ssh_ban: props.alerts.ssh_ban,
});

const save = () => form.post(route('security.alerts'), { preserveScroll: true });

const options = [
    { key: 'panel_login', label: 'Panel sign-in from a new address', hint: 'The first time your account signs in from an address it has not used before.' },
    { key: 'panel_lockout', label: 'Panel lockouts', hint: 'An address used up its failed sign-ins and is locked out.' },
    { key: 'ssh_login', label: 'Every successful SSH login', hint: 'Who logged in, from where, and with a password or a key.' },
    { key: 'ssh_ban', label: 'New SSH bans', hint: 'A digest of addresses fail2ban banned. Busy servers ban many a day.' },
];
</script>

<template>
    <div class="grid gap-6 lg:grid-cols-2">
        <div :class="card">
            <h3 class="font-semibold text-slate-900">Email alerts</h3>
            <p class="mt-1 text-sm text-slate-500">Checked every minute and sent through your SMTP server.</p>

            <div v-if="!smtpReady" class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800">
                No SMTP server is connected, so no alert can be sent.
                <Link :href="route('settings')" class="font-medium underline">Connect one in Settings</Link>.
            </div>

            <form class="mt-4 space-y-5" @submit.prevent="save">
                <div>
                    <label class="block text-sm font-medium text-slate-700">Send alerts to</label>
                    <input v-model="form.email" type="email" :class="field" />
                    <InputError class="mt-2" :message="form.errors.email" />
                </div>

                <label v-for="option in options" :key="option.key" class="flex items-start gap-3">
                    <Checkbox v-model:checked="form[option.key]" class="mt-0.5" />
                    <span>
                        <span class="block text-sm font-medium text-slate-700">{{ option.label }}</span>
                        <span class="block text-xs text-slate-500">{{ option.hint }}</span>
                    </span>
                </label>

                <PrimaryButton :disabled="form.processing">Save alerts</PrimaryButton>
            </form>
        </div>
    </div>
</template>
