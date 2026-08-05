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
