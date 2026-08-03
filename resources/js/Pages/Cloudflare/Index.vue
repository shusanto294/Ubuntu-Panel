<script setup>
import { ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, router, useForm } from '@inertiajs/vue3';

defineProps({ accounts: Array });

const form = useForm({ label: '', api_token: '', email: '' });

const zones = ref({});
const loadingZones = ref(null);

const submit = () =>
    form.post(route('cloudflare.store'), {
        preserveScroll: true,
        onSuccess: () => form.reset(),
    });

const verify = (id) =>
    router.post(route('cloudflare.verify', id), {}, { preserveScroll: true });

const destroy = (account) => {
    if (
        confirm(
            `Disconnect ${account.label}? Sites using it keep running, but DNS records will no longer be managed.`,
        )
    ) {
        router.delete(route('cloudflare.destroy', account.id), {
            preserveScroll: true,
        });
    }
};

const loadZones = async (id) => {
    loadingZones.value = id;
    try {
        const { data } = await window.axios.get(route('cloudflare.zones', id));
        zones.value = { ...zones.value, [id]: data.zones };
    } catch (e) {
        zones.value = { ...zones.value, [id]: [] };
    } finally {
        loadingZones.value = null;
    }
};
</script>

<template>
    <Head title="Cloudflare" />

    <AuthenticatedLayout>
        <template #header>
            <h2 class="text-xl font-semibold text-slate-800">
                Cloudflare integration
            </h2>
        </template>

        <div class="grid gap-6 lg:grid-cols-3">
            <!-- Connect form -->
            <div class="rounded-xl border border-slate-200 bg-white p-6">
                <h3 class="font-semibold text-slate-800">Connect an account</h3>
                <p class="mt-2 text-sm text-slate-500">
                    Create an API token in Cloudflare with
                    <strong>Zone → DNS → Edit</strong> and
                    <strong>Zone → Zone → Read</strong> permissions.
                </p>

                <form @submit.prevent="submit" class="mt-4 space-y-4">
                    <div>
                        <InputLabel for="label" value="Label" />
                        <TextInput
                            id="label"
                            v-model="form.label"
                            class="mt-1 block w-full"
                            placeholder="Personal Cloudflare"
                        />
                        <InputError class="mt-2" :message="form.errors.label" />
                    </div>

                    <div>
                        <InputLabel for="api_token" value="API token" />
                        <TextInput
                            id="api_token"
                            type="password"
                            v-model="form.api_token"
                            class="mt-1 block w-full"
                            autocomplete="off"
                        />
                        <InputError
                            class="mt-2"
                            :message="form.errors.api_token"
                        />
                    </div>

                    <div>
                        <InputLabel for="email" value="Account email (optional)" />
                        <TextInput
                            id="email"
                            type="email"
                            v-model="form.email"
                            class="mt-1 block w-full"
                        />
                        <InputError class="mt-2" :message="form.errors.email" />
                    </div>

                    <button
                        :disabled="form.processing"
                        class="w-full rounded-md bg-orange-500 px-4 py-2 text-sm font-medium text-white hover:bg-orange-600 disabled:opacity-50"
                    >
                        Verify and connect
                    </button>
                </form>
            </div>

            <!-- Connected accounts -->
            <div class="lg:col-span-2">
                <div class="space-y-4">
                    <div
                        v-for="account in accounts"
                        :key="account.id"
                        class="rounded-xl border border-slate-200 bg-white p-6"
                    >
                        <div
                            class="flex flex-wrap items-start justify-between gap-3"
                        >
                            <div>
                                <p class="font-medium text-slate-800">
                                    {{ account.label }}
                                </p>
                                <p class="text-sm text-slate-500">
                                    {{ account.email ?? 'no email set' }} ·
                                    {{ account.sites_count }} site(s)
                                </p>
                                <p class="mt-1 text-xs">
                                    <span
                                        v-if="account.verified_at"
                                        class="text-emerald-600"
                                        >Token verified
                                        {{ account.verified_at }}</span
                                    >
                                    <span v-else class="text-rose-600"
                                        >Token not verified</span
                                    >
                                </p>
                            </div>
                            <div class="flex flex-wrap gap-2">
                                <button
                                    @click="loadZones(account.id)"
                                    class="rounded-md border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50"
                                >
                                    {{
                                        loadingZones === account.id
                                            ? 'Loading…'
                                            : 'Show zones'
                                    }}
                                </button>
                                <button
                                    @click="verify(account.id)"
                                    class="rounded-md border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50"
                                >
                                    Re-verify
                                </button>
                                <button
                                    @click="destroy(account)"
                                    class="rounded-md border border-rose-300 px-3 py-1.5 text-sm text-rose-600 hover:bg-rose-50"
                                >
                                    Disconnect
                                </button>
                            </div>
                        </div>

                        <div v-if="zones[account.id]" class="mt-4">
                            <p
                                class="text-xs font-semibold uppercase tracking-wide text-slate-500"
                            >
                                Zones
                            </p>
                            <div class="mt-2 flex flex-wrap gap-2">
                                <span
                                    v-for="zone in zones[account.id]"
                                    :key="zone.id"
                                    class="rounded-full bg-slate-100 px-2.5 py-0.5 text-xs text-slate-700"
                                >
                                    {{ zone.name }}
                                </span>
                                <span
                                    v-if="!zones[account.id].length"
                                    class="text-sm text-slate-500"
                                >
                                    No zones visible to this token.
                                </span>
                            </div>
                        </div>
                    </div>

                    <div
                        v-if="!accounts.length"
                        class="rounded-xl border border-dashed border-slate-300 bg-white p-10 text-center text-sm text-slate-500"
                    >
                        No Cloudflare account connected yet. Add a token on the
                        left and DNS records will be created and deleted along
                        with your sites.
                    </div>
                </div>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
