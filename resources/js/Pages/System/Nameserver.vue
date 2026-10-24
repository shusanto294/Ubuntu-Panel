<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import DnsTabs from '@/Components/DnsTabs.vue';
import NameserverGuide from '@/Components/NameserverGuide.vue';
import NameserverZones from '@/Components/NameserverZones.vue';
import DnsRecords from '@/Components/DnsRecords.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, router, useForm } from '@inertiajs/vue3';

/**
 * This machine as a DNS host.
 *
 * The page is the guide first and the controls second, which is the right way
 * round for exactly as long as it takes somebody to get set up: none of the
 * forms below do anything visible from outside until two names have been
 * registered at a registrar, and a page that led with the forms would be a
 * zone editor saving records nothing in the world could find.
 */
const props = defineProps({
    status: { type: Object, required: true },
    zones: { type: Array, default: () => [] },
    accountId: { type: [Number, String], default: null },
    recordTypes: { type: Array, default: () => [] },
    installed: { type: Boolean, default: false },
});

const form = useForm({
    ns1: props.status.settings?.ns1 || props.status.suggested?.ns1 || '',
    ns2: props.status.settings?.ns2 || props.status.suggested?.ns2 || '',
    hostmaster:
        props.status.settings?.hostmaster || props.status.suggested?.hostmaster || '',
});

const submit = () => form.post(route('dns.nameserver.settings'), { preserveScroll: true });

const resync = () =>
    router.post(route('dns.nameserver.resync'), {}, { preserveScroll: true });

const nameservers = computed(() =>
    [props.status.settings?.ns1, props.status.settings?.ns2].filter(Boolean),
);

const ready = computed(() => nameservers.value.length > 0);

// The record editor is the same component the connected providers get, pointed
// at the credential row that stands for this machine. The zones it lists are
// the ones above, read back through the provider interface — so what the editor
// shows is what BIND was handed, not what the form thought it saved.
const editorAccount = computed(() =>
    props.accountId
        ? {
              id: props.accountId,
              label: 'This server',
              provider: 'panel',
              provider_label: 'This server',
              supports_proxy: false,
          }
        : null,
);
</script>

<template>
    <Head title="Nameserver" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">
                        This server's DNS
                    </h2>
                    <p class="text-sm text-slate-500">
                        <template v-if="status.ip">
                            Answering on {{ status.ip }} ·
                        </template>
                        {{ zones.length }}
                        {{ zones.length === 1 ? 'zone' : 'zones' }} hosted
                    </p>
                </div>
                <button
                    type="button"
                    class="rounded-xl px-4 py-2.5 text-sm text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-white"
                    @click="resync"
                >
                    Rewrite and reload
                </button>
            </div>
        </template>

        <DnsTabs current="nameserver" :counts="{ zones: zones.length }" />

        <!-- Anything BIND refused, said out loud. A reload that failed leaves
             the panel's rows right and the machine answering with the zone as
             it was, which is invisible otherwise. -->
        <div
            v-if="status.last_error"
            class="mb-6 rounded-2xl bg-rose-50 p-5 text-sm text-rose-800 ring-1 ring-rose-200/60"
        >
            <p class="font-medium">The nameserver did not take the last change</p>
            <p class="mt-1 break-words font-mono text-xs">{{ status.last_error }}</p>
            <p class="mt-2">
                The zones still say what the panel says they say; this machine is
                still answering with what it had before. Fix the record and the next
                change publishes everything, or use Rewrite and reload.
            </p>
        </div>

        <div
            v-else-if="installed && status.running && !status.answering"
            class="mb-6 rounded-2xl bg-amber-50 p-5 text-sm text-amber-800 ring-1 ring-amber-200/60"
        >
            <p class="font-medium">Running, but not answering for its own zones</p>
            <p class="mt-1">
                BIND is up and refusing queries for a zone it is supposed to hold.
                Rewrite and reload is the first thing to try.
            </p>
        </div>

        <div class="space-y-6">
            <NameserverGuide
                :status="status"
                :installed="installed"
                :zones="zones"
            />

            <!-- The names -->
            <form
                @submit.prevent="submit"
                class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
            >
                <h3 class="font-semibold text-slate-900">Nameserver hostnames</h3>
                <p class="mt-1 text-sm text-slate-500">
                    What every domain hosted here will be pointed at. Changing them
                    rewrites every zone — and means registering the new names as
                    glue records before any domain can be moved to them.
                </p>

                <div class="mt-5 grid gap-5 sm:grid-cols-2">
                    <div>
                        <InputLabel for="ns1" value="First nameserver" />
                        <TextInput
                            id="ns1"
                            v-model="form.ns1"
                            type="text"
                            class="mt-1 block w-full font-mono text-sm"
                            placeholder="ns1.example.com"
                            autocomplete="off"
                        />
                        <InputError class="mt-1" :message="form.errors.ns1" />
                    </div>

                    <div>
                        <InputLabel for="ns2" value="Second nameserver" />
                        <TextInput
                            id="ns2"
                            v-model="form.ns2"
                            type="text"
                            class="mt-1 block w-full font-mono text-sm"
                            placeholder="ns2.example.com"
                            autocomplete="off"
                        />
                        <InputError class="mt-1" :message="form.errors.ns2" />
                        <p class="mt-1 text-xs text-slate-500">
                            Optional here, and expected by most registries. One
                            nameserver serves a working domain; some registrars will
                            not accept a delegation with only one.
                        </p>
                    </div>

                    <div class="sm:col-span-2">
                        <InputLabel for="hostmaster" value="Hostmaster address" />
                        <TextInput
                            id="hostmaster"
                            v-model="form.hostmaster"
                            type="text"
                            class="mt-1 block w-full font-mono text-sm"
                            placeholder="hostmaster@example.com"
                            autocomplete="off"
                        />
                        <InputError class="mt-1" :message="form.errors.hostmaster" />
                        <p class="mt-1 text-xs text-slate-500">
                            Goes into every zone's SOA record as the address
                            responsible for the zone. It is published, so it gets
                            scraped — a role address rather than a personal one.
                        </p>
                    </div>
                </div>

                <div class="mt-6 flex items-center gap-3 border-t border-slate-100 pt-6">
                    <PrimaryButton :disabled="form.processing">
                        {{ form.processing ? 'Saving…' : 'Save' }}
                    </PrimaryButton>
                    <span v-if="form.recentlySuccessful" class="text-sm text-slate-500">
                        Saved.
                    </span>
                </div>
            </form>

            <NameserverZones
                :zones="zones"
                :nameservers="nameservers"
                :ready="ready"
            />

            <!-- The records, through the same editor the providers use. -->
            <DnsRecords
                v-if="editorAccount && zones.length"
                :account="editorAccount"
                :record-types="recordTypes"
            />
        </div>
    </AuthenticatedLayout>
</template>
