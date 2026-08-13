<script setup>
// One level of the file manager's folder tree, which draws the level under it
// with itself. Nothing is fetched here: the page owns the nodes and is asked
// to open a branch, because it is also the thing that has to keep the tree and
// the listing agreeing with each other.
defineProps({
    nodes: { type: Array, default: () => [] },
    current: { type: String, default: '/' },
    depth: { type: Number, default: 0 },
});

const emit = defineEmits(['open', 'toggle']);
</script>

<template>
    <ul class="text-sm">
        <li v-for="node in nodes" :key="node.path">
            <div
                class="flex items-center gap-0.5 rounded-lg pr-2 transition"
                :class="
                    current === node.path
                        ? 'bg-brand-50 text-brand-700'
                        : 'text-slate-600 hover:bg-slate-100'
                "
                :style="{ paddingLeft: `${depth * 12}px` }"
            >
                <!--
                    The arrow opens the branch and the name walks to the
                    folder. Two targets in one row, because opening a folder to
                    see what is under it and going into it are different
                    intentions.
                -->
                <button
                    @click="emit('toggle', node)"
                    class="flex h-6 w-5 shrink-0 items-center justify-center text-slate-400 hover:text-slate-700"
                    :aria-label="node.open ? 'Collapse' : 'Expand'"
                >
                    <svg
                        v-if="!node.loading"
                        class="h-3 w-3 transition-transform"
                        :class="node.open ? 'rotate-90' : ''"
                        viewBox="0 0 12 12"
                        fill="currentColor"
                    >
                        <path d="M4 2l4 4-4 4z" />
                    </svg>
                    <span
                        v-else
                        class="h-2 w-2 animate-pulse rounded-full bg-slate-400"
                    />
                </button>

                <button
                    @click="emit('open', node.path)"
                    class="min-w-0 flex-1 truncate py-1 text-left"
                    :class="current === node.path ? 'font-medium' : ''"
                    :title="node.name"
                >
                    {{ node.name }}
                </button>
            </div>

            <FolderTree
                v-if="node.open && node.children?.length"
                :nodes="node.children"
                :current="current"
                :depth="depth + 1"
                @open="emit('open', $event)"
                @toggle="emit('toggle', $event)"
            />

            <p
                v-else-if="node.open && node.children && !node.children.length"
                class="py-1 text-xs text-slate-400"
                :style="{ paddingLeft: `${depth * 12 + 24}px` }"
            >
                No folders
            </p>
        </li>
    </ul>
</template>
