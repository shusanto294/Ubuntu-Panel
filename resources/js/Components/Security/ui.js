/*
 * What the Security page's tabs share: the classes a card and a field are
 * made of, and the two ways the server writes a time — Unix seconds for the
 * panel's own rows, RFC 3339 for lines read out of sshd's log.
 */
export const card = 'rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5';

export const field =
    'mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500';

export const smallButton =
    'rounded-lg px-2.5 py-1 text-xs font-medium ring-1 ring-slate-900/10 transition hover:bg-slate-50';

const toDate = (value) => {
    if (value === null || value === undefined || value === '') return null;

    const date = typeof value === 'number' ? new Date(value * 1000) : new Date(value);

    return Number.isNaN(date.getTime()) ? null : date;
};

/** "10 Aug, 17:04" in the viewer's zone. */
export const when = (value) => {
    const date = toDate(value);

    return date
        ? date.toLocaleString(undefined, {
              day: 'numeric',
              month: 'short',
              hour: '2-digit',
              minute: '2-digit',
          })
        : '—';
};

/** "3 minutes ago". */
export const ago = (value) => {
    const date = toDate(value);
    if (!date) return '—';

    const seconds = Math.round((Date.now() - date.getTime()) / 1000);

    if (seconds < 0) {
        const ahead = -seconds;
        if (ahead < 3600) return `in ${Math.max(1, Math.round(ahead / 60))} min`;
        return `in ${Math.round(ahead / 3600)} h`;
    }
    if (seconds < 60) return 'just now';
    if (seconds < 3600) return `${Math.round(seconds / 60)} min ago`;
    if (seconds < 86400) return `${Math.round(seconds / 3600)} h ago`;

    return `${Math.round(seconds / 86400)} d ago`;
};

/** A browser's user-agent, cut down to what a person recognises. */
export const browser = (agent = '') => {
    if (!agent) return 'Unknown browser';

    const name =
        (/Edg\//.test(agent) && 'Edge') ||
        (/Firefox\//.test(agent) && 'Firefox') ||
        (/Chrome\//.test(agent) && 'Chrome') ||
        (/Safari\//.test(agent) && 'Safari') ||
        (/curl|wget|python|go-http/i.test(agent) && 'Script') ||
        'Browser';

    const os =
        (/Windows/.test(agent) && 'Windows') ||
        (/iPhone|iPad/.test(agent) && 'iOS') ||
        (/Mac OS X/.test(agent) && 'macOS') ||
        (/Android/.test(agent) && 'Android') ||
        (/Linux/.test(agent) && 'Linux') ||
        '';

    return os ? `${name} on ${os}` : name;
};
