<script setup>
import { ref } from 'vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import Pagination from '@/Components/Pagination.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { usePagination } from '@/Composables/usePagination';
import { router, useForm } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

/**
 * The zones this machine answers for, with what the world thinks of each.
 *
 * The delegation check is per row and on demand: it is a query to a resolver
 * out on the internet, and a panel holding thirty zones would spend thirty
 * round trips on every page load to tell most people something they already
 * know.
 */
const props = defineProps({
    zones: { type: Array, default: () => [] },
    nameservers: { type: Array, default: () => [] },
    ready: { type: Boolean, default: false },
});

const { page, pages, total, perPage, visible: visibleZones } = usePagination(
    () => props.zones,
);

const form = useForm({ name: '' });

const submit = () =>
    form.post(route('dns.zones.store'), {
        preserveScroll: true,
        onSuccess: () => form.reset('name'),
    });

const remove = async (zone) => {
    const go = await confirmDanger(
        `Stop serving ${zone.name}?`,
        'Every name in the zone stops resolving within its TTL — the site, and ' +
            'any mail the domain receives. Point the domain at another nameserver ' +
            'first if it is still in use.',
        'Remove the zone',
    );

    if (!go) return;

    router.delete(route('dns.zones.destroy', zone.id), { preserveScroll: true });
};

// What a public resolver says about each zone's delegation, once asked.
const checks = ref({});
const checking = ref(null);

const check = async (zone) => {
    checking.value = zone.id;

    try {
        const { data } = await window.axios.get(route('dns.zones.check', zone.id));
        checks.value = { ...checks.value, [zone.id]: data };
    } catch (e) {
        checks.value = {
            ...checks.value,
            [zone.id]: {
                delegated: false,
                nameservers: [],
                error: e?.response?.data?.error ?? 'Could not ask the public DNS.',
            },
        };
    } finally {
        checking.value = null;
    }
};
</script>

<template>
    <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
        <div class="border-b border-slate-200 px-5 py-4">
            <h3 class="font-semibold text-slate-900">Zones on this server</h3>
            <p class="mt-1 text-sm text-slate-500">
                A zone here is a domain this machine is the authority for. Add it
                before you change the domain's nameservers, not after.
            </p>
        </div>

        <!-- Add -->
        <form
            @submit.prevent="submit"
            class="flex flex-wrap items-end gap-3 border-b border-slate-200 bg-slate-50 px-5 py-4"
        >
            <div class="min-w-64 flex-1">
                <InputLabel for="zone" value="Domain" />
                <TextInput
                    id="zone"
                    v-model="form.name"
                    type="text"
                    class="mt-1 block w-full"
                    placeholder="example.com"
                    autocomplete="off"
                />
                <InputError class="mt-1" :message="form.errors.name" />
                <p class="mt-1 text-xs text-slate-500">
                    The domain itself, not a subdomain of it — www and the rest are
                    records inside the zone.
                </p>
            </div>
            <PrimaryButton :disabled="form.processing || !ready">
                {{ form.processing ? 'Adding…' : 'Add zone' }}
            </PrimaryButton>
        </form>

        <p
            v-if="!ready"
            class="border-b border-amber-200 bg-amber-50 px-5 py-3 text-sm text-amber-800"
        >
            Set the nameserver hostnames first — a zone has to name the servers that
            answer for it, and those names go into every zone's apex.
        </p>

        <!-- List -->
        <ul class="divide-y divide-slate-100">
            <li v-for="zone in visibleZones" :key="zone.id" class="px-5 py-4">
                <div class="flex flex-wrap items-start justify-between gap-3">
                    <div class="min-w-0">
                        <p class="font-medium text-slate-800">
                            {{ zone.name }}
                            <span
                                v-if="zone.holds_the_ns"
                                class="ml-2 rounded-full bg-brand-50 px-2 py-0.5 text-[11px] font-medium text-brand-700"
                                >holds the nameserver records</span
                            >
                        </p>
                        <p class="mt-0.5 text-xs text-slate-500">
                            {{ zone.records }}
                            {{ zone.records === 1 ? 'record' : 'records' }} · serial
                            {{ zone.serial }}
                            <span v-if="zone.updated_at">
                                · changed {{ zone.updated_at }}</span
                            >
                        </p>

                        <!-- What the public DNS says -->
                        <p
                            v-if="checks[zone.id]"
                            class="mt-1 text-xs"
                            :class="
                                checks[zone.id].delegated
                                    ? 'text-emerald-700'
                                    : 'text-amber-700'
                            "
                        >
                            <template v-if="checks[zone.id].delegated">
                                Delegated here —
                                {{ checks[zone.id].nameservers.join(', ') }}
                            </template>
                            <template v-else-if="checks[zone.id].nameservers?.length">
                                Still delegated to
                                {{ checks[zone.id].nameservers.join(', ') }}. Change
                                the nameservers at the domain's registrar to
                                {{ nameservers.join(' and ') }}.
                            </template>
                            <template v-else>
                                {{ checks[zone.id].error }}
                            </template>
                        </p>
                    </div>

                    <div class="flex shrink-0 gap-2">
                        <button
                            type="button"
                            :disabled="checking === zone.id"
                            class="rounded-xl px-3 py-2 text-sm text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                            @click="check(zone)"
                        >
                            {{ checking === zone.id ? 'Asking…' : 'Check delegation' }}
                        </button>
                        <button
                            type="button"
                            class="rounded-xl px-3 py-2 text-sm text-rose-600 ring-1 ring-rose-200 transition hover:bg-rose-50"
                            @click="remove(zone)"
                        >
                            Remove
                        </button>
                    </div>
                </div>
            </li>

            <li v-if="!zones.length" class="px-5 py-16 text-center text-sm text-slate-500">
                No zones yet. Add the domain your nameservers are named on first —
                it is the one that has to be here for any of the others to work.
            </li>
        </ul>
        <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
    </div>
</template>
