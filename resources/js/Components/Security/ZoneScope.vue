<script setup>
import { computed, ref, watch } from 'vue';

/*
 * Which zones a new rule goes to: this one — the default — every zone with a
 * site on this server, or a hand-picked list, across every Cloudflare account
 * connected. The model is the list of targets, each { account, zone_id,
 * zone_name }.
 */
const props = defineProps({
    zones: { type: Array, default: () => [] }, // { account, account_label, id, name, has_site }
    current: { type: Object, default: null }, // the zone the tab is showing
    loading: { type: Boolean, default: false },
});

const model = defineModel({ type: Array, default: () => [] });

const mode = ref('one');
const picked = ref(new Set());
const filter = ref('');

const withSites = computed(() => props.zones.filter((z) => z.has_site));
const accounts = computed(() => new Set(props.zones.map((z) => z.account)).size);

const target = (z) => ({ account: z.account, zone_id: z.id, zone_name: z.name });

const targets = computed(() => {
    switch (mode.value) {
        case 'sites':
            return withSites.value.map(target);
        case 'all':
            return props.zones.map(target);
        case 'pick':
            return props.zones.filter((z) => picked.value.has(z.id)).map(target);
        default:
            return props.current ? [target(props.current)] : [];
    }
});

watch(targets, (list) => (model.value = list), { immediate: true });

// Choosing by hand starts from this zone.
watch(mode, (value) => {
    if (value === 'pick' && !picked.value.size && props.current) picked.value = new Set([props.current.id]);
});

const toggle = (id) => {
    const next = new Set(picked.value);
    next.has(id) ? next.delete(id) : next.add(id);
    picked.value = next;
};

const pickAll = (on) => (picked.value = new Set(on ? visible.value.map((z) => z.id) : []));

const visible = computed(() => {
    const q = filter.value.trim().toLowerCase();
    return q ? props.zones.filter((z) => z.name.includes(q)) : props.zones;
});

const option = 'flex items-start gap-2 rounded-lg px-3 py-2 text-sm ring-1 transition cursor-pointer';
const on = 'bg-brand-50 ring-brand-300';
const off = 'bg-white ring-slate-200 hover:bg-slate-50';
</script>

<template>
    <fieldset>
        <legend class="text-xs font-medium text-slate-700">Add it to</legend>

        <div class="mt-1 grid gap-2 sm:grid-cols-2">
            <label :class="[option, mode === 'one' ? on : off]">
                <input v-model="mode" type="radio" value="one" class="mt-0.5 text-brand-600" />
                <span>
                    <span class="font-medium text-slate-800">Only {{ current?.name ?? 'this zone' }}</span>
                    <span class="block text-xs text-slate-500">This site alone.</span>
                </span>
            </label>

            <label v-if="withSites.length" :class="[option, mode === 'sites' ? on : off]">
                <input v-model="mode" type="radio" value="sites" class="mt-0.5 text-brand-600" />
                <span>
                    <span class="font-medium text-slate-800">Every site on this server</span>
                    <span class="block text-xs text-slate-500">
                        {{ withSites.length }} zone{{ withSites.length === 1 ? '' : 's' }} with a site here
                    </span>
                </span>
            </label>

            <label v-if="zones.length > 1" :class="[option, mode === 'all' ? on : off]">
                <input v-model="mode" type="radio" value="all" class="mt-0.5 text-brand-600" />
                <span>
                    <span class="font-medium text-slate-800">Every zone</span>
                    <span class="block text-xs text-slate-500">
                        All {{ zones.length }}{{ accounts > 1 ? `, on ${accounts} Cloudflare accounts` : '' }}
                    </span>
                </span>
            </label>

            <label v-if="zones.length > 1" :class="[option, mode === 'pick' ? on : off]">
                <input v-model="mode" type="radio" value="pick" class="mt-0.5 text-brand-600" />
                <span>
                    <span class="font-medium text-slate-800">Choose zones</span>
                    <span class="block text-xs text-slate-500">Tick the ones it goes to.</span>
                </span>
            </label>
        </div>

        <p v-if="loading" class="mt-2 text-xs text-slate-400">Reading the zones on every Cloudflare account…</p>

        <div v-if="mode === 'pick'" class="mt-2 rounded-lg bg-white ring-1 ring-slate-200">
            <div class="flex flex-wrap items-center gap-2 border-b border-slate-100 px-3 py-2">
                <input
                    v-if="zones.length > 8"
                    v-model="filter"
                    type="search"
                    placeholder="Filter"
                    class="w-40 rounded-md border-slate-300 py-1 text-xs shadow-sm"
                />
                <button type="button" class="text-xs font-medium text-brand-600 hover:underline" @click="pickAll(true)">Select all</button>
                <button type="button" class="text-xs font-medium text-slate-500 hover:underline" @click="pickAll(false)">None</button>
                <span class="ml-auto text-xs text-slate-500">{{ picked.size }} chosen</span>
            </div>
            <ul class="max-h-56 overflow-y-auto py-1">
                <li v-for="z in visible" :key="z.id">
                    <label class="flex cursor-pointer items-center gap-2 px-3 py-1.5 text-sm hover:bg-slate-50">
                        <input
                            type="checkbox"
                            :checked="picked.has(z.id)"
                            class="rounded border-slate-300 text-brand-600"
                            @change="toggle(z.id)"
                        />
                        <span class="min-w-0 flex-1 truncate text-slate-800">{{ z.name }}</span>
                        <span v-if="z.has_site" class="rounded bg-emerald-50 px-1.5 text-[11px] text-emerald-700">site here</span>
                        <span v-if="accounts > 1" class="text-[11px] text-slate-400">{{ z.account_label }}</span>
                    </label>
                </li>
            </ul>
        </div>

        <p v-if="model.length > 1" class="mt-2 text-xs text-slate-500">
            Zones that already have the same rule are skipped, so adding it again later only fills in the new ones.
        </p>
    </fieldset>
</template>
