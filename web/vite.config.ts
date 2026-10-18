import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// `npm run dev` proxies the API to a locally running daemon.
const daemon = process.env.MCSMAN_DEV_API ?? 'http://127.0.0.1:8091';

export default defineConfig({
	plugins: [
		sveltekit({
			adapter: adapter({ pages: 'build', assets: 'build', fallback: 'index.html', strict: true })
		})
	],
	server: {
		proxy: { '/api': { target: daemon, ws: true, changeOrigin: true } }
	}
});
