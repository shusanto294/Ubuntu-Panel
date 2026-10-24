<script setup>
import { computed, ref } from 'vue';
import { Link } from '@inertiajs/vue3';
import { copyText } from '@/clipboard';

/**
 * What has to happen outside the panel before this machine's DNS answers.
 *
 * This component is longer than the feature it explains, and deliberately. The
 * panel cannot do any of it: registering a nameserver is a screen at a
 * registrar, every registrar calls it something different, and getting it
 * wrong produces a zone editor that saves records nothing in the world can
 * find. So each step says what to do, what it is called where, and why — the
 * why because a glue record is the one piece of DNS that makes no sense at all
 * until somebody explains the loop it exists to break.
 */
const props = defineProps({
    status: { type: Object, required: true },
    installed: { type: Boolean, default: false },
    zones: { type: Array, default: () => [] },
});

const nameservers = computed(() => {
    const configured = [props.status.settings?.ns1, props.status.settings?.ns2].filter(
        Boolean,
    );

    if (configured.length) return configured;

    // Nothing saved yet, so the examples are drawn from what the form is
    // pre-filled with — the steps read as instructions rather than as
    // placeholders somebody has to mentally substitute.
    return [props.status.suggested?.ns1, props.status.suggested?.ns2].filter(Boolean);
});

const ip = computed(() => props.status.ip || 'this server’s address');

// The domain the nameservers are named on, which is the one that needs the
// glue records and the one whose zone has to be hosted here.
const nsDomain = computed(() => {
    const first = nameservers.value[0];

    if (!first) return 'example.com';

    return first.split('.').slice(1).join('.') || first;
});

const nsZoneHosted = computed(() =>
    props.zones.some((zone) => zone.name === nsDomain.value),
);

// Where the screen is, per registrar. Names rather than deep links: the paths
// change, and a wrong link is worse than a name somebody can search for in
// their own account.
const REGISTRARS = [
    {
        name: 'Namecheap',
        where: 'Domain List → Manage → Advanced DNS → Personal DNS Server',
        note: 'Add each nameserver, then they become selectable as custom nameservers.',
    },
    {
        name: 'GoDaddy',
        where: 'My Products → Domain → DNS → Hostnames → Add',
        note: 'GoDaddy calls them hostnames; the IP goes in the same row.',
    },
    {
        name: 'Porkbun',
        where: 'Domain Management → Details → Glue Records',
        note: 'Named exactly that, which is rarer than it should be.',
    },
    {
        name: 'Hostinger',
        where: 'Domains → Manage → DNS / Nameservers → Child nameservers',
        note: '"Child nameserver" is the same thing again.',
    },
    {
        name: 'Cloudflare Registrar',
        where: 'Not supported',
        note:
            'Cloudflare Registrar does not offer nameserver registration. Name your ' +
            'nameservers on a domain held somewhere that does — they do not have to ' +
            'be on a domain you are hosting here.',
        unsupported: true,
    },
];

const showAllRegistrars = ref(false);

// Copying the pair, because what the registrar's form wants is the hostname
// and the address and nobody types an IPv4 address correctly the first time.
const copied = ref('');

const copyRow = async (name) => {
    if (await copyText(`${name}\t${ip.value}`)) {
        copied.value = name;
        setTimeout(() => (copied.value = ''), 2000);
    }
};

const visibleRegistrars = computed(() =>
    showAllRegistrars.value ? REGISTRARS : REGISTRARS.slice(0, 3),
);

// Each step is done, waiting or not started, and the page says which rather
// than listing six things with no indication of where you are.
const steps = computed(() => [
    {
        key: 'install',
        title: 'Install the DNS server',
        done: props.installed && props.status.running,
    },
    {
        key: 'name',
        title: 'Name your nameservers',
        done: Boolean(props.status.settings?.ns1),
    },
    {
        key: 'glue',
        title: 'Register them at the registrar',
        done:
            props.status.glue?.length > 0 &&
            props.status.glue.every((check) => check.matches),
    },
    {
        key: 'zone',
        title: `Host ${nsDomain.value} here`,
        done: nsZoneHosted.value,
    },
    {
        key: 'point',
        title: 'Point a domain at them',
        done: props.zones.length > (nsZoneHosted.value ? 1 : 0),
    },
]);
</script>

<template>
    <div class="space-y-6">
        <!-- Where you are -->
        <ol
            class="flex flex-wrap gap-x-6 gap-y-2 rounded-2xl bg-white px-5 py-4 text-sm shadow-sm ring-1 ring-slate-900/5"
        >
            <li
                v-for="(step, index) in steps"
                :key="step.key"
                class="flex items-center gap-2"
            >
                <span
                    class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold"
                    :class="
                        step.done
                            ? 'bg-emerald-100 text-emerald-700'
                            : 'bg-slate-100 text-slate-500'
                    "
                >
                    <svg
                        v-if="step.done"
                        class="h-3 w-3"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="3"
                        viewBox="0 0 24 24"
                    >
                        <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            d="M4.5 12.75l6 6 9-13.5"
                        />
                    </svg>
                    <template v-else>{{ index + 1 }}</template>
                </span>
                <span :class="step.done ? 'text-slate-500' : 'font-medium text-slate-800'">
                    {{ step.title }}
                </span>
            </li>
        </ol>

        <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
            <div class="border-b border-slate-200 px-5 py-4">
                <h3 class="font-semibold text-slate-900">
                    Running your own nameservers
                </h3>
                <p class="mt-1 text-sm text-slate-500">
                    This is the arrangement Namecheap and GoDaddy are on the other
                    side of: a machine that answers DNS queries, and a registry that
                    sends people to it. The panel is the first half. The second half
                    is two names registered at a registrar, and nothing in here can
                    do it for you.
                </p>
            </div>

            <div class="divide-y divide-slate-100">
                <!-- 1 — the package -->
                <section class="px-5 py-5">
                    <h4 class="text-sm font-semibold text-slate-800">
                        1 · Install the DNS server
                    </h4>
                    <p class="mt-1 text-sm text-slate-600">
                        BIND, from the
                        <Link
                            :href="route('services.index')"
                            class="text-brand-600 hover:underline"
                            >Apps page</Link
                        >. The panel configures it as an authoritative server for
                        your zones and nothing else — recursion off, so this machine
                        cannot be used to reflect an attack at somebody — and opens
                        53 on both TCP and UDP.
                    </p>
                    <p
                        v-if="installed && status.running"
                        class="mt-2 text-sm text-emerald-700"
                    >
                        Installed and running.
                    </p>
                    <p v-else-if="installed" class="mt-2 text-sm text-amber-700">
                        Installed, but not running. The Apps page has its log.
                    </p>
                    <p v-else class="mt-2 text-sm text-amber-700">
                        Not installed yet. Everything below can be set up first; the
                        zones are written as soon as the package is there.
                    </p>
                    <p class="mt-2 text-xs text-slate-500">
                        Your host may have a firewall of its own, in front of the
                        machine. If port 53 is closed there, nothing reaches BIND
                        however this is configured.
                    </p>
                </section>

                <!-- 2 — the names -->
                <section class="px-5 py-5">
                    <h4 class="text-sm font-semibold text-slate-800">
                        2 · Name your nameservers
                    </h4>
                    <p class="mt-1 text-sm text-slate-600">
                        A delegation names nameservers; it does not carry addresses.
                        So this machine needs two hostnames — by convention
                        <code class="rounded bg-slate-100 px-1 font-mono text-xs"
                            >ns1</code
                        >
                        and
                        <code class="rounded bg-slate-100 px-1 font-mono text-xs"
                            >ns2</code
                        >
                        on a domain you own and intend to keep. Every domain you
                        host here will be pointed at those two names, so losing the
                        domain they are on takes every one of them down with it.
                    </p>
                    <p class="mt-2 text-sm text-slate-600">
                        They do not have to be on a domain hosted in this panel, and
                        the domain they are on does not have to be one of the ones
                        you are hosting. It does have to be at a registrar that
                        offers nameserver registration — see the next step.
                    </p>
                    <p class="mt-2 text-xs text-slate-500">
                        The form below this guide is where they go.
                    </p>
                </section>

                <!-- 3 — glue -->
                <section class="px-5 py-5">
                    <h4 class="text-sm font-semibold text-slate-800">
                        3 · Register them at the registrar — the glue records
                    </h4>
                    <p class="mt-1 text-sm text-slate-600">
                        At the registrar of
                        <span class="font-mono text-xs">{{ nsDomain }}</span>, tell
                        it that each nameserver name has this address:
                    </p>

                    <div class="mt-3 overflow-x-auto">
                        <table class="min-w-full text-sm">
                            <thead>
                                <tr
                                    class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                                >
                                    <th class="py-2 pr-6">Hostname</th>
                                    <th class="py-2 pr-6">IPv4 address</th>
                                    <th class="py-2"></th>
                                </tr>
                            </thead>
                            <tbody class="divide-y divide-slate-100">
                                <tr
                                    v-for="name in nameservers"
                                    :key="name"
                                    class="font-mono text-xs"
                                >
                                    <td class="py-2 pr-6 text-slate-800">{{ name }}</td>
                                    <td class="py-2 pr-6 text-slate-800">{{ ip }}</td>
                                    <td class="py-2 text-right">
                                        <button
                                            type="button"
                                            class="font-sans text-xs text-brand-600 hover:underline"
                                            @click="copyRow(name)"
                                        >
                                            {{ copied === name ? 'Copied' : 'Copy' }}
                                        </button>
                                    </td>
                                </tr>
                                <tr v-if="!nameservers.length">
                                    <td colspan="3" class="py-3 text-sm text-slate-500">
                                        Fill the form in below and the exact rows to
                                        add appear here.
                                    </td>
                                </tr>
                            </tbody>
                        </table>
                    </div>

                    <div
                        class="mt-4 rounded-xl bg-slate-50 p-4 text-sm text-slate-600 ring-1 ring-slate-900/5"
                    >
                        <p class="font-medium text-slate-700">
                            Why this step exists at all
                        </p>
                        <p class="mt-1">
                            A resolver looking up
                            <span class="font-mono text-xs">yoursite.com</span> asks
                            the
                            <span class="font-mono text-xs">.com</span> registry who
                            serves it, and is told
                            <span class="font-mono text-xs">{{
                                nameservers[0] || 'ns1.' + nsDomain
                            }}</span>. That is a name, so now it needs that name's
                            address — and if the only machine that knows the address
                            is the one it is trying to reach, it can never get
                            started. The glue record is the registry storing the
                            address <em>alongside</em> the delegation, so the answer
                            arrives in the same reply and the loop never happens.
                        </p>
                        <p class="mt-2">
                            It is also why this is done at a registrar rather than
                            in a zone file: only the registry sitting above your
                            domain is in a position to hand that address out.
                        </p>
                    </div>

                    <div class="mt-4">
                        <p class="text-sm font-medium text-slate-700">
                            What it is called, where
                        </p>
                        <p class="mt-1 text-sm text-slate-600">
                            Nobody agrees on the name. Glue record, host record,
                            child nameserver, private nameserver, personal DNS
                            server, "register a nameserver" — all the same screen.
                        </p>

                        <dl class="mt-3 space-y-3">
                            <div
                                v-for="registrar in visibleRegistrars"
                                :key="registrar.name"
                                class="text-sm"
                            >
                                <dt class="font-medium text-slate-800">
                                    {{ registrar.name }}
                                </dt>
                                <dd
                                    class="text-slate-600"
                                    :class="registrar.unsupported ? 'text-amber-700' : ''"
                                >
                                    {{ registrar.where }}
                                    <span class="block text-xs text-slate-500">
                                        {{ registrar.note }}
                                    </span>
                                </dd>
                            </div>
                        </dl>

                        <button
                            v-if="!showAllRegistrars"
                            type="button"
                            class="mt-3 text-sm text-brand-600 hover:underline"
                            @click="showAllRegistrars = true"
                        >
                            Show the rest
                        </button>
                    </div>

                    <div
                        v-if="nameservers.length > 1"
                        class="mt-4 rounded-xl bg-amber-50 p-4 text-sm text-amber-800 ring-1 ring-amber-200/60"
                    >
                        <p class="font-medium">Both names, one address</p>
                        <p class="mt-1">
                            Two nameservers on the same machine means two names
                            pointing at {{ ip }}. Most registries accept that and
                            the domain works. Some insist the two addresses differ,
                            and a few insist they are on different networks — if
                            yours refuses the second registration, either add a
                            second address to this server and register
                            <span class="font-mono text-xs">{{ nameservers[1] }}</span>
                            against that one, or point it at a different machine
                            entirely. A delegation to a nameserver that does not
                            answer is worse than one name: resolvers pick at random,
                            so the domain works intermittently.
                        </p>
                    </div>

                    <!-- Live check -->
                    <div v-if="status.glue?.length" class="mt-4 space-y-2">
                        <p class="text-sm font-medium text-slate-700">
                            What the internet says right now
                        </p>
                        <p
                            v-for="check in status.glue"
                            :key="check.name"
                            class="text-sm"
                            :class="
                                check.matches
                                    ? 'text-emerald-700'
                                    : check.addresses?.length
                                      ? 'text-rose-700'
                                      : 'text-amber-700'
                            "
                        >
                            <span class="font-mono text-xs">{{ check.name }}</span>
                            <template v-if="check.matches">
                                → {{ ip }} · registered correctly
                            </template>
                            <template v-else-if="check.addresses?.length">
                                → {{ check.addresses.join(', ') }} · that is not this
                                server
                            </template>
                            <template v-else>
                                → nothing yet. A registration just made can take an
                                hour or two to appear, sometimes longer.
                            </template>
                        </p>
                    </div>
                </section>

                <!-- 4 — the nameservers' own zone -->
                <section class="px-5 py-5">
                    <h4 class="text-sm font-semibold text-slate-800">
                        4 · Host
                        <span class="font-mono text-xs">{{ nsDomain }}</span> here too
                    </h4>
                    <p class="mt-1 text-sm text-slate-600">
                        The glue gets a resolver here the first time. After that it
                        asks this machine for the addresses of
                        <span class="font-mono text-xs">{{
                            nameservers[0] || 'ns1.' + nsDomain
                        }}</span>
                        and believes the answer — so this machine has to have one.
                        Add
                        <span class="font-mono text-xs">{{ nsDomain }}</span>
                        as a zone below and the panel creates those A records for
                        you.
                    </p>
                    <p class="mt-2 text-sm text-slate-600">
                        A zone that delegates to names it holds no addresses for is
                        what every DNS checker calls a lame delegation, and what
                        some registries refuse to accept at all.
                    </p>
                    <p
                        v-if="nsZoneHosted"
                        class="mt-2 text-sm text-emerald-700"
                    >
                        {{ nsDomain }} is hosted here.
                    </p>
                    <p v-else class="mt-2 text-sm text-amber-700">
                        {{ nsDomain }} is not hosted here yet.
                    </p>
                </section>

                <!-- 5 — the domains -->
                <section class="px-5 py-5">
                    <h4 class="text-sm font-semibold text-slate-800">
                        5 · Point each domain at your nameservers
                    </h4>
                    <p class="mt-1 text-sm text-slate-600">
                        For every domain you want to manage here: add it as a zone
                        below, then at <em>its</em> registrar replace the
                        nameservers with
                        <span v-for="(name, index) in nameservers" :key="name">
                            <span class="font-mono text-xs">{{ name }}</span
                            ><span v-if="index < nameservers.length - 1"> and </span>
                        </span>
                        <span v-if="!nameservers.length" class="font-mono text-xs"
                            >yours</span
                        >.
                    </p>
                    <p class="mt-2 text-sm text-slate-600">
                        Add the zone first. Changing the nameservers while the zone
                        does not exist here points the domain at a machine that
                        answers "no such domain", and that answer gets cached.
                    </p>
                    <p class="mt-2 text-xs text-slate-500">
                        Nameserver changes propagate over hours rather than minutes,
                        and the old nameservers keep answering for whoever has them
                        cached. Leave the records at the old host in place until
                        traffic has moved.
                    </p>
                </section>

                <!-- 6 — the rest of the panel -->
                <section class="px-5 py-5">
                    <h4 class="text-sm font-semibold text-slate-800">
                        6 · Then the panel uses it like any other provider
                    </h4>
                    <p class="mt-1 text-sm text-slate-600">
                        Once a zone is here, "This server" shows up wherever a DNS
                        provider does. Create a site on a hosted domain and its A
                        records are published and its certificate issued without
                        anybody copying anything; add a mail domain and the MX, SPF,
                        DKIM and DMARC records are written straight into the zone.
                        Deleting either takes its records back out.
                    </p>
                </section>
            </div>
        </div>
    </div>
</template>
