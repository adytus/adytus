import adapter from '@sveltejs/adapter-vercel';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},

			// Deployed on Vercel. The client-rendered shell is served statically and
			// /api is rewritten to the backend in vercel.json.
			adapter: adapter()
		})
	],
	server: {
		proxy: {
			'/api': 'http://localhost:8080',
			'/healthz': 'http://localhost:8080'
		}
	}
});
