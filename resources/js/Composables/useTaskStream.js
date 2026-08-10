import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { router } from '@inertiajs/vue3';
import { streamState, subscribe } from '@/stream';

/**
 * Follow one task without its output.
 *
 * Shared by the status line and the step list: both want to know what is
 * happening and neither wants the console. The socket carries the whole
 * payload when the daemon is reachable; the poll is the fallback, and asks for
 * an offset past the end of the output so the response is a status line rather
 * than a hundred kilobytes of apt nobody is going to render.
 *
 * @param {() => object|null} taskGetter  the task prop, read reactively
 * @param {{reloadOnFinish?: boolean}} options
 */
export function useTaskStream(taskGetter, { reloadOnFinish = true } = {}) {
    const initial = taskGetter();
    const state = ref(initial ? { ...initial, output: undefined } : null);

    let timer = null;
    let unsubscribe = null;

    const running = computed(() => state.value?.status === 'running');

    const stopPolling = () => {
        if (!timer) return;
        clearInterval(timer);
        timer = null;
    };

    const finish = () => {
        stopPolling();
        unsubscribe?.();
        unsubscribe = null;

        if (reloadOnFinish) router.reload({ preserveScroll: true });
    };

    const fetchTask = async () => {
        if (!state.value?.id) return;

        try {
            const { data } = await window.axios.get(route('tasks.show', state.value.id), {
                params: { offset: 1_000_000_000 },
            });

            const wasRunning = running.value;

            state.value = { ...data, output: undefined };

            if (wasRunning && !running.value) finish();
        } catch (e) {
            if (e?.response?.status === 403 || e?.response?.status === 404) stopPolling();
        }
    };

    const startPolling = () => {
        if (timer) return;
        timer = setInterval(fetchTask, 4000);
    };

    const watchOverSocket = (id) => {
        unsubscribe?.();

        unsubscribe = subscribe(`task:${id}`, (data) => {
            const wasRunning = running.value;

            state.value = { ...data, output: undefined };

            if (wasRunning && !running.value) finish();
        });
    };

    const follow = () => {
        if (!state.value?.id || !running.value) return;

        watchOverSocket(state.value.id);
        if (streamState.value !== 'live') startPolling();
    };

    watch(streamState, (value) => {
        if (value === 'live') stopPolling();
        else if (running.value) startPolling();
    });

    watch(
        () => taskGetter()?.id,
        (id) => {
            if (!id || id === state.value?.id) return;

            state.value = { ...taskGetter(), output: undefined };
            follow();
        },
    );

    onMounted(follow);

    onBeforeUnmount(() => {
        stopPolling();
        unsubscribe?.();
    });

    return {
        state,
        running,
        failed: computed(() => state.value?.status === 'failed'),
        progress: computed(() => state.value?.progress ?? 0),
        steps: computed(() => state.value?.steps ?? []),
    };
}
