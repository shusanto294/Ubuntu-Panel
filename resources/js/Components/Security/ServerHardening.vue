<script setup>
import { computed, ref } from 'vue';
import { confirmAction } from '@/dialog';
import { router } from '@inertiajs/vue3';
import { card, smallButton } from './ui';

/*
 * Hardening that costs nothing while the server runs: each switch writes a
 * setting the machine reads once, and site locks only change file owners.
 * Nothing here watches traffic or bans an address.
 */
const props = defineProps({
    server: Object,
});

const s = computed(() => props.server.settings ?? {});

const SWITCHES = [
    {
        key: 'kernel',
        title: 'Network hardening',
        text: 'SYN-flood protection and a deeper connection backlog; ICMP redirects and source-routed packets refused. Forwarding is untouched, so Docker and VPNs keep working. Turning it off puts the previous values back.',
        applied: 'kernel_applied',
    },
    {
        key: 'php_harden',
        title: 'PHP hardening',
        text: 'Turns off the PHP functions that run other programs — exec, system, shell_exec, passthru, proc_open, popen — for every website. A web shell uploaded through a vulnerable plugin then cannot run commands or download malware. WP-CLI, Composer and cron are not affected.',
        confirm: 'Every PHP site loses exec, system, shell_exec, passthru, proc_open and popen. Most sites never use them, but some backup and image-optimising plugins do. PHP-FPM reloads; nothing goes down.',
        applied: 'php_applied',
    },
    {
        key: 'compilers',
        title: 'Compilers for administrators only',
        text: 'gcc, cc, as, ld and clang can only be run by root and the panel. An attacker with a shell as a website often compiles an exploit on the server; this stops that step. Node.js packages that compile native code during a deploy fail while it is on.',
        confirm: 'Only root and the panel will be able to run the compilers. Deploys of Node.js sites whose packages compile native code will fail until you turn this off.',
    },
    {
        key: 'no_ping',
        title: 'Do not answer ping',
        text: 'The server stops answering ping, so sweeps of the internet that ping first skip it. Uptime monitors that use ping will report the server down; use an HTTP check instead.',
        applied: 'ping_applied',
    },
];

const busy = ref('');

const toggle = async (item) => {
    const on = !s.value[item.key];
    if (on && item.confirm && !(await confirmAction(`Turn on ${item.title.toLowerCase()}?`, item.confirm, 'Turn on'))) return;

    busy.value = item.key;
    router.post(
        route('security.server-hardening'),
        { feature: item.key, enabled: on },
        { preserveScroll: true, onFinish: () => (busy.value = '') },
    );
};

// Site locks.
const locks = computed(() => props.server.locks ?? []);
const minutesLeft = (at) => Math.max(1, Math.round((at * 1000 - Date.now()) / 60000));
const lockAction = async (site, action) => {
    if (
        action === 'lock' &&
        !(await confirmAction(
            `Lock ${site.domain}'s files?`,
            'WordPress cannot update itself, its plugins or its themes while the files are locked. Press “Unlock for 30 minutes” before updating.',
            'Lock files',
        ))
    )
        return;

    busy.value = `lock:${site.domain}`;
    router.post(route('security.locks'), { site: site.domain, action }, { preserveScroll: true, onFinish: () => (busy.value = '') });
};

// Cloudflare's own Under Attack mode, for the sites behind it.
const edgeSites = computed(() => (props.server.sites ?? []).filter((x) => x.edge));
const cfOn = (domain) => Boolean(props.server.cloudflare_attack?.[domain]);
const cloudflareAttack = (domain, on) => {
    busy.value = `cf:${domain}`;
    router.post(
        route('security.cloudflare-attack'),
        { site: domain, enabled: on },
        { preserveScroll: true, onFinish: () => (busy.value = '') },
    );
};
</script>

<template>
    <div class="space-y-6">
        <div class="grid gap-6 md:grid-cols-2">
            <div v-for="item in SWITCHES" :key="item.key" :class="card" class="flex flex-col">
                <div class="flex items-start justify-between gap-4">
                    <h3 class="font-semibold text-slate-900">{{ item.title }}</h3>
                    <button
                        type="button"
                        role="switch"
                        :aria-checked="Boolean(s[item.key])"
                        :aria-label="item.title"
                        class="relative inline-flex h-6 w-11 shrink-0 rounded-full transition disabled:opacity-50"
                        :class="s[item.key] ? 'bg-emerald-600' : 'bg-slate-300'"
                        :disabled="busy !== ''"
                        @click="toggle(item)"
                    >
                        <span
                            class="absolute top-0.5 h-5 w-5 rounded-full bg-white shadow transition"
                            :class="s[item.key] ? 'left-[22px]' : 'left-0.5'"
                        />
                    </button>
                </div>
                <p class="mt-2 text-sm text-slate-500">{{ item.text }}</p>
                <p v-if="item.applied && s[item.key] && !server[item.applied]" class="mt-3 text-xs text-amber-700">
                    On, but not applied on the machine right now — switch it off and on again.
                </p>
            </div>
        </div>

        <div :class="card">
            <h3 class="font-semibold text-slate-900">Lock site files</h3>
            <p class="mt-1 text-sm text-slate-500">
                A locked site's code belongs to root and cannot be changed by PHP, so a hacked plugin cannot rewrite
                index.php or drop a web shell beside it. Uploads and caches stay writable. The panel's own deploys,
                restores and PHP changes unlock and relock it for you.
            </p>
            <p v-if="!locks.length" class="mt-4 text-xs text-slate-500">There are no sites yet.</p>
            <ul v-else class="mt-4 divide-y divide-slate-100 rounded-lg ring-1 ring-slate-200">
                <li v-for="site in locks" :key="site.domain" class="flex flex-wrap items-center justify-between gap-2 px-3 py-2.5">
                    <span class="min-w-0">
                        <span class="block truncate text-sm font-medium text-slate-900">{{ site.domain }}</span>
                        <span class="text-xs" :class="site.locked ? 'text-emerald-700' : site.relock_at ? 'text-amber-700' : 'text-slate-500'">
                            <template v-if="site.locked">Locked</template>
                            <template v-else-if="site.relock_at">Unlocked for updates — locks again in {{ minutesLeft(site.relock_at) }} min</template>
                            <template v-else-if="!site.lockable">{{ site.why_not }}</template>
                            <template v-else>Not locked</template>
                        </span>
                    </span>
                    <span v-if="site.lockable" class="flex shrink-0 flex-wrap gap-2">
                        <template v-if="site.locked">
                            <button type="button" :class="smallButton" :disabled="busy !== ''" @click="lockAction(site, 'unlock_temp')">
                                Unlock for 30 minutes
                            </button>
                            <button type="button" :class="smallButton" :disabled="busy !== ''" @click="lockAction(site, 'unlock')">Unlock</button>
                        </template>
                        <button
                            v-else
                            type="button"
                            class="rounded-lg bg-slate-900 px-3 py-2 text-xs font-medium text-white hover:bg-slate-800 disabled:opacity-50 sm:py-1.5"
                            :disabled="busy !== ''"
                            @click="lockAction(site, 'lock')"
                        >
                            {{ busy === `lock:${site.domain}` ? 'Locking…' : site.relock_at ? 'Lock now' : 'Lock files' }}
                        </button>
                    </span>
                </li>
            </ul>
        </div>

        <div v-if="edgeSites.length" :class="card">
            <h3 class="font-semibold text-slate-900">Cloudflare Under Attack mode</h3>
            <p class="mt-1 text-sm text-slate-500">
                For a site behind Cloudflare that is being flooded: Cloudflare checks every visitor's browser before
                the request ever reaches this server, so the flood costs this server nothing.
            </p>
            <ul class="mt-4 divide-y divide-slate-100 rounded-lg ring-1 ring-slate-200">
                <li v-for="site in edgeSites" :key="site.domain" class="flex items-center justify-between gap-2 px-3 py-2.5 text-sm">
                    <span class="truncate">{{ site.domain }}</span>
                    <button
                        type="button"
                        class="shrink-0 rounded-lg px-3 py-2 text-xs font-medium sm:py-1.5"
                        :class="cfOn(site.domain) ? 'bg-rose-600 text-white hover:bg-rose-700' : 'bg-white text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50'"
                        :disabled="busy !== ''"
                        @click="cloudflareAttack(site.domain, !cfOn(site.domain))"
                    >
                        {{ busy === `cf:${site.domain}` ? 'Asking Cloudflare…' : cfOn(site.domain) ? 'On — turn off' : 'Turn on' }}
                    </button>
                </li>
            </ul>
        </div>
    </div>
</template>
