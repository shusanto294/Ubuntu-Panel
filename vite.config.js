import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import { fileURLToPath, URL } from 'node:url';

/*
 * Plain Vite: the Go panel reads the manifest itself (see internal/inertia).
 *
 * The output goes into internal/assets/public so that `go:embed` can pick it
 * up — a release is one binary with the frontend inside it, and there is no
 * second directory to install, mismatch, or forget.
 */
export default defineConfig({
    plugins: [vue()],

    resolve: {
        alias: {
            '@': fileURLToPath(new URL('./resources/js', import.meta.url)),
        },
    },

    build: {
        outDir: 'internal/assets/public/build',
        emptyOutDir: true,
        // The panel serves /build/<file> out of the embedded copy and reads
        // manifest.json to know which files those are.
        manifest: true,
        rollupOptions: {
            input: 'resources/js/app.js',
        },
    },

    base: '/build/',
});
