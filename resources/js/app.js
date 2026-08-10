import '../css/app.css';
import './bootstrap';

import { createInertiaApp, router } from '@inertiajs/vue3';
import { createApp, h } from 'vue';
import { RoutePlugin } from './route';

const appName = 'Ubuntu Panel';

/*
 * The Laravel build resolved pages through laravel-vite-plugin's
 * `resolvePageComponent`, which is a thin wrapper around `import.meta.glob`
 * that throws a readable error for a missing page. That is all this needs to
 * be, and doing it here removes the last dependency on a PHP package.
 */
const pages = import.meta.glob('./Pages/**/*.vue');

function resolvePage(name) {
    const path = `./Pages/${name}.vue`;
    const loader = pages[path];

    if (!loader) {
        throw new Error(`Page not found: ${path}. Did you forget to create it?`);
    }

    return loader().then((module) => module.default ?? module);
}

/*
 * The route table travels in the page's `ziggy` prop. `route()` reads it from
 * `window.Ziggy`, so it is published here — once for the first render, then
 * again after every visit, because a deploy can change the table underneath a
 * long-lived tab.
 */
function publishRoutes(page) {
    if (page?.props?.ziggy) window.Ziggy = page.props.ziggy;
}

router.on('success', (event) => publishRoutes(event.detail.page));

createInertiaApp({
    title: (title) => (title ? `${title} - ${appName}` : appName),
    resolve: resolvePage,
    setup({ el, App, props, plugin }) {
        publishRoutes(props.initialPage);

        return createApp({ render: () => h(App, props) })
            .use(plugin)
            .use(RoutePlugin)
            .mount(el);
    },
    progress: {
        color: '#4B5563',
    },
});
