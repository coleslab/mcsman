<script lang="ts">
	// Read-only xterm.js view fed with console lines; input is a separate
	// field because the backend console is line-based.
	import { onMount } from 'svelte';
	import { Terminal } from '@xterm/xterm';
	import { FitAddon } from '@xterm/addon-fit';
	import '@xterm/xterm/css/xterm.css';

	let { register }: { register: (api: { write: (line: string) => void; clear: () => void }) => void } =
		$props();

	let el: HTMLDivElement;

	onMount(() => {
		const term = new Terminal({
			convertEol: true,
			disableStdin: true,
			cursorBlink: false,
			cursorStyle: 'bar',
			cursorInactiveStyle: 'none',
			scrollback: 5000,
			fontFamily: getComputedStyle(document.documentElement).getPropertyValue('--mono'),
			fontSize: window.innerWidth < 600 ? 11 : 13,
			theme: { background: '#0f1216', foreground: '#d6dbe1', selectionBackground: '#3a4553' }
		});
		const fit = new FitAddon();
		term.loadAddon(fit);
		term.open(el);
		fit.fit();

		const ro = new ResizeObserver(() => fit.fit());
		ro.observe(el);

		register({
			// Lines come from a PTY and may start with "> \r\x1b[K" prompt
			// redraws from Paper's line editor; write them through as-is.
			write: (line) => term.writeln(line),
			clear: () => term.clear()
		});

		return () => {
			ro.disconnect();
			term.dispose();
		};
	});
</script>

<div class="term" bind:this={el}></div>

<style>
	.term {
		width: 100%;
		height: 100%;
		min-height: 0;
		background: var(--terminal-bg);
		padding: 8px 0 0 8px;
	}
	.term :global(.xterm-viewport) {
		background: var(--terminal-bg) !important;
	}
</style>
