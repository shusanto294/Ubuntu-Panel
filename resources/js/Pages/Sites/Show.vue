<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import FileIcon from '@/Components/FileIcon.vue';
import FolderTree from '@/Components/FolderTree.vue';
import TaskProgress from '@/Components/TaskProgress.vue';
import { formatRelative } from '@/time';
import { Head, Link, router } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

const props = defineProps({
    site: Object,
    logs: Array,
    activeTask: Object,
    latestTask: Object,
    certbot: Boolean,
    resolution: Object,
    phpDirectives: { type: Array, default: () => [] },
    cronJobs: { type: Array, default: () => [] },
    accessUsers: { type: Array, default: () => [] },
    database: { type: Object, default: null },
    logFiles: { type: Array, default: () => [] },
    certificate: { type: Object, default: () => ({}) },
    cache: { type: Object, default: () => ({}) },
});

const deleteFiles = ref(true);
const showSecrets = ref(false);

// Which section is on screen. General first, because the thing somebody opens
// a site for is usually to read it rather than to change it.
const tab = ref('general');

const tabs = computed(() => {
    const entries = [
        { key: 'general', label: 'General' },
        { key: 'settings', label: 'Settings' },
    ];

    if (!props.site.is_proxied) entries.push({ key: 'php', label: 'PHP' });

    entries.push({ key: 'database', label: 'Database' });
    entries.push({ key: 'ssl', label: 'SSL/TLS' });
    entries.push({ key: 'cache', label: 'Cache' });
    entries.push({ key: 'files', label: 'File manager' });
    entries.push({ key: 'access', label: 'SSH/SFTP' });
    entries.push({ key: 'cron', label: 'Cron jobs' });
    entries.push({ key: 'logs', label: 'Logs' });

    return entries;
});

// ---- Settings: the document root, inside the site's own directory.
const webDirectory = ref((props.site.web_directory ?? '').replace(/^\//, ''));

const saveDirectory = () =>
    router.post(
        route('sites.settings', props.site.id),
        { web_directory: webDirectory.value },
        { preserveScroll: true },
    );

// ---- PHP: one field per directive the panel is willing to write.
const phpValues = ref(
    Object.fromEntries(
        props.phpDirectives.map((directive) => [directive.key, directive.value ?? '']),
    ),
);

const savePHP = () =>
    router.post(route('sites.php', props.site.id), phpValues.value, {
        preserveScroll: true,
    });

// ---- Cache.
//
// nginx keeps the page it built and serves the copy to the next visitor, so a
// second request for the same page never reaches PHP or the application at
// all. The two things worth deciding are whether it happens and for how long.
const cacheMode = ref(props.cache.mode ?? 'off');
const cacheTTL = ref(props.cache.ttl ?? '12h');
const cacheWorking = ref(false);

const cacheSubmit = (extra) => {
    cacheWorking.value = true;

    router.post(route('sites.cache', props.site.id), extra, {
        preserveScroll: true,
        onFinish: () => (cacheWorking.value = false),
    });
};

const saveCache = () =>
    cacheSubmit({ action: 'save', mode: cacheMode.value, ttl: cacheTTL.value });

// Turning the cache off does not empty it, and publishing a change is exactly
// when somebody wants it gone — so this is its own button rather than a
// consequence of the other one.
const purgeCache = () => cacheSubmit({ action: 'purge' });

// ---- Cron: the whole list is posted, so removing a row removes the line.
//
// Five fields separated by spaces is a language, and somebody who wants a
// backup at three in the morning should not have to learn it. So the schedule
// is a menu of the ones people actually ask for, and the five boxes are still
// there behind Custom for the ones they do not.
const SCHEDULES = [
    { label: 'Every minute', value: '* * * * *' },
    { label: 'Every 5 minutes', value: '*/5 * * * *' },
    { label: 'Every 15 minutes', value: '*/15 * * * *' },
    { label: 'Every 30 minutes', value: '*/30 * * * *' },
    { label: 'Every hour', value: '0 * * * *' },
    { label: 'Every 6 hours', value: '0 */6 * * *' },
    { label: 'Every day at midnight', value: '0 0 * * *' },
    { label: 'Every day at 3am', value: '0 3 * * *' },
    { label: 'Every Monday at 3am', value: '0 3 * * 1' },
    { label: 'Every Sunday at midnight', value: '0 0 * * 0' },
    { label: 'First of the month at 3am', value: '0 3 1 * *' },
];

const jobs = ref(props.cronJobs.map((job) => ({ ...job })));

// A new row starts on the first thing in the menu, which is also the schedule
// somebody adding a job is most often after — a runner that has to be asked
// every minute whether there is anything to do.
const addJob = () => jobs.value.push({ schedule: SCHEDULES[0].value, command: '' });
const removeJob = (index) => jobs.value.splice(index, 1);

// The five fields, always on screen and always editable. The menu writes them
// and typing in one writes back: change the hour of "Every day at 3am" and the
// row is a custom schedule that keeps the rest of what the preset chose,
// rather than a blank expression to retype.
const FIELDS = [
    { label: 'Minute' },
    { label: 'Hour' },
    { label: 'Day of month' },
    { label: 'Month' },
    { label: 'Day of week' },
];

const partsOf = (schedule) => {
    const parts = String(schedule ?? '').trim().split(/\s+/).filter(Boolean);

    while (parts.length < 5) parts.push('*');

    return parts.slice(0, 5);
};

const field = (job, index) => partsOf(job.schedule)[index];

const setField = (job, index, value) => {
    const parts = partsOf(job.schedule);

    parts[index] = String(value).trim() || '*';
    job.schedule = parts.join(' ');
};

// A row is on a preset when its expression is one of them, and custom
// otherwise — including a line somebody wrote into the file by hand, which
// keeps its own schedule and shows the boxes.
const presetFor = (job) =>
    SCHEDULES.some((entry) => entry.value === job.schedule) ? job.schedule : 'custom';

const pickPreset = (job, value) => {
    if (value === 'custom') return;

    job.schedule = value;
};

const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

// The expression back in words, so the row says what it will do rather than
// what it is set to. Only the shapes people write: anything else is left as it
// is, which is more honest than guessing at it.
const describe = (schedule) => {
    const parts = String(schedule ?? '').trim().split(/\s+/);

    if (parts.length !== 5) return 'Five fields: minute, hour, day, month, weekday.';

    const [minute, hour, day, month, weekday] = parts;
    const known = SCHEDULES.find((entry) => entry.value === parts.join(' '));

    if (known) return known.label;

    const at = (h, m) =>
        `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;

    const every = /^\*\/(\d+)$/;

    if (every.test(minute) && hour === '*' && day === '*' && month === '*' && weekday === '*')
        return `Every ${minute.match(every)[1]} minutes`;

    if (/^\d+$/.test(minute) && hour === '*' && day === '*' && month === '*' && weekday === '*')
        return `Every hour, at ${minute} minutes past`;

    if (/^\d+$/.test(minute) && /^\d+$/.test(hour) && month === '*') {
        if (day === '*' && weekday === '*') return `Every day at ${at(hour, minute)}`;
        if (day === '*' && /^[0-6]$/.test(weekday))
            return `Every ${DAYS[Number(weekday)]} at ${at(hour, minute)}`;
        if (weekday === '*' && /^\d+$/.test(day))
            return `Day ${day} of each month, at ${at(hour, minute)}`;
    }

    return 'Custom schedule.';
};

const saveJobs = () =>
    router.post(
        route('sites.cron', props.site.id),
        { jobs: JSON.stringify(jobs.value) },
        { preserveScroll: true },
    );

// ---- File manager.
//
// Everything here names a path relative to the site's own directory, and the
// panel resolves it against that root and refuses anything that leaves — so a
// path typed into this page cannot reach the rest of the machine.
const files = ref({ path: '/', entries: [], error: null });
const filesLoading = ref(false);
const editing = ref(null);

const loadFiles = async (where = '/') => {
    filesLoading.value = true;

    try {
        const { data } = await window.axios.get(route('sites.files', props.site.id), {
            params: { path: where },
        });

        files.value = {
            path: data.path ?? where,
            entries: data.entries ?? [],
            error: data.error ?? null,
        };
        selected.value = [];
        anchor.value = null;

        syncTree(files.value.path, files.value.entries);
    } catch {
        files.value = { path: where, entries: [], error: 'The folder could not be read.' };
    } finally {
        filesLoading.value = false;
    }
};

// ---- The folder tree.
//
// The listing says what is in one folder; the tree says where that folder is,
// which is the thing a listing on its own cannot tell you. It holds only
// directories, and a branch is read from the server the first time it is
// opened rather than the whole site being walked up front.
const tree = ref([
    { name: props.site.domain, path: '/', open: false, loading: false, children: null },
]);

const foldersFrom = (entries) =>
    (entries ?? [])
        .filter((entry) => entry.directory)
        .map((entry) => ({
            name: entry.name,
            path: entry.path,
            open: false,
            loading: false,
            children: null,
        }));

const loadBranch = async (node) => {
    node.loading = true;

    try {
        const { data } = await window.axios.get(route('sites.files', props.site.id), {
            params: { path: node.path },
        });

        node.children = foldersFrom(data.entries);
    } catch {
        node.children = [];
    } finally {
        node.loading = false;
    }
};

const toggleBranch = async (node) => {
    if (node.open) {
        node.open = false;

        return;
    }

    if (!node.children) await loadBranch(node);

    node.open = true;
};

// Walking to a folder in the listing opens the tree down to it, and the
// folders it just read become that branch — so the two halves cannot drift,
// and a folder made or deleted here shows in both.
const syncTree = async (path, entries) => {
    let node = tree.value[0];
    node.open = true;

    for (const part of String(path).split('/').filter(Boolean)) {
        if (!node.children) await loadBranch(node);

        const next = (node.children ?? []).find((child) => child.name === part);
        if (!next) return;

        next.open = true;
        node = next;
    }

    // Keep the branches already opened underneath open.
    const opened = new Map(
        (node.children ?? []).map((child) => [child.name, child]),
    );

    node.children = foldersFrom(entries).map((child) => {
        const had = opened.get(child.name);

        return had ? { ...child, open: had.open, children: had.children } : child;
    });
};

// The path split into the links above the listing.
const crumbs = computed(() => {
    const parts = files.value.path.split('/').filter(Boolean);
    const trail = [{ name: props.site.domain, path: '/' }];

    parts.forEach((part, index) => {
        trail.push({ name: part, path: '/' + parts.slice(0, index + 1).join('/') });
    });

    return trail;
});

const parentPath = computed(() => {
    const parts = files.value.path.split('/').filter(Boolean);
    parts.pop();

    return '/' + parts.join('/');
});

const openEntry = (entry) => {
    if (entry.directory) return loadFiles(entry.path);

    return openEditor(entry);
};

const openEditor = async (entry) => {
    editing.value = { path: entry.path, contents: '', loading: true, error: null };

    try {
        const { data } = await window.axios.get(route('sites.file', props.site.id), {
            params: { path: entry.path },
        });

        editing.value = {
            path: entry.path,
            contents: data.contents ?? '',
            loading: false,
            error: data.error ?? null,
        };
    } catch {
        editing.value = {
            path: entry.path,
            contents: '',
            loading: false,
            error: 'That file could not be opened.',
        };
    }
};

const saveFile = async () => {
    const { data } = await window.axios.post(route('sites.file.save', props.site.id), {
        path: editing.value.path,
        contents: editing.value.contents,
    });

    if (data.error) {
        editing.value.error = data.error;

        return;
    }

    editing.value = null;
    loadFiles(files.value.path);
};

// One endpoint for new, rename and delete: three verbs against one path, and
// three routes would be three places to get the path check wrong.
const fileAction = async (action, entry, extra = {}) => {
    const { data } = await window.axios.post(route('sites.files.action', props.site.id), {
        action,
        path: entry,
        ...extra,
    });

    if (data.error) {
        files.value.error = data.error;

        return;
    }

    files.value.error = null;
    loadFiles(files.value.path);
};

const newFolder = () => {
    const name = window.prompt('Name for the new folder');
    if (!name) return;

    fileAction('folder', `${files.value.path}/${name}`);
};

const newFile = () => {
    const name = window.prompt('Name for the new file');
    if (!name) return;

    fileAction('file', `${files.value.path}/${name}`);
};

const renameEntry = (entry) => {
    const name = window.prompt(`Rename ${entry.name} to`, entry.name);
    if (!name || name === entry.name) return;

    fileAction('rename', entry.path, { to: `${files.value.path}/${name}` });
};

const humanSize = (bytes) => {
    if (bytes >= 1 << 30) return `${(bytes / (1 << 30)).toFixed(1)} GB`;
    if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`;
    if (bytes >= 1 << 10) return `${Math.round(bytes / (1 << 10))} KB`;

    return `${bytes} B`;
};

// ---- Selection, the way a file manager does it: a click picks one, a
// modifier adds or extends, and a double click opens. No checkbox column —
// Finder has none, and the toolbar reads the selection just as well without.
const selected = ref([]);
const anchor = ref(null);

const isSelected = (entry) => selected.value.includes(entry.name);

const selectEntry = (entry, event) => {
    const names = files.value.entries.map((one) => one.name);

    if (event.shiftKey && anchor.value !== null) {
        const from = names.indexOf(anchor.value);
        const to = names.indexOf(entry.name);

        if (from !== -1 && to !== -1) {
            const [start, end] = from < to ? [from, to] : [to, from];
            selected.value = names.slice(start, end + 1);

            return;
        }
    }

    if (event.metaKey || event.ctrlKey) {
        selected.value = isSelected(entry)
            ? selected.value.filter((name) => name !== entry.name)
            : [...selected.value, entry.name];
        anchor.value = entry.name;

        return;
    }

    selected.value = [entry.name];
    anchor.value = entry.name;
};

const downloadURL = (entry) =>
    `${route('sites.download', props.site.id)}?path=${encodeURIComponent(entry.path)}`;

const zipSelected = async () => {
    if (!selected.value.length) return;

    const name = window.prompt(
        'Name for the archive',
        selected.value.length === 1 ? selected.value[0] : 'archive',
    );

    if (!name) return;

    await fileAction('zip', files.value.path, {
        names: JSON.stringify(selected.value),
        name,
    });

    selected.value = [];
};

const deleteSelected = async () => {
    if (!selected.value.length) return;

    const go = await confirmDanger(
        `Delete ${selected.value.length} item${selected.value.length === 1 ? '' : 's'}?`,
        'Folders are removed with everything in them.',
        'Delete',
    );

    if (!go) return;

    for (const name of selected.value) {
        await fileAction('delete', `${files.value.path}/${name}`);
    }

    selected.value = [];
};

// What the toolbar acts on when exactly one thing is picked.
const onlySelected = computed(() =>
    selected.value.length === 1
        ? files.value.entries.find((entry) => entry.name === selected.value[0])
        : null,
);

// ---- Upload. Streamed as multipart so a large file is not held in memory at
// either end, and the folder it lands in is the one on screen.
const uploading = ref(false);
const uploadInput = ref(null);
const dragging = ref(false);

const uploadFiles = async (list) => {
    if (!list || !list.length) return;

    const body = new FormData();
    body.append('path', files.value.path);

    for (const file of list) body.append('files[]', file, file.name);

    uploading.value = true;

    try {
        const { data } = await window.axios.post(
            route('sites.upload', props.site.id),
            body,
        );

        files.value.error = data.error ?? null;
    } catch {
        files.value.error = 'The upload did not finish.';
    } finally {
        uploading.value = false;
        if (uploadInput.value) uploadInput.value.value = '';
        loadFiles(files.value.path);
    }
};

const onDrop = (event) => {
    dragging.value = false;
    uploadFiles(event.dataTransfer?.files);
};

// ---- SSH and SFTP accounts.
//
// Real system accounts, with the site directory as their home and www-data as
// their group — so what they upload is what the web server can read.
// Two accounts, not one account with a tickbox. An SFTP account moves files
// and an SSH account is a shell on the server, and the second is a decision
// about the whole machine rather than a detail of the first — so it is asked
// for in its own words, in its own box.
const forms = ref({
    sftp: { name: '', password: '' },
    ssh: { name: '', password: '' },
});

const accountWorking = ref(false);

const accessSubmit = (action, extra = {}) => {
    accountWorking.value = true;

    router.post(
        route('sites.access', props.site.id),
        { action, ...extra },
        {
            preserveScroll: true,
            onFinish: () => {
                accountWorking.value = false;
                forms.value = {
                    sftp: { name: '', password: '' },
                    ssh: { name: '', password: '' },
                };
            },
        },
    );
};

const addAccount = (kind) => {
    const form = forms.value[kind];

    if (!form.name.trim()) return;

    accessSubmit('create', {
        username: form.name,
        password: form.password,
        sftp_only: kind === 'sftp' ? '1' : '0',
    });
};

// The list reads as the two kinds it holds. An account with a shell can do
// everything an SFTP one can and more, so which list a name is in is the
// thing worth seeing first.
const sftpAccounts = computed(() => props.accessUsers.filter((user) => user.sftp_only));
const sshAccounts = computed(() => props.accessUsers.filter((user) => !user.sftp_only));

const changeAccountPassword = (user) => {
    const chosen = window.prompt(
        `New password for ${user.name}. Leave blank to have one generated.`,
        '',
    );

    if (chosen === null) return;

    accessSubmit('password', { username: user.name, password: chosen });
};

const removeAccount = async (user) => {
    const go = await confirmDanger(
        `Remove ${user.name}?`,
        'The account can no longer sign in. The site and its files are untouched.',
        'Remove',
    );

    if (!go) return;

    accessSubmit('delete', { username: user.name });
};


// ---- Logs: nginx's own for this site, and the panel's history of it.
const logView = ref(props.logFiles[0]?.key ?? 'activity');

const shownLog = computed(() =>
    props.logFiles.find((file) => file.key === logView.value),
);

watch(tab, (value) => {
    if (value === 'files' && !files.value.entries.length) loadFiles('/');
});


// A deploy runs on the queue, so the page has to go and look for the result.
const busy = computed(() =>
    ['pending', 'deploying', 'deleting'].includes(props.site.status),
);

// The step list stays up while the work runs, and while the run that failed is
// still the last thing that happened — that is when the steps are worth
// reading, because one of them has the ✕ against it.
const showProgress = computed(
    () =>
        busy.value ||
        props.activeTask?.status === 'running' ||
        ((props.activeTask ?? props.latestTask)?.status === 'failed' &&
            props.site.status !== 'active'),
);

const live = computed(() => props.site.status === 'active' && !busy.value);

// The site can be built, running and answering nginx on this machine while its
// name resolves to nothing at all — every step of the deployment passes and
// the browser still says "this site can't be reached". That is DNS, not the
// site, and the page has to say which of the two it is.
const unreachable = computed(
    () => props.resolution?.checked && !props.resolution.resolves,
);

let timer = null;

const startPolling = () => {
    if (timer) return;
    timer = setInterval(
        () =>
            router.reload({
                only: ['site', 'logs', 'activeTask', 'latestTask'],
                preserveScroll: true,
            }),
        3000,
    );
};

const stopPolling = () => {
    if (timer) {
        clearInterval(timer);
        timer = null;
    }
};

// The list of logs and the site's own state can both have moved on since this
// page was last rendered into Inertia's cache — a deploy that finished after
// the redirect, a certificate issued on a retry.
const refetch = () =>
    router.reload({
        only: ['site', 'logs', 'activeTask', 'latestTask'],
        preserveScroll: true,
        preserveState: true,
    });

onMounted(() => {
    refetch();

    if (busy.value) startPolling();
});
onBeforeUnmount(stopPolling);
watch(busy, (value) => (value ? startPolling() : stopPolling()));

const post = (name, extra = {}) =>
    router.post(route(name, props.site.id), extra, { preserveScroll: true });

const destroy = async () => {
    const parts = ['its nginx vhost'];
    if (props.site.manage_dns)
        parts.push(`${props.site.dns_provider ?? 'DNS'} records`);
    if (props.database) parts.push('the site database');
    if (props.site.is_proxied) parts.push('the systemd service');

    const go = await confirmDanger(
        `Delete ${props.site.domain}?`,
        `This removes ${parts.join(', ')}${
            deleteFiles.value ? ' and all site files' : ''
        }.`,
    );

    if (!go) return;

    router.delete(route('sites.destroy', props.site.id), {
        data: { delete_files: deleteFiles.value },
    });
};
</script>

<template>
    <Head :title="site.domain" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-800">
                        {{ site.domain }}
                    </h2>
                    <p class="text-sm text-slate-500">
                        {{ site.type_label }} · {{ site.document_root }}
                    </p>
                </div>
                <div class="flex flex-wrap items-center gap-2">
                    <StatusBadge :status="site.status" />
                    <a
                        :href="site.url"
                        target="_blank"
                        rel="noopener"
                        class="rounded-xl bg-white ring-1 ring-slate-900/10 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                        >Visit</a
                    >
                    <button
                        v-if="site.repository"
                        @click="post('sites.pull')"
                        class="rounded-xl bg-white ring-1 ring-slate-900/10 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                    >
                        Pull latest
                    </button>
                    <button
                        v-if="site.is_proxied"
                        @click="post('sites.restart')"
                        class="rounded-xl bg-white ring-1 ring-slate-900/10 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                    >
                        Restart app
                    </button>
                    <button
                        v-if="site.manage_dns"
                        @click="post('sites.dns-sync')"
                        class="rounded-xl bg-white ring-1 ring-slate-900/10 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                    >
                        Sync DNS
                    </button>
                    <button
                        @click="post('sites.redeploy')"
                        class="rounded-md bg-slate-900 px-3 py-2 text-sm font-medium text-white hover:bg-slate-700"
                    >
                        Redeploy
                    </button>
                </div>
            </div>
        </template>

        <div
            v-if="busy && !activeTask && !latestTask"
            class="mb-8 flex items-center gap-3 rounded-xl border border-brand-200 bg-brand-50 px-5 py-4 text-sm text-brand-900"
        >
            <span class="h-2 w-2 animate-pulse rounded-full bg-brand-500" />
            Starting. If nothing happens within a minute, the panel's workers are
            not running — check
            <code class="rounded bg-brand-100 px-1">systemctl status ubuntu-panel</code
            >.
        </div>

        <!--
            Building a site takes minutes and every one of them happens out of
            sight: the step list is the page while that is true, and while the
            run that just failed still needs reading.
        -->
        <div v-if="showProgress" class="mb-8">
            <TaskProgress
                :task="activeTask ?? latestTask"
                :title="`Setting up ${site.domain}`"
                :subtitle="site.type_label"
            />
        </div>

        <!-- What you came back for once it lands. -->
        <div
            v-if="live"
            class="mb-8 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <!--
                One row, not two. The card used to say the domain is live, then
                print the URL, then print the domain again under a heading — a
                block of white saying one thing three times, with the tabs
                below the fold. The domain is the link now, the dot beside it
                is the state, and the way into the application is the last
                column rather than a row of its own.
            -->
            <dl
                class="grid grid-cols-2 items-center gap-x-8 gap-y-4 text-sm sm:grid-cols-5"
            >
                <div class="min-w-0">
                    <dt class="text-xs uppercase tracking-wide text-slate-400">
                        Domain
                    </dt>
                    <dd class="mt-1 flex min-w-0 items-center gap-1.5">
                        <span
                            class="h-2 w-2 shrink-0 rounded-full"
                            :class="unreachable ? 'bg-rose-500' : 'bg-emerald-500'"
                            :title="
                                unreachable
                                    ? `${site.domain} is built, but nothing resolves it`
                                    : `${site.domain} is live`
                            "
                        />
                        <a
                            :href="site.url"
                            target="_blank"
                            rel="noopener"
                            class="inline-flex min-w-0 items-center gap-1 font-medium text-brand-600 hover:underline"
                            :title="`Open ${site.url} in a new tab`"
                        >
                            <span class="truncate">{{ site.domain }}</span>
                            <!-- The mark that says this leaves the panel. -->
                            <svg
                                class="h-3.5 w-3.5 shrink-0"
                                viewBox="0 0 24 24"
                                fill="none"
                                stroke="currentColor"
                                stroke-width="2"
                                stroke-linecap="round"
                                stroke-linejoin="round"
                                aria-hidden="true"
                            >
                                <path d="M14 4h6v6" />
                                <path d="M20 4l-8.5 8.5" />
                                <path d="M19 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h5" />
                            </svg>
                        </a>
                    </dd>
                </div>
                <div class="min-w-0">
                    <dt class="text-xs uppercase tracking-wide text-slate-400">Type</dt>
                    <dd class="mt-1 truncate font-medium text-slate-800">
                        {{ site.type_label }}
                    </dd>
                </div>
                <div class="min-w-0">
                    <dt class="text-xs uppercase tracking-wide text-slate-400">
                        {{ site.is_proxied ? 'App port' : 'PHP' }}
                    </dt>
                    <dd class="mt-1 truncate font-medium text-slate-800">
                        {{ site.is_proxied ? site.app_port : site.php_version }}
                    </dd>
                </div>
                <div class="min-w-0">
                    <dt class="text-xs uppercase tracking-wide text-slate-400">
                        IP address
                    </dt>
                    <dd class="mt-1 truncate font-medium text-slate-800">
                        {{ resolution?.expected || '—' }}
                    </dd>
                </div>

                <!--
                    For WordPress this goes through the panel, which mints a
                    one-time token and sends the browser on already signed in.
                    Everything else opens the application's own front door.
                -->
                <div class="sm:justify-self-end">
                    <a
                        v-if="site.type === 'wordpress'"
                        :href="route('sites.wp-admin', site.id)"
                        target="_blank"
                        rel="noopener"
                        class="inline-block rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700"
                    >
                        WP Admin
                    </a>
                    <a
                        v-else-if="site.app"
                        :href="site.app.url"
                        target="_blank"
                        rel="noopener"
                        class="inline-block rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700"
                    >
                        Open {{ site.type_label }}
                    </a>
                </div>
            </dl>

            <!--
                The deployment checks the application on its port and nginx
                with the site's own Host header, both on this machine, so all
                of it passes on a domain that resolves to nothing. Opening the
                link then gives DNS_PROBE_FINISHED_NXDOMAIN and reads as the
                panel having built a broken site. It has not: the name has
                nowhere to send anyone.
            -->
            <div
                v-if="unreachable"
                class="mt-4 rounded-xl bg-rose-50 px-4 py-3 text-xs text-rose-800"
            >
                <span class="font-medium">{{ site.domain }} does not resolve.</span>
                The site is built and nginx answers for it on this server, but the
                domain points nowhere, so a browser shows
                <code class="rounded bg-rose-100 px-1"
                    >DNS_PROBE_FINISHED_NXDOMAIN</code
                >
                rather than reaching it.
                <template v-if="site.manage_dns">
                    The panel published the record itself, so either it has not
                    spread yet — that takes minutes — or the zone at the provider
                    is not the one serving this name. Check the zone, then
                    <button
                        @click="post('sites.dns-sync')"
                        class="font-medium underline"
                    >
                        Sync DNS</button
                    >.
                </template>
                <template v-else>
                    No connected DNS credential holds this domain's zone, so the
                    panel published nothing for it. Add this record at whoever
                    runs
                    <span class="font-mono">{{ site.domain }}</span
                    >'s DNS:
                    <span class="mt-2 block font-mono text-rose-900"
                        >{{ resolution.record?.type || 'A' }}&nbsp;&nbsp;{{
                            resolution.record?.name || site.domain
                        }}&nbsp;&nbsp;→&nbsp;&nbsp;{{
                            resolution.record?.content ||
                            resolution.expected ||
                            'this server’s IP'
                        }}</span
                    >
                    Nothing needs redeploying afterwards; the site is already
                    waiting for it. Connecting the provider on the
                    <Link :href="route('dns.index')" class="font-medium underline"
                        >DNS page</Link
                    >
                    lets the panel write it — and take it away again when the
                    site is deleted.
                </template>
            </div>

            <p
                v-if="!site.ssl"
                class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-xs text-amber-800"
            >
                Served over HTTPS with a self-signed certificate, so browsers warn
                and a CDN in front needs its origin mode on "Full" rather than
                "Full (strict)".
                <template v-if="certbot">
                    Certbot is installed, so it tried and did not get one —
                    <Link
                        v-if="latestTask"
                        :href="route('logs.show', latestTask.id)"
                        class="font-medium underline"
                        >the log says why</Link
                    ><span v-else>the log says why</span>. Usually
                    {{ site.domain }} does not resolve to this machine yet, or a
                    CDN answered the challenge itself. Fix that and Redeploy.
                </template>
                <template v-else>
                    Install Certbot from Apps, make sure {{ site.domain }} resolves
                    here, then Redeploy.
                </template>
            </p>

        </div>

        <div
            v-if="site.last_error && !busy"
            class="mb-6 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800"
        >
            <span class="font-medium">Last error:</span> {{ site.last_error }}
        </div>

        <!--
            Nothing below the step list while the site is being built. Half of
            it is not true yet — the port, the certificate, the login the
            deployment has not generated — and a Danger zone under a running
            build invites deleting a site mid-deployment.
            
            `busy`, not `showProgress`: a deployment that failed leaves its
            steps on screen with a ✕ against one of them, and that is precisely
            when the Danger zone has to be reachable. Hiding it there left a
            half-built site with no way to remove it.
        -->
        <!--
            The same facts for a site that is not live — where the card above
            them is not on screen to carry them.
        -->
        <dl
            v-if="!busy && !live"
            class="mb-6 grid grid-cols-2 gap-x-8 gap-y-4 border-b border-slate-200 pb-6 text-sm sm:grid-cols-4"
        >
            <div class="min-w-0">
                <dt class="text-xs uppercase tracking-wide text-slate-400">Domain</dt>
                <dd class="mt-1 flex items-center gap-2 truncate font-medium text-slate-800">
                    {{ site.domain }}
                    <a
                        :href="site.url"
                        target="_blank"
                        rel="noopener"
                        class="text-xs font-normal text-brand-600 hover:underline"
                        >open</a
                    >
                </dd>
            </div>
            <div class="min-w-0">
                <dt class="text-xs uppercase tracking-wide text-slate-400">Type</dt>
                <dd class="mt-1 truncate font-medium text-slate-800">
                    {{ site.type_label }}
                </dd>
            </div>
            <div class="min-w-0">
                <dt class="text-xs uppercase tracking-wide text-slate-400">
                    {{ site.is_proxied ? 'App port' : 'PHP' }}
                </dt>
                <dd class="mt-1 truncate font-medium text-slate-800">
                    {{ site.is_proxied ? site.app_port : site.php_version }}
                </dd>
            </div>
            <div class="min-w-0">
                <dt class="text-xs uppercase tracking-wide text-slate-400">
                    IP address
                </dt>
                <dd class="mt-1 truncate font-medium text-slate-800">
                    {{ resolution?.expected || '—' }}
                </dd>
            </div>
        </dl>

        <!--
            One site, several subjects. The page used to be one column of
            everything at once — settings, credentials, DNS, the vhost, the
            history — and finding the one you came for meant reading past the
            other four.
        -->
        <div v-if="!busy" class="mb-6 border-b border-slate-200">
            <nav class="-mb-px flex flex-wrap gap-6" aria-label="Sections">
                <button
                    v-for="entry in tabs"
                    :key="entry.key"
                    @click="tab = entry.key"
                    class="border-b-2 px-1 pb-3 text-sm font-medium transition"
                    :class="
                        tab === entry.key
                            ? 'border-brand-600 text-brand-700'
                            : 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700'
                    "
                >
                    {{ entry.label }}
                </button>
            </nav>
        </div>

        <div v-if="!busy && tab === 'general'" class="grid gap-6 lg:grid-cols-3">
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6">
                <h3 class="font-semibold text-slate-800">Site</h3>
                <dl class="mt-4 space-y-3 text-sm">
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Type</dt>
                        <dd class="text-slate-800">{{ site.type_label }}</dd>
                    </div>
                    <div
                        v-if="site.aliases.length"
                        class="flex justify-between gap-4"
                    >
                        <dt class="text-slate-500">Aliases</dt>
                        <dd class="text-right text-slate-800">
                            {{ site.aliases.join(', ') }}
                        </dd>
                    </div>
                    <div v-if="!site.is_proxied" class="flex justify-between gap-4">
                        <dt class="text-slate-500">PHP</dt>
                        <dd class="text-slate-800">{{ site.php_version }}</dd>
                    </div>
                    <div v-else class="flex justify-between gap-4">
                        <dt class="text-slate-500">App port</dt>
                        <dd class="text-slate-800">{{ site.app_port }}</dd>
                    </div>
                    <div v-if="site.service_name" class="flex justify-between gap-4">
                        <dt class="text-slate-500">Service</dt>
                        <dd class="break-all text-right text-xs text-slate-700">
                            {{ site.service_name }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">SSL</dt>
                        <dd class="text-slate-800">
                            {{ site.ssl ? 'Let’s Encrypt' : 'none' }}
                        </dd>
                    </div>
                    <div v-if="site.repository" class="flex justify-between gap-4">
                        <dt class="text-slate-500">Repository</dt>
                        <dd class="break-all text-right text-xs text-slate-700">
                            {{ site.repository }} ({{ site.branch }})
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Document root</dt>
                        <dd class="break-all text-right text-slate-800">
                            {{ site.document_root }}
                        </dd>
                    </div>
                </dl>
            </div>

            <!--
                The login the panel generated when it installed the
                application, which nobody else has a copy of. It used to be a
                line under the live card saying the password was "below", and
                then further down the page — so the page said where the thing
                was instead of being where it was.
            -->
            <div
                v-if="site.app"
                class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6"
            >
                <h3 class="font-semibold text-slate-800">{{ site.app.label }}</h3>

                <dl class="mt-4 space-y-3 text-sm">
                    <div v-if="!site.app.token" class="flex justify-between gap-4">
                        <dt class="text-slate-500">Username</dt>
                        <dd class="break-all text-right font-mono text-slate-800">
                            {{ site.app.user }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">
                            {{ site.app.token ? 'Token' : 'Password' }}
                        </dt>
                        <dd class="min-w-0 break-all text-right">
                            <span v-if="showSecrets" class="font-mono text-slate-800">{{
                                site.app.password
                            }}</span>
                            <button
                                v-else
                                @click="showSecrets = true"
                                class="text-brand-600 hover:underline"
                            >
                                reveal
                            </button>
                        </dd>
                    </div>
                </dl>

                <a
                    :href="
                        site.type === 'wordpress'
                            ? route('sites.wp-admin', site.id)
                            : site.app.url
                    "
                    target="_blank"
                    rel="noopener"
                    class="mt-4 inline-block rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white transition hover:bg-slate-700"
                >
                    {{ site.type === 'wordpress' ? 'WP Admin' : `Open ${site.type_label}` }}
                </a>

            </div>

            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-6">
                <h3 class="font-semibold text-slate-800">Danger zone</h3>
                <p class="mt-2 text-sm text-slate-500">
                    Deleting removes the vhost, the systemd service, the site
                    database created by the panel, and its DNS records.
                </p>
                <label class="mt-4 flex items-center gap-2 text-sm">
                    <input
                        type="checkbox"
                        v-model="deleteFiles"
                        class="rounded border-slate-300 text-brand-500 focus:ring-brand-500"
                    />
                    Also delete files in {{ site.root_path }}
                </label>
                <button
                    @click="destroy"
                    class="mt-4 w-full rounded-md bg-rose-600 px-4 py-2 text-sm font-medium text-white hover:bg-rose-700"
                >
                    Delete this site
                </button>
            </div>
        </div>

        <div v-if="!busy && tab === 'database'" class="grid gap-6 lg:grid-cols-3">
            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5 lg:col-span-2">
                <h3 class="font-semibold text-slate-800">Database</h3>

                <div v-if="database" class="mt-4 space-y-3 text-sm">
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Engine</dt>
                        <dd class="text-slate-800">{{ database.engine_label }}</dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Database</dt>
                        <dd class="break-all text-right text-slate-800">
                            {{ database.name }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">User</dt>
                        <dd class="break-all text-right text-slate-800">
                            {{ database.username }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Status</dt>
                        <dd><StatusBadge :status="database.status" /></dd>
                    </div>
                </div>

                <p v-else class="mt-4 text-sm text-slate-500">
                    This kind of site does not use a database, and the panel did
                    not create one for it.
                </p>

                <!--
                    The user, the password and the connection details live on
                    the database's own page, which is also where the password is
                    changed. Two places to manage one database is one place too
                    many.
                -->
                <Link
                    v-if="database"
                    :href="route('databases.show', database.id)"
                    class="mt-5 inline-block rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
                >
                    Credentials and users
                </Link>
                <Link
                    v-else
                    :href="route('databases.index')"
                    class="mt-5 inline-block text-sm text-brand-600 hover:underline"
                >
                    All databases
                </Link>
            </div>
        </div>

        <div
            v-if="!busy && tab === 'settings'"
            class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <h3 class="font-semibold text-slate-800">Root directory</h3>
            <p class="mt-2 text-sm text-slate-500">
                The folder inside the site that nginx serves. A Laravel site
                serves <code>public</code>; a plain HTML site serves the site
                directory itself, which is this box left empty.
            </p>

            <div class="mt-4 flex flex-wrap items-center gap-2">
                <span class="font-mono text-sm text-slate-500">{{ site.root_path }}/</span>
                <input
                    v-model="webDirectory"
                    type="text"
                    placeholder="public"
                    class="w-56 rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                />
                <button
                    @click="saveDirectory"
                    class="rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
                >
                    Save
                </button>
            </div>

            <p class="mt-3 text-xs text-slate-500">
                The folder is created if it is not there, and nginx is reloaded
                — after being asked whether it will take the new configuration,
                and given the old one back if it will not.
            </p>
        </div>

        <div
            v-if="!busy && tab === 'php'"
            class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <h3 class="font-semibold text-slate-800">PHP settings</h3>
            <p class="mt-2 text-sm text-slate-500">
                For this site only, passed to PHP-FPM with every request. Left
                blank, each takes the value PHP itself was configured with.
            </p>

            <div class="mt-5 grid gap-5 sm:grid-cols-2">
                <div v-for="directive in phpDirectives" :key="directive.key">
                    <label class="block text-sm font-medium text-slate-700">
                        {{ directive.label }}
                    </label>
                    <input
                        v-model="phpValues[directive.key]"
                        type="text"
                        :placeholder="directive.default"
                        class="mt-1 w-full rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                    />
                    <p class="mt-1 text-xs text-slate-500">{{ directive.help }}</p>
                </div>
            </div>

            <button
                @click="savePHP"
                class="mt-6 rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
            >
                Apply
            </button>

            <p class="mt-3 text-xs text-slate-500">
                An upload also has to fit inside the maximum POST size, and
                nginx has its own limit on top of both — this panel sets that to
                128 MB for every site.
            </p>
        </div>

        <!--
            What was issued, read off the certificate on disk rather than from
            what the site asked for. The two differ exactly when somebody comes
            to this tab.
        -->
        <div
            v-if="!busy && tab === 'ssl'"
            class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <h3 class="font-semibold text-slate-800">SSL/TLS</h3>

            <!--
                A site behind Cloudflare's proxy on Flexible has no certificate
                here on purpose and is served over HTTPS all the same. Without
                this the tab said "None yet" about a site that is working, and
                sent people looking for a fault.
            -->
            <p
                v-if="certificate.edge"
                class="mt-3 rounded-xl bg-emerald-50 px-4 py-3 text-sm text-emerald-800 ring-1 ring-emerald-600/20"
            >
                Cloudflare is serving this site's certificate. Its proxy answers
                visitors over HTTPS and fetches from this server over HTTP, so
                there is nothing to issue here and the deployment skips the step.
                Switch the zone to Full in Cloudflare and redeploy if you want
                the hop between Cloudflare and this server encrypted too.
            </p>

            <dl class="mt-4 space-y-3 text-sm">
                <div class="flex justify-between gap-4">
                    <dt class="text-slate-500">Certificate</dt>
                    <dd class="text-slate-800">
                        <template v-if="certificate.issued">Issued</template>
                        <template v-else-if="certificate.edge">
                            Cloudflare's
                        </template>
                        <template v-else>None yet</template>
                    </dd>
                </div>
                <div v-if="certificate.expires" class="flex justify-between gap-4">
                    <dt class="text-slate-500">Expires</dt>
                    <dd class="text-right text-slate-800">{{ certificate.expires }}</dd>
                </div>
                <div v-if="certificate.issuer" class="flex justify-between gap-4">
                    <dt class="text-slate-500">Issuer</dt>
                    <dd class="break-all text-right text-xs text-slate-700">
                        {{ certificate.issuer }}
                    </dd>
                </div>
                <div class="flex justify-between gap-4">
                    <dt class="text-slate-500">Serving over</dt>
                    <dd class="text-slate-800">
                        {{ site.ssl || certificate.edge ? 'HTTPS' : 'HTTP' }}
                    </dd>
                </div>
            </dl>

            <p
                v-if="!certificate.certbot"
                class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-xs text-amber-800"
            >
                Certbot is not installed, so no certificate can be issued.
                Install it from Apps first.
            </p>

            <button
                v-else
                @click="post('sites.certificate')"
                class="mt-5 rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
            >
                {{ certificate.issued ? 'Renew the certificate' : 'Issue a certificate' }}
            </button>

            <p class="mt-3 text-xs text-slate-500">
                Certbot is asked for this site alone — no rebuild, no
                redeployment. It needs the domain to resolve to this server
                first; the Logs tab has the run.
            </p>
        </div>

        <!--
            Two columns of the same shape: an SSH account and an SFTP account
            are the same kind of thing to whoever has to hand one out, and
            reading down one column should answer every question about that
            kind — who has one, how to make one, what to type to use it.
        -->
        <!--
            The page cache. nginx keeps what the application built and hands
            the copy to the next visitor, so the second request for a page
            never reaches PHP at all — which is most of the difference between
            a site that survives being linked to and one that does not.
        -->
        <div v-if="!busy && tab === 'cache'" class="grid gap-6 lg:grid-cols-3">
            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5 lg:col-span-2">
                <h3 class="font-semibold text-slate-800">Page cache</h3>

                <template v-if="cache.supported || cache.redis_supported">
                    <p class="mt-2 text-sm text-slate-500">
                        Two caches, and they are not the same thing. One keeps
                        the finished page; the other keeps what the site looked
                        up while building it.
                    </p>

                    <div class="mt-5 space-y-3">
                        <!--
                            Radio buttons, not two switches. Running both means
                            the page cache answers first and the object cache
                            never gets asked — so the second one would be a
                            setting that quietly did nothing.

                            What each one is, and nothing else: the lifetime is
                            one field below, because it is one field whichever
                            of the two is picked, and a form that moves its own
                            controls around as you answer it is a form you have
                            to re-read.
                        -->
                        <label
                            class="flex cursor-pointer items-start gap-3 rounded-xl border p-4 transition"
                            :class="
                                cacheMode === 'off'
                                    ? 'border-brand-300 bg-brand-50/60'
                                    : 'border-slate-200 hover:bg-slate-50'
                            "
                        >
                            <input
                                type="radio"
                                value="off"
                                v-model="cacheMode"
                                class="mt-0.5 border-slate-300 text-brand-500 focus:ring-brand-500"
                            />
                            <span>
                                <span class="font-medium text-slate-800">No cache</span>
                                <span class="block text-xs text-slate-500">
                                    Every request is built from scratch. Right
                                    while a site is being worked on, and for one
                                    that changes on every view.
                                </span>
                            </span>
                        </label>

                        <label
                            v-if="cache.supported"
                            class="flex cursor-pointer items-start gap-3 rounded-xl border p-4 transition"
                            :class="
                                cacheMode === 'nginx'
                                    ? 'border-brand-300 bg-brand-50/60'
                                    : 'border-slate-200 hover:bg-slate-50'
                            "
                        >
                            <input
                                type="radio"
                                value="nginx"
                                v-model="cacheMode"
                                class="mt-0.5 border-slate-300 text-brand-500 focus:ring-brand-500"
                            />
                            <span class="min-w-0">
                                <span class="font-medium text-slate-800">
                                    nginx page cache
                                </span>
                                <span class="block text-xs text-slate-500">
                                    nginx stores the page
                                    {{ cache.proxied ? 'the application' : 'PHP' }}
                                    built and serves that copy to whoever asks
                                    next, so the second request never reaches
                                    the site at all. The fastest of the two, and
                                    the one with rules about who may not have it.
                                </span>
                            </span>
                        </label>

                        <label
                            v-if="cache.redis_supported"
                            class="flex items-start gap-3 rounded-xl border p-4 transition"
                            :class="[
                                cacheMode === 'redis'
                                    ? 'border-brand-300 bg-brand-50/60'
                                    : 'border-slate-200 hover:bg-slate-50',
                                cache.redis_installed
                                    ? 'cursor-pointer'
                                    : 'cursor-not-allowed opacity-60',
                            ]"
                        >
                            <input
                                type="radio"
                                value="redis"
                                v-model="cacheMode"
                                :disabled="!cache.redis_installed"
                                class="mt-0.5 border-slate-300 text-brand-500 focus:ring-brand-500"
                            />
                            <span class="min-w-0">
                                <span class="font-medium text-slate-800">
                                    Redis object cache
                                </span>
                                <span class="block text-xs text-slate-500">
                                    WordPress keeps what it looked up — options,
                                    queries, transients — in Redis instead of
                                    asking MariaDB the same questions on every
                                    view. Every visitor still gets their own
                                    page, so this is the one for a shop, a
                                    members' area, or anything behind a login.
                                </span>
                                <span
                                    v-if="!cache.redis_installed"
                                    class="mt-2 block text-xs font-medium text-amber-700"
                                >
                                    Redis is not installed on this machine.
                                    Install it from the Apps page first.
                                </span>
                                <span
                                    v-else
                                    class="mt-2 block text-xs text-slate-500"
                                >
                                    The panel installs the Redis Object Cache
                                    plugin, points wp-config.php at Redis with
                                    the password it was configured with, and
                                    installs the drop-in. This site's keys are
                                    prefixed
                                    <span class="font-mono">{{ cache.redis_prefix }}</span>
                                    so they cannot be read by another site.
                                </span>
                            </span>
                        </label>
                    </div>

                    <!--
                        One field, whichever cache is on — but it is not the
                        same clock in the two cases, so the label and the
                        sentence under it change and the field does not move.
                    -->
                    <div v-if="cacheMode !== 'off'" class="mt-6">
                        <label class="block text-sm font-medium text-slate-700">
                            {{
                                cacheMode === 'redis'
                                    ? 'Keep an entry at most'
                                    : 'Keep a page for'
                            }}
                        </label>
                        <select
                            v-model="cacheTTL"
                            class="mt-1 w-56 rounded-xl border-slate-300 text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        >
                            <option
                                v-for="entry in cache.lifetimes"
                                :key="entry.value"
                                :value="entry.value"
                            >
                                {{ entry.label }}
                            </option>
                        </select>

                        <!--
                            Forever is the honest name for it, and the honest
                            warning with it: nothing on a clock will ever clear
                            this, so publishing a change and not pressing the
                            button means the change is not on the site.
                        -->
                        <p
                            v-if="cacheTTL === 'forever'"
                            class="mt-2 text-xs text-amber-700"
                        >
                            Nothing expires this. A page stays as it was built
                            until you press Empty the cache — right for a site
                            that changes when you say so, and wrong for one that
                            changes on its own. What you publish will not be on
                            the site until the cache is emptied.
                            <template v-if="cacheMode === 'nginx'">
                                The 256 MB the panel allows this site is still a
                                ceiling: past it, nginx drops the pages nobody
                                has asked for lately.
                            </template>
                            <template v-else>
                                WordPress still replaces an entry itself as soon
                                as what it holds changes, so the site stays
                                correct — this only removes the backstop for
                                keys nothing invalidates.
                            </template>
                        </p>
                        <p
                            v-else-if="cacheMode === 'redis'"
                            class="mt-2 text-xs text-slate-500"
                        >
                            A ceiling, not a refresh interval. WordPress
                            replaces an entry as soon as what it holds changes —
                            this is how long one may sit in Redis untouched if
                            nothing ever does.
                        </p>
                        <p v-else class="mt-2 text-xs text-slate-500">
                            How long before nginx asks the site to build the
                            page again. An expired page is still served
                            immediately and refreshed behind the visitor, so
                            nobody waits for it — and Empty the cache is there
                            for when you have just published something.
                        </p>
                    </div>

                    <div class="mt-6 flex flex-wrap items-center gap-2">
                        <button
                            @click="saveCache"
                            :disabled="cacheWorking"
                            class="rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white transition hover:bg-slate-700 disabled:opacity-50"
                        >
                            {{ cacheWorking ? 'Applying…' : 'Apply' }}
                        </button>
                        <button
                            @click="purgeCache"
                            :disabled="cacheWorking"
                            class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                        >
                            Empty the cache
                        </button>
                    </div>

                    <p class="mt-3 text-xs text-slate-500">
                        nginx is asked whether it will take the new
                        configuration before it is reloaded, and given the old
                        one back if it will not.
                    </p>
                </template>

                <!--
                    A tab that says why there is no cache, rather than no tab.
                    A missing tab is a question; this is an answer.
                -->
                <p v-else class="mt-2 text-sm text-slate-500">
                    The panel does not cache a
                    {{ site.type_label }} site.
                    <template v-if="site.type === 'static'">
                        Its pages are already files nginx reads off disk — a
                        cache in front of that is a second copy of the same
                        file, and the browser caching rules the panel writes
                        are doing the part that helps.
                    </template>
                    <template v-else>
                        Every page it serves is built for whoever asked for it,
                        and its own manual says not to put a page cache in
                        front of it.
                    </template>
                </p>
            </div>

            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-800">
                    {{ cache.mode === 'redis' ? 'About the object cache' : 'What is not cached' }}
                </h3>

                <template v-if="cache.mode === 'redis'">
                    <ul class="mt-4 space-y-3 text-sm text-slate-600">
                        <li>
                            Every page is still built for the visitor who asked
                            for it, so nothing personal is ever served to
                            somebody else.
                        </li>
                        <li>
                            What is kept is what WordPress looked up on the way:
                            options, meta, and the results of queries it marks
                            as cacheable.
                        </li>
                        <li>
                            Keys are prefixed
                            <span class="font-mono">{{ cache.redis_prefix }}</span
                            >, so Empty the cache clears this site and leaves
                            every other site on the machine alone.
                        </li>
                        <li>
                            An entry lives until WordPress replaces it, or until
                            the ceiling on the left, whichever comes first. It
                            is a backstop for keys nothing invalidates, not a
                            refresh interval.
                        </li>
                    </ul>
                </template>

                <template v-else>

                <ul class="mt-4 space-y-3 text-sm text-slate-600">
                    <li>Anything posted — every form, every comment, every login.</li>
                    <li>Any request carrying a session cookie, so a signed-in visitor always sees their own page.</li>
                    <li>Anything with a query string, and anything sent with an Authorization header.</li>
                    <li v-if="site.type === 'wordpress'">
                        wp-admin, the REST API, feeds, and the cart, checkout
                        and account pages a shop needs to be personal.
                    </li>
                </ul>

                <p class="mt-4 text-xs text-slate-500">
                    A response says which it was:
                    <span class="font-mono">X-Panel-Cache: HIT</span> came from
                    the cache, <span class="font-mono">MISS</span> was built and
                    stored, <span class="font-mono">BYPASS</span> was one of the
                    above.
                </p>
                </template>

                <dl class="mt-5 space-y-3 border-t border-slate-200 pt-4 text-sm">
                    <div class="flex justify-between gap-4">
                        <dt class="text-slate-500">Cache</dt>
                        <dd class="text-slate-800">
                            {{
                                cache.mode === 'nginx'
                                    ? 'nginx page cache'
                                    : cache.mode === 'redis'
                                      ? 'Redis object cache'
                                      : 'None'
                            }}
                        </dd>
                    </div>
                    <div v-if="cache.mode !== 'off'" class="flex justify-between gap-4">
                        <dt class="text-slate-500">
                            {{ cache.mode === 'redis' ? 'At most' : 'Lifetime' }}
                        </dt>
                        <dd class="text-slate-800">{{ cache.ttl }}</dd>
                    </div>
                    <div v-if="cache.mode !== 'redis'" class="min-w-0">
                        <dt class="text-slate-500">Stored in</dt>
                        <dd class="mt-1 break-all font-mono text-xs text-slate-700">
                            {{ cache.path }}
                        </dd>
                    </div>
                    <div v-else class="min-w-0">
                        <dt class="text-slate-500">Stored in</dt>
                        <dd class="mt-1 break-all font-mono text-xs text-slate-700">
                            redis://127.0.0.1:6379 · {{ cache.redis_prefix }}*
                        </dd>
                    </div>
                </dl>
            </div>
        </div>

        <div v-if="!busy && tab === 'access'">
            <p class="mb-5 text-sm text-slate-500">
                Real system accounts whose home is this site's directory. They
                belong to <span class="font-mono">www-data</span>, so what they
                upload is what the web server can read.
            </p>

            <div class="grid items-start gap-6 lg:grid-cols-2">
                <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                    <h3 class="font-semibold text-slate-800">SSH accounts</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        A real shell on this server, which SFTP works over too.
                        The commands it runs are the machine's.
                    </p>

                    <ul class="mt-4 divide-y divide-slate-100 text-sm">
                        <li
                            v-for="user in sshAccounts"
                            :key="user.name"
                            class="flex flex-wrap items-center justify-between gap-2 py-3"
                        >
                            <div class="min-w-0">
                                <p class="break-all font-mono text-slate-800">
                                    {{ user.name }}
                                </p>
                                <p class="break-all font-mono text-xs text-slate-500">
                                    {{ user.home }}
                                </p>
                            </div>
                            <div class="flex shrink-0 items-center gap-3">
                                <button
                                    @click="changeAccountPassword(user)"
                                    :disabled="accountWorking"
                                    class="text-xs font-medium text-brand-600 hover:underline disabled:opacity-50"
                                >
                                    Password
                                </button>
                                <button
                                    @click="removeAccount(user)"
                                    :disabled="accountWorking"
                                    class="text-xs font-medium text-rose-600 hover:underline disabled:opacity-50"
                                >
                                    Remove
                                </button>
                            </div>
                        </li>
                        <li v-if="!sshAccounts.length" class="py-3 text-slate-500">
                            No account has a shell here.
                        </li>
                    </ul>

                    <div class="mt-5 border-t border-slate-200 pt-4">
                        <p class="text-xs font-medium uppercase tracking-wide text-slate-400">
                            Add an SSH account
                        </p>
                        <input
                            v-model="forms.ssh.name"
                            type="text"
                            placeholder="username"
                            class="mt-3 w-full rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        />
                        <input
                            v-model="forms.ssh.password"
                            type="text"
                            autocomplete="off"
                            placeholder="Leave blank to generate a password"
                            class="mt-2 w-full rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        />
                        <button
                            @click="addAccount('ssh')"
                            :disabled="accountWorking || !forms.ssh.name.trim()"
                            class="mt-3 rounded-xl bg-slate-900 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-slate-700 disabled:opacity-50"
                        >
                            Add SSH account
                        </button>
                    </div>

                </div>

                <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                    <h3 class="font-semibold text-slate-800">SFTP accounts</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        Files in and out of this site, and nothing else. No
                        shell, no commands.
                    </p>

                    <ul class="mt-4 divide-y divide-slate-100 text-sm">
                        <li
                            v-for="user in sftpAccounts"
                            :key="user.name"
                            class="flex flex-wrap items-center justify-between gap-2 py-3"
                        >
                            <div class="min-w-0">
                                <p class="break-all font-mono text-slate-800">
                                    {{ user.name }}
                                </p>
                                <p class="break-all font-mono text-xs text-slate-500">
                                    {{ user.home }}
                                </p>
                            </div>
                            <div class="flex shrink-0 items-center gap-3">
                                <button
                                    @click="changeAccountPassword(user)"
                                    :disabled="accountWorking"
                                    class="text-xs font-medium text-brand-600 hover:underline disabled:opacity-50"
                                >
                                    Password
                                </button>
                                <button
                                    @click="removeAccount(user)"
                                    :disabled="accountWorking"
                                    class="text-xs font-medium text-rose-600 hover:underline disabled:opacity-50"
                                >
                                    Remove
                                </button>
                            </div>
                        </li>
                        <li v-if="!sftpAccounts.length" class="py-3 text-slate-500">
                            No SFTP account for this site yet.
                        </li>
                    </ul>

                    <div class="mt-5 border-t border-slate-200 pt-4">
                        <p class="text-xs font-medium uppercase tracking-wide text-slate-400">
                            Add an SFTP account
                        </p>
                        <input
                            v-model="forms.sftp.name"
                            type="text"
                            placeholder="username"
                            class="mt-3 w-full rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        />
                        <input
                            v-model="forms.sftp.password"
                            type="text"
                            autocomplete="off"
                            placeholder="Leave blank to generate a password"
                            class="mt-2 w-full rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        />
                        <button
                            @click="addAccount('sftp')"
                            :disabled="accountWorking || !forms.sftp.name.trim()"
                            class="mt-3 rounded-xl bg-slate-900 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-slate-700 disabled:opacity-50"
                        >
                            Add SFTP account
                        </button>
                    </div>

                </div>
            </div>

            <p class="mt-4 text-xs text-slate-500">
                The password is shown once, when it is set — the panel does not
                store it, and there is nowhere to read it again.
            </p>
        </div>

        <div
            v-if="!busy && tab === 'cron'"
            class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h3 class="font-semibold text-slate-800">Cron jobs</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        Run as <span class="font-mono">www-data</span>, from
                        <span class="font-mono">{{ site.root_path }}</span>.
                    </p>
                </div>
                <button
                    @click="addJob"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                >
                    Add a job
                </button>
            </div>

            <div v-if="jobs.length" class="mt-5 space-y-4">
                <div
                    v-for="(job, index) in jobs"
                    :key="index"
                    class="rounded-xl border border-slate-200 p-4"
                >
                    <div class="flex flex-wrap items-center gap-2">
                        <select
                            :value="presetFor(job)"
                            @change="pickPreset(job, $event.target.value)"
                            class="w-56 rounded-xl border-slate-300 text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        >
                            <option
                                v-for="entry in SCHEDULES"
                                :key="entry.value"
                                :value="entry.value"
                            >
                                {{ entry.label }}
                            </option>
                            <option value="custom">Custom…</option>
                        </select>

                        <input
                            v-model="job.command"
                            type="text"
                            placeholder="php artisan schedule:run"
                            class="min-w-0 flex-1 rounded-xl border-slate-300 font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                        />

                        <button
                            @click="removeJob(index)"
                            class="px-2 py-2 text-sm text-rose-600 hover:underline"
                        >
                            Remove
                        </button>
                    </div>

                    <!--
                        The five fields, under the menu rather than instead of
                        it. A preset is a way to fill them in, not a different
                        mode: pick "Every day at 3am", change the hour, and the
                        row goes on being a schedule rather than becoming a
                        blank box to retype.
                    -->
                    <div class="mt-3 flex flex-wrap items-end gap-2">
                        <div v-for="(entry, position) in FIELDS" :key="entry.label">
                            <label
                                class="block text-[11px] uppercase tracking-wide text-slate-400"
                            >
                                {{ entry.label }}
                            </label>
                            <input
                                :value="field(job, position)"
                                @input="setField(job, position, $event.target.value)"
                                type="text"
                                placeholder="*"
                                class="mt-1 w-24 rounded-xl border-slate-300 text-center font-mono text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                            />
                        </div>

                        <p class="ml-1 pb-2 text-xs text-slate-500">
                            {{ describe(job.schedule) }}
                            <span class="ml-1 font-mono text-slate-400">{{
                                job.schedule
                            }}</span>
                        </p>
                    </div>
                </div>
            </div>

            <p v-else class="mt-5 text-sm text-slate-500">
                Nothing scheduled for this site.
            </p>

            <button
                @click="saveJobs"
                class="mt-6 rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
            >
                Save the schedule
            </button>

            <p class="mt-3 text-xs text-slate-500">
                The whole list is written at once, so removing a row here
                removes the line on the machine.
            </p>
        </div>

        <!--
            Laid out the way a desktop file manager is: a path bar, a toolbar
            that acts on what is picked, striped rows, and one click to select
            with two to open. The panel's other tables are lists of records —
            this one is a folder, and it reads better as one.
        -->
        <div
            v-if="!busy && tab === 'files'"
            class="flex h-[calc(100vh-19rem)] min-h-[26rem] flex-col overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
            @dragover.prevent="dragging = true"
            @dragleave.prevent="dragging = false"
            @drop.prevent="onDrop"
        >
            <div class="shrink-0 border-b border-slate-200 bg-slate-50/80 px-5 py-3">
                <nav class="flex flex-wrap items-center gap-1 text-sm text-slate-500">
                    <template v-for="(crumb, index) in crumbs" :key="crumb.path">
                        <span v-if="index" class="text-slate-300">›</span>
                        <button
                            @click="loadFiles(crumb.path)"
                            class="rounded px-1.5 py-0.5 transition hover:bg-slate-200/70 hover:text-slate-800"
                            :class="
                                index === crumbs.length - 1
                                    ? 'font-medium text-slate-800'
                                    : ''
                            "
                        >
                            {{ crumb.name }}
                        </button>
                    </template>
                </nav>

                <div class="mt-3 flex flex-wrap items-center gap-2">
                    <button
                        @click="uploadInput?.click()"
                        :disabled="uploading"
                        class="rounded-lg bg-brand-600 px-3 py-1.5 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                    >
                        {{ uploading ? 'Uploading…' : 'Upload' }}
                    </button>
                    <input
                        ref="uploadInput"
                        type="file"
                        multiple
                        class="hidden"
                        @change="uploadFiles($event.target.files)"
                    />

                    <button
                        @click="newFolder"
                        class="rounded-lg bg-white px-3 py-1.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    >
                        New folder
                    </button>
                    <button
                        @click="newFile"
                        class="rounded-lg bg-white px-3 py-1.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    >
                        New file
                    </button>

                    <span class="mx-1 h-5 w-px bg-slate-200" />

                    <a
                        v-if="onlySelected"
                        :href="downloadURL(onlySelected)"
                        class="rounded-lg bg-white px-3 py-1.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    >
                        Download
                    </a>
                    <button
                        v-if="onlySelected?.archive"
                        @click="fileAction('unzip', onlySelected.path)"
                        class="rounded-lg bg-white px-3 py-1.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    >
                        Extract
                    </button>
                    <button
                        v-if="onlySelected"
                        @click="renameEntry(onlySelected)"
                        class="rounded-lg bg-white px-3 py-1.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    >
                        Rename
                    </button>
                    <button
                        @click="zipSelected"
                        :disabled="!selected.length"
                        class="rounded-lg bg-white px-3 py-1.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-40"
                    >
                        Compress
                    </button>
                    <button
                        @click="deleteSelected"
                        :disabled="!selected.length"
                        class="rounded-lg bg-white px-3 py-1.5 text-sm font-medium text-rose-600 ring-1 ring-slate-900/10 transition hover:bg-rose-50 disabled:opacity-40"
                    >
                        Delete
                    </button>

                    <span v-if="selected.length" class="ml-auto text-xs text-slate-500">
                        {{ selected.length }} selected
                    </span>
                </div>
            </div>

            <p
                v-if="dragging"
                class="shrink-0 border-b border-brand-200 bg-brand-50 px-5 py-2.5 text-sm text-brand-800"
            >
                Drop the files to upload them into {{ files.path }}
            </p>

            <p
                v-if="files.error"
                class="shrink-0 border-b border-rose-100 bg-rose-50 px-5 py-2.5 text-sm text-rose-700"
            >
                {{ files.error }}
            </p>

            <!--
                The tree and the listing scroll separately inside a card of a
                fixed height, so a folder with two thousand files in it scrolls
                where it is rather than making the whole page long — and the
                path bar and the toolbar stay where they were put.
            -->
            <div class="flex min-h-0 flex-1">
                <div
                    class="hidden w-60 shrink-0 overflow-auto border-r border-slate-200 bg-slate-50/40 px-2 py-3 md:block"
                >
                    <FolderTree
                        :nodes="tree"
                        :current="files.path"
                        @open="loadFiles"
                        @toggle="toggleBranch"
                    />
                </div>

                <div class="min-w-0 flex-1 overflow-auto">
                    <p v-if="filesLoading" class="px-5 py-10 text-sm text-slate-500">
                        Reading…
                    </p>

                    <table v-else class="min-w-full text-sm">
                        <!--
                            The listing scrolls under its own headings, which
                            is the only way a column is still labelled four
                            hundred rows down.
                        -->
                        <thead class="sticky top-0 z-10 bg-white">
                            <tr
                                class="border-b border-slate-200 text-left text-xs font-medium text-slate-500"
                            >
                                <th class="px-5 py-2 font-medium">Name</th>
                                <th class="w-28 px-3 py-2 font-medium">Size</th>
                                <th class="w-44 px-3 py-2 font-medium">Date modified</th>
                                <th class="w-32 px-5 py-2 font-medium">Permissions</th>
                            </tr>
                        </thead>
                    <tbody>
                        <tr v-if="files.path !== '/'">
                            <td colspan="4" class="px-5 py-1.5">
                                <button
                                    @click="loadFiles(parentPath)"
                                    class="text-xs text-slate-500 hover:text-brand-600"
                                >
                                    ‹ back
                                </button>
                            </td>
                        </tr>
                        <tr
                            v-for="(entry, index) in files.entries"
                            :key="entry.path"
                            @click="selectEntry(entry, $event)"
                            @dblclick="openEntry(entry)"
                            class="cursor-default select-none"
                            :class="
                                isSelected(entry)
                                    ? 'bg-brand-600 text-white'
                                    : index % 2
                                      ? 'bg-slate-50/70 hover:bg-slate-100'
                                      : 'hover:bg-slate-100'
                            "
                        >
                            <td class="px-5 py-1.5">
                                <span class="flex items-center gap-2">
                                    <FileIcon
                                        :name="entry.name"
                                        :directory="entry.directory"
                                        :archive="entry.archive"
                                        :class="isSelected(entry) ? 'text-white' : ''"
                                    />
                                    <span class="break-all">{{ entry.name }}</span>
                                </span>
                            </td>
                            <td
                                class="whitespace-nowrap px-3 py-1.5"
                                :class="isSelected(entry) ? 'text-white/80' : 'text-slate-500'"
                            >
                                {{ entry.directory ? '—' : humanSize(entry.size) }}
                            </td>
                            <td
                                class="whitespace-nowrap px-3 py-1.5"
                                :class="isSelected(entry) ? 'text-white/80' : 'text-slate-500'"
                            >
                                {{ entry.modified }}
                            </td>
                            <td
                                class="whitespace-nowrap px-5 py-1.5 font-mono text-xs"
                                :class="isSelected(entry) ? 'text-white/80' : 'text-slate-400'"
                            >
                                {{ entry.mode }}
                            </td>
                        </tr>
                        <tr v-if="!files.entries.length && !files.error">
                            <td
                                colspan="4"
                                class="px-5 py-20 text-center text-sm text-slate-500"
                            >
                                This folder is empty. Drop files anywhere here to upload
                                them.
                            </td>
                        </tr>
                    </tbody>
                    </table>
                </div>
            </div>

            <p
                class="shrink-0 border-t border-slate-200 bg-slate-50/80 px-5 py-2 text-xs text-slate-500"
            >
                {{ files.entries.length }}
                {{ files.entries.length === 1 ? 'item' : 'items' }} · double-click to
                open, ⌘ or shift to pick more than one
            </p>
        </div>

        <!-- The editor, for the text files a site is actually configured by. -->
        <div
            v-if="editing"
            class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4"
            @click.self="editing = null"
        >
            <div
                class="flex max-h-[85vh] w-full max-w-4xl flex-col rounded-2xl bg-white p-6 shadow-xl ring-1 ring-slate-900/10"
            >
                <div class="flex items-start justify-between gap-4">
                    <h3 class="break-all font-mono text-sm font-medium text-slate-800">
                        {{ editing.path }}
                    </h3>
                    <button
                        @click="editing = null"
                        class="rounded-lg p-1 text-slate-400 hover:bg-slate-100 hover:text-slate-600"
                        aria-label="Close"
                    >
                        ✕
                    </button>
                </div>

                <p v-if="editing.loading" class="mt-6 text-sm text-slate-500">Opening…</p>

                <p
                    v-else-if="editing.error"
                    class="mt-6 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800"
                >
                    {{ editing.error }}
                </p>

                <template v-else>
                    <textarea
                        v-model="editing.contents"
                        spellcheck="false"
                        class="mt-4 min-h-[50vh] flex-1 rounded-xl border-slate-300 font-mono text-xs shadow-sm focus:border-brand-500 focus:ring-brand-500"
                    />
                    <div class="mt-4 flex items-center justify-end gap-2">
                        <button
                            @click="editing = null"
                            class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                        >
                            Cancel
                        </button>
                        <button
                            @click="saveFile"
                            class="rounded-xl bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
                        >
                            Save
                        </button>
                    </div>
                </template>
            </div>
        </div>

        <div v-if="!busy && tab === 'logs'">
            <!--
                nginx writes two files for every site the panel renders a vhost
                for, and a proxied application writes a third. Those are this
                site's logs. The panel's own history of deploying it is a
                different thing and is one of the choices here rather than the
                only one.
            -->
            <div class="mb-4 flex flex-wrap items-center gap-2">
                <button
                    v-for="file in logFiles"
                    :key="file.key"
                    @click="logView = file.key"
                    class="rounded-xl px-3 py-2 text-sm font-medium transition"
                    :class="
                        logView === file.key
                            ? 'bg-slate-900 text-white'
                            : 'bg-white text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50'
                    "
                >
                    {{ file.label }}
                </button>
                <button
                    @click="logView = 'activity'"
                    class="rounded-xl px-3 py-2 text-sm font-medium transition"
                    :class="
                        logView === 'activity'
                            ? 'bg-slate-900 text-white'
                            : 'bg-white text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50'
                    "
                >
                    Panel activity
                </button>
            </div>

            <div
                v-if="shownLog"
                class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
            >
                <p class="break-all font-mono text-xs text-slate-500">
                    {{ shownLog.path }} · last 200 lines
                </p>
                <pre
                    v-if="shownLog.lines"
                    class="mt-3 max-h-[60vh] overflow-auto rounded-xl bg-slate-900 px-4 py-3 font-mono text-xs leading-relaxed text-slate-200"
                    >{{ shownLog.lines }}</pre
                >
                <p v-else class="mt-3 text-sm text-slate-500">
                    Nothing in this log yet.
                </p>
            </div>

            <div
                v-if="logView === 'activity'"
                class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
            >
                <div class="border-b border-slate-200 px-5 py-4">
                    <h3 class="font-semibold text-slate-800">Activity log</h3>
                </div>
                <ul class="divide-y divide-slate-100">
                    <li v-for="log in logs" :key="log.id">
                        <!-- The output itself is on the log's own page now, so this
                             stays a list of what happened rather than a place a
                             thousand lines of npm can unfold in. -->
                        <Link
                            :href="route('logs.show', log.id)"
                            class="flex items-start justify-between gap-3 px-5 py-3 transition hover:bg-slate-50"
                        >
                            <div class="min-w-0">
                                <p class="text-sm font-medium text-slate-800">
                                    {{ log.action }}
                                </p>
                                <p class="text-xs text-slate-500">
                                    {{ log.message }}
                                </p>
                            </div>
                            <div class="shrink-0 text-right">
                                <StatusBadge :status="log.status" />
                                <p class="mt-1 text-xs text-slate-400">
                                    {{ formatRelative(log.created_at) }}
                                </p>
                            </div>
                        </Link>
                    </li>
                    <li
                        v-if="!logs.length"
                        class="px-5 py-8 text-center text-sm text-slate-500"
                    >
                        Nothing logged yet.
                    </li>
                </ul>
            </div>
        </div>

    </AuthenticatedLayout>
</template>
