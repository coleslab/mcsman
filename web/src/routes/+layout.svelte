<script lang="ts">
	import '../app.css';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { setUnauthorizedHandler } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { live } from '#lib/ws.svelte.ts';

	let { children } = $props();

	const PUBLIC = ['/login'];
	const isPublic = $derived(PUBLIC.some((p) => page.url.pathname.startsWith(p)));

	setUnauthorizedHandler(() => {
		session.user = null;
		live.stop();
		goto(`/login?next=${encodeURIComponent(page.url.pathname)}`);
	});

	session.load();

	$effect(() => {
		if (!session.loaded) return;
		if (session.user) {
			live.start();
		} else if (!isPublic) {
			live.stop();
			goto(`/login?next=${encodeURIComponent(page.url.pathname)}`, { replaceState: true });
		}
	});

	async function logout() {
		live.stop();
		await session.logout();
		goto('/login');
	}
</script>

{#if session.user && !isPublic}
	<header>
		<div class="container bar">
			<a href="/servers" class="brand">
				<img src="/favicon.svg" alt="" width="24" height="24" />
				<span>mcsman</span>
			</a>
			<span class="spacer"></span>
			{#if !live.connected}
				<span class="offline" title="Live updates disconnected; reconnecting…">offline</span>
			{/if}
			<span class="muted user">{session.user.username}</span>
			<button class="ghost small" onclick={logout}>Log out</button>
		</div>
	</header>
{/if}

{#if session.loaded && (session.user || isPublic)}
	{@render children()}
{/if}

<style>
	header {
		position: sticky;
		top: 0;
		z-index: 10;
		background: color-mix(in srgb, var(--bg) 85%, transparent);
		backdrop-filter: blur(8px);
		border-bottom: 1px solid var(--border);
	}
	.bar {
		display: flex;
		align-items: center;
		gap: 10px;
		padding-top: 10px;
		padding-bottom: 10px;
	}
	.brand {
		display: flex;
		align-items: center;
		gap: 8px;
		font-weight: 700;
		text-decoration: none;
		font-size: 1.05rem;
	}
	.user {
		font-size: 0.875rem;
	}
	.offline {
		font-size: 0.75rem;
		padding: 2px 8px;
		border-radius: 999px;
		background: var(--warn-soft);
		color: var(--warn);
	}
	@media (max-width: 420px) {
		.user {
			display: none;
		}
	}
</style>
