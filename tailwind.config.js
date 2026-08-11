import colors from 'tailwindcss/colors';
import defaultTheme from 'tailwindcss/defaultTheme';
import forms from '@tailwindcss/forms';

/** @type {import('tailwindcss').Config} */
export default {
    content: [
        './vendor/laravel/framework/src/Illuminate/Pagination/resources/views/*.blade.php',
        './storage/framework/views/*.php',
        './resources/views/**/*.blade.php',
        './resources/js/**/*.vue',
        // The dialogs name their classes in JavaScript, because SweetAlert
        // takes them as strings rather than as markup. A file Tailwind does
        // not read is a set of classes it never emits, and the dialog would
        // render unstyled.
        './resources/js/**/*.js',
    ],

    theme: {
        extend: {
            fontFamily: {
                sans: ['Figtree', ...defaultTheme.fontFamily.sans],
            },

            colors: {
                // The accent, named for what it is rather than what colour it
                // happens to be. Everything interactive — buttons, links, the
                // current menu row, focus rings — reaches for `brand`, so the
                // panel can be recoloured from this one line. Status colours
                // are deliberately not in here: amber means "working on it"
                // and red means "broken" whatever the accent is.
                brand: colors.blue,
            },
        },
    },

    plugins: [forms],
};
