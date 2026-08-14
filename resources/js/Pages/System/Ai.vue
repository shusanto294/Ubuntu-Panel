<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import DangerButton from '@/Components/DangerButton.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, router, useForm } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

/**
 * AI access: the keys an assistant signs its requests with, and the three lines
 * of configuration that point one at this panel.
 *
 * The key is shown once, on the response that created it — the panel keeps only
 * a hash — so the card that carries it is the loudest thing on the page and
 * says so.
 */
const props = defineProps({
    tokens: { type: Array, default: () => [] },
    tools: { type: Array, default: () => [] },
    endpoint: { type: String, default: '' },
    issued: { type: String, default: null },
});

const form = useForm({ name: '' });

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

// The key in every snippet: the one just issued when there is one, and a
// placeholder otherwise — a page reloaded tomorrow cannot show a key again, and
// a snippet with somebody else's in it would be worse than one with a blank.
const key = computed(() => props.issued || 'YOUR-KEY');

const snippets = computed(() => [
    {
        key: 'claude-code',
        label: 'Claude Code',
        note: 'Run this in a terminal on the machine you use Claude Code from.',
        body: `claude mcp add --transport http ubuntu-panel ${props.endpoint} \\\n    --header "Authorization: Bearer ${key.value}"`,
    },
    {
        key: 'codex',
        label: 'Codex CLI',
        note: 'Add this to ~/.codex/config.toml.',
        body: `[mcp_servers.ubuntu_panel]\nurl = "${props.endpoint}"\nhttp_headers = { Authorization = "Bearer ${key.value}" }`,
    },
    {
        key: 'json',
        label: 'Claude Desktop, Cursor, Windsurf',
        note:
            'Add this to the client\'s mcpServers configuration. It goes through mcp-remote, ' +
            'which is what bridges a client that only speaks stdio to an HTTP server.',
        body: `{\n  "mcpServers": {\n    "ubuntu-panel": {\n      "command": "npx",\n      "args": [\n        "-y", "mcp-remote", "${props.endpoint}",\n        "--header", "Authorization: Bearer ${key.value}"\n      ]\n    }\n  }\n}`,
    },
]);

const showing = ref('claude-code');
const copied = ref('');

const copy = async (text, which) => {
    try {
        await navigator.clipboard.writeText(text);
        copied.value = which;
    } catch {
        // A browser that refuses the clipboard leaves the text on screen to be
        // selected by hand, which is what it was rendered for anyway.
        copied.value = '';
    }
};

const current = computed(
    () => snippets.value.find((snippet) => snippet.key === showing.value) || snippets.value[0],
);

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
            </div>

            <!-- Connecting a client. -->
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                <div class="border-b border-slate-200 px-5 py-4">
                    <h3 class="font-semibold text-slate-800">
                        Connect an assistant
                    </h3>
                    <p class="mt-1 text-sm text-slate-500">
                        The panel speaks the Model Context Protocol over HTTP at
                        <code class="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-xs">{{
                            endpoint
                        }}</code
                        >. Point a client at it with a key below.
                    </p>
                </div>

                <div class="px-5 py-4">
                    <p
                        v-if="local"
                        class="mb-4 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800 ring-1 ring-amber-600/20"
                    >
                        This panel has no hostname of its own yet, so the address
                        above is only reachable from the server itself. Set one
                        on the Settings page — an assistant running on your own
                        machine has to be able to reach the panel over the
                        network, and a certificate is what stops the key
                        travelling in the clear.
                    </p>

                    <div class="flex flex-wrap gap-2">
                        <button
                            v-for="snippet in snippets"
                            :key="snippet.key"
                            type="button"
                            class="rounded-lg px-3 py-1.5 text-sm font-medium transition"
                            :class="
                                showing === snippet.key
                                    ? 'bg-brand-50 text-brand-700 ring-1 ring-brand-600/20'
                                    : 'text-slate-500 hover:bg-slate-50 hover:text-slate-700'
                            "
                            @click="showing = snippet.key"
                        >
                            {{ snippet.label }}
                        </button>
                    </div>

                    <p class="mt-3 text-sm text-slate-500">{{ current.note }}</p>

                    <div class="relative mt-2">
                        <pre
                            class="overflow-x-auto rounded-xl bg-slate-900 px-4 py-3 pe-24 font-mono text-xs leading-relaxed text-slate-100"
                        >{{ current.body }}</pre>
                        <button
                            type="button"
                            class="absolute end-3 top-3 rounded-lg bg-white/10 px-2.5 py-1 text-xs font-medium text-white transition hover:bg-white/20"
                            @click="copy(current.body, current.key)"
                        >
                            {{ copied === current.key ? 'Copied' : 'Copy' }}
                        </button>
                    </div>

                    <p v-if="!issued" class="mt-3 text-xs text-slate-500">
                        Replace <code class="font-mono">YOUR-KEY</code> with a
                        key from below — an existing one if you kept it, a new
                        one if you did not.
                    </p>
                </div>
            </div>

            <!-- The keys. -->
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                <div class="border-b border-slate-200 px-5 py-4">
                    <h3 class="font-semibold text-slate-800">Access keys</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        A key can do everything you can do on this panel: create
                        sites, read and write their files, and delete them. Give
                        each assistant its own, so one can be revoked without
                        stopping the rest.
                    </p>
                </div>

                <form
                    class="flex flex-wrap items-end gap-3 border-b border-slate-100 px-5 py-4"
                    @submit.prevent="create"
                >
                    <div class="min-w-0 flex-1">
                        <InputLabel for="name" value="What is it for?" />
                        <TextInput
                            id="name"
                            v-model="form.name"
                            class="mt-1 block w-full"
                            placeholder="Claude on my laptop"
                        />
                    </div>
                    <PrimaryButton :disabled="form.processing">
                        {{ form.processing ? 'Creating…' : 'Create a key' }}
                    </PrimaryButton>
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
                        No keys yet. Create one above, then paste the snippet
                        into your assistant.
                    </li>
                </ul>
            </div>

            <!-- What an assistant can actually do. -->
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                <div class="border-b border-slate-200 px-5 py-4">
                    <h3 class="font-semibold text-slate-800">
                        What an assistant can do
                    </h3>
                    <p class="mt-1 text-sm text-slate-500">
                        These are the {{ tools.length }} tools it is given. Ask
                        in your own words — "make me a Next.js site at
                        app.example.com from this GitHub repository" is enough —
                        and it picks from this list.
                    </p>
                </div>

                <ul class="divide-y divide-slate-100">
                    <li
                        v-for="tool in tools"
                        :key="tool.name"
                        class="px-5 py-3"
                    >
                        <p class="flex items-center gap-2">
                            <code class="font-mono text-sm text-slate-800">{{ tool.name }}</code>
                            <span
                                v-if="tool.read_only"
                                class="rounded-full bg-slate-100 px-2 py-0.5 text-[11px] font-medium text-slate-500"
                            >
                                reads only
                            </span>
                        </p>
                        <p class="mt-0.5 text-sm text-slate-500">
                            {{ tool.description }}
                        </p>
                    </li>
                </ul>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
