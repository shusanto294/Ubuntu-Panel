<script setup>
import { ref } from 'vue';
import Checkbox from '@/Components/Checkbox.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { confirmDanger } from '@/dialog';
import { router, useForm } from '@inertiajs/vue3';

/*
 * The S3-compatible bucket remote site backups go to. The secret key field
 * starts empty and stays that way: leaving it blank keeps the stored one. Saving
 * writes a small file to the bucket and deletes it, and storage that cannot be
 * written to is not saved.
 */
const props = defineProps({
    storage: Object,
});

const form = useForm({
    endpoint: props.storage.endpoint ?? '',
    region: props.storage.region || 'us-east-1',
    bucket: props.storage.bucket ?? '',
    prefix: props.storage.prefix ?? '',
    access_key: props.storage.access_key ?? '',
    secret_key: '',
    path_style: props.storage.configured ? props.storage.path_style : true,
});

// What each provider's endpoint looks like. Picking one fills in what it can
// and leaves the placeholders for the parts only the account knows.
const presets = [
    { key: 'aws', label: 'Amazon S3', endpoint: 'https://s3.us-east-1.amazonaws.com', region: 'us-east-1', pathStyle: false },
    { key: 'r2', label: 'Cloudflare R2', endpoint: 'https://ACCOUNT_ID.r2.cloudflarestorage.com', region: 'auto', pathStyle: true },
    { key: 'b2', label: 'Backblaze B2', endpoint: 'https://s3.us-west-004.backblazeb2.com', region: 'us-west-004', pathStyle: false },
    { key: 'wasabi', label: 'Wasabi', endpoint: 'https://s3.us-east-1.wasabisys.com', region: 'us-east-1', pathStyle: false },
    { key: 'do', label: 'DigitalOcean Spaces', endpoint: 'https://nyc3.digitaloceanspaces.com', region: 'nyc3', pathStyle: false },
    { key: 'minio', label: 'MinIO / other', endpoint: 'https://minio.example.com:9000', region: 'us-east-1', pathStyle: true },
];

const applyPreset = (key) => {
    const preset = presets.find((p) => p.key === key);
    if (!preset) return;

    form.endpoint = preset.endpoint;
    form.region = preset.region;
    form.path_style = preset.pathStyle;
};

const save = () =>
    form.patch(route('settings.backup-storage'), {
        preserveScroll: true,
        onSuccess: () => form.reset('secret_key'),
    });

const testing = ref(false);
const test = () =>
    router.post(route('settings.backup-storage.test'), {}, {
        preserveScroll: true,
        onStart: () => (testing.value = true),
        onFinish: () => (testing.value = false),
    });

const remove = async () => {
    const sites = props.storage.sites;

    if (
        !(await confirmDanger(
            'Remove remote backup storage?',
            'Backups already in the bucket are left there, but cannot be restored from the panel until the storage is added again.' +
                (sites ? ` The ${sites} site(s) using it will back up to this server instead.` : ''),
            'Remove',
        ))
    )
        return;

    router.delete(route('settings.backup-storage.destroy'), { preserveScroll: true });
};
</script>

<template>
    <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
        <h3 class="flex items-center gap-2 font-semibold text-slate-900">
            Remote backup storage (S3)
            <span
                class="rounded-full px-2 py-0.5 text-xs font-medium"
                :class="storage.configured ? 'bg-emerald-50 text-emerald-700' : 'bg-slate-100 text-slate-600'"
            >
                {{ storage.configured ? 'connected' : 'not set' }}
            </span>
        </h3>
        <p class="mt-1 text-sm text-slate-500">
            An S3-compatible bucket to keep site backups in, off this server — so losing the server
            does not lose its backups. Works with Amazon S3, Cloudflare R2, Backblaze B2, Wasabi,
            DigitalOcean Spaces, MinIO and others. Choose it per site on the site's Backups tab.
            <template v-if="storage.configured && storage.sites">
                {{ storage.sites }} site(s) back up here now.
            </template>
        </p>

        <form @submit.prevent="save" class="mt-4 grid gap-4 sm:grid-cols-2">
            <div class="sm:col-span-2">
                <InputLabel value="Provider" />
                <div class="mt-1 flex flex-wrap gap-2">
                    <button
                        v-for="preset in presets"
                        :key="preset.key"
                        type="button"
                        class="rounded-lg px-3 py-1.5 text-xs font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                        @click="applyPreset(preset.key)"
                    >
                        {{ preset.label }}
                    </button>
                </div>
                <p class="mt-1 text-xs text-slate-500">Fills in the endpoint and region; replace the parts in capitals.</p>
            </div>

            <div class="sm:col-span-2">
                <InputLabel for="s3_endpoint" value="Endpoint" />
                <TextInput id="s3_endpoint" v-model="form.endpoint" type="text" class="mt-1 block w-full font-mono text-sm" placeholder="https://s3.us-east-1.amazonaws.com" />
                <InputError class="mt-2" :message="form.errors.endpoint" />
            </div>

            <div>
                <InputLabel for="s3_bucket" value="Bucket" />
                <TextInput id="s3_bucket" v-model="form.bucket" type="text" class="mt-1 block w-full font-mono text-sm" placeholder="my-server-backups" />
                <InputError class="mt-2" :message="form.errors.bucket" />
            </div>

            <div>
                <InputLabel for="s3_region" value="Region" />
                <TextInput id="s3_region" v-model="form.region" type="text" class="mt-1 block w-full font-mono text-sm" placeholder="us-east-1" />
                <InputError class="mt-2" :message="form.errors.region" />
            </div>

            <div>
                <InputLabel for="s3_access_key" value="Access key ID" />
                <TextInput id="s3_access_key" v-model="form.access_key" type="text" class="mt-1 block w-full font-mono text-sm" autocomplete="off" />
                <InputError class="mt-2" :message="form.errors.access_key" />
            </div>

            <div>
                <InputLabel for="s3_secret_key" value="Secret access key" />
                <TextInput
                    id="s3_secret_key"
                    v-model="form.secret_key"
                    type="password"
                    class="mt-1 block w-full font-mono text-sm"
                    autocomplete="new-password"
                    :placeholder="storage.has_secret ? 'Stored — leave empty to keep' : ''"
                />
                <InputError class="mt-2" :message="form.errors.secret_key" />
            </div>

            <div class="sm:col-span-2">
                <InputLabel for="s3_prefix" value="Folder in the bucket (optional)" />
                <TextInput id="s3_prefix" v-model="form.prefix" type="text" class="mt-1 block w-full font-mono text-sm" placeholder="e.g. servers/web-1" />
                <InputError class="mt-2" :message="form.errors.prefix" />
                <p class="mt-1 text-xs text-slate-500">
                    Backups go under <span class="font-mono">{{ form.prefix ? form.prefix + '/' : '' }}example.com/…</span>
                    — a folder per server lets one bucket hold several.
                </p>
            </div>

            <label class="flex items-start gap-3 sm:col-span-2">
                <Checkbox v-model:checked="form.path_style" class="mt-0.5" />
                <span>
                    <span class="block text-sm font-medium text-slate-700">Path-style addresses</span>
                    <span class="block text-xs text-slate-500">
                        endpoint/bucket rather than bucket.endpoint. MinIO and R2 need it; the presets set it.
                    </span>
                </span>
            </label>

            <div class="flex flex-wrap items-center gap-3 sm:col-span-2">
                <PrimaryButton :disabled="form.processing">
                    {{ form.processing ? 'Checking the bucket…' : 'Save storage' }}
                </PrimaryButton>
                <button
                    v-if="storage.configured"
                    type="button"
                    class="rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 shadow-sm ring-1 ring-slate-900/10 hover:bg-slate-50 disabled:opacity-50"
                    :disabled="testing"
                    @click="test"
                >
                    {{ testing ? 'Testing…' : 'Test connection' }}
                </button>
                <button
                    v-if="storage.configured"
                    type="button"
                    class="text-sm font-medium text-rose-600 hover:underline"
                    @click="remove"
                >
                    Remove
                </button>
            </div>
        </form>
    </div>
</template>
