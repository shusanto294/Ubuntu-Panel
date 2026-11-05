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
    cpu_high: props.alerts.cpu_high,
    disk_full: props.alerts.disk_full,
    mail_queue: props.alerts.mail_queue,
    cert_expiry: props.alerts.cert_expiry,
    web_ban: props.alerts.web_ban,
    attacks: props.alerts.attacks,
    critical: props.alerts.critical,
    suspicious: props.alerts.suspicious,
    task_failed: props.alerts.task_failed,
});

const save = () => form.post(route('security.alerts'), { preserveScroll: true });

const options = [
    { key: 'panel_login', label: 'Panel sign-in from a new address', hint: 'The first time your account signs in from an address it has not used before.' },
    { key: 'panel_lockout', label: 'Panel lockouts', hint: 'An address used up its failed sign-ins and is locked out.' },
    { key: 'ssh_login', label: 'Every successful SSH login', hint: 'Who logged in, from where, and with a password or a key.' },
    { key: 'ssh_ban', label: 'New SSH bans', hint: 'A digest of addresses fail2ban banned. Busy servers ban many a day.' },
    { key: 'attacks', label: 'Attacks on the sites', hint: 'An address brute-forcing a login, scanning for vulnerable files or flooding a site — what Logs → “What stands out” flags. Checked every 5 minutes; each address at most once a day. Your own addresses are never included.' },
    { key: 'web_ban', label: 'New website and mail bans', hint: 'A digest of addresses the Protection tab’s jails banned.' },
];

const health = [
    { key: 'cpu_high', label: 'CPU stays high', hint: 'Above 90% for five minutes. The email says which programs, which sites and which addresses. At most once an hour.' },
    { key: 'disk_full', label: 'Disk nearly full', hint: 'Above 90%. At most twice a day.' },
    { key: 'mail_queue', label: 'Mail piling up', hint: '100 or more messages waiting to send — usually a stolen mailbox sending spam.' },
    { key: 'cert_expiry', label: 'Certificate not renewing', hint: 'A site’s Let’s Encrypt certificate expires within 14 days.' },
    { key: 'task_failed', label: 'Failed panel tasks', hint: 'A deployment, backup, certificate or install that stopped with an error.' },
    { key: 'suspicious', label: 'Suspicious programs running', hint: 'A cryptocurrency miner, a program running from /tmp or /dev/shm, or a connection to a mining pool — what a hacked server looks like. Checked every five minutes; at most one email in six hours. Nothing is stopped.' },
    { key: 'critical', label: 'Critical errors in the server’s logs', hint: 'An out-of-memory kill, a crash, a fatal error. At most one email every 30 minutes, carrying everything since the last.' },
];
</script>

<template>
    <div class="grid gap-6 lg:grid-cols-2">
        <div :class="card">
            <h3 class="font-semibold text-slate-900">Notifications and email alerts</h3>
            <p class="mt-1 text-sm text-slate-500">
                Checked every minute. Each one you tick shows under the bell in the top bar, and is emailed as
                well when an SMTP server is connected — sent in the background, and retried if the mail server
                does not answer.
            </p>

            <div v-if="!smtpReady" class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800">
                No SMTP server is connected, so these show in the panel only.
                <Link :href="route('settings')" class="font-medium underline">Connect one in Settings</Link> to be
                emailed as well.
            </div>

            <form class="mt-4 space-y-5" @submit.prevent="save">
                <div>
                    <label class="block text-sm font-medium text-slate-700">Email alerts to</label>
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

                <p class="border-t border-slate-100 pt-4 text-xs font-semibold uppercase tracking-wide text-slate-400">
                    The server's health
                </p>

                <label v-for="option in health" :key="option.key" class="flex items-start gap-3">
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
