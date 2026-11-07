<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import { confirmAction, confirmDanger } from '@/dialog';
import { copyText } from '@/clipboard';
import { formatRelative } from '@/time';
import { Head, Link, router, useForm } from '@inertiajs/vue3';

/*
 * Moving sites and mail domains between two servers running this panel.
 *
 * The new server issues a migration key; this one is given that key and the
 * new server's address. Everything sent between them is encrypted and signed
 * with the key, so the move is private even over plain http://.
 */
const props = defineProps({
    receive: Object,
    peer: Object,
    thisServer: Object,
    sites: { type: Array, default: () => [] },
    mailDomains: { type: Array, default: () => [] },
    migrations: { type: Array, default: () => [] },
});

const card = 'rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5 sm:p-6';
const field = 'mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500';

const hello = computed(() => props.peer?.hello ?? null);
const connected = computed(() => Boolean(props.peer?.address && hello.value));

// ----------------------------------------------------------------- receive

const showKey = ref(false);
const thisAddress = typeof window !== 'undefined' ? window.location.origin : '';

const issueKey = async () => {
    if (
        props.receive.exists &&
        !(await confirmAction('Issue a new key?', 'The current key stops working. A move already running with it will fail.', 'Issue new key'))
    )
        return;
    router.post(route('migrate.key'), {}, { preserveScroll: true, onSuccess: () => (showKey.value = true) });
};

const revokeKey = async () => {
    if (!(await confirmDanger('Revoke the migration key?', 'Nothing can be moved to this server until a new key is issued.', 'Revoke')))
        return;
    router.delete(route('migrate.key.destroy'), { preserveScroll: true });
};

const copied = ref('');
const copy = async (text, what) => {
    await copyText(text);
    copied.value = what;
    setTimeout(() => (copied.value = ''), 2000);
};

// -------------------------------------------------------------------- send

const connectForm = useForm({ address: props.peer?.address ?? '', key: '' });
const connect = () => connectForm.post(route('migrate.connect'), { preserveScroll: true, onSuccess: () => connectForm.reset('key') });

const disconnect = async () => {
    if (!(await confirmAction('Disconnect?', 'The other panel’s key is forgotten here. Moves already finished are not affected.', 'Disconnect')))
        return;
    router.post(route('migrate.disconnect'), {}, { preserveScroll: true });
};

const onPeer = (list, name) => (list ?? []).some((other) => other.toLowerCase() === name.toLowerCase());
const busyHere = (kind, name) =>
    props.migrations.some((m) => m.kind === kind && m.name === name && !['done', 'failed', 'cancelled'].includes(m.status));

const pickedSites = ref([]);
const pickedMail = ref([]);
const removeAfter = ref(false);

onMounted(() => {
    const query = new URLSearchParams(window.location.search);
    const site = Number(query.get('site'));
    const mail = Number(query.get('mail'));
    if (site && props.sites.some((s) => s.id === site)) pickedSites.value = [site];
    if (mail && props.mailDomains.some((d) => d.id === mail)) pickedMail.value = [mail];
});

const pickedCount = computed(() => pickedSites.value.length + pickedMail.value.length);
const starting = ref(false);

const start = async () => {
    const names = [
        ...props.sites.filter((s) => pickedSites.value.includes(s.id)).map((s) => s.domain),
        ...props.mailDomains.filter((d) => pickedMail.value.includes(d.id)).map((d) => d.domain),
    ];

    const text =
        `${names.join(', ')} will be copied to ${props.peer.address} (${hello.value.public_ip}), set up there, and then the DNS records will be pointed at it. ` +
        (removeAfter.value
            ? 'Once each one works on the new server, it is deleted from this one — files, databases and stored mail included.'
            : 'Each one stays on this server too, until you delete it.');

    const ok = removeAfter.value ? await confirmDanger('Start the move?', text, 'Move them') : await confirmAction('Start the move?', text, 'Move them');
    if (!ok) return;

    starting.value = true;
    router.post(
        route('migrate.start'),
        { sites: JSON.stringify(pickedSites.value), mail: JSON.stringify(pickedMail.value), remove_after: removeAfter.value },
        {
            preserveScroll: true,
            onSuccess: () => {
                pickedSites.value = [];
                pickedMail.value = [];
            },
            onFinish: () => (starting.value = false),
        },
    );
};

// ----------------------------------------------------------------- history

const STATUS = {
    queued: { text: 'Queued', style: 'bg-slate-100 text-slate-600' },
    running: { text: 'Moving', style: 'bg-sky-50 text-sky-700' },
    receiving: { text: 'Receiving', style: 'bg-sky-50 text-sky-700' },
    importing: { text: 'Setting up', style: 'bg-sky-50 text-sky-700' },
    imported: { text: 'Waiting for DNS', style: 'bg-amber-50 text-amber-700' },
    finishing: { text: 'Getting certificates', style: 'bg-sky-50 text-sky-700' },
    finished: { text: 'Copying late mail', style: 'bg-sky-50 text-sky-700' },
    catching_up: { text: 'Copying late mail', style: 'bg-sky-50 text-sky-700' },
    done: { text: 'Done', style: 'bg-emerald-50 text-emerald-700' },
    failed: { text: 'Failed', style: 'bg-rose-50 text-rose-700' },
};

const running = computed(() => props.migrations.some((m) => !['done', 'failed', 'cancelled'].includes(m.status)));

let timer = null;
watch(
    running,
    (on) => {
        clearInterval(timer);
        timer = on ? setInterval(() => router.reload({ only: ['migrations', 'sites', 'mailDomains'] }), 5000) : null;
    },
    { immediate: true },
);
onBeforeUnmount(() => clearInterval(timer));

const catchUp = (m) => router.post(route('migrate.catch-up', m.id), {}, { preserveScroll: true });
</script>

<template>
    <Head title="Migrate" />

    <AuthenticatedLayout>
        <template #header>
            <div class="min-w-0">
                <h2 class="text-xl font-semibold text-slate-900">Migrate</h2>
                <p class="text-sm text-slate-500">
                    Move sites and mail to another server running Ubuntu Panel — files, databases, mailboxes and stored
                    mail — and point their DNS at it.
                </p>
            </div>
        </template>

        <div class="space-y-6">
            <div class="grid gap-6 lg:grid-cols-2">
                <!-- Send -->
                <div :class="card">
                    <h3 class="font-semibold text-slate-900">Send to another server</h3>

                    <template v-if="connected">
                        <div class="mt-3 rounded-xl bg-emerald-50 p-4 text-sm text-emerald-900 ring-1 ring-emerald-200">
                            <p class="font-medium">Connected to {{ peer.address }}</p>
                            <p class="mt-1 text-emerald-800">
                                Ubuntu Panel {{ hello.version }} · its address is
                                <span class="font-mono">{{ hello.public_ip }}</span>
                                <template v-if="!hello.mail_configured"> · no mail server yet</template>
                            </p>
                        </div>
                        <button type="button" class="mt-3 text-sm font-medium text-slate-600 hover:underline" @click="disconnect">
                            Disconnect
                        </button>
                    </template>

                    <template v-else>
                        <p class="mt-1 text-sm text-slate-500">
                            On the server you are moving to, open Migrate and press “Issue a key”. Paste its address and
                            the key here.
                        </p>
                        <!--
                            Not a login form, and nothing a browser should fill: a text box
                            followed by a password box is what Chrome takes for one, and it
                            offered the panel's own email and password here. So the key is a
                            text box whose characters are hidden, and neither field is named
                            or typed like a credential.
                        -->
                        <form class="mt-4 space-y-3" autocomplete="off" @submit.prevent="connect">
                            <div>
                                <label class="block text-xs font-medium text-slate-700">The other panel’s address</label>
                                <input
                                    v-model="connectForm.address"
                                    type="text"
                                    name="migration-peer-address"
                                    inputmode="url"
                                    autocomplete="off"
                                    autocapitalize="off"
                                    spellcheck="false"
                                    data-1p-ignore
                                    data-lpignore="true"
                                    data-form-type="other"
                                    placeholder="https://panel.example.com or http://203.0.113.5:8443"
                                    :class="field"
                                    class="font-mono"
                                />
                                <InputError class="mt-1" :message="connectForm.errors.address" />
                            </div>
                            <div>
                                <label class="block text-xs font-medium text-slate-700">Its migration key</label>
                                <input
                                    v-model="connectForm.key"
                                    type="text"
                                    name="migration-peer-key"
                                    autocomplete="off"
                                    autocapitalize="off"
                                    spellcheck="false"
                                    data-1p-ignore
                                    data-lpignore="true"
                                    data-form-type="other"
                                    placeholder="upm1.…"
                                    :class="field"
                                    class="font-mono [-webkit-text-security:disc]"
                                />
                                <InputError class="mt-1" :message="connectForm.errors.key" />
                            </div>
                            <PrimaryButton :disabled="connectForm.processing || !connectForm.address || !connectForm.key">
                                {{ connectForm.processing ? 'Connecting…' : 'Connect' }}
                            </PrimaryButton>
                        </form>
                    </template>
                </div>

                <!-- Receive -->
                <div :class="card">
                    <h3 class="font-semibold text-slate-900">Receive from another server</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        Give the server you are moving from this panel’s address and a migration key. Whoever has the key can
                        move sites onto this server, so keep it private and revoke it when you are done.
                    </p>

                    <div class="mt-4 space-y-3 text-sm">
                        <div>
                            <p class="text-xs font-medium text-slate-700">This panel’s address</p>
                            <div class="mt-1 flex items-center gap-2">
                                <code class="min-w-0 flex-1 truncate rounded-lg bg-slate-50 px-3 py-2 ring-1 ring-slate-200">{{ thisAddress }}</code>
                                <button type="button" class="shrink-0 text-xs font-medium text-brand-600 hover:underline" @click="copy(thisAddress, 'address')">
                                    {{ copied === 'address' ? 'Copied' : 'Copy' }}
                                </button>
                            </div>
                        </div>

                        <div v-if="receive.key">
                            <p class="text-xs font-medium text-slate-700">
                                Migration key
                                <span class="font-normal text-slate-400">· issued {{ formatRelative(Date.parse(receive.created_at) / 1000) }}</span>
                            </p>
                            <div class="mt-1 flex items-center gap-2">
                                <code class="min-w-0 flex-1 truncate rounded-lg bg-slate-50 px-3 py-2 ring-1 ring-slate-200">
                                    {{ showKey ? receive.key : '•'.repeat(32) }}
                                </code>
                                <button type="button" class="shrink-0 text-xs font-medium text-brand-600 hover:underline" @click="showKey = !showKey">
                                    {{ showKey ? 'Hide' : 'Show' }}
                                </button>
                                <button type="button" class="shrink-0 text-xs font-medium text-brand-600 hover:underline" @click="copy(receive.key, 'key')">
                                    {{ copied === 'key' ? 'Copied' : 'Copy' }}
                                </button>
                            </div>
                        </div>
                        <p v-else-if="receive.exists" class="text-slate-500">A key is issued. Only an administrator can see it.</p>

                        <div class="flex flex-wrap gap-2 pt-1">
                            <button
                                type="button"
                                class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white hover:bg-brand-700"
                                @click="issueKey"
                            >
                                {{ receive.exists ? 'Issue a new key' : 'Issue a key' }}
                            </button>
                            <button
                                v-if="receive.exists"
                                type="button"
                                class="rounded-xl px-4 py-2 text-sm font-medium text-rose-600 ring-1 ring-rose-200 hover:bg-rose-50"
                                @click="revokeKey"
                            >
                                Revoke
                            </button>
                        </div>
                    </div>
                </div>
            </div>

            <!-- What to move -->
            <div v-if="connected" :class="card" class="!p-0">
                <div class="border-b border-slate-100 px-4 py-4 sm:px-6">
                    <h3 class="font-semibold text-slate-900">Choose what to move to {{ peer.address }}</h3>
                    <p class="text-sm text-slate-500">
                        Each one is copied and set up there first. Only once it works there are its DNS records pointed at
                        <span class="font-mono">{{ hello.public_ip }}</span> — so visitors and mail switch over with nothing lost.
                    </p>
                </div>

                <div class="grid gap-0 divide-y divide-slate-100 lg:grid-cols-2 lg:divide-x lg:divide-y-0">
                    <div class="px-4 py-4 sm:px-6">
                        <p class="text-xs font-medium uppercase tracking-wide text-slate-400">Sites</p>
                        <p v-if="!sites.length" class="mt-2 text-sm text-slate-500">There are no sites on this server.</p>
                        <ul class="mt-2 space-y-1">
                            <li v-for="site in sites" :key="site.id">
                                <label
                                    class="flex items-start gap-3 rounded-lg px-2 py-2 hover:bg-slate-50"
                                    :class="{ 'opacity-50': onPeer(hello.sites, site.domain) || busyHere('site', site.domain) }"
                                >
                                    <input
                                        v-model="pickedSites"
                                        type="checkbox"
                                        :value="site.id"
                                        class="mt-0.5 rounded border-slate-300 text-brand-600 focus:ring-brand-500"
                                        :disabled="onPeer(hello.sites, site.domain) || busyHere('site', site.domain)"
                                    />
                                    <span class="min-w-0">
                                        <span class="block truncate text-sm font-medium text-slate-900">{{ site.domain }}</span>
                                        <span class="text-xs text-slate-500">
                                            {{ site.type }}
                                            <template v-if="onPeer(hello.sites, site.domain)"> · already on the other server</template>
                                            <template v-else-if="busyHere('site', site.domain)"> · being moved</template>
                                            <template v-else-if="site.dns"> · DNS switched automatically</template>
                                            <template v-else> · DNS not managed here — you change the A record</template>
                                        </span>
                                    </span>
                                </label>
                            </li>
                        </ul>
                    </div>

                    <div class="px-4 py-4 sm:px-6">
                        <p class="text-xs font-medium uppercase tracking-wide text-slate-400">Mail domains</p>
                        <p v-if="!mailDomains.length" class="mt-2 text-sm text-slate-500">There are no mail domains on this server.</p>
                        <p v-else-if="!hello.mail_configured" class="mt-2 text-sm text-amber-700">
                            The other server has no mail server. Install Mail on its Apps page, then connect again.
                        </p>
                        <ul class="mt-2 space-y-1">
                            <li v-for="domain in mailDomains" :key="domain.id">
                                <label
                                    class="flex items-start gap-3 rounded-lg px-2 py-2 hover:bg-slate-50"
                                    :class="{ 'opacity-50': !hello.mail_configured || onPeer(hello.mail_domains, domain.domain) || busyHere('mail', domain.domain) }"
                                >
                                    <input
                                        v-model="pickedMail"
                                        type="checkbox"
                                        :value="domain.id"
                                        class="mt-0.5 rounded border-slate-300 text-brand-600 focus:ring-brand-500"
                                        :disabled="!hello.mail_configured || onPeer(hello.mail_domains, domain.domain) || busyHere('mail', domain.domain)"
                                    />
                                    <span class="min-w-0">
                                        <span class="block truncate text-sm font-medium text-slate-900">{{ domain.domain }}</span>
                                        <span class="text-xs text-slate-500">
                                            {{ domain.mailboxes }} {{ domain.mailboxes === 1 ? 'mailbox' : 'mailboxes' }}, passwords and mail included
                                            <template v-if="onPeer(hello.mail_domains, domain.domain)"> · already on the other server</template>
                                            <template v-else-if="domain.dns"> · DNS switched automatically</template>
                                            <template v-else> · DNS not managed here — you change the records</template>
                                        </span>
                                    </span>
                                </label>
                            </li>
                        </ul>
                    </div>
                </div>

                <div class="flex flex-wrap items-center justify-between gap-4 border-t border-slate-100 px-4 py-4 sm:px-6">
                    <label class="flex items-start gap-3 text-sm text-slate-700">
                        <input v-model="removeAfter" type="checkbox" class="mt-0.5 rounded border-slate-300 text-rose-600 focus:ring-rose-500" />
                        <span>
                            Delete from this server once it works on the new one
                            <span class="block text-xs text-slate-500">
                                Leave it off to keep a copy here until you have checked the new server. For mail, wait a day
                                before deleting: servers that remember the old address keep delivering here for a while, and
                                “Copy new mail again” brings that mail across.
                            </span>
                        </span>
                    </label>
                    <PrimaryButton :disabled="!pickedCount || starting" @click="start">
                        {{ starting ? 'Starting…' : pickedCount ? `Move ${pickedCount} to the new server` : 'Pick what to move' }}
                    </PrimaryButton>
                </div>
            </div>

            <!-- History -->
            <div :class="card" class="!p-0">
                <div class="border-b border-slate-100 px-4 py-4 sm:px-6">
                    <h3 class="font-semibold text-slate-900">Moves</h3>
                    <p class="text-sm text-slate-500">What this server has sent and received. Open the log for every step.</p>
                </div>
                <ul class="divide-y divide-slate-100">
                    <li v-for="m in migrations" :key="m.id" class="flex flex-wrap items-start gap-3 px-4 py-4 sm:px-6">
                        <span class="w-6 shrink-0 pt-0.5 text-lg text-slate-400" :title="m.direction === 'out' ? 'Sent' : 'Received'">
                            {{ m.direction === 'out' ? '→' : '←' }}
                        </span>
                        <div class="min-w-0 flex-1">
                            <p class="flex flex-wrap items-center gap-2 text-sm font-medium text-slate-900">
                                {{ m.name }}
                                <span class="text-xs font-normal text-slate-400">{{ m.kind === 'mail' ? 'mail' : 'site' }}</span>
                                <span class="rounded-full px-2 py-0.5 text-xs font-medium" :class="(STATUS[m.status] ?? STATUS.queued).style">
                                    {{ (STATUS[m.status] ?? { text: m.status }).text }}
                                </span>
                            </p>
                            <p class="text-xs text-slate-500">
                                {{ m.direction === 'out' ? 'to' : 'from' }} {{ m.peer }} · {{ formatRelative(m.updated_at) }}
                            </p>
                            <p v-if="m.message" class="mt-1 break-words text-sm" :class="m.status === 'failed' ? 'text-rose-700' : 'text-slate-600'">
                                {{ m.message }}
                            </p>
                        </div>
                        <div class="flex shrink-0 flex-wrap gap-2">
                            <button
                                v-if="m.direction === 'out' && m.kind === 'mail' && m.status === 'done'"
                                type="button"
                                class="rounded-lg px-3 py-1.5 text-xs font-medium ring-1 ring-slate-900/10 hover:bg-slate-50"
                                title="For mail a DNS cache kept sending to this server"
                                @click="catchUp(m)"
                            >
                                Copy new mail again
                            </button>
                            <Link
                                v-if="m.task_id"
                                :href="route('logs.show', m.task_id)"
                                class="rounded-lg px-3 py-1.5 text-xs font-medium text-brand-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                            >
                                Log
                            </Link>
                        </div>
                    </li>
                    <li v-if="!migrations.length" class="px-6 py-8 text-center text-sm text-slate-400">Nothing has been moved yet.</li>
                </ul>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
