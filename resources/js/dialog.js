/*
 * The panel's dialogs.
 *
 * Everything destructive used to go through `window.confirm`, which the
 * browser draws in its own chrome, at the top of the window, with the site's
 * URL above it and no room for the one thing these questions actually need:
 * saying plainly what is about to be removed. This is SweetAlert2 with the
 * panel's own colours and wording, bundled with the rest of the frontend —
 * the panel serves its own assets and makes no outbound requests from a page,
 * so nothing here is fetched at runtime.
 *
 * Every function returns a promise. A caller that used to read `confirm()`
 * has to await this instead, which is the only change at the call sites.
 */
import Swal from 'sweetalert2';

// Tailwind classes rather than SweetAlert's stylesheet, so a dialog is made of
// the same buttons and cards as the page behind it. `brand` is the accent from
// tailwind.config.js: recolour the panel and the dialogs follow.
// SweetAlert gives the popup no padding of its own and spaces its parts with
// large margins instead — the icon alone carries 2.5em above it — so the box
// ends up with more air at the top than at the bottom. The margins are zeroed
// and the popup gets even padding on all four sides, with the rhythm between
// the parts set here rather than inherited.
//
// `!` on every one of them: SweetAlert injects its stylesheet at runtime,
// after this page's CSS, so a plain utility of the same specificity loses.
const base = {
    buttonsStyling: false,
    reverseButtons: true,
    focusCancel: true,
    heightAuto: false,
    customClass: {
        popup: 'rounded-2xl !p-6',
        icon: '!mx-auto !mt-0 !mb-5',
        title: '!p-0 !mb-2 text-lg font-semibold text-slate-900',
        htmlContainer: '!m-0 !px-0 text-sm text-slate-600',
        actions: '!mt-6 !mb-0 gap-2',
        confirmButton:
            'rounded-xl px-4 py-2.5 text-sm font-medium text-white shadow-sm transition',
        cancelButton:
            'rounded-xl px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50',
        denyButton:
            'rounded-xl px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50',
    },
};

const dangerConfirm = 'bg-rose-600 hover:bg-rose-700';
const brandConfirm = 'bg-brand-600 hover:bg-brand-700';

/**
 * Ask before something irreversible: dropping a database, deleting a site,
 * removing a mailbox and its mail.
 *
 * The confirm button is red and says what it does — "Delete", "Drop", "Remove"
 * — rather than "OK", because "OK" tells you nothing about which way round the
 * buttons are.
 *
 * @returns {Promise<boolean>} whether the operator went through with it.
 */
export async function confirmDanger(title, text, confirmText = 'Delete') {
    const result = await Swal.fire({
        ...base,
        icon: 'warning',
        iconColor: '#e11d48',
        title,
        text,
        showCancelButton: true,
        confirmButtonText: confirmText,
        cancelButtonText: 'Cancel',
        customClass: {
            ...base.customClass,
            confirmButton: `${base.customClass.confirmButton} ${dangerConfirm}`,
        },
    });

    return result.isConfirmed;
}

/**
 * Ask before something disruptive that is not destructive: reinstalling an
 * app, updating the panel out from under the page you are reading.
 *
 * @returns {Promise<boolean>}
 */
export async function confirmAction(title, text, confirmText = 'Continue') {
    const result = await Swal.fire({
        ...base,
        icon: 'question',
        iconColor: '#2563eb',
        title,
        text,
        showCancelButton: true,
        confirmButtonText: confirmText,
        cancelButtonText: 'Cancel',
        customClass: {
            ...base.customClass,
            confirmButton: `${base.customClass.confirmButton} ${brandConfirm}`,
        },
    });

    return result.isConfirmed;
}

/**
 * Say something happened, when there is no page change to say it for us.
 */
export function notify(title, text = '', icon = 'success') {
    return Swal.fire({
        ...base,
        icon,
        title,
        text,
        confirmButtonText: 'Close',
        customClass: {
            ...base.customClass,
            confirmButton: `${base.customClass.confirmButton} ${brandConfirm}`,
        },
    });
}

export default Swal;
