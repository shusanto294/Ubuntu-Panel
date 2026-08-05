import { onBeforeUnmount, watch } from 'vue';
import { router } from '@inertiajs/vue3';

/**
 * Keep a page current for as long as something on it is still happening.
 *
 * Everything the panel does that touches the machine is queued: creating a
 * site, deleting one, adding a mail domain, creating a mailbox, installing
 * software. The row appears immediately as `pending` and the worker moves it
 * along seconds or minutes later — and nothing told the page. You watched a
 * yellow badge until you thought to press reload, which is the difference
 * between a control panel and a form that submits somewhere.
 *
 * So: while anything is in a transient state, reload the props that carry it.
 * Partial reloads, so it is one query and one small JSON response rather than
 * a page; `preserveScroll` and `preserveState`, so nothing moves under the
 * cursor and open panels stay open.
 *
 * It stops the moment nothing is busy, and pauses while the tab is in the
 * background — a panel left open on a second monitor should not be asking the
 * server for anything.
 *
 * @param {import('vue').ComputedRef<boolean>} isBusy  is anything still working?
 * @param {string[]} only  the props to refetch; empty means the whole page
 * @param {number} interval  milliseconds between reloads
 */
export function useLiveRefresh(isBusy, only = [], interval = 3000) {
    let timer = null;

    const tick = () => {
        // A reload while the previous one is in flight just queues work up
        // behind itself; Inertia cancels the earlier visit, so the guard is
        // about not asking rather than not rendering.
        if (document.hidden) return;

        router.reload({
            only,
            preserveScroll: true,
            preserveState: true,
        });
    };

    const start = () => {
        if (timer) return;
        timer = setInterval(tick, interval);
    };

    const stop = () => {
        if (!timer) return;
        clearInterval(timer);
        timer = null;
    };

    // One catch-up reload on becoming visible again: coming back to the tab
    // should show the finished state immediately, not one interval later.
    const onVisibility = () => {
        if (!document.hidden && isBusy.value) tick();
    };

    document.addEventListener('visibilitychange', onVisibility);

    watch(isBusy, (busy) => (busy ? start() : stop()), { immediate: true });

    onBeforeUnmount(() => {
        stop();
        document.removeEventListener('visibilitychange', onVisibility);
    });

    return { start, stop };
}

/** The states that mean "the queue has not finished with this yet". */
export const BUSY_STATES = [
    'pending',
    'queued',
    'creating',
    'installing',
    'preparing',
    'deploying',
    'deleting',
    'running',
];

export const isBusyStatus = (status) => BUSY_STATES.includes(status);
