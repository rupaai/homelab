import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vitest/config';

const hub = 'http://127.0.0.1:7080';

export default defineConfig({
	plugins: [
		sveltekit({
			// Single-page app: every route falls back to index.html, which the Go program serves.
			adapter: adapter({ fallback: 'index.html', strict: false })
		})
	],
	server: { proxy: { '/api': hub, '/healthz': hub } },
	test: { include: ['src/**/*.test.ts'] }
});
