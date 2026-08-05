<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
import '@xterm/xterm/css/xterm.css';

const props = defineProps({
    host: { type: String, default: '' },
    // The tab is hidden until clicked; fit() only works on a visible element.
    active: { type: Boolean, default: true },
});

const screen = ref(null);
const state = ref('idle'); // idle | connecting | connected | closed | failed
const message = ref('');

// The daemon names the box it opened the shell on; until then say what we know.
const title = computed(() =>
    state.value === 'connected' && message.value
        ? message.value
        : props.host || 'terminal',
);

let term = null;
let fit = null;
let socket = null;
let observer = null;

const theme = {
    background: '#020617',
    foreground: '#e2e8f0',
    cursor: '#fb923c',
    cursorAccent: '#020617',
    selectionBackground: '#334155',
    black: '#0f172a',
    red: '#f87171',
    green: '#4ade80',
    yellow: '#fbbf24',
    blue: '#60a5fa',
    magenta: '#c084fc',
    cyan: '#22d3ee',
    white: '#e2e8f0',
    brightBlack: '#475569',
    brightRed: '#fca5a5',
    brightGreen: '#86efac',
    brightYellow: '#fcd34d',
    brightBlue: '#93c5fd',
    brightMagenta: '#d8b4fe',
    brightCyan: '#67e8f9',
    brightWhite: '#f8fafc',
};

const write = (text) => term?.write(text);

/**
 * The panel hands back a path, not an address.
 *
 * The terminal daemon listens on loopback *on the server*; nginx proxies the
 * panel's own origin through to it. Resolving here keeps the socket on the same
 * host and the same scheme as the page — a ws:// socket on an https:// page is
 * blocked as mixed content, and a hard-coded 127.0.0.1 would point the browser
 * at the machine the browser is running on.
 */
const socketUrl = (target) => {
    if (/^wss?:\/\//i.test(target)) return target;

    const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';
    const path = target.startsWith('/') ? target : `/${target}`;

    return `${scheme}://${window.location.host}${path}`;
};

const notice = (text, colour = '33') => write(`\r\n\x1b[${colour}m${text}\x1b[0m\r\n`);

const fitNow = () => {
    if (!fit || !term || !props.active) return;

    try {
        fit.fit();
    } catch (e) {
        return;
    }

    if (socket?.readyState === WebSocket.OPEN) {
        socket.send(
            JSON.stringify({
                type: 'resize',
                cols: term.cols,
                rows: term.rows,
            }),
        );
    }
};

const connect = async () => {
    if (state.value === 'connecting' || state.value === 'connected') return;

    state.value = 'connecting';
    message.value = '';

    let ticket;
    let url;

    try {
        const response = await window.axios.post(
            route('terminal.ticket'),
        );
        ticket = response.data.ticket;
        url = socketUrl(response.data.url);
    } catch (e) {
        state.value = 'failed';
        message.value = 'Could not get a session ticket.';
        notice('Could not get a session ticket from the panel.', '31');
        return;
    }

    const query = new URLSearchParams({
        ticket,
        cols: String(term?.cols ?? 120),
        rows: String(term?.rows ?? 30),
    });

    try {
        socket = new WebSocket(`${url}?${query.toString()}`);
    } catch (e) {
        state.value = 'failed';
        message.value = 'Terminal server unreachable.';
        return;
    }

    socket.onmessage = (event) => {
        let frame;

        try {
            frame = JSON.parse(event.data);
        } catch (e) {
            return;
        }

        if (frame.type === 'output') {
            write(frame.data);
            return;
        }

        if (frame.type === 'status') {
            if (frame.state === 'connected') {
                state.value = 'connected';
                message.value = frame.message ?? '';
                fitNow();
                term?.focus();
                return;
            }

            state.value = frame.state === 'closed' ? 'closed' : 'failed';
            message.value = frame.message ?? '';
            notice(frame.message ?? 'Session ended.', frame.state === 'closed' ? '33' : '31');
        }
    };

    socket.onerror = () => {
        if (state.value === 'connected') return;
        state.value = 'failed';
        message.value = `Could not reach the terminal server at ${url}.`;
        notice(
            `Could not reach the terminal server at ${url}.\r\n` +
                'On the server, check:  systemctl status ubuntu-panel-terminal\r\n' +
                'and that nginx proxies the terminal:  php artisan panel:sync-nginx',
            '31',
        );
    };

    socket.onclose = () => {
        if (state.value === 'connected') {
            state.value = 'closed';
            notice('Disconnected.', '33');
        }
        socket = null;
    };
};

const disconnect = () => {
    socket?.close();
    socket = null;
    state.value = 'closed';
};

const reconnect = () => {
    socket?.close();
    socket = null;
    term?.reset();
    state.value = 'idle';
    connect();
};

onMounted(() => {
    term = new Terminal({
        theme,
        fontFamily:
            'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
        fontSize: 13,
        lineHeight: 1.3,
        cursorBlink: true,
        scrollback: 10000,
        convertEol: false,
        allowProposedApi: true,
    });

    fit = new FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new WebLinksAddon());
    term.open(screen.value);

    term.onData((data) => {
        if (socket?.readyState === WebSocket.OPEN) {
            socket.send(JSON.stringify({ type: 'input', data }));
        }
    });

    // Resize with the pane, not just the window.
    observer = new ResizeObserver(() => fitNow());
    observer.observe(screen.value);
    window.addEventListener('resize', fitNow);

    fitNow();
    connect();
});

onBeforeUnmount(() => {
    observer?.disconnect();
    window.removeEventListener('resize', fitNow);
    socket?.close();
    term?.dispose();
});

// Becoming visible again needs a re-fit; the pane had no size while hidden.
watch(
    () => props.active,
    (visible) => {
        if (!visible) return;
        setTimeout(() => {
            fitNow();
            term?.focus();
        }, 0);
    },
);
</script>

<template>
    <div class="overflow-hidden rounded-xl border border-slate-800 bg-slate-950">
        <div
            class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-800 px-4 py-2.5"
        >
            <div class="flex items-center gap-2">
                <span class="h-3 w-3 rounded-full bg-rose-500/70" />
                <span class="h-3 w-3 rounded-full bg-amber-500/70" />
                <span class="h-3 w-3 rounded-full bg-emerald-500/70" />
                <span class="ml-2 font-mono text-xs text-slate-400">
                    {{ title }}
                </span>
            </div>

            <div class="flex items-center gap-3 text-xs">
                <span
                    class="flex items-center gap-1.5"
                    :class="{
                        'text-emerald-400': state === 'connected',
                        'text-brand-400': state === 'connecting',
                        'text-slate-400': state === 'idle' || state === 'closed',
                        'text-rose-400': state === 'failed',
                    }"
                >
                    <span
                        class="h-1.5 w-1.5 rounded-full"
                        :class="{
                            'bg-emerald-400': state === 'connected',
                            'animate-pulse bg-brand-400': state === 'connecting',
                            'bg-slate-500': state === 'idle' || state === 'closed',
                            'bg-rose-400': state === 'failed',
                        }"
                    />
                    {{
                        state === 'connected'
                            ? 'live'
                            : state === 'connecting'
                              ? 'connecting…'
                              : state === 'failed'
                                ? 'failed'
                                : 'disconnected'
                    }}
                </span>

                <button
                    v-if="state === 'connected'"
                    @click="disconnect"
                    class="rounded border border-slate-700 px-2 py-0.5 text-slate-300 hover:bg-slate-800"
                >
                    Disconnect
                </button>
                <button
                    v-else
                    @click="reconnect"
                    class="rounded border border-slate-700 px-2 py-0.5 text-slate-300 hover:bg-slate-800"
                >
                    {{ state === 'connecting' ? 'Reconnect' : 'Connect' }}
                </button>
            </div>
        </div>

        <div ref="screen" class="h-[28rem] px-3 py-2" />
    </div>
</template>

<style>
/* xterm sizes itself to its container; keep the scrollbar in the dark theme. */
.xterm .xterm-viewport {
    scrollbar-width: thin;
    scrollbar-color: #334155 transparent;
}
</style>
