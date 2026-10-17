import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

// The backoffice is built as one ES module that the API serves to admins
// only (GET /api/backoffice/ui/backoffice.js). It is never part of the web
// app's bundle: the web app imports it at run time by URL. Styles are
// injected by the module, and the page inherits the web app's tokens and
// global classes (.btn, .field) from the shell it mounts into.
export default defineConfig({
	plugins: [svelte({ compilerOptions: { css: 'injected', runes: true } })],
	define: { 'process.env.NODE_ENV': '"production"' },
	build: {
		outDir: 'dist',
		emptyOutDir: true,
		lib: { entry: 'src/main.ts', formats: ['es'], fileName: () => 'backoffice.js' }
	}
});
