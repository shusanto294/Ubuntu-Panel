/*
 * Which country an address is in, for the flag beside it.
 *
 * Every address on a page asks at once as it renders, so the asks are
 * gathered for a moment and sent as one request, and each answer is kept for
 * as long as the page is open. The server keeps them for longer; this only
 * saves asking it twice. A code of '' is an address nobody could place.
 */
import { reactive } from 'vue';

const known = reactive({});
const asked = new Set();
const waiting = new Set();
let timer = null;

const send = async () => {
    timer = null;

    const batch = [...waiting];
    waiting.clear();

    // A hundred at a time, which is what the server looks up in one go.
    for (let start = 0; start < batch.length; start += 100) {
        const ips = batch.slice(start, start + 100);

        try {
            const { data } = await window.axios.get(route('security.countries'), {
                params: { ips: ips.join(',') },
            });

            for (const ip of ips) known[ip] = data.countries?.[ip] ?? '';
        } catch {
            for (const ip of ips) known[ip] = '';
        }
    }
};

/** Asks about an address, once. The answer arrives in `countryOf`. */
export const lookUp = (ip) => {
    if (!ip || asked.has(ip)) return;

    asked.add(ip);
    waiting.add(ip);
    timer ??= setTimeout(send, 50);
};

/** The two-letter code for an address, '' if unknown, undefined until asked. */
export const countryOf = (ip) => known[ip];

/** 🇩🇪 from "DE": two regional-indicator letters make a flag. */
export const flagOf = (code) =>
    String.fromCodePoint(...[...code.toUpperCase()].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));

let names = null;

/** "Germany" from "DE", in the viewer's language. */
export const countryName = (code) => {
    try {
        names ??= new Intl.DisplayNames(undefined, { type: 'region' });

        return names.of(code) ?? code;
    } catch {
        return code;
    }
};
