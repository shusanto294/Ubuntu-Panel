<script setup>
import { computed, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Checkbox from '@/Components/Checkbox.vue';
import DangerButton from '@/Components/DangerButton.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, router, useForm } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';
import { copyText } from '@/clipboard';

/**
 * AI access: what an assistant is allowed to do with this server, the keys it
 * signs its requests with, and the one paragraph that connects it.
 *
 * The connection instructions are below the keys and only appear once there is
 * a key to put in them — before that they would be a snippet with a blank where
 * the credential goes, which is a thing to be pasted and then wondered about.
 */
const props = defineProps({
    tokens: { type: Array, default: () => [] },
    capabilities: { type: Array, default: () => [] },
    settings: { type: Object, default: () => ({}) },
    serverName: { type: String, default: 'Ubuntu Panel' },
    serverSlug: { type: String, default: 'ubuntu-panel' },
    defaultName: { type: String, default: 'Ubuntu Panel' },
    endpoint: { type: String, default: '' },
    issued: { type: String, default: null },
});

// ------------------------------------------------------------------ copying

const copied = ref('');
const copyFailed = ref('');

const copy = async (text, which) => {
    const done = await copyText(text);

    copied.value = done ? which : '';
    copyFailed.value = done ? '' : which;

    if (done) setTimeout(() => (copied.value = ''), 2000);
};

// ------------------------------------------------------- server and switches

// The server's name is not here: it is set beside the key it goes into, which
// is the only place it is needed and the only time anybody is thinking about
// it. This form leaves it alone.
const settings = useForm({
    enabled: props.settings.enabled ?? true,
    confirm_destructive: props.settings.confirm_destructive ?? true,
    allow_all: props.settings.allow_all ?? true,
    permissions: props.capabilities.filter((c) => c.granted).map((c) => c.key),
});

// Ticking "everything" is a statement, not a shortcut: the individual list goes
// with it so the two can never disagree on screen.
watch(
    () => settings.allow_all,
    (all) => {
        if (all) settings.permissions = props.capabilities.map((c) => c.key);
    },
);

const toggle = (key) => {
    if (settings.allow_all) settings.allow_all = false;

    settings.permissions = settings.permissions.includes(key)
        ? settings.permissions.filter((granted) => granted !== key)
        : [...settings.permissions, key];
};

const saveSettings = () => settings.post(route('ai.settings'), { preserveScroll: true });

// ---------------------------------------------------------------- the keys

const form = useForm({ name: '', server_name: props.serverName });

const create = () => {
    form.post(route('ai.tokens.store'), {
        preserveScroll: true,
        onSuccess: () => form.reset('name'),
    });
};

const revoke = async (token) => {
    const confirmed = await confirmDanger(
        `Revoke ${token.name}?`,
        'Any assistant still using this key stops being able to reach the panel. ' +
            'Nothing it already created is touched.',
        'Revoke',
    );

    if (!confirmed) return;

    router.delete(route('ai.tokens.destroy', token.id), { preserveScroll: true });
};

// ------------------------------------------------------------- connecting

// The key in the instructions is always the one just issued. A key cannot be
// shown twice — the panel keeps a hash of it — so these instructions exist only
// on the response that created one, and the whole card goes with it.
const key = computed(() => props.issued || '');

const prompt = computed(
    () => `Add an MCP server to my AI setup, or update it if it is already there.

    name:      ${props.serverSlug}
    transport: HTTP (streamable HTTP, not SSE)
    url:       ${props.endpoint}
    header:    Authorization: Bearer ${key.value}

If a server called "${props.serverSlug}" is already configured, replace its URL and its Authorization header with these rather than adding a second one. Use whichever mechanism my client has — the CLI command, the config file, the settings UI, whatever is right for it.

Then reconnect to it, call its server_status tool, and tell me what came back so I know it works. The server introduces itself as "${props.serverName}".`,
);

const manual = computed(
    () => `{
  "mcpServers": {
    "${props.serverSlug}": {
      "type": "http",
      "url": "${props.endpoint}",
      "headers": {
        "Authorization": "Bearer ${key.value}"
      }
    }
  }
}`,
);

const showing = ref('prompt');

const local = computed(() => /^https?:\/\/(127\.0\.0\.1|localhost)/.test(props.endpoint));
</script>

<template>
    <Head title="AI access" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">AI access</h2>
                <p class="text-sm text-slate-500">
                    Connect Claude, Codex or any other MCP client to this panel,
                    and manage the server by asking for what you want.
                </p>
            </div>
        </template>

        <div class="space-y-6">
            <!-- What it is allowed to do. -->
            <form
                class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
                @submit.prevent="saveSettings"
            >
                <div class="border-b border-slate-200 px-5 py-4">
                    <h3 class="font-semibold text-slate-800">MCP server</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        What an assistant may do with this machine, and whether
                        it may reach it at all.
                    </p>
                    <!--
                        The name is set beside the key it goes into, so it is
                        read here rather than typed — and it is read here at all
                        because otherwise nothing on this page says what your
                        assistant calls this server.
                    -->
                    <p class="mt-2 text-sm text-slate-600">
                        Assistants see it as
                        <span class="font-medium text-slate-800">{{ serverName }}</span>
                        <span class="text-slate-400">
                            · <code class="font-mono text-xs">{{ serverSlug }}</code> in a
                            configuration file
                        </span>
                    </p>
                </div>

                <div class="space-y-5 px-5 py-4">
                    <label class="flex items-start gap-3">
                        <Checkbox v-model:checked="settings.enabled" class="mt-1" />
                        <span>
                            <span class="block font-medium text-slate-800">
                                Answer assistants
                            </span>
                            <span class="block text-sm text-slate-500">
                                Off, the endpoint refuses every request and the
                                keys below stay where they are.
                            </span>
                        </span>
                    </label>

                    <label class="flex items-start gap-3">
                        <Checkbox
                            v-model:checked="settings.confirm_destructive"
                            class="mt-1"
                        />
                        <span>
                            <span class="block font-medium text-slate-800">
                                Ask me before anything destructive
                            </span>
                            <span class="block text-sm text-slate-500">
                                Deleting a site, replacing a file, running a
                                command that is not plainly read-only: the panel
                                refuses the first attempt and tells the
                                assistant to put it to you in words and come
                                back with your answer. It cannot make an
                                assistant ask honestly — nothing on this side of
                                the connection can — but it stops the version
                                where one sentence deletes a site.
                            </span>
                        </span>
                    </label>

                    <div class="rounded-xl ring-1 ring-slate-200">
                        <label
                            class="flex items-start gap-3 border-b border-slate-200 px-4 py-3"
                        >
                            <Checkbox v-model:checked="settings.allow_all" class="mt-1" />
                            <span>
                                <span class="block font-medium text-slate-800">
                                    Everything
                                </span>
                                <span class="block text-sm text-slate-500">
                                    Every tool the panel has, including the shell.
                                </span>
                            </span>
                        </label>

                        <div class="divide-y divide-slate-100">
                            <label
                                v-for="capability in capabilities"
                                :key="capability.key"
                                class="flex items-start gap-3 px-4 py-3"
                                :class="settings.allow_all ? 'opacity-60' : ''"
                            >
                                <Checkbox
                                    class="mt-1"
                                    :checked="settings.permissions.includes(capability.key)"
                                    @update:checked="toggle(capability.key)"
                                />
                                <span>
                                    <span class="flex items-center gap-2">
                                        <span class="font-medium text-slate-800">
                                            {{ capability.label }}
                                        </span>
                                        <span
                                            v-if="capability.destructive"
                                            class="rounded-full bg-amber-50 px-2 py-0.5 text-[11px] font-medium text-amber-700 ring-1 ring-amber-600/20"
                                        >
                                            can destroy things
                                        </span>
                                    </span>
                                    <span class="block text-sm text-slate-500">
                                        {{ capability.help }}
                                    </span>
                                </span>
                            </label>
                        </div>
                    </div>
                </div>

                <div class="flex items-center gap-3 border-t border-slate-100 px-5 py-4">
                    <PrimaryButton :disabled="settings.processing">
                        {{ settings.processing ? 'Saving…' : 'Save' }}
                    </PrimaryButton>
                    <span v-if="settings.recentlySuccessful" class="text-sm text-slate-500">
                        Saved.
                    </span>
                </div>
            </form>

            <!-- The key, once. -->
            <div
                v-if="issued"
                class="rounded-2xl bg-emerald-50 p-5 ring-1 ring-emerald-600/20"
            >
                <p class="font-medium text-emerald-900">
                    Your new key — copy it now
                </p>
                <p class="mt-1 text-sm text-emerald-800">
                    The panel stores only a hash of it, so this is the last time
                    it can be shown. Leaving this page loses it; revoke the key
                    and make another if that happens.
                </p>

                <div class="mt-3 flex flex-wrap items-center gap-2">
                    <code
                        class="min-w-0 flex-1 overflow-x-auto rounded-lg bg-white px-3 py-2 font-mono text-sm text-slate-800 ring-1 ring-emerald-600/20"
                    >
                        {{ issued }}
                    </code>
                    <button
                        type="button"
                        class="rounded-lg bg-emerald-600 px-3 py-2 text-sm font-medium text-white transition hover:bg-emerald-700"
                        @click="copy(issued, 'key')"
                    >
                        {{ copied === 'key' ? 'Copied' : 'Copy' }}
                    </button>
                </div>

                <p v-if="copyFailed === 'key'" class="mt-2 text-xs text-emerald-800">
                    This browser would not let the page copy for you — select the
                    key above and copy it yourself.
                </p>
            </div>

            <!-- The keys. -->
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                <div class="border-b border-slate-200 px-5 py-4">
                    <h3 class="font-semibold text-slate-800">Access keys</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        A key carries everything ticked above. Give each
                        assistant its own, so one can be revoked without stopping
                        the rest.
                    </p>
                </div>

                <form
                    class="grid gap-3 border-b border-slate-100 px-5 py-4 sm:grid-cols-2"
                    @submit.prevent="create"
                >
                    <div>
                        <InputLabel for="name" value="What is this key for?" />
                        <TextInput
                            id="name"
                            v-model="form.name"
                            class="mt-1 block w-full"
                            placeholder="Claude on my laptop"
                        />
                    </div>

                    <div>
                        <InputLabel
                            for="key-server-name"
                            value="What this server is called"
                        />
                        <TextInput
                            id="key-server-name"
                            v-model="form.server_name"
                            class="mt-1 block w-full"
                            :placeholder="defaultName"
                        />
                    </div>

                    <p class="text-sm text-slate-500 sm:col-span-2">
                        The first name is yours, for telling your own keys apart.
                        The second is what the assistant calls this panel — worth
                        changing when you have more than one of them connected to
                        the same machine. It applies to every key, not only this
                        one.
                    </p>

                    <div class="sm:col-span-2">
                        <PrimaryButton :disabled="form.processing">
                            {{ form.processing ? 'Creating…' : 'Create a key' }}
                        </PrimaryButton>
                    </div>
                </form>

                <ul class="divide-y divide-slate-100">
                    <li
                        v-for="token in tokens"
                        :key="token.id"
                        class="flex flex-wrap items-center justify-between gap-3 px-5 py-4"
                    >
                        <div class="min-w-0">
                            <p class="font-medium text-slate-800">
                                {{ token.name }}
                                <span class="ms-2 font-mono text-xs font-normal text-slate-400">
                                    {{ token.prefix }}…
                                </span>
                            </p>
                            <p class="mt-0.5 text-xs text-slate-500">
                                Created {{ token.created_at }} ·
                                <template v-if="token.last_used_at">
                                    last used {{ token.last_used_at }}
                                </template>
                                <template v-else> never used </template>
                            </p>
                        </div>

                        <DangerButton type="button" @click="revoke(token)">
                            Revoke
                        </DangerButton>
                    </li>

                    <li
                        v-if="!tokens.length"
                        class="px-5 py-12 text-center text-sm text-slate-500"
                    >
                        No keys yet. Create one above and the instructions for
                        connecting an assistant appear with it.
                    </li>
                </ul>

                <p
                    v-if="tokens.length && !issued"
                    class="border-t border-slate-100 px-5 py-3 text-xs text-slate-500"
                >
                    The instructions for connecting an assistant come with a new
                    key, because they contain it. The panel keeps only a hash of
                    the keys above, so it cannot put one of them back into them.
                </p>
            </div>

            <!--
                Connecting. Only on the response that issued a key, because the
                key is in it: a version of this card with a blank where the
                credential goes is something to paste and then wonder about.
            -->
            <div
                v-if="issued"
                class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
            >
                <div class="border-b border-slate-200 px-5 py-4">
                    <h3 class="font-semibold text-slate-800">
                        Connect an assistant
                    </h3>
                    <p class="mt-1 text-sm text-slate-500">
                        Paste the prompt into whatever you already talk to —
                        Claude Code, Codex, Cursor — and it adds this server
                        itself, or updates the key if it is already there. The
                        manual tab has the same thing as configuration.
                    </p>
                </div>

                <div class="px-5 py-4">
                    <p
                        v-if="local"
                        class="mb-4 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800 ring-1 ring-amber-600/20"
                    >
                        This panel has no hostname of its own yet, so the address
                        below is only reachable from the server itself. Set one
                        on the Settings page — an assistant on your own machine
                        has to be able to reach the panel, and a certificate is
                        what stops the key travelling in the clear.
                    </p>

                    <div class="flex flex-wrap gap-2">
                        <button
                            v-for="tab in [
                                { key: 'prompt', label: 'Prompt' },
                                { key: 'manual', label: 'Manual' },
                            ]"
                            :key="tab.key"
                            type="button"
                            class="rounded-lg px-3 py-1.5 text-sm font-medium transition"
                            :class="
                                showing === tab.key
                                    ? 'bg-brand-50 text-brand-700 ring-1 ring-brand-600/20'
                                    : 'text-slate-500 hover:bg-slate-50 hover:text-slate-700'
                            "
                            @click="showing = tab.key"
                        >
                            {{ tab.label }}
                        </button>
                    </div>

                    <div v-if="showing === 'prompt'" class="relative mt-3">
                        <pre
                            class="overflow-x-auto whitespace-pre-wrap rounded-xl bg-slate-900 px-4 py-3 pe-24 font-mono text-xs leading-relaxed text-slate-100"
                        >{{ prompt }}</pre>
                        <button
                            type="button"
                            class="absolute end-3 top-3 rounded-lg bg-white/10 px-2.5 py-1 text-xs font-medium text-white transition hover:bg-white/20"
                            @click="copy(prompt, 'prompt')"
                        >
                            {{ copied === 'prompt' ? 'Copied' : 'Copy' }}
                        </button>
                    </div>

                    <div v-else class="mt-3 space-y-3">
                        <dl class="grid gap-2 text-sm sm:grid-cols-[10rem_1fr]">
                            <dt class="text-slate-500">Name</dt>
                            <dd class="font-mono text-slate-800">{{ serverSlug }}</dd>

                            <dt class="text-slate-500">Transport</dt>
                            <dd class="text-slate-800">HTTP (streamable)</dd>

                            <dt class="text-slate-500">URL</dt>
                            <dd class="break-all font-mono text-slate-800">{{ endpoint }}</dd>

                            <dt class="text-slate-500">Header</dt>
                            <dd class="break-all font-mono text-slate-800">
                                Authorization: Bearer {{ key }}
                            </dd>
                        </dl>

                        <div class="relative">
                            <pre
                                class="overflow-x-auto rounded-xl bg-slate-900 px-4 py-3 pe-24 font-mono text-xs leading-relaxed text-slate-100"
                            >{{ manual }}</pre>
                            <button
                                type="button"
                                class="absolute end-3 top-3 rounded-lg bg-white/10 px-2.5 py-1 text-xs font-medium text-white transition hover:bg-white/20"
                                @click="copy(manual, 'manual')"
                            >
                                {{ copied === 'manual' ? 'Copied' : 'Copy' }}
                            </button>
                        </div>
                    </div>

                    <p
                        v-if="copyFailed === 'prompt' || copyFailed === 'manual'"
                        class="mt-2 text-xs text-slate-500"
                    >
                        This browser would not let the page copy for you — select
                        the text and copy it yourself.
                    </p>

                    <p class="mt-3 text-xs text-slate-500">
                        Both carry the key above, which is why they are here and
                        not on the page you come back to. Leaving without pasting
                        one of them means making another key.
                    </p>
                </div>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
