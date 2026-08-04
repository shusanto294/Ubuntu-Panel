import { ref } from 'vue';

/**
 * One websocket for the whole panel, shared by everything that wants to be told
 * when something changes.
 *
 * Replaces a page's worth of timers. Polling meant a full HTTP request — boot
 * the framework, open a session, run the middleware — several times a second
 * just to read three files in /proc, which showed up as a CPU spike every
 * second on an idle machine. Here the daemon is already running, samples once
 * for everyone, and sends only what has actually changed.
 *
 * Every subscriber gets a fallback callback for the case where the socket
 * cannot be reached at all — the terminal daemon stopped, nginx not carrying
 * the proxy yet, a proxy in the middle that eats websockets. The pages then
 * poll as they always did, more slowly, and nothing looks broken.
 */

const listeners = new Map(); // topic -> Set<callback>

let socket = null;
let opening = null;
let attempts = 0;
let reconnectTimer = null;

export const streamState = ref('idle'); // idle | connecting | live | polling

const socketUrl = (path) => {
    const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';

    return `${scheme}://${window.location.host}${path.startsWith('/') ? path : `/${path}`}`;
};

const topics = () => Array.from(listeners.keys());

const sendSubscription = () => {
    if (socket?.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: 'subscribe', topics: topics() }));
    }
};

const open = async () => {
    if (socket || opening) return opening;

    streamState.value = 'connecting';

    opening = (async () => {
        let ticket;
        let path;

        try {
            const { data } = await window.axios.post(route('terminal.ticket'), { mode: 'stream' });
            ticket = data.ticket;
            path = data.url;
        } catch (e) {
            // No ticket means no socket, and no amount of retrying fixes that
            // from here.
            streamState.value = 'polling';
            opening = null;

            return;
        }

        const url = /^wss?:\/\//i.test(path) ? path : socketUrl(path);

        try {
            socket = new WebSocket(`${url}?ticket=${encodeURIComponent(ticket)}&mode=stream`);
        } catch (e) {
            streamState.value = 'polling';
            opening = null;

            return;
        }

        socket.onopen = () => {
            attempts = 0;
            streamState.value = 'live';
            sendSubscription();
        };

        socket.onmessage = (event) => {
            let frame;

            try {
                frame = JSON.parse(event.data);
            } catch (e) {
                return;
            }

            if (frame.type !== 'update') return;

            listeners.get(frame.topic)?.forEach((callback) => callback(frame.data));
        };

        socket.onclose = () => {
            socket = null;
            opening = null;

            if (listeners.size === 0) {
                streamState.value = 'idle';

                return;
            }

            // Backing off rather than hammering: a daemon that is down stays
            // down for a while, and the pages are polling in the meantime.
            attempts += 1;
            streamState.value = 'polling';

            clearTimeout(reconnectTimer);
            reconnectTimer = setTimeout(open, Math.min(30000, 2000 * attempts));
        };

        socket.onerror = () => {
            streamState.value = 'polling';
        };

        opening = null;
    })();

    return opening;
};

/**
 * Listen to a topic. Returns the function that stops listening.
 *
 * @param {string} topic  `metrics`, or `task:<id>`
 * @param {(data: any) => void} onUpdate
 */
export const subscribe = (topic, onUpdate) => {
    if (!listeners.has(topic)) {
        listeners.set(topic, new Set());
    }

    listeners.get(topic).add(onUpdate);

    if (socket?.readyState === WebSocket.OPEN) {
        sendSubscription();
    } else {
        open();
    }

    return () => {
        const set = listeners.get(topic);

        set?.delete(onUpdate);

        if (set && set.size === 0) {
            listeners.delete(topic);
        }

        sendSubscription();

        // Nothing left to watch: let go of the socket rather than holding one
        // open on every tab for the rest of the day.
        if (listeners.size === 0 && socket) {
            socket.close();
            socket = null;
            streamState.value = 'idle';
        }
    };
};

/** Is the socket carrying data right now? */
export const isLive = () => streamState.value === 'live';
