<script setup>
import { ref } from 'vue';
import GuestLayout from '@/Layouts/GuestLayout.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, Link, useForm } from '@inertiajs/vue3';

// The second half of a sign-in: the password was right, and this is the code
// from the phone — or, with the phone gone, one of the recovery codes.
const recovery = ref(false);

const form = useForm({
    code: '',
    recovery_code: '',
});

const submit = () =>
    form
        .transform((data) => (recovery.value ? { recovery_code: data.recovery_code } : { code: data.code }))
        .post('/login/two-factor', { onFinish: () => form.reset() });

const toggle = () => {
    recovery.value = !recovery.value;
    form.clearErrors();
};
</script>

<template>
    <GuestLayout>
        <Head title="Two-factor authentication" />

        <p class="mb-4 text-sm text-slate-600">
            <template v-if="!recovery">
                Enter the six-digit code from your authenticator app.
            </template>
            <template v-else>
                Enter one of the recovery codes you saved when you turned two-factor on.
                Each works once.
            </template>
        </p>

        <form @submit.prevent="submit">
            <div v-if="!recovery">
                <InputLabel for="code" value="Code" />
                <TextInput
                    id="code"
                    v-model="form.code"
                    type="text"
                    inputmode="numeric"
                    pattern="[0-9 ]*"
                    maxlength="7"
                    class="mt-1 block w-full text-center font-mono text-2xl tracking-[0.5em]"
                    autocomplete="one-time-code"
                    autofocus
                    required
                />
                <InputError class="mt-2" :message="form.errors.code" />
            </div>

            <div v-else>
                <InputLabel for="recovery_code" value="Recovery code" />
                <TextInput
                    id="recovery_code"
                    v-model="form.recovery_code"
                    type="text"
                    class="mt-1 block w-full font-mono"
                    autocomplete="off"
                    autofocus
                    required
                />
                <InputError class="mt-2" :message="form.errors.recovery_code" />
            </div>

            <div class="mt-4 flex items-center justify-between">
                <button type="button" class="text-sm text-slate-600 underline hover:text-slate-900" @click="toggle">
                    {{ recovery ? 'Use the authenticator app' : 'Use a recovery code' }}
                </button>

                <PrimaryButton :class="{ 'opacity-25': form.processing }" :disabled="form.processing">
                    Verify
                </PrimaryButton>
            </div>

            <p class="mt-6 text-center text-xs">
                <Link :href="route('login')" class="text-slate-500 hover:text-slate-700">Back to sign in</Link>
            </p>
        </form>
    </GuestLayout>
</template>
