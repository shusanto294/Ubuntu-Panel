<script setup>
import { computed } from 'vue';
import IpAddress from '@/Components/IpAddress.vue';
import Pagination from '@/Components/Pagination.vue';
import { usePagination } from '@/Composables/usePagination';
import { confirmAction, confirmDanger } from '@/dialog';
import { formatRelative, formatStamp } from '@/time';
import { Link, router } from '@inertiajs/vue3';

/*
 * Everything the server has on one address: what it asked the sites for,
 * what the server logged about it, whether anything has banned it, whether it
 * is trusted or a useful bot — and the buttons to ban or unban it.
 */
const props = defineProps({
    report: { type: Object, required: true },
    // The page's prop that carries this report, reloaded after a ban or an
    // unban so the buttons follow — the page's own, every other prop kept.
    reload: { type: Array, default: () => [] },
});

const visit = () => ({ preserveScroll: true, preserveState: true, ...(props.reload.length ? { only: props.reload } : {}) });

const r = computed(() => props.report);

const THREAT = { 'wp-login': 'WordPress login', xmlrpc: 'XML-RPC', login: 'Login form', probe: 'Scanning' };
const BAN = {
    ssh: 'SSH brute-force (SSH port)',
    web_login: 'Website login attack (web ports)',
    web_scan: 'Website scanner (web ports)',
    mail: 'Mail login attack (mail ports)',
    recidive: 'Repeat offender (every port)',
    firewall: 'Firewall block (every port)',
    panel: 'Panel lockout (panel sign-in)',
};
const BOT_KIND = { search: 'search engine', ai: 'AI crawler', preview: 'link preview', monitor: 'uptime monitor' };

const linePages = usePagination(() => r.value.lines ?? [], 20);

const ban = async () => {
    const bot = r.value.good_bot;
    const verified = r.value.bot_check?.verified;

    let title = `Ban ${r.value.ip} from the whole server?`;
    let text = `${r.value.ip} will be refused on every port — every site, SSH and the panel — by a firewall rule. It can be unbanned here or on Security → Banned IPs.`;

    if (bot) {
        title = `${r.value.ip} says it is ${bot.name}. Ban it anyway?`;
        text =
            (verified
                ? `It really is ${bot.name}: its address belongs to them (${r.value.bot_check.host}). `
                : `${bot.name} is a ${BOT_KIND[bot.kind] ?? 'useful bot'}; this address could not be confirmed as theirs, so it may be somebody using the name. `) +
            `Banning a real ${BOT_KIND[bot.kind] ?? 'bot'} keeps your sites out of it. ` +
            text;
    }

    const confirmed = bot ? await confirmDanger(title, text, 'Ban anyway') : await confirmAction(title, text, 'Ban');
    if (!confirmed) return;

    router.post(
        route('security.bans.store'),
        { ip: r.value.ip, scope: 'server', comment: 'Banned from its report in the panel' },
        visit(),
    );
};

const unban = async () => {
    if (!(await confirmDanger(`Unban ${r.value.ip}?`, 'It is let go from everything that refuses it.', 'Unban'))) return;
    router.post(route('security.bans.release'), { ip: r.value.ip }, visit());
};

const banEnds = (source) => {
    if (source.until) return `until ${formatStamp(source.until)}`;
    if (source.at && source.kind !== 'panel') return 'permanently';
    return '';
};

const statusClass = (status) =>
    status >= 500 ? 'text-rose-600' : status >= 400 ? 'text-amber-600' : status >= 300 ? 'text-slate-500' : 'text-emerald-700';
</script>

<template>
    <div class="space-y-4">
        <div class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
            <div class="flex flex-wrap items-start justify-between gap-4">
                <div class="min-w-0 space-y-2">
                    <p class="text-lg font-semibold text-slate-900"><IpAddress :ip="r.ip" /></p>

                    <div class="flex flex-wrap gap-2 text-xs">
                        <span v-if="r.trusted" class="rounded-full bg-emerald-50 px-2.5 py-1 font-medium text-emerald-700">
                            Trusted — {{ r.trusted }}. Never flagged or banned.
                        </span>
                        <span
                            v-if="r.good_bot"
                            class="rounded-full px-2.5 py-1 font-medium"
                            :class="r.bot_check?.verified || !r.bot_check ? 'bg-sky-50 text-sky-700' : 'bg-amber-50 text-amber-800'"
                        >
                            {{ r.good_bot.name }} · {{ BOT_KIND[r.good_bot.kind] ?? 'useful bot' }}
                            <template v-if="r.bot_check?.verified"> · verified ({{ r.bot_check.host }})</template>
                            <template v-else-if="r.bot_check"> · not confirmed — its address is not {{ r.good_bot.name }}'s</template>
                        </span>
                        <span v-if="!r.bans.length && !r.trusted" class="rounded-full bg-slate-100 px-2.5 py-1 font-medium text-slate-600">
                            Not banned
                        </span>
                    </div>

                    <ul v-if="r.bans.length" class="space-y-1 text-sm">
                        <li v-for="source in r.bans" :key="source.kind + (source.rule ?? '')" class="text-rose-700">
                            Banned: {{ BAN[source.kind] ?? source.kind }}
                            <span class="text-slate-500">
                                <template v-if="source.at"> · {{ formatRelative(source.at) }}</template>
                                <template v-if="banEnds(source)"> · {{ banEnds(source) }}</template>
                            </span>
                        </li>
                    </ul>
                </div>

                <div class="flex flex-wrap gap-2">
                    <button
                        v-if="r.bans.length"
                        type="button"
                        class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                        @click="unban"
                    >
                        Unban
                    </button>
                    <button
                        v-else-if="!r.trusted"
                        type="button"
                        class="rounded-xl bg-rose-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-rose-700"
                        @click="ban"
                    >
                        Ban from the server
                    </button>
                </div>
            </div>

            <dl class="mt-5 grid grid-cols-2 gap-4 border-t border-slate-100 pt-4 text-sm sm:grid-cols-4">
                <div>
                    <dt class="text-xs text-slate-500">Requests found</dt>
                    <dd class="font-semibold text-slate-900">{{ r.requests.toLocaleString() }}</dd>
                </div>
                <div>
                    <dt class="text-xs text-slate-500">First seen</dt>
                    <dd class="text-slate-900">{{ r.first ? formatRelative(r.first) : '—' }}</dd>
                </div>
                <div>
                    <dt class="text-xs text-slate-500">Last seen</dt>
                    <dd class="text-slate-900">{{ r.last ? formatRelative(r.last) : '—' }}</dd>
                </div>
                <div>
                    <dt class="text-xs text-slate-500">Responses</dt>
                    <dd class="break-words text-slate-900">
                        <span v-for="(count, cls) in r.statuses" :key="cls" class="mr-2">{{ cls }} {{ count }}</span>
                        <span v-if="!Object.keys(r.statuses).length">—</span>
                    </dd>
                </div>
            </dl>

            <p v-if="Object.keys(r.threats).length" class="mt-3 text-sm text-amber-700">
                Suspicious:
                <span v-for="(count, kind) in r.threats" :key="kind" class="mr-3">{{ THREAT[kind] ?? kind }} × {{ count }}</span>
            </p>
        </div>

        <div v-if="r.requests" class="grid gap-4 lg:grid-cols-3">
            <div class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <h4 class="text-sm font-semibold text-slate-900">Sites it asked</h4>
                <ul class="mt-2 space-y-1 text-sm">
                    <li v-for="s in r.sites" :key="s.name" class="flex justify-between gap-2">
                        <span class="min-w-0 truncate text-slate-700">{{ s.name }}</span>
                        <span class="shrink-0 text-slate-500">{{ s.count }}</span>
                    </li>
                </ul>
            </div>
            <div class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <h4 class="text-sm font-semibold text-slate-900">What it asked for most</h4>
                <ul class="mt-2 space-y-1 text-xs">
                    <li v-for="p in r.paths.slice(0, 10)" :key="p.name" class="flex justify-between gap-2">
                        <span class="min-w-0 truncate font-mono text-slate-700" :title="p.name">{{ p.name }}</span>
                        <span class="shrink-0 text-slate-500">{{ p.count }}</span>
                    </li>
                </ul>
            </div>
            <div class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <h4 class="text-sm font-semibold text-slate-900">What it said it was</h4>
                <ul class="mt-2 space-y-2 text-xs">
                    <li v-for="a in r.agents" :key="a.name" class="break-all font-mono text-slate-700">
                        {{ a.name || '(no user agent)' }} <span class="text-slate-400">× {{ a.count }}</span>
                    </li>
                </ul>
            </div>
        </div>

        <div class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
            <div class="px-5 pt-5">
                <h4 class="font-semibold text-slate-900">Its requests, newest first</h4>
                <p class="text-xs text-slate-500">From every site's access log and yesterday's rotated copy.</p>
            </div>
            <p v-if="!r.lines.length" class="px-5 py-8 text-center text-sm text-slate-500">
                No requests from this address in the current access logs.
            </p>
            <div v-else class="mt-3 overflow-x-auto">
                <table class="min-w-full divide-y divide-slate-200 text-xs">
                    <thead class="bg-slate-50 text-left font-semibold uppercase tracking-wider text-slate-500">
                        <tr>
                            <th class="px-4 py-2">When</th>
                            <th class="px-4 py-2">Site</th>
                            <th class="px-4 py-2">Request</th>
                            <th class="px-4 py-2 text-right">Status</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="(line, i) in linePages.visible.value" :key="i" class="align-top">
                            <td class="whitespace-nowrap px-4 py-2 text-slate-500" :title="formatStamp(line.at)">{{ formatRelative(line.at) }}</td>
                            <td class="whitespace-nowrap px-4 py-2 text-slate-600">{{ line.site }}</td>
                            <td class="min-w-[14rem] max-w-xl px-4 py-2">
                                <span class="break-all font-mono text-slate-900">{{ line.method }} {{ line.path }}</span>
                                <span v-if="line.threat" class="ml-2 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-800">
                                    {{ THREAT[line.threat] ?? line.threat }}
                                </span>
                            </td>
                            <td class="px-4 py-2 text-right font-medium" :class="statusClass(line.status)">{{ line.status }}</td>
                        </tr>
                    </tbody>
                </table>
                <Pagination
                    v-model="linePages.page.value"
                    :pages="linePages.pages.value"
                    :total="linePages.total.value"
                    :per-page="linePages.perPage"
                />
            </div>
        </div>

        <div v-if="r.events.length" class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
            <div class="px-5 pt-5">
                <h4 class="font-semibold text-slate-900">What the server and the panel logged about it</h4>
                <p class="text-xs text-slate-500">The last 30 days: bans, sign-ins, SSH, errors.</p>
            </div>
            <ul class="mt-3 divide-y divide-slate-100 text-sm">
                <li v-for="(entry, i) in r.events" :key="i" class="flex gap-3 px-5 py-2.5 sm:gap-4">
                    <span class="w-20 shrink-0 text-xs text-slate-500 sm:w-28">
                        {{ formatRelative(entry.event?.created_at ?? entry.login?.created_at ?? entry.ssh?.created_at ?? entry.task?.created_at) }}
                    </span>
                    <span class="min-w-0 break-all text-xs">
                        <template v-if="entry.event"><span class="font-medium text-slate-700">{{ entry.event.source }}</span> · <span class="font-mono text-slate-600">{{ entry.event.message }}</span></template>
                        <template v-else-if="entry.login">Panel sign-in {{ entry.login.success ? 'succeeded' : 'refused' }} for {{ entry.login.email || 'no email' }}</template>
                        <template v-else-if="entry.ssh">SSH {{ entry.ssh.kind }} as {{ entry.ssh.user || '—' }} ({{ entry.ssh.method }})</template>
                        <template v-else-if="entry.task">{{ entry.task.action }} — {{ entry.task.status }}</template>
                    </span>
                </li>
            </ul>
        </div>

        <p class="text-xs text-slate-500">
            <Link :href="`${route('logs.index')}?range=30d&q=${r.ip}`" class="font-medium text-brand-600 hover:underline">
                Search the server logs for {{ r.ip }} →
            </Link>
        </p>
    </div>
</template>
