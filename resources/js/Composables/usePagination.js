import { computed, ref, toValue, watch } from 'vue';

/*
 * A page at a time of a list the server already sent whole.
 *
 * The lists this is for are small enough to ship in one go — a few hundred
 * log lines, a few dozen sites — but not to read in one go, so the slicing
 * happens here rather than on the server. `items` is a ref, a computed or a
 * getter; when it shrinks under the current page (a filter, a delete) the page
 * follows it back rather than showing an empty one.
 */
export function usePagination(items, perPage = 25) {
    const page = ref(1);

    const total = computed(() => (toValue(items) ?? []).length);
    const pages = computed(() => Math.max(1, Math.ceil(total.value / perPage)));

    watch(pages, (count) => {
        if (page.value > count) page.value = count;
    });

    const visible = computed(() =>
        (toValue(items) ?? []).slice((page.value - 1) * perPage, page.value * perPage),
    );

    return { page, pages, total, perPage, visible };
}
