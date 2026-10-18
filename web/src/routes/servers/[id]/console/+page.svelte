<script lang="ts">
	import { onMount } from 'svelte';
	import { openSocket } from '#lib/api.ts';
	import { useServer } from '#lib/serverContext.svelte.ts';
	import Xterm from '#lib/Xterm.svelte';

	const ctx = useServer();

	let term: { write: (l: string) => void; clear: () => void } | null = null;
	let socket: WebSocket | null = null;
	let input = $state('');
	let conn = $state<'connecting' | 'open' | 'closed'>('connecting');
	let history: string[] = [];
	let historyIdx = -1;
	let closed = false;

	function register(api: { write: (l: string) => void; clear: () => void }) {
		term = api;
	}

	async function connect() {
		conn = 'connecting';
		try {
			const ws = await openSocket(`/api/servers/${ctx.id}/console`);
			socket = ws;
			ws.onopen = () => {
				conn = 'open';
				term?.clear();
			};
			ws.onmessage = (m) => {
				const e = JSON.parse(m.data);
				if (e.type === 'console_output') term?.write(e.data.line);
			};
			ws.onclose = () => {
				conn = 'closed';
				if (!closed) setTimeout(connect, 3000);
			};
		} catch {
			conn = 'closed';
			if (!closed) setTimeout(connect, 3000);
		}
	}

	onMount(() => {
		connect();
		return () => {
			closed = true;
			socket?.close();
		};
	});

	function send(e: SubmitEvent) {
		e.preventDefault();
		const cmd = input.trim().replace(/^\//, '');
		if (!cmd || socket?.readyState !== WebSocket.OPEN) return;
		socket.send(JSON.stringify({ type: 'input', data: cmd + '\n' }));
		history = [cmd, ...history.filter((h) => h !== cmd)].slice(0, 50);
		historyIdx = -1;
		input = '';
	}

	function keydown(e: KeyboardEvent) {
		if (e.key === 'ArrowUp' && history.length) {
			historyIdx = Math.min(historyIdx + 1, history.length - 1);
			input = history[historyIdx];
			e.preventDefault();
		} else if (e.key === 'ArrowDown') {
			historyIdx = Math.max(historyIdx - 1, -1);
			input = historyIdx < 0 ? '' : history[historyIdx];
			e.preventDefault();
		}
	}
</script>

<svelte:head><title>Console · {ctx.server?.name ?? ''} · mcsman</title></svelte:head>

<section class="console">
	<div class="screen">
		<Xterm {register} />
		{#if conn !== 'open'}
			<div class="overlay">{conn === 'connecting' ? 'Connecting…' : 'Disconnected — retrying…'}</div>
		{/if}
	</div>
	<form onsubmit={send}>
		<span class="prompt mono">&gt;</span>
		<input
			bind:value={input}
			onkeydown={keydown}
			placeholder={ctx.status === 'running' ? 'Type a command, e.g. say hello' : 'Server is not running'}
			disabled={conn !== 'open' || ctx.status !== 'running'}
			autocomplete="off"
			autocapitalize="none"
			spellcheck="false"
			class="mono"
		/>
		<button class="primary" type="submit" disabled={!input.trim() || conn !== 'open'}>Send</button>
	</form>
</section>

<style>
	.console {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		overflow: hidden;
		background: var(--terminal-bg);
		height: calc(100dvh - 260px);
		min-height: 320px;
	}
	.screen {
		position: relative;
		flex: 1;
		min-height: 0;
	}
	.overlay {
		position: absolute;
		top: 8px;
		right: 8px;
		padding: 4px 10px;
		border-radius: 999px;
		background: #2a2f36;
		color: #c9d1d9;
		font-size: 0.8rem;
	}
	form {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px;
		border-top: 1px solid #262b31;
		background: #151a1f;
	}
	.prompt {
		color: #4cb46c;
		padding-left: 4px;
	}
	input {
		flex: 1;
		background: #0f1216;
		border-color: #2d333b;
		color: #e6e9ed;
		min-height: 38px;
	}
</style>
