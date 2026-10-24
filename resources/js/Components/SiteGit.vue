<script setup>
import { computed, ref } from 'vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import Checkbox from '@/Components/Checkbox.vue';
import { router, useForm } from '@inertiajs/vue3';
import { copyText } from '@/clipboard';
import { confirmDanger } from '@/dialog';

/**
 * The Git tab: where the site comes from, how the panel gets in, and what it
 * does when somebody pushes.
 *
 * The middle one is most of this file. A public repository needs nothing and a
 * private one needs a credential the operator has to go and create somewhere
 * else — so the panel's job is to say exactly what to paste where, and then to
 * tell them whether it worked rather than leaving them to find out at the next
 * deployment.
 */
const props = defineProps({
    site: { type: Object, required: true },
    git: { type: Object, required: true },
});

// ------------------------------------------------------------------ source

const source = useForm({
    action: 'source',
    repository: props.git.repository ?? '',
    branch: props.git.branch ?? 'main',
});

const saveSource = () =>
    source.post(route('sites.git', props.site.id), { preserveScroll: true });

// -------------------------------------------------------------------- auth

const auth = useForm({
    action: 'auth',
    auth: props.git.auth ?? 'none',
    token: '',
});

const saveAuth = () =>
    auth.post(route('sites.git', props.site.id), { preserveScroll: true });

const selectedMethod = computed(
    () => props.git.methods.find((m) => m.key === auth.auth) ?? props.git.methods[0],
);

const copied = ref('');

const copy = async (text, which) => {
    if (await copyText(text)) {
        copied.value = which;
        setTimeout(() => (copied.value = ''), 2000);
    }
};

const regenerateKey = async () => {
    const go = await confirmDanger(
        'Generate a new deploy key?',
        'The key currently at the repository stops working immediately, so this site ' +
            'cannot pull until you add the new public key there. Worth doing if the ' +
            'old one may have been exposed.',
        'Generate',
    );

    if (!go) return;

    router.post(
        route('sites.git', props.site.id),
        { action: 'key' },
        { preserveScroll: true },
    );
};

const test = () =>
    router.post(
        route('sites.git', props.site.id),
        { action: 'test' },
        { preserveScroll: true },
    );

const pull = () => router.post(route('sites.pull', props.site.id), {}, { preserveScroll: true });

// -------------------------------------------------------------- automation

const automation = useForm({
    action: 'automation',
    auto_pull: props.git.auto_pull ?? 'off',
    webhook: !!props.git.webhook_secret,
});

const saveAutomation = () =>
    automation.post(route('sites.git', props.site.id), { preserveScroll: true });

// Where the deploy key goes, per host. Names rather than deep links: the paths
// change, and a wrong link is worse than a name somebody can find in their own
// settings.
const KEY_LOCATIONS = [
    ['GitHub', 'the repository → Settings → Deploy keys → Add deploy key'],
    ['GitLab', 'the project → Settings → Repository → Deploy keys'],
    ['Bitbucket', 'the repository → Repository settings → Access keys'],
    ['Gitea', 'the repository → Settings → Deploy Keys'],
];

const TOKEN_LOCATIONS = [
    [
        'GitHub',
        'Settings → Developer settings → Personal access tokens. A fine-grained ' +
            'token needs Contents: Read-only on this repository.',
    ],
    ['GitLab', 'Settings → Access tokens on the project, with the read_repository scope.'],
    ['Bitbucket', 'Personal settings → App passwords, with Repositories: Read.'],
];

const hasRepository = computed(() => !!props.git.repository);
</script>

<template>
    <div class="grid gap-6 lg:grid-cols-3">
        <div class="space-y-6 lg:col-span-2">
            <!-- ============================================ where it comes from -->
            <form
                @submit.prevent="saveSource"
                class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
            >
                <h3 class="font-semibold text-slate-800">Repository</h3>
                <p class="mt-1 text-sm text-slate-500">
                    What the panel clones and pulls. Changing it is checked against
                    the remote straight away — saving something that does not work
                    and finding out at the next deployment is the thing this page
                    exists to stop.
                </p>

                <div class="mt-5 grid gap-5 sm:grid-cols-3">
                    <div class="sm:col-span-2">
                        <InputLabel for="repository" value="Repository URL" />
                        <TextInput
                            id="repository"
                            v-model="source.repository"
                            class="mt-1 block w-full font-mono text-sm"
                            placeholder="https://github.com/owner/repo.git"
                            autocomplete="off"
                        />
                        <InputError class="mt-1" :message="source.errors.repository" />
                        <p class="mt-1 text-xs text-slate-500">
                            An https URL or an SSH one —
                            <span class="font-mono">git@github.com:owner/repo.git</span>.
                            A deploy key needs the SSH form; a token needs https.
                        </p>
                    </div>

                    <div>
                        <InputLabel for="branch" value="Branch" />
                        <TextInput
                            id="branch"
                            v-model="source.branch"
                            class="mt-1 block w-full font-mono text-sm"
                            placeholder="main"
                            autocomplete="off"
                        />
                        <InputError class="mt-1" :message="source.errors.branch" />
                    </div>
                </div>

                <div
                    class="mt-6 flex flex-wrap items-center gap-3 border-t border-slate-100 pt-6"
                >
                    <PrimaryButton :disabled="source.processing">
                        {{ source.processing ? 'Checking…' : 'Save' }}
                    </PrimaryButton>
                    <button
                        v-if="hasRepository"
                        type="button"
                        class="rounded-xl px-4 py-2 text-sm text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                        @click="test"
                    >
                        Test connection
                    </button>
                    <button
                        v-if="hasRepository"
                        type="button"
                        class="rounded-xl px-4 py-2 text-sm font-medium text-white transition bg-brand-600 hover:bg-brand-700"
                        @click="pull"
                    >
                        Pull now
                    </button>
                    <span v-if="git.last_commit" class="text-xs text-slate-500">
                        Serving
                        <span class="font-mono">{{ git.last_commit }}</span>
                    </span>
                </div>
            </form>

            <!-- ==================================================== the credential -->
            <form
                v-if="hasRepository"
                @submit.prevent="saveAuth"
                class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
            >
                <h3 class="font-semibold text-slate-800">Access</h3>
                <p class="mt-1 text-sm text-slate-500">
                    How the panel authenticates. A private repository reports itself
                    as "not found" to anybody who cannot see it, so an unexpected
                    "not found" is usually this and not the URL.
                </p>

                <div class="mt-5 space-y-3">
                    <label
                        v-for="method in git.methods"
                        :key="method.key"
                        class="flex items-start gap-3 rounded-xl border p-4 transition"
                        :class="
                            auth.auth === method.key
                                ? 'border-brand-400 bg-brand-50'
                                : 'border-slate-200 hover:border-slate-300'
                        "
                    >
                        <input
                            v-model="auth.auth"
                            type="radio"
                            :value="method.key"
                            class="mt-0.5 border-slate-300 text-brand-600 focus:ring-brand-500"
                        />
                        <span class="text-sm">
                            <span class="font-medium text-slate-800">
                                {{ method.label }}
                            </span>
                            <span class="block text-slate-500">{{ method.help }}</span>
                        </span>
                    </label>
                </div>
                <InputError class="mt-2" :message="auth.errors.auth" />

                <!-- the token -->
                <div v-if="auth.auth === 'token'" class="mt-5">
                    <InputLabel for="token" value="Access token" />
                    <TextInput
                        id="token"
                        v-model="auth.token"
                        type="password"
                        autocomplete="off"
                        class="mt-1 block w-full font-mono text-sm"
                        :placeholder="git.has_token ? '•••••••• (stored)' : ''"
                    />
                    <InputError class="mt-1" :message="auth.errors.token" />
                    <p class="mt-1 text-xs text-slate-500">
                        Stored encrypted and never sent back to the browser. Leave it
                        blank to keep the one already saved.
                        <span v-if="git.has_token">A token is stored for this site.</span>
                    </p>

                    <dl class="mt-4 space-y-2 text-xs">
                        <div v-for="[host, where] in TOKEN_LOCATIONS" :key="host">
                            <dt class="font-medium text-slate-700">{{ host }}</dt>
                            <dd class="text-slate-500">{{ where }}</dd>
                        </div>
                    </dl>
                    <p class="mt-3 text-xs text-slate-500">
                        Read access to the repository's contents is all the panel
                        needs. A token with more than that is a token that can do
                        more than this if it leaks.
                    </p>
                </div>

                <!-- the deploy key -->
                <div v-if="auth.auth === 'key'" class="mt-5">
                    <template v-if="git.deploy_key">
                        <InputLabel value="Public key — add this to the repository" />
                        <div
                            class="mt-1 break-all rounded-xl bg-slate-900 p-4 font-mono text-xs text-slate-100"
                        >
                            {{ git.deploy_key }}
                        </div>
                        <div class="mt-2 flex flex-wrap items-center gap-3">
                            <button
                                type="button"
                                class="text-xs font-medium text-brand-600 hover:underline"
                                @click="copy(git.deploy_key, 'key')"
                            >
                                {{ copied === 'key' ? 'Copied' : 'Copy the public key' }}
                            </button>
                            <button
                                type="button"
                                class="text-xs text-rose-600 hover:underline"
                                @click="regenerateKey"
                            >
                                Generate a new one
                            </button>
                        </div>

                        <dl class="mt-4 space-y-2 text-xs">
                            <div v-for="[host, where] in KEY_LOCATIONS" :key="host">
                                <dt class="font-medium text-slate-700">{{ host }}</dt>
                                <dd class="text-slate-500">{{ where }}</dd>
                            </div>
                        </dl>
                        <p class="mt-3 text-xs text-slate-500">
                            On the repository itself, not on your account — an account
                            key would give this server access to everything you can
                            see. Read-only is enough; the panel never pushes.
                        </p>
                    </template>
                    <p v-else class="text-sm text-amber-700">
                        No key yet. Save this choice and the panel generates one, then
                        shows you the public half to paste.
                    </p>
                </div>

                <div class="mt-6 flex items-center gap-3 border-t border-slate-100 pt-6">
                    <PrimaryButton :disabled="auth.processing">
                        {{ auth.processing ? 'Checking…' : 'Save and test' }}
                    </PrimaryButton>
                </div>
            </form>

            <!-- ====================================================== automation -->
            <form
                v-if="hasRepository"
                @submit.prevent="saveAutomation"
                class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
            >
                <h3 class="font-semibold text-slate-800">Deploy on push</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Two ways, and they fail in opposite situations — so most sites
                    want both. A webhook deploys the moment somebody pushes and needs
                    this panel to be reachable from the internet. Checking on an
                    interval needs nothing of anybody and finds out late.
                </p>

                <!-- webhook -->
                <div class="mt-5">
                    <label class="flex items-start gap-3">
                        <Checkbox v-model:checked="automation.webhook" />
                        <span class="text-sm">
                            <span class="font-medium text-slate-800">
                                Accept a webhook from the repository
                            </span>
                            <span class="block text-slate-500">
                                The panel generates a secret. A push is only acted on
                                when it carries a signature made with that secret, so
                                nobody else can trigger a deployment.
                            </span>
                        </span>
                    </label>

                    <div v-if="git.webhook_secret" class="mt-4 space-y-3">
                        <div>
                            <InputLabel value="Payload URL" />
                            <div class="mt-1 flex flex-wrap items-center gap-2">
                                <code
                                    class="break-all rounded-lg bg-slate-100 px-3 py-2 font-mono text-xs text-slate-800"
                                    >{{ git.webhook_url }}</code
                                >
                                <button
                                    type="button"
                                    class="text-xs font-medium text-brand-600 hover:underline"
                                    @click="copy(git.webhook_url, 'url')"
                                >
                                    {{ copied === 'url' ? 'Copied' : 'Copy' }}
                                </button>
                            </div>
                        </div>

                        <div>
                            <InputLabel value="Secret" />
                            <div class="mt-1 flex flex-wrap items-center gap-2">
                                <code
                                    class="break-all rounded-lg bg-slate-100 px-3 py-2 font-mono text-xs text-slate-800"
                                    >{{ git.webhook_secret }}</code
                                >
                                <button
                                    type="button"
                                    class="text-xs font-medium text-brand-600 hover:underline"
                                    @click="copy(git.webhook_secret, 'secret')"
                                >
                                    {{ copied === 'secret' ? 'Copied' : 'Copy' }}
                                </button>
                            </div>
                        </div>

                        <p class="text-xs text-slate-500">
                            On GitHub: the repository → Settings → Webhooks → Add
                            webhook. Paste both, leave the content type as
                            <span class="font-mono">application/json</span>, and send
                            the push event only. GitLab calls the secret a "secret
                            token"; Gitea and Bitbucket are the same shape again.
                        </p>

                        <p
                            v-if="!git.reachable"
                            class="rounded-xl bg-amber-50 p-3 text-xs text-amber-800"
                        >
                            This panel has no hostname set, so the URL above is this
                            machine's address and port. A git host will post to it,
                            but over plain HTTP — which sends the secret in the clear
                            — and the address changes if the machine does. Give the
                            panel a hostname on the Settings page and the URL becomes
                            an https one.
                        </p>

                        <p class="text-xs text-slate-500">
                            Only a push to
                            <span class="font-mono">{{ git.branch }}</span> deploys.
                            A push to any other branch is answered and ignored, so a
                            feature branch pushed for review cannot land on this site.
                        </p>
                    </div>
                </div>

                <!-- polling -->
                <div class="mt-6 border-t border-slate-100 pt-5">
                    <InputLabel for="auto_pull" value="Or check the repository every" />
                    <select
                        id="auto_pull"
                        v-model="automation.auto_pull"
                        class="mt-1 block w-full max-w-xs rounded-lg border-slate-300 text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                    >
                        <option
                            v-for="schedule in git.schedules"
                            :key="schedule.key"
                            :value="schedule.key"
                        >
                            {{ schedule.label }}
                        </option>
                    </select>
                    <InputError class="mt-1" :message="automation.errors.auto_pull" />
                    <p class="mt-1 text-xs text-slate-500">
                        The panel asks the remote for the branch's commit and pulls
                        only when it has changed — so an interval on a repository
                        nobody touched costs one small request and nothing else.
                    </p>
                    <p
                        v-if="git.auto_pull !== 'off' && git.auto_pull_next"
                        class="mt-1 text-xs text-slate-500"
                    >
                        Next check around
                        {{ new Date(git.auto_pull_next).toLocaleString() }}.
                    </p>
                </div>

                <div class="mt-6 flex items-center gap-3 border-t border-slate-100 pt-6">
                    <PrimaryButton :disabled="automation.processing">
                        {{ automation.processing ? 'Saving…' : 'Save' }}
                    </PrimaryButton>
                </div>
            </form>

            <p
                v-if="!hasRepository"
                class="rounded-2xl bg-slate-50 p-5 text-sm text-slate-600 ring-1 ring-slate-900/5"
            >
                This site is not deployed from a repository. Add one above and the
                panel can clone it, pull it, and deploy it when you push.
            </p>
        </div>

        <!-- ================================================== the side column -->
        <div class="space-y-6">
            <div class="rounded-2xl bg-slate-50 p-5 text-sm text-slate-600 ring-1 ring-slate-900/5">
                <p class="font-medium text-slate-700">A deploy key or a token?</p>
                <p class="mt-1">
                    A deploy key, where you can. It is generated here, it never
                    leaves this server, it works on one repository and nothing else,
                    and it is read-only — so the worst it can do if this machine is
                    compromised is let somebody read the code that is already on it.
                </p>
                <p class="mt-2">
                    A token is the answer when a deploy key is not an option: an
                    organisation that disallows them, or a host that does not offer
                    them. Scope it to read-only on the one repository. A token with
                    write access to everything you can see is a much larger thing to
                    have sitting on a web server.
                </p>
            </div>

            <div class="rounded-2xl bg-slate-50 p-5 text-sm text-slate-600 ring-1 ring-slate-900/5">
                <p class="font-medium text-slate-700">What a pull does</p>
                <p class="mt-1">
                    Fetches the branch and resets the working tree to it, so anything
                    tracked that you changed on the server is lost — that is what
                    makes a deployment repeatable. Untracked files are left alone, so
                    uploads, <span class="font-mono text-xs">.env</span> and
                    <span class="font-mono text-xs">node_modules</span> survive.
                </p>
                <p class="mt-2">
                    Then, per type: Composer and migrations for Laravel; npm and a
                    rebuild and a service restart for the Node types. The site is
                    down for the restart.
                </p>
            </div>
        </div>
    </div>
</template>
