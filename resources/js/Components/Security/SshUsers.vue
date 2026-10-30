<script setup>
import { ref } from 'vue';
import Checkbox from '@/Components/Checkbox.vue';
import InputError from '@/Components/InputError.vue';
import IpAddress from '@/Components/IpAddress.vue';
import Pagination from '@/Components/Pagination.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import { usePagination } from '@/Composables/usePagination';
import Swal, { confirmAction, confirmDanger } from '@/dialog';
import { ago, card, field, smallButton } from './ui';
import { router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    users: Array,
    hardening: Object,
    service: Object,
});

const { page, pages, total, perPage, visible: visibleUsers } = usePagination(() => props.users, 10);

const showCreate = ref(false);

const createForm = useForm({
    name: '',
    password: '',
    public_key: '',
    sudo: false,
});

const create = () =>
    createForm.post(route('security.users.store'), {
        preserveScroll: true,
        onSuccess: () => {
            createForm.reset();
            showCreate.value = false;
        },
    });

// Which account's panel is open, and the two small forms inside it.
const open = ref(null);
const keyForm = useForm({ public_key: '' });
const passwordForm = useForm({ action: 'password', password: '', password_confirmation: '' });

const toggle = (name) => {
    open.value = open.value === name ? null : name;
    keyForm.reset();
    passwordForm.reset();
    keyForm.clearErrors();
    passwordForm.clearErrors();
};

const addKey = (name) =>
    keyForm.post(route('security.users.keys.store', name), {
        preserveScroll: true,
        onSuccess: () => keyForm.reset(),
    });

const removeKey = async (name, key) => {
    if (!(await confirmDanger('Remove this key?', `${key.comment || key.fingerprint} will no longer log in as ${name}.`, 'Remove')))
        return;
    router.post(route('security.users.keys.destroy', name), { fingerprint: key.fingerprint }, { preserveScroll: true });
};

const setPassword = (name) =>
    passwordForm.post(route('security.users.update', name), {
        preserveScroll: true,
        onSuccess: () => passwordForm.reset(),
    });

const setSudo = async (user, enabled) => {
    const text = enabled
        ? `${user.name} will be able to run any command as root.`
        : `${user.name} will no longer be able to use sudo.`;
    if (!(await confirmAction(enabled ? 'Give sudo?' : 'Take sudo away?', text, enabled ? 'Give sudo' : 'Remove sudo')))
        return;
    router.post(route('security.users.update', user.name), { action: 'sudo', enabled }, { preserveScroll: true });
};

const setLocked = async (user, locked) => {
    if (locked) {
        if (!(await confirmDanger(`Lock ${user.name}?`, 'No password or key will log in as it, and its open sessions are ended now.', 'Lock')))
            return;
    }
    router.post(route('security.users.update', user.name), { action: locked ? 'lock' : 'unlock' }, { preserveScroll: true });
};

const remove = async (user) => {
    const result = await Swal.fire({
        icon: 'warning',
        iconColor: '#e11d48',
        title: `Delete ${user.name}?`,
        text: `The account is removed and its processes are stopped. ${user.home} can be kept or deleted with it.`,
        showCancelButton: true,
        showDenyButton: true,
        confirmButtonText: 'Delete with home',
        denyButtonText: 'Keep home folder',
        cancelButtonText: 'Cancel',
        buttonsStyling: false,
        reverseButtons: true,
        focusCancel: true,
        customClass: {
            popup: 'rounded-2xl !p-6',
            actions: '!mt-6 gap-2',
            confirmButton: 'rounded-xl bg-rose-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-rose-700',
            denyButton: 'rounded-xl px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50',
            cancelButton: 'rounded-xl px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50',
        },
    });

    if (result.isDismissed) return;

    router.delete(route('security.users.destroy', user.name) + (result.isConfirmed ? '?home=1' : ''), {
        preserveScroll: true,
    });
};
</script>

<template>
    <div class="space-y-6">
        <div :class="card">
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h3 class="font-semibold text-slate-900">SSH users</h3>
                    <p class="mt-1 text-sm text-slate-500">
                        Every account on this machine that can open a shell.
                        <template v-if="service && !service.running">
                            SSH is turned off, so none of these can log in until it is turned
                            back on under SSH settings.
                        </template>
                        <template v-else-if="hardening.readable">
                            SSH listens on port {{ hardening.ports.join(', ') }}; passwords are
                            {{ hardening.password_authentication ? 'accepted' : 'refused — keys only' }}.
                        </template>
                    </p>
                </div>
                <PrimaryButton type="button" @click="showCreate = !showCreate">
                    {{ showCreate ? 'Cancel' : 'Add user' }}
                </PrimaryButton>
            </div>

            <form v-if="showCreate" class="mt-6 grid gap-4 border-t border-slate-100 pt-6 sm:grid-cols-2" @submit.prevent="create">
                <div>
                    <label class="block text-sm font-medium text-slate-700">Username</label>
                    <input v-model="createForm.name" type="text" :class="field" class="font-mono" placeholder="deploy" autocomplete="off" />
                    <InputError class="mt-2" :message="createForm.errors.name" />
                </div>
                <div>
                    <label class="block text-sm font-medium text-slate-700">Password <span class="font-normal text-slate-400">(optional with a key)</span></label>
                    <input v-model="createForm.password" type="password" :class="field" autocomplete="new-password" />
                    <InputError class="mt-2" :message="createForm.errors.password" />
                </div>
                <div class="sm:col-span-2">
                    <label class="block text-sm font-medium text-slate-700">SSH public key <span class="font-normal text-slate-400">(recommended)</span></label>
                    <textarea v-model="createForm.public_key" rows="3" :class="field" class="font-mono text-xs" placeholder="ssh-ed25519 AAAAC3Nza... you@laptop" />
                    <InputError class="mt-2" :message="createForm.errors.public_key" />
                </div>
                <label class="flex items-center gap-2 text-sm text-slate-700 sm:col-span-2">
                    <Checkbox v-model:checked="createForm.sudo" />
                    Can use sudo (full administrator)
                </label>
                <div class="sm:col-span-2">
                    <PrimaryButton :disabled="createForm.processing">Create user</PrimaryButton>
                </div>
            </form>
        </div>

        <div :class="card" class="!p-0">
            <ul class="divide-y divide-slate-100">
                <li v-for="user in visibleUsers" :key="user.name" class="px-6 py-4">
                    <div class="flex flex-wrap items-center gap-3">
                        <span
                            class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-sm font-semibold uppercase"
                            :class="user.locked ? 'bg-slate-100 text-slate-400' : 'bg-brand-50 text-brand-700'"
                        >
                            {{ user.name.charAt(0) }}
                        </span>
                        <div class="min-w-0 flex-1">
                            <p class="flex flex-wrap items-center gap-2 text-sm font-medium text-slate-900">
                                <span class="font-mono">{{ user.name }}</span>
                                <span v-if="user.sudo" class="rounded-full bg-violet-50 px-2 py-0.5 text-xs text-violet-700">sudo</span>
                                <span v-if="user.expired" class="rounded-full bg-rose-50 px-2 py-0.5 text-xs text-rose-700">locked</span>
                                <span
                                    v-else-if="user.password_locked"
                                    class="rounded-full bg-amber-50 px-2 py-0.5 text-xs text-amber-700"
                                    title="Its password is locked, so it cannot log in with one. SSH keys still log in."
                                >
                                    password locked
                                </span>
                                <span v-if="user.protected && user.uid !== 0" class="rounded-full bg-slate-100 px-2 py-0.5 text-xs text-slate-500">panel &amp; terminal</span>
                                <span v-else-if="user.protected" class="rounded-full bg-slate-100 px-2 py-0.5 text-xs text-slate-500">system</span>
                            </p>
                            <p class="text-xs text-slate-500">
                                uid {{ user.uid }} · {{ user.home }} ·
                                {{ user.keys.length }} key{{ user.keys.length === 1 ? '' : 's' }} ·
                                {{ user.has_password ? 'password set' : 'no password' }} ·
                                <template v-if="user.last_login">
                                    last SSH login {{ ago(user.last_login.time) }} from
                                    <IpAddress :ip="user.last_login.ip" />
                                </template>
                                <template v-else>no SSH login in the last 7 days</template>
                            </p>
                        </div>
                        <button type="button" :class="smallButton" class="text-slate-700" @click="toggle(user.name)">
                            {{ open === user.name ? 'Close' : 'Manage' }}
                        </button>
                    </div>

                    <div v-if="open === user.name" class="mt-4 space-y-5 rounded-xl bg-slate-50 p-4">
                        <p v-if="user.protected" class="text-xs text-slate-500">{{ user.protected_reason }}</p>

                        <!-- Keys -->
                        <div>
                            <p class="text-sm font-medium text-slate-900">Authorized keys</p>
                            <ul class="mt-2 space-y-1">
                                <li
                                    v-for="key in user.keys"
                                    :key="key.fingerprint"
                                    class="flex items-center justify-between gap-3 rounded-lg bg-white px-3 py-2 text-xs ring-1 ring-slate-900/5"
                                >
                                    <span class="min-w-0">
                                        <span class="font-medium text-slate-700">{{ key.comment || key.type }}</span>
                                        <span class="block truncate font-mono text-slate-400">{{ key.type }} {{ key.fingerprint }}</span>
                                    </span>
                                    <button
                                        v-if="!user.protected || user.uid === 0"
                                        type="button"
                                        class="shrink-0 text-rose-600 hover:underline"
                                        @click="removeKey(user.name, key)"
                                    >
                                        Remove
                                    </button>
                                </li>
                                <li v-if="!user.keys.length" class="text-xs text-slate-400">No keys installed.</li>
                            </ul>
                            <form v-if="!user.protected || user.uid === 0" class="mt-2 flex gap-2" @submit.prevent="addKey(user.name)">
                                <input
                                    v-model="keyForm.public_key"
                                    type="text"
                                    placeholder="Paste a public key: ssh-ed25519 AAAA… comment"
                                    class="block w-full rounded-lg border-slate-300 font-mono text-xs shadow-sm"
                                />
                                <button type="submit" :class="smallButton" class="shrink-0 bg-white text-slate-700" :disabled="!keyForm.public_key || keyForm.processing">
                                    Add key
                                </button>
                            </form>
                            <InputError class="mt-1" :message="keyForm.errors.public_key" />
                        </div>

                        <template v-if="!user.protected || user.uid === 0">
                            <p v-if="user.uid === 0 && user.password_locked" class="text-xs text-slate-600">
                                root's password is locked, which is how Ubuntu ships it: it can log in
                                with a key, never with a password. Setting a password below unlocks it.
                                To log in over SSH with that password, also allow root and passwords
                                under SSH settings.
                            </p>

                            <!-- Password -->
                            <form class="grid gap-2 sm:grid-cols-3" @submit.prevent="setPassword(user.name)">
                                <div>
                                    <label class="text-xs font-medium text-slate-700">New password</label>
                                    <input v-model="passwordForm.password" type="password" autocomplete="new-password" class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm" />
                                </div>
                                <div>
                                    <label class="text-xs font-medium text-slate-700">Confirm</label>
                                    <input v-model="passwordForm.password_confirmation" type="password" autocomplete="new-password" class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm" />
                                </div>
                                <div class="flex items-end">
                                    <button type="submit" :class="smallButton" class="bg-white py-2 text-slate-700" :disabled="!passwordForm.password || passwordForm.processing">
                                        Set password
                                    </button>
                                </div>
                                <InputError class="sm:col-span-3" :message="passwordForm.errors.password_confirmation || passwordForm.errors.password" />
                            </form>

                            <!-- Actions. Root can only be unlocked from here, never locked or removed. -->
                            <div v-if="user.uid === 0" class="flex flex-wrap gap-2 border-t border-slate-200 pt-4">
                                <button
                                    v-if="user.locked"
                                    type="button"
                                    :class="smallButton"
                                    class="bg-white text-emerald-600"
                                    @click="setLocked(user, false)"
                                >
                                    Unlock
                                </button>
                            </div>
                            <div v-else class="flex flex-wrap gap-2 border-t border-slate-200 pt-4">
                                <button type="button" :class="smallButton" class="bg-white text-slate-700" @click="setSudo(user, !user.sudo)">
                                    {{ user.sudo ? 'Remove sudo' : 'Give sudo' }}
                                </button>
                                <button type="button" :class="smallButton" class="bg-white" :style="{ color: user.locked ? '#059669' : '#d97706' }" @click="setLocked(user, !user.locked)">
                                    {{ user.locked ? 'Unlock' : 'Lock account' }}
                                </button>
                                <button type="button" :class="smallButton" class="bg-white text-rose-600" @click="remove(user)">
                                    Delete user
                                </button>
                            </div>
                        </template>
                    </div>
                </li>
                <li v-if="!users.length" class="px-6 py-8 text-center text-sm text-slate-400">
                    Could not read the accounts on this machine.
                </li>
            </ul>
            <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
        </div>
    </div>
</template>
