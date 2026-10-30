<script setup>
import { ref, watch } from 'vue';
import QRCode from 'qrcode';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import SecondaryButton from '@/Components/SecondaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { useForm } from '@inertiajs/vue3';

const props = defineProps({
    twoFactor: Object,
});

// Drawn in the browser from the otpauth:// address the server sends. The
// secret is already on the page for typing in by hand, so drawing it here
// gives nothing away that the page does not, and fetches nothing from anywhere.
const qr = ref('');

watch(
    () => props.twoFactor.uri,
    async (uri) => {
        qr.value = uri ? await QRCode.toString(uri, { type: 'svg', margin: 1, width: 192 }) : '';
    },
    { immediate: true },
);

const startForm = useForm({ password: '' });
const confirmForm = useForm({ code: '' });
const codesForm = useForm({ password: '' });
const disableForm = useForm({ password: '' });

const asking = ref(null); // 'codes' | 'disable' — which password prompt is open

const start = () => startForm.post(route('two-factor.enable'), { preserveScroll: true, onFinish: () => startForm.reset() });
const confirm = () => confirmForm.post(route('two-factor.confirm'), { preserveScroll: true, onFinish: () => confirmForm.reset() });
const regenerate = () =>
    codesForm.post(route('two-factor.recovery-codes'), {
        preserveScroll: true,
        onSuccess: () => (asking.value = null),
        onFinish: () => codesForm.reset(),
    });
const disable = () =>
    disableForm.post(route('two-factor.disable'), {
        preserveScroll: true,
        onSuccess: () => (asking.value = null),
        onFinish: () => disableForm.reset(),
    });

const grouped = (secret) => secret?.match(/.{1,4}/g)?.join(' ') ?? '';

const copyCodes = () => navigator.clipboard?.writeText(props.twoFactor.codes.join('\n'));
</script>

<template>
    <section>
        <header>
            <h2 class="flex items-center gap-2 text-lg font-medium text-slate-900">
                Two-factor authentication
                <span
                    class="rounded-full px-2 py-0.5 text-xs font-medium"
                    :class="twoFactor.enabled ? 'bg-emerald-50 text-emerald-700' : 'bg-slate-100 text-slate-600'"
                >
                    {{ twoFactor.enabled ? 'on' : 'off' }}
                </span>
            </h2>
            <p class="mt-1 text-sm text-slate-600">
                Ask for a code from an authenticator app — Google Authenticator, 1Password,
                Authy, Bitwarden — as well as your password when signing in.
            </p>
        </header>

        <!-- Recovery codes, shown once -->
        <div v-if="twoFactor.codes" class="mt-6 rounded-xl bg-amber-50 p-4 ring-1 ring-amber-200">
            <p class="text-sm font-medium text-amber-900">Save these recovery codes now.</p>
            <p class="mt-1 text-xs text-amber-800">
                Each signs you in once if you lose your phone. They will not be shown again.
            </p>
            <div class="mt-3 grid grid-cols-2 gap-2 font-mono text-sm text-slate-900">
                <span v-for="code in twoFactor.codes" :key="code" class="rounded bg-white px-2 py-1">{{ code }}</span>
            </div>
            <button type="button" class="mt-3 text-xs font-medium text-amber-900 underline" @click="copyCodes">Copy all</button>
        </div>

        <!-- Off: start enrolling -->
        <form v-if="!twoFactor.enabled && !twoFactor.pending" class="mt-6 max-w-xl space-y-4" @submit.prevent="start">
            <div>
                <InputLabel for="tf_password" value="Current password" />
                <TextInput id="tf_password" v-model="startForm.password" type="password" class="mt-1 block w-full" autocomplete="current-password" />
                <InputError class="mt-2" :message="startForm.errors.password" />
            </div>
            <PrimaryButton :disabled="startForm.processing">Set up two-factor</PrimaryButton>
        </form>

        <!-- Enrolling: scan and confirm -->
        <div v-else-if="twoFactor.pending" class="mt-6 grid max-w-2xl gap-6 sm:grid-cols-[auto,1fr]">
            <div class="rounded-xl bg-white p-2 ring-1 ring-slate-900/10" v-html="qr" />
            <div class="space-y-4">
                <ol class="list-decimal space-y-1 ps-4 text-sm text-slate-600">
                    <li>Scan the code with your authenticator app.</li>
                    <li>Or type this key in by hand:</li>
                </ol>
                <p class="select-all break-all rounded-lg bg-slate-50 px-3 py-2 font-mono text-sm">{{ grouped(twoFactor.secret) }}</p>
                <form class="space-y-3" @submit.prevent="confirm">
                    <div>
                        <InputLabel for="tf_code" value="Then enter the six-digit code it shows" />
                        <TextInput
                            id="tf_code"
                            v-model="confirmForm.code"
                            type="text"
                            inputmode="numeric"
                            maxlength="7"
                            class="mt-1 block w-40 text-center font-mono text-lg tracking-widest"
                            autocomplete="one-time-code"
                        />
                        <InputError class="mt-2" :message="confirmForm.errors.code" />
                    </div>
                    <PrimaryButton :disabled="confirmForm.processing">Confirm and turn on</PrimaryButton>
                </form>
            </div>
        </div>

        <!-- On: manage -->
        <div v-else class="mt-6 max-w-xl space-y-4">
            <p class="text-sm text-slate-600">
                {{ twoFactor.remaining }} recovery code{{ twoFactor.remaining === 1 ? '' : 's' }} left.
            </p>

            <div class="flex flex-wrap gap-3">
                <SecondaryButton @click="asking = asking === 'codes' ? null : 'codes'">New recovery codes</SecondaryButton>
                <button
                    type="button"
                    class="rounded-xl px-4 py-2.5 text-sm font-medium text-rose-600 ring-1 ring-rose-200 hover:bg-rose-50"
                    @click="asking = asking === 'disable' ? null : 'disable'"
                >
                    Turn off
                </button>
            </div>

            <form v-if="asking === 'codes'" class="space-y-3" @submit.prevent="regenerate">
                <InputLabel for="tf_codes_password" value="Confirm with your password" />
                <TextInput id="tf_codes_password" v-model="codesForm.password" type="password" class="block w-full" autocomplete="current-password" />
                <InputError :message="codesForm.errors.password" />
                <PrimaryButton :disabled="codesForm.processing">Make new codes</PrimaryButton>
            </form>

            <form v-if="asking === 'disable'" class="space-y-3" @submit.prevent="disable">
                <InputLabel for="tf_disable_password" value="Confirm with your password" />
                <TextInput id="tf_disable_password" v-model="disableForm.password" type="password" class="block w-full" autocomplete="current-password" />
                <InputError :message="disableForm.errors.password" />
                <button type="submit" class="rounded-xl bg-rose-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-rose-700" :disabled="disableForm.processing">
                    Turn two-factor off
                </button>
            </form>
        </div>
    </section>
</template>
