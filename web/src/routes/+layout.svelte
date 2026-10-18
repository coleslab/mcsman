<script lang="ts">
	import '../app.css';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { setUnauthorizedHandler } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { live } from '#lib/ws.svelte.ts';

	let { children } = $props();

	// Pages anyone can open without signing in ("/" redirects to /status).
	const PUBLIC = ['/login', '/status', '/join'];
	// Pages that render without the app header.
	const BARE = ['/login', '/join'];
	const path = $derived(page.url.pathname);
	const isPublic = $derived(path === '/' || PUBLIC.some((p) => path.startsWith(p)));
	const isBare = $derived(BARE.some((p) => path.startsWith(p)));

	setUnauthorizedHandler(() => {
		session.user = null;
		live.stop();
		if (!isPublic) goto(`/login?next=${encodeURIComponent(path)}`);
	});

	session.load();

	$effect(() => {
		if (!session.loaded) return;
		if (session.user) {
			live.start();
		} else {
			live.stop();
			if (!isPublic) goto(`/login?next=${encodeURIComponent(path)}`, { replaceState: true });
		}
	});

	const nav = $derived(
		session.isAdmin
			? [
					{ href: '/servers', label: 'Servers' },
					{ href: '/users', label: 'Users' },
					{ href: '/status', label: 'Status' }
				]
			: [{ href: '/status', label: 'Status' }]
	);

	async function logout() {
		live.stop();
		await session.logout();
		goto('/status');
	}
</script>

{#if !isBare && session.loaded}
	<header>
		<div class="container bar">
			<a href={session.isAdmin ? '/servers' : '/status'} class="brand">
				<img src="/favicon.svg" alt="" width="24" height="24" />
				<span>mcsman</span>
			</a>
			{#if session.user}
				<nav>
					{#each nav as n (n.href)}
						<a href={n.href} class:active={path.startsWith(n.href)}>{n.label}</a>
					{/each}
				</nav>
			{/if}
			<span class="spacer"></span>
			{#if session.user}
				{#if !live.connected}
					<span class="offline" title="Live updates disconnected; reconnecting…">offline</span>
				{/if}
				<a href="/account" class="user" class:active={path.startsWith('/account')}>{session.user.username}</a>
				<button class="ghost small" onclick={logout}>Log out</button>
			{:else}
				<a class="btn small-btn" href="/login">Log in</a>
			{/if}
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
	nav {
		display: flex;
		gap: 2px;
		margin-left: 8px;
	}
	nav a,
	.user {
		padding: 6px 10px;
		border-radius: var(--radius-sm);
		text-decoration: none;
		color: var(--text-2);
		font-size: 0.9rem;
		font-weight: 550;
	}
	nav a:hover,
	.user:hover {
		color: var(--text);
	}
	nav a.active,
	.user.active {
		color: var(--text);
		background: var(--surface-2);
	}
	.small-btn {
		min-height: 34px;
		font-size: 0.9rem;
	}
	.offline {
		font-size: 0.75rem;
		padding: 2px 8px;
		border-radius: 999px;
		background: var(--warn-soft);
		color: var(--warn);
	}
	@media (max-width: 560px) {
		.brand span {
			display: none;
		}
		nav {
			margin-left: 0;
		}
		.user {
			max-width: 90px;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}
	}
</style>
