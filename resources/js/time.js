/**
 * Timestamps from the panel.
 *
 * Every stamp the server writes is UTC in `YYYY-MM-DD HH:MM:SS` form, with no
 * zone on the end. A browser handed that string reads it as local time, so a
 * log line written a moment ago shows up hours out — the fix is to say `Z`
 * before parsing rather than to trust the default.
 */
const parse = (stamp) => {
    if (!stamp) return null;

    const date = new Date(stamp.replace(' ', 'T') + 'Z');

    return Number.isNaN(date.getTime()) ? null : date;
};

/** "10 Aug 2026, 17:04" in the viewer's own zone. */
export const formatStamp = (stamp) => {
    const date = parse(stamp);

    if (!date) return '';

    return date.toLocaleString(undefined, {
        day: 'numeric',
        month: 'short',
        year: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
    });
};

/** Just the clock, for rows already grouped under a date. */
export const formatTime = (stamp) => {
    const date = parse(stamp);

    return date
        ? date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
        : '';
};

/** "3 minutes ago". Coarse on purpose: this is a log, not a stopwatch. */
export const formatRelative = (stamp) => {
    const date = parse(stamp);

    if (!date) return '';

    const seconds = Math.round((Date.now() - date.getTime()) / 1000);

    if (seconds < 60) return 'just now';

    const units = [
        ['minute', 60],
        ['hour', 3600],
        ['day', 86400],
        ['week', 604800],
        ['month', 2629800],
        ['year', 31557600],
    ];

    let [name, size] = units[0];

    for (const [unit, unitSeconds] of units) {
        if (seconds < unitSeconds) break;
        name = unit;
        size = unitSeconds;
    }

    const count = Math.floor(seconds / size);

    return `${count} ${name}${count === 1 ? '' : 's'} ago`;
};

/** How long a run took, from its own two stamps. Empty while it is running. */
export const formatDuration = (start, end) => {
    const from = parse(start);
    const to = parse(end);

    if (!from || !to) return '';

    const seconds = Math.max(0, Math.round((to.getTime() - from.getTime()) / 1000));

    if (seconds < 60) return `${seconds}s`;

    const minutes = Math.floor(seconds / 60);

    if (minutes < 60) return `${minutes}m ${seconds % 60}s`;

    return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
};
