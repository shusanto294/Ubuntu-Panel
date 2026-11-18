<script setup>
// The one thing about sending mail the panel cannot do itself: the reverse DNS
// (PTR) record is set at the hosting provider. Shown only while it is wrong,
// with the exact value to set; the panel notices when it is fixed.
defineProps({
    status: { type: Object, default: null },
});
</script>

<template>
    <div
        v-if="status && status.ip && !status.confirmed"
        class="rounded-2xl bg-amber-50 p-5 text-sm text-amber-900 ring-1 ring-amber-200/60"
    >
        <p class="font-medium">Gmail and others will refuse mail from this server until its reverse DNS is set.</p>
        <p class="mt-2">
            <template v-if="status.name">
                The reverse DNS for <code class="font-mono">{{ status.ip }}</code> says
                <code class="font-mono">{{ status.name }}</code>, which does not resolve back to this server.
            </template>
            <template v-else>
                <code class="font-mono">{{ status.ip }}</code> has no reverse DNS (PTR) record.
            </template>
            In your hosting provider's control panel, set the reverse DNS of
            <code class="font-mono">{{ status.ip }}</code> to
            <code class="select-all font-mono font-semibold">{{ status.suggested }}</code>.
        </p>
        <p class="mt-2 text-amber-800/80">
            It is set where the server was bought, not in DNS, so the panel cannot do it for you. Once it is
            right, the panel picks it up by itself within ten minutes and this notice goes away.
        </p>
    </div>
</template>
