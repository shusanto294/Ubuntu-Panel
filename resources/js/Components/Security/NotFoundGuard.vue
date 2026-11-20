<script setup>
import { computed, onMounted, reactive, ref } from 'vue';
import Pagination from '@/Components/Pagination.vue';
import { usePagination } from '@/Composables/usePagination';
import { PERMISSION } from './edge';
import { ago, card, field, smallButton, when } from './ui';
import { confirmDanger } from '@/dialog';

/*
 * 404 protection: a visitor who asks a site for pages that do not exist is
 * challenged or blocked at Cloudflare for a while, on every site domain at
 * once.
 *
 * Built for Cloudflare's free plan. Free rate limiting rules cannot count by
 * response code, so this server counts the 404s from its own nginx log, and
 * the answer is an IP access rule — which every plan has. The panel takes each
 * rule away again when its time is up.
 */
const status = ref(null);
const loading = ref(true);
const saving = ref(false);
const error = ref('');
const message = ref('');
const busy = ref('');

const form = reactive({ enabled: false, action: 'managed_challenge', threshold: 1, hours: 24 });

const blocks = computed(() => status.value?.blocks ?? []);
const pages = usePagination(blocks, 15);

const failure = (e, fallback) => e?.response?.data?.error ?? fallback;

const take = (data) => {
    status.value = data;
    Object.assign(form, data.settings);
};

const load = async () => {
    loading.value = true;
    try {
        const { data } = await window.axios.get(route('security.notfound'));
        take(data);
    } catch (e) {
        error.value = failure(e, 'Could not read the 404 protection settings.');
    } finally {
        loading.value = false;
    }
};

onMounted(load);

const save = async (enabled = form.enabled) => {
    saving.value = true;
    error.value = '';
    message.value = '';
    try {
        const { data } = await window.axios.post(route('security.notfound.save'), {
            enabled: enabled ? '1' : '0',
            action: form.action,
            threshold: String(form.threshold),
            hours: String(form.hours),
        });
        take(data);
        message.value = enabled ? 'Saved. 404 protection is on for every site on this server.' : '404 protection is off.';
    } catch (e) {
        error.value = failure(e, 'Could not save.');
        form.enabled = status.value?.settings?.enabled ?? false;
    } finally {
        saving.value = false;
    }
};

const toggle = () => save(!form.enabled);

const lift = async (block) => {
    const all = block === 'all';
    if (
        all &&
        !(await confirmDanger(
            'Remove every 404 rule?',
            `The ${status.value.total} addresses challenged or blocked for 404s are let through again on every domain now.`,
            'Remove all',
        ))
    )
        return;

    busy.value = all ? 'all' : block.id;
    error.value = '';
    try {
        const { data } = await window.axios.delete(route('security.notfound.lift', all ? 'all' : block.id));
        take(data);
    } catch (e) {
        if (e?.response?.data?.settings) take(e.response.data);
        error.value = failure(e, 'Could not remove that rule.');
    } finally {
        busy.value = '';
    }
};

const actionLabel = (key) => status.value?.actions?.find((a) => a.key === key)?.label ?? key;
const hoursLabel = (h) =>
    h === 0
        ? 'Until I remove it'
        : h === 1
          ? '1 hour'
          : h < 24
            ? `${h} hours`
            : h === 24
              ? '1 day'
              : `${h / 24} days`;
</script>

<template>
    <section :class="card">
        <div class="flex flex-wrap items-start justify-between gap-4">
            <div class="max-w-2xl">
                <h3 class="font-semibold text-slate-900">404 protection</h3>
                <p class="text-sm text-slate-500">
                    Challenge or block a visitor at Cloudflare as soon as they ask the sites on this server for pages
                    that do not exist, which is how scanners hunt for <code>/wp-login.php</code>, <code>/.env</code> and
                    <code>/phpmyadmin</code>. The address is sent to every one of your domains on Cloudflare at once, so
                    it cannot move on to the next. Works on the free plan.
                </p>
            </div>
            <button
                v-if="status"
                type="button"
                role="switch"
                :aria-checked="form.enabled"
                :disabled="saving"
                class="relative inline-flex h-6 w-11 shrink-0 rounded-full transition disabled:opacity-50"
                :class="form.enabled ? 'bg-brand-600' : 'bg-slate-300'"
                @click="toggle"
            >
                <span class="sr-only">404 protection</span>
                <span
                    class="absolute top-0.5 h-5 w-5 rounded-full bg-white shadow transition"
                    :class="form.enabled ? 'left-[22px]' : 'left-0.5'"
                />
            </button>
        </div>

        <p v-if="loading" class="mt-4 text-sm text-slate-500">Reading…</p>

        <template v-else-if="status">
            <form class="mt-4 grid gap-4 sm:grid-cols-3" @submit.prevent="save()">
                <label class="block text-sm">
                    <span class="text-xs font-medium text-slate-700">What the visitor gets</span>
                    <select v-model="form.action" :class="field">
                        <option v-for="a in status.actions" :key="a.key" :value="a.key">
                            {{ a.key === 'managed_challenge' ? 'Managed Challenge (captcha)' : a.label }}
                        </option>
                    </select>
                </label>
                <label class="block text-sm">
                    <span class="text-xs font-medium text-slate-700">After how many 404s in 10 minutes, across all sites</span>
                    <input v-model.number="form.threshold" type="number" min="1" max="100" :class="field" />
                </label>
                <label class="block text-sm">
                    <span class="text-xs font-medium text-slate-700">Blocklisted for how long</span>
                    <select v-model.number="form.hours" :class="field">
                        <option v-for="h in status.durations" :key="h" :value="h">{{ hoursLabel(h) }}</option>
                    </select>
                </label>
                <div class="flex flex-wrap items-center gap-3 sm:col-span-3">
                    <button
                        type="submit"
                        :disabled="saving"
                        class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                    >
                        {{ saving ? 'Saving…' : form.enabled ? 'Save' : 'Save and turn on' }}
                    </button>
                    <span v-if="status.settings.enabled" class="text-xs" :class="status.watching ? 'text-emerald-700' : 'text-amber-700'">
                        {{ status.watching ? 'On — watching for 404s.' : 'On, but not watching yet — it starts with the panel.' }}
                    </span>
                </div>
            </form>

            <p class="mt-3 text-xs text-slate-500">
                Missing images, stylesheets, scripts and fonts are not counted, so a broken image on a page does not
                challenge everybody who opens it. Search engines' crawlers, proved by their reverse DNS, are never
                challenged. Each rule is an IP access rule in every zone your sites are in (IPv6 visitors by their /64),
                removed again when its time is up, or kept until you remove it here. Changing the length applies to
                addresses blocklisted from then on. Cloudflare is told in the background, after the 404 has already been
                answered, so pages load exactly as fast with this on. The Cloudflare token needs {{ PERMISSION.access }}.
            </p>

            <p v-if="message" class="mt-3 rounded-xl bg-emerald-50 px-4 py-2.5 text-sm text-emerald-700 ring-1 ring-emerald-200">
                {{ message }}
            </p>
            <p v-if="error" class="mt-3 rounded-xl bg-rose-50 px-4 py-2.5 text-sm text-rose-700 ring-1 ring-rose-200">
                {{ error }}
            </p>
            <p
                v-if="status.last_error"
                class="mt-3 rounded-xl bg-amber-50 px-4 py-2.5 text-sm text-amber-800 ring-1 ring-amber-200"
            >
                {{ status.last_error }} <span class="text-xs text-amber-700/80">({{ ago(status.error_at) }})</span>
            </p>

            <div v-if="blocks.length" class="mt-5">
                <div class="flex flex-wrap items-center justify-between gap-2">
                    <h4 class="text-sm font-semibold text-slate-800">
                        Challenged or blocked now ({{ status.total }})
                    </h4>
                    <div class="flex items-center gap-2">
                        <button type="button" :class="smallButton" @click="load">Reload</button>
                        <button
                            type="button"
                            :class="smallButton"
                            class="text-rose-700"
                            :disabled="busy === 'all'"
                            @click="lift('all')"
                        >
                            Remove all
                        </button>
                    </div>
                </div>
                <div class="mt-2 overflow-x-auto rounded-xl ring-1 ring-slate-200">
                    <table class="w-full text-sm">
                        <thead class="bg-slate-50 text-left text-xs text-slate-500">
                            <tr>
                                <th class="px-4 py-2 font-medium">Address</th>
                                <th class="px-4 py-2 font-medium">Asked for</th>
                                <th class="hidden px-4 py-2 font-medium sm:table-cell">Then</th>
                                <th class="hidden px-4 py-2 font-medium md:table-cell">Since</th>
                                <th class="px-4 py-2 font-medium">Until</th>
                                <th class="px-4 py-2"><span class="sr-only">Actions</span></th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100">
                            <tr v-for="b in pages.visible.value" :key="b.id">
                                <td class="whitespace-nowrap px-4 py-2 font-mono text-xs text-slate-800">{{ b.ip }}</td>
                                <td class="max-w-xs break-all px-4 py-2 text-xs text-slate-600">
                                    {{ b.host }}<span class="text-slate-400">{{ b.path }}</span>
                                </td>
                                <td class="hidden whitespace-nowrap px-4 py-2 text-xs sm:table-cell">
                                    <span :class="b.mode === 'block' ? 'text-rose-700' : 'text-amber-700'">{{ actionLabel(b.mode) }}</span>
                                    <span class="block text-slate-400">on {{ b.zones }} domain{{ b.zones === 1 ? '' : 's' }}</span>
                                </td>
                                <td class="hidden whitespace-nowrap px-4 py-2 text-xs text-slate-500 md:table-cell">{{ ago(b.created_at) }}</td>
                                <td class="whitespace-nowrap px-4 py-2 text-xs text-slate-500">
                                    {{ b.expires_at ? when(b.expires_at) : 'Until removed' }}
                                </td>
                                <td class="whitespace-nowrap px-4 py-2 text-right">
                                    <button
                                        type="button"
                                        :class="smallButton"
                                        :disabled="busy === b.id"
                                        @click="lift(b)"
                                    >
                                        {{ busy === b.id ? 'Removing…' : 'Remove' }}
                                    </button>
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>
                <Pagination
                    v-model="pages.page.value"
                    :pages="pages.pages.value"
                    :total="pages.total.value"
                    :per-page="pages.perPage"
                />
            </div>
            <p v-else-if="status.settings.enabled" class="mt-4 text-xs text-slate-500">
                Nobody is challenged or blocked for 404s right now.
            </p>
        </template>

        <p v-else-if="error" class="mt-3 rounded-xl bg-rose-50 px-4 py-2.5 text-sm text-rose-700 ring-1 ring-rose-200">
            {{ error }}
        </p>
    </section>
</template>
