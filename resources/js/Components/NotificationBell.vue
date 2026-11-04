<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { formatRelative } from '@/time';
import { Link, router, usePage } from '@inertiajs/vue3';

/*
 * The bell in the top bar: how many notifications are unread, and the newest
 * few. Every page carries the count; while the tab is open it is asked again
 * every minute, so an attack or a full disk shows up without a reload.
 */
const page = usePage();

const summary = ref(page.props.notifications ?? { unread: 0, latest: [] });
const open = ref(false);
const root = ref(null);

watch(
    () => page.props.notifications,
    (fresh) => {
        if (fresh) summary.value = fresh;
    },
);

const unread = computed(() => summary.value?.unread ?? 0);
const latest = computed(() => summary.value?.latest ?? []);

const refresh = async () => {
    if (document.hidden) return;
    try {
        const { data } = await window.axios.get(route('notifications.summary'));
        summary.value = data;
    } catch {
        // A blip; the next minute asks again.
    }
};

let timer = null;

const onClickAway = (event) => {
    if (open.value && root.value && !root.value.contains(event.target)) open.value = false;
};

const onVisible = () => {
    if (!document.hidden) refresh();
};

onMounted(() => {
    timer = setInterval(refresh, 60000);
    document.addEventListener('click', onClickAway);
    document.addEventListener('visibilitychange', onVisible);
});

onBeforeUnmount(() => {
    clearInterval(timer);
    document.removeEventListener('click', onClickAway);
    document.removeEventListener('visibilitychange', onVisible);
});

const markRead = async (ids) => {
    try {
        const { data } = await window.axios.post(
            route('notifications.read'),
            ids ? { ids: ids.join(',') } : { all: '1' },
            { headers: { Accept: 'application/json' } },
        );
        summary.value = data;
    } catch {
        // Left unread; nothing lost.
    }
};

const openOne = async (notification) => {
    open.value = false;
    // Its own page marks it seen, and has what it is about.
    router.visit(route('notifications.show', notification.id));
    if (!notification.read_at) refresh();
};

const DOT = {
    critical: 'bg-rose-500',
    warning: 'bg-amber-500',
    info: 'bg-sky-500',
};
</script>

<template>
    <div ref="root" class="sm:relative">
        <button
            type="button"
            class="relative inline-flex h-10 w-10 items-center justify-center rounded-full bg-white text-slate-600 shadow-sm ring-1 ring-slate-900/5 transition hover:text-slate-900"
            :aria-label="unread ? `${unread} unread notifications` : 'Notifications'"
            @click="open = !open; if (open) refresh()"
        >
            <svg class="h-5 w-5" fill="none" stroke="currentColor" stroke-width="1.8" viewBox="0 0 24 24">
                <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    d="M14.857 17.082a23.848 23.848 0 005.454-1.31A8.967 8.967 0 0118 9.75V9A6 6 0 006 9v.75a8.967 8.967 0 01-2.312 6.022c1.733.64 3.56 1.085 5.455 1.31m5.714 0a24.255 24.255 0 01-5.714 0m5.714 0a3 3 0 11-5.714 0"
                />
            </svg>
            <span
                v-if="unread"
                class="absolute -right-1 -top-1 min-w-[1.25rem] rounded-full bg-rose-600 px-1.5 text-center text-[11px] font-semibold leading-5 text-white"
            >
                {{ unread > 99 ? '99+' : unread }}
            </span>
        </button>

        <div
            v-if="open"
            class="absolute inset-x-4 top-full z-40 mt-1 overflow-hidden sm:inset-x-auto sm:right-0 sm:top-auto sm:mt-2 sm:w-[22rem] sm:max-w-[calc(100vw-2rem)] rounded-2xl bg-white shadow-xl ring-1 ring-slate-900/10"
        >
            <div class="flex items-center justify-between border-b border-slate-100 px-4 py-3">
                <p class="text-sm font-semibold text-slate-900">Notifications</p>
                <button
                    v-if="unread"
                    type="button"
                    class="text-xs font-medium text-brand-600 hover:underline"
                    @click="markRead(null)"
                >
                    Mark all as seen
                </button>
            </div>

            <p v-if="!latest.length" class="px-4 py-8 text-center text-sm text-slate-500">
                Nothing yet. Attacks, new sign-ins, server problems and failed tasks show up here.
            </p>

            <ul v-else class="max-h-[26rem] divide-y divide-slate-100 overflow-y-auto">
                <li v-for="n in latest" :key="n.id">
                    <button
                        type="button"
                        class="flex w-full gap-3 px-4 py-3 text-left transition hover:bg-slate-50"
                        :class="n.read_at ? '' : 'bg-brand-50/40'"
                        @click="openOne(n)"
                    >
                        <span class="mt-1.5 h-2 w-2 shrink-0 rounded-full" :class="DOT[n.level] ?? DOT.info" />
                        <span class="min-w-0 flex-1">
                            <span class="block text-sm text-slate-900" :class="n.read_at ? '' : 'font-semibold'">
                                {{ n.title }}
                            </span>
                            <span class="mt-0.5 line-clamp-2 block whitespace-pre-line text-xs text-slate-500">{{ n.body }}</span>
                            <span class="mt-1 block text-[11px] text-slate-400">{{ formatRelative(n.created_at) }}</span>
                        </span>
                    </button>
                </li>
            </ul>

            <Link
                :href="route('notifications.index')"
                class="block border-t border-slate-100 px-4 py-2.5 text-center text-xs font-medium text-brand-600 hover:bg-slate-50"
                @click="open = false"
            >
                See all notifications
            </Link>
        </div>
    </div>
</template>
