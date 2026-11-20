<script setup>
import { onMounted, reactive, ref } from 'vue';
import { PERMISSION } from './edge';
import { ago, card, field } from './ui';

/*
 * 404 protection: a visitor who asks a site for pages that do not exist is
 * challenged or blocked at Cloudflare for a while, on every site domain at
 * once.
 *
 * Built for Cloudflare's free plan. Free rate limiting rules cannot count by
 * response code, so this server counts the 404s from its own nginx log, and
 * the answer is an IP access rule — which every plan has. The panel takes each
 * rule away again when its time is up.
 *
 * The addresses it has blocked are not listed here: they are IP access rules,
 * and the list under "IP access rules" further down the tab shows them, marked
 * as 404 protection's, with Remove.
 */
const status = ref(null);
const loading = ref(true);
const saving = ref(false);
const error = ref('');
const message = ref('');

const form = reactive({ enabled: false, action: 'managed_challenge', threshold: 10, hours: 24 });

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
                    Challenge or block an address at Cloudflare only once it behaves like a scanner: asking for a secret
                    or admin file such as <code>/.env</code>, <code>/.git</code>, a <code>.sql</code> backup or
                    <code>/phpmyadmin</code>, or for many missing pages in a short time. Visitors who meet a broken link
                    or mistype an address are never challenged. The address is sent to every one of your domains on
                    Cloudflare at once, so it cannot move on to the next. Works on the free plan.
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
                    <span class="text-xs font-medium text-slate-700">Or after how many missing pages in 10 minutes (5 or more)</span>
                    <input v-model.number="form.threshold" type="number" min="5" max="100" :class="field" />
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
                A single request for a secret or admin file is enough; ordinary missing pages are counted across all
                sites and need the number above. Missing images, stylesheets, scripts and fonts are not counted, so a
                broken image on a page does not challenge everybody who opens it. Never challenged: search engines'
                crawlers (proved by their reverse DNS), anyone who signed in to this panel in the last 30 days, and this
                server. Each rule is an IP access rule in every zone your sites are in (IPv6 visitors by their /64),
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

            <p v-if="status.settings.enabled" class="mt-4 text-xs text-slate-500">
                {{ status.total ? `${status.total} address${status.total === 1 ? ' is' : 'es are'} challenged or blocked right now.` : 'Nobody is challenged or blocked for 404s right now.' }}
                The addresses are listed under IP access rules below, marked “404 protection”.
            </p>
        </template>

        <p v-else-if="error" class="mt-3 rounded-xl bg-rose-50 px-4 py-2.5 text-sm text-rose-700 ring-1 ring-rose-200">
            {{ error }}
        </p>
    </section>
</template>
