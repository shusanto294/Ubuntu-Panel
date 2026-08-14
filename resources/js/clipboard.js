/**
 * Copying text, on a page that is usually not a secure context.
 *
 * navigator.clipboard exists only over HTTPS and on localhost. A panel reached
 * at http://203.0.113.10:8088 — which is every panel until somebody gives it a
 * hostname — has the object missing or its promise rejected, and a Copy button
 * written against it does nothing at all and says nothing about why.
 *
 * So: the real API where it works, and the old selection trick where it does
 * not. Returns whether anything was copied, so the caller can say "Copied" or
 * fall back to asking the person to select the text themselves.
 */
export async function copyText(text) {
    if (!text) return false;

    if (window.isSecureContext && navigator.clipboard?.writeText) {
        try {
            await navigator.clipboard.writeText(text);

            return true;
        } catch {
            // Refused — a permissions policy, or a browser that wants a
            // gesture it did not see. The fallback below needs neither.
        }
    }

    try {
        const area = document.createElement('textarea');

        area.value = text;
        area.setAttribute('readonly', '');

        // Off-screen rather than hidden: a display:none element cannot be
        // selected, and selecting it is the whole mechanism.
        area.style.position = 'fixed';
        area.style.top = '-1000px';
        area.style.opacity = '0';

        document.body.appendChild(area);

        area.select();
        area.setSelectionRange(0, text.length);

        const copied = document.execCommand('copy');

        document.body.removeChild(area);

        return copied;
    } catch {
        return false;
    }
}
