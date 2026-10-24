<script setup>
import { computed, ref } from 'vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import { Link, router, useForm } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

/**
 * The Staging tab, which is the same tab on both halves of a pair.
 *
 * On a production site it offers the clone and lists the copies. On a copy it
 * offers the push back, and spends most of its words on what that replaces —
 * because a push is the one button on this page that overwrites a live site,
 * and the thing people get wrong is assuming it only moves what they changed.
 */
const props = defineProps({
    site: { type: Object, required: true },
    staging: { type: Object, required: true },
});

const form = useForm({
    domain: props.staging.suggested_domain ?? '',
});

const create = () =>
    form.post(route('sites.staging.store', props.site.id), { preserveScroll: true });

// What a push moves. Files on, database off: pushing code up is the ordinary
// case, and replacing a live database is the one that needs somebody to have
// decided on purpose.
const push = useForm({
    files: true,
    database: false,
    keep_uploads: true,
});

const pushing = ref(false);

const confirmPush = async () => {
    const parts = [];

    if (push.files) parts.push('files');
    if (push.database) parts.push('database');

    if (!parts.length) return;

    const target = props.staging.parent?.domain ?? 'the production site';

    let detail =
        `The ${parts.join(' and ')} on ${target} will be replaced with this ` +
        "site's. A backup of it is taken first, and restoring that backup undoes " +
        'the push.';

    if (push.database) {
        detail +=
            ` Everything written to ${target} since this copy was made — orders, ` +
            'comments, new pages, new accounts — is in its database and will be gone.';
    }

    if (push.files && push.keep_uploads && props.site.type === 'wordpress') {
        detail += ` The media library already on ${target} is kept.`;
    }

    if (!(await confirmDanger(`Push to ${target}?`, detail, 'Push'))) return;

    pushing.value = true;

    push.post(route('sites.staging.push', props.site.id), {
        preserveScroll: true,
        onFinish: () => (pushing.value = false),
    });
};

const remove = async (copy) => {
    const go = await confirmDanger(
        `Delete ${copy.domain}?`,
        'The staging copy, its files, its database and its DNS records go. ' +
            'Nothing happens to the site it was copied from.',
        'Delete',
    );

    if (!go) return;

    router.delete(route('sites.destroy', copy.id), {
        data: { files: true },
        preserveScroll: true,
    });
};

// Whether this site type stores its own address in a database the panel has to
// rewrite — which decides how much of the explanation below is relevant.
const rewrites = computed(() => props.staging.rewrites_database);
</script>

<template>
    <div class="grid gap-6 lg:grid-cols-3">
        <div class="space-y-6 lg:col-span-2">
            <!-- ================================================== a copy -->
            <template v-if="staging.is_staging">
                <div
                    class="rounded-2xl bg-amber-50 p-6 text-sm text-amber-900 ring-1 ring-amber-200/60"
                >
                    <h3 class="font-semibold">This is a staging copy</h3>
                    <p class="mt-1">
                        <template v-if="staging.parent">
                            It was cloned from
                            <Link
                                :href="route('sites.show', staging.parent.id)"
                                class="font-medium underline"
                                >{{ staging.parent.domain }}</Link
                            >, and it is a site of its own: its own files, its own
                            database, its own certificate. Nothing you do here
                            touches the site it came from until you push.
                        </template>
                        <template v-else>
                            The site it was cloned from no longer exists, so there is
                            nothing to push to. This is now an ordinary site.
                        </template>
                    </p>
                </div>

                <div
                    v-if="staging.parent"
                    class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
                >
                    <h3 class="font-semibold text-slate-800">
                        Push to {{ staging.parent.domain }}
                    </h3>
                    <p class="mt-1 text-sm text-slate-500">
                        Copies this site over the production one and rewrites the
                        addresses as it goes, so what arrives there believes it lives
                        there.
                        {{ staging.parent.domain }} is backed up before anything is
                        replaced.
                    </p>

                    <div class="mt-5 space-y-4">
                        <label class="flex items-start gap-3">
                            <input
                                v-model="push.files"
                                type="checkbox"
                                class="mt-0.5 rounded border-slate-300 text-brand-600 focus:ring-brand-500"
                            />
                            <span class="text-sm">
                                <span class="font-medium text-slate-800">The files</span>
                                <span class="block text-slate-500">
                                    Themes, plugins, code — everything under the
                                    site's directory. The copy's configuration file
                                    is rewritten afterwards so it still names
                                    production's own database.
                                </span>
                            </span>
                        </label>

                        <label
                            v-if="staging.has_database"
                            class="flex items-start gap-3"
                        >
                            <input
                                v-model="push.database"
                                type="checkbox"
                                class="mt-0.5 rounded border-slate-300 text-brand-600 focus:ring-brand-500"
                            />
                            <span class="text-sm">
                                <span class="font-medium text-slate-800">
                                    The database
                                </span>
                                <span class="block text-slate-500">
                                    Replaces production's database with this one.
                                    Everything written there since this copy was
                                    made — orders, comments, new posts, new accounts
                                    — goes with it. Leave it off to push code alone.
                                </span>
                            </span>
                        </label>

                        <label
                            v-if="push.files && site.type === 'wordpress'"
                            class="flex items-start gap-3"
                        >
                            <input
                                v-model="push.keep_uploads"
                                type="checkbox"
                                class="mt-0.5 rounded border-slate-300 text-brand-600 focus:ring-brand-500"
                            />
                            <span class="text-sm">
                                <span class="font-medium text-slate-800">
                                    Keep production's media library
                                </span>
                                <span class="block text-slate-500">
                                    Leaves
                                    <span class="font-mono text-xs"
                                        >wp-content/uploads</span
                                    >
                                    on production as it is. Off, it is replaced with
                                    this site's — which deletes anything uploaded to
                                    production since the clone was taken.
                                </span>
                            </span>
                        </label>
                    </div>

                    <div class="mt-6 flex flex-wrap items-center gap-3 border-t border-slate-100 pt-6">
                        <button
                            type="button"
                            :disabled="pushing || (!push.files && !push.database)"
                            class="rounded-xl bg-rose-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-rose-700 disabled:opacity-50"
                            @click="confirmPush"
                        >
                            {{ pushing ? 'Pushing…' : 'Push to production' }}
                        </button>
                        <p class="text-xs text-slate-500">
                            Production is down for as long as the copy takes.
                        </p>
                    </div>
                </div>
            </template>

            <!-- ============================================ a production site -->
            <template v-else>
                <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                    <h3 class="font-semibold text-slate-800">Create a staging copy</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        A complete copy of this site on a hostname of its own — its
                        own files, its own database, its own certificate. Break it,
                        try an upgrade on it, hand it to somebody to look at, and
                        push it back when it is right.
                    </p>

                    <p
                        v-if="!staging.supported"
                        class="mt-4 rounded-xl bg-amber-50 p-4 text-sm text-amber-800 ring-1 ring-amber-200/60"
                    >
                        {{ staging.unsupported_why }}
                    </p>

                    <form v-else @submit.prevent="create" class="mt-5">
                        <label
                            for="staging-domain"
                            class="block text-sm font-medium text-slate-700"
                        >
                            Staging domain
                        </label>
                        <div class="mt-1 flex flex-wrap gap-3">
                            <input
                                id="staging-domain"
                                v-model="form.domain"
                                type="text"
                                autocomplete="off"
                                class="min-w-64 flex-1 rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                                placeholder="staging.example.com"
                            />
                            <button
                                type="submit"
                                :disabled="form.processing || !form.domain"
                                class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                            >
                                {{ form.processing ? 'Cloning…' : 'Create staging site' }}
                            </button>
                        </div>
                        <p v-if="form.errors.domain" class="mt-2 text-sm text-rose-600">
                            {{ form.errors.domain }}
                        </p>
                        <p class="mt-2 text-xs text-slate-500">
                            Any hostname you control. A subdomain of this site is
                            suggested because it is in the same DNS zone, so the
                            panel can publish its record and issue its certificate
                            without you doing anything at the registrar.
                        </p>
                    </form>
                </div>

                <div
                    v-if="staging.copies.length"
                    class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
                >
                    <div class="border-b border-slate-200 px-6 py-4">
                        <h3 class="font-semibold text-slate-800">Staging copies</h3>
                    </div>
                    <ul class="divide-y divide-slate-100">
                        <li
                            v-for="copy in staging.copies"
                            :key="copy.id"
                            class="flex flex-wrap items-center justify-between gap-3 px-6 py-4"
                        >
                            <div class="min-w-0">
                                <p class="flex items-center gap-2 font-medium text-slate-800">
                                    <Link
                                        :href="route('sites.show', copy.id)"
                                        class="hover:underline"
                                        >{{ copy.domain }}</Link
                                    >
                                    <StatusBadge :status="copy.status" />
                                </p>
                                <p class="mt-0.5 text-xs text-slate-500">
                                    Created {{ copy.created_at }}
                                    <span v-if="copy.last_error" class="text-rose-600">
                                        · {{ copy.last_error }}
                                    </span>
                                </p>
                            </div>
                            <div class="flex shrink-0 gap-2">
                                <a
                                    :href="copy.url"
                                    target="_blank"
                                    rel="noopener"
                                    class="rounded-xl px-3 py-2 text-sm text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                                >
                                    Visit
                                </a>
                                <Link
                                    :href="route('sites.show', copy.id)"
                                    class="rounded-xl px-3 py-2 text-sm text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                                >
                                    Open
                                </Link>
                                <button
                                    type="button"
                                    class="rounded-xl px-3 py-2 text-sm text-rose-600 ring-1 ring-rose-200 transition hover:bg-rose-50"
                                    @click="remove(copy)"
                                >
                                    Delete
                                </button>
                            </div>
                        </li>
                    </ul>
                </div>
            </template>
        </div>

        <!-- ===================================== what happens, in a column -->
        <div class="space-y-6">
            <div class="rounded-2xl bg-slate-50 p-5 text-sm text-slate-600 ring-1 ring-slate-900/5">
                <p class="font-medium text-slate-700">The addresses get rewritten</p>
                <template v-if="rewrites">
                    <p class="mt-1">
                        This is the part that breaks a copy made by hand.
                        {{ site.type === 'wordpress' ? 'WordPress' : 'Laravel' }}
                        stores absolute URLs
                        <template v-if="site.type === 'wordpress'">
                            all over its database — in options, in post content, in
                            widgets, and inside serialised PHP where a plain text
                            replacement corrupts them.
                        </template>
                        <template v-else>in its .env and its compiled config.</template>
                        A copy that is not rewritten loads, and then sends the
                        visitor back to the site it was copied from.
                    </p>
                    <p v-if="site.type === 'wordpress'" class="mt-2">
                        So every copy, in both directions, ends with WP-CLI's own
                        search and replace over every table — in plain, encoded and
                        protocol-relative form, and correctly through serialised
                        data — plus
                        <span class="font-mono text-xs">home</span> and
                        <span class="font-mono text-xs">siteurl</span>, the
                        <span class="font-mono text-xs">WP_HOME</span> and
                        <span class="font-mono text-xs">WP_SITEURL</span> constants
                        if the install uses them, and a flush of the permalinks,
                        transients and object cache.
                    </p>
                    <p class="mt-2">
                        The copy's configuration file is pointed at its own database
                        before anything else runs, so a staging site never reads or
                        writes production's.
                    </p>
                </template>
                <p v-else class="mt-1">
                    Nothing in a {{ site.type_label }} site stores its own address
                    where the panel can find it, so there is nothing to rewrite.
                    Anything your own code hardcodes is yours to change. A
                    hostname in a
                    <span class="font-mono text-xs">.env</span> file is swapped
                    where the panel finds one.
                </p>
            </div>

            <div
                v-if="!staging.is_staging"
                class="rounded-2xl bg-slate-50 p-5 text-sm text-slate-600 ring-1 ring-slate-900/5"
            >
                <p class="font-medium text-slate-700">And what is not copied</p>
                <p class="mt-1">
                    The copy gets no aliases of its own — production's hostnames
                    stay on production, because two vhosts claiming one hostname is
                    a site served from the wrong directory. Its cache starts off, so
                    it cannot read production's cached pages. On WordPress it asks
                    search engines not to index it, so it does not compete with the
                    site it came from.
                </p>
            </div>

            <div
                v-if="staging.is_staging && staging.parent"
                class="rounded-2xl bg-slate-50 p-5 text-sm text-slate-600 ring-1 ring-slate-900/5"
            >
                <p class="font-medium text-slate-700">Before you push</p>
                <p class="mt-1">
                    A push is the only thing on this page that changes a live site.
                    {{ staging.parent.domain }} is backed up first and the push does
                    not start if that backup fails — so there is always a way back,
                    on its own Backups tab.
                </p>
                <p class="mt-2">
                    If the push stops halfway, production is left in whatever state
                    the failed step produced and the site is marked failed. Restoring
                    that backup is the undo.
                </p>
            </div>
        </div>
    </div>
</template>
