<script setup>
import { computed, ref, watch } from 'vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import Checkbox from '@/Components/Checkbox.vue';
import { router, useForm } from '@inertiajs/vue3';

/**
 * The records in one zone, read from the provider rather than from the panel.
 *
 * Nothing is stored here: the provider is the record of what exists, and a
 * copy in the panel's database would be wrong the first time somebody used
 * the provider's own dashboard.
 */
const props = defineProps({
    account: { type: Object, required: true },
    recordTypes: { type: Array, default: () => [] },
});

const zones = ref([]);
const zone = ref(null);
const records = ref([]);
const loadingZones = ref(false);
const loadingRecords = ref(false);
const error = ref(null);
const adding = ref(false);

const form = useForm({
    zone_id: '',
    zone_name: '',
    type: 'A',
    name: '',
    content: '',
    priority: 10,
    ttl: 0,
    proxied: false,
});

const selectedZone = computed(() =>
    zones.value.find((z) => z.id === zone.value),
);

const needsPriority = computed(() => form.type === 'MX' || form.type === 'SRV');

const loadZones = async () => {
    loadingZones.value = true;
    error.value = null;

    try {
        const { data } = await window.axios.get(route('dns.zones', props.account.id));
        zones.value = data.zones ?? [];
        zone.value = zones.value[0]?.id ?? null;
    } catch (e) {
        error.value =
            e?.response?.data?.error ?? 'Could not read the zones from the provider.';
    } finally {
        loadingZones.value = false;
    }
};

const loadRecords = async () => {
    if (!selectedZone.value) {
        records.value = [];

        return;
    }

    loadingRecords.value = true;
    error.value = null;

    try {
        const { data } = await window.axios.get(route('dns.records', props.account.id), {
            params: {
                zone_id: selectedZone.value.id,
                zone_name: selectedZone.value.name,
            },
        });
        records.value = data.records ?? [];
    } catch (e) {
        records.value = [];
        error.value =
            e?.response?.data?.error ?? 'Could not read the records from the provider.';
    } finally {
        loadingRecords.value = false;
    }
};

watch(zone, loadRecords);

const submit = () => {
    if (!selectedZone.value) return;

    form.zone_id = selectedZone.value.id;
    form.zone_name = selectedZone.value.name;

    form.post(route('dns.records.store', props.account.id), {
        preserveScroll: true,
        onSuccess: () => {
            form.reset('name', 'content');
            adding.value = false;
            loadRecords();
        },
    });
};

const remove = (record) => {
    if (
        !window.confirm(
            `Delete the ${record.type} record for ${record.name}? Anything relying on it stops resolving.`,
        )
    ) {
        return;
    }

    router.delete(route('dns.records.destroy', props.account.id), {
        data: {
            zone_id: selectedZone.value.id,
            zone_name: selectedZone.value.name,
            record_id: record.id,
        },
        preserveScroll: true,
        onSuccess: loadRecords,
    });
};

// The name column reads better as the label alone; the zone is on the picker
// right above it and repeating it on every row is noise.
const shortName = (name) => {
    const suffix = '.' + (selectedZone.value?.name ?? '');

    if (name === selectedZone.value?.name) return '@';

    return name.endsWith(suffix) ? name.slice(0, -suffix.length) : name;
};

loadZones().then(loadRecords);
</script>

<template>
    <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
        <div
            class="flex flex-wrap items-end justify-between gap-3 border-b border-slate-200 px-5 py-4"
        >
            <div class="min-w-0">
                <h3 class="font-semibold text-slate-900">
                    Records — {{ account.label }}
                </h3>
                <p class="text-sm text-slate-500">
                    Read from {{ account.provider_label }} each time this opens,
                    so it is what is actually published.
                </p>
            </div>

            <div class="flex items-center gap-2">
                <select
                    v-model="zone"
                    :disabled="loadingZones || !zones.length"
                    class="rounded-lg border-slate-300 text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500 disabled:opacity-50"
                >
                    <option v-for="z in zones" :key="z.id" :value="z.id">
                        {{ z.name }}
                    </option>
                </select>
                <button
                    type="button"
                    :disabled="loadingRecords || !selectedZone"
                    class="rounded-xl px-3 py-2 text-sm text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                    @click="loadRecords"
                >
                    {{ loadingRecords ? 'Reading…' : 'Refresh' }}
                </button>
                <button
                    type="button"
                    :disabled="!selectedZone"
                    class="rounded-xl bg-brand-600 px-3 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                    @click="adding = !adding"
                >
                    {{ adding ? 'Cancel' : 'Add record' }}
                </button>
            </div>
        </div>

        <div
            v-if="error"
            class="border-b border-rose-200 bg-rose-50 px-5 py-3 text-sm text-rose-700"
        >
            {{ error }}
        </div>

        <!-- Add -->
        <form
            v-if="adding && selectedZone"
            @submit.prevent="submit"
            class="grid gap-4 border-b border-slate-200 bg-slate-50 px-5 py-4 sm:grid-cols-6"
        >
            <div>
                <InputLabel value="Type" />
                <select
                    v-model="form.type"
                    class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm focus:border-brand-500 focus:ring-brand-500"
                >
                    <option v-for="t in recordTypes" :key="t" :value="t">
                        {{ t }}
                    </option>
                </select>
            </div>

            <div class="sm:col-span-2">
                <InputLabel value="Name" />
                <div class="mt-1 flex items-center">
                    <TextInput
                        v-model="form.name"
                        class="block w-full rounded-r-none text-sm"
                        placeholder="www"
                    />
                    <span
                        class="truncate rounded-r-lg border border-l-0 border-slate-300 bg-white px-2 py-2 text-xs text-slate-500"
                        >.{{ selectedZone.name }}</span
                    >
                </div>
                <InputError class="mt-1" :message="form.errors.name" />
                <p class="mt-1 text-xs text-slate-500">
                    Leave empty for the domain itself.
                </p>
            </div>

            <div :class="needsPriority ? 'sm:col-span-2' : 'sm:col-span-3'">
                <InputLabel value="Value" />
                <TextInput
                    v-model="form.content"
                    class="mt-1 block w-full text-sm"
                    placeholder="203.0.113.10"
                />
                <InputError class="mt-1" :message="form.errors.content" />
            </div>

            <div v-if="needsPriority">
                <InputLabel value="Priority" />
                <TextInput
                    v-model="form.priority"
                    type="number"
                    class="mt-1 block w-full text-sm"
                />
                <InputError class="mt-1" :message="form.errors.priority" />
            </div>

            <div class="flex flex-wrap items-center gap-4 sm:col-span-6">
                <div class="flex items-center gap-2">
                    <InputLabel value="TTL" class="!mb-0" />
                    <TextInput
                        v-model="form.ttl"
                        type="number"
                        class="w-28 text-sm"
                        placeholder="0"
                    />
                    <span class="text-xs text-slate-500">
                        seconds — 0 leaves it to the provider
                    </span>
                </div>

                <label
                    v-if="account.supports_proxy"
                    class="flex items-center gap-2"
                >
                    <Checkbox v-model:checked="form.proxied" />
                    <span class="text-sm text-slate-700">
                        Proxy through {{ account.provider_label }}
                    </span>
                </label>

                <PrimaryButton class="ms-auto" :disabled="form.processing">
                    {{ form.processing ? 'Writing…' : 'Save record' }}
                </PrimaryButton>
            </div>

            <p class="text-xs text-slate-500 sm:col-span-6">
                A record of the same type and name is overwritten rather than
                added twice.
            </p>
        </form>

        <!-- List -->
        <div class="overflow-x-auto">
            <table class="min-w-full divide-y divide-slate-200">
                <thead class="bg-slate-50">
                    <tr
                        class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                    >
                        <th class="px-5 py-3">Type</th>
                        <th class="px-5 py-3">Name</th>
                        <th class="px-5 py-3">Value</th>
                        <th class="px-5 py-3">TTL</th>
                        <th class="px-5 py-3"></th>
                    </tr>
                </thead>
                <tbody class="divide-y divide-slate-100 text-sm">
                    <tr v-for="record in records" :key="record.id">
                        <td class="whitespace-nowrap px-5 py-3">
                            <span class="font-medium text-slate-800">
                                {{ record.type }}
                            </span>
                            <span
                                v-if="record.priority !== null"
                                class="ml-1 text-xs text-slate-500"
                                >pri {{ record.priority }}</span
                            >
                        </td>
                        <td class="whitespace-nowrap px-5 py-3 font-mono text-slate-800">
                            {{ shortName(record.name) }}
                        </td>
                        <td class="max-w-md break-all px-5 py-3 font-mono text-xs text-slate-600">
                            {{ record.content }}
                            <span
                                v-if="record.proxied"
                                class="ml-1 rounded-full bg-amber-100 px-2 py-0.5 text-[11px] font-medium text-amber-800"
                                >proxied</span
                            >
                        </td>
                        <td class="whitespace-nowrap px-5 py-3 text-slate-500">
                            {{ record.ttl ?? 'auto' }}
                        </td>
                        <td class="whitespace-nowrap px-5 py-3 text-right">
                            <button
                                class="text-rose-600 hover:underline"
                                @click="remove(record)"
                            >
                                Delete
                            </button>
                        </td>
                    </tr>
                    <tr v-if="!records.length">
                        <td
                            colspan="5"
                            class="px-5 py-12 text-center text-sm text-slate-500"
                        >
                            <span v-if="loadingRecords || loadingZones">Reading…</span>
                            <span v-else-if="!zones.length">
                                This credential can see no zones.
                            </span>
                            <span v-else>No records in {{ selectedZone?.name }}.</span>
                        </td>
                    </tr>
                </tbody>
            </table>
        </div>
    </div>
</template>
