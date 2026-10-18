<script lang="ts">
	let { value, large = false }: { value: string; large?: boolean } = $props();
	let copied = $state(false);

	async function copy() {
		try {
			await navigator.clipboard.writeText(value);
		} catch {
			// Clipboard API needs a secure context; fall back to selection.
			const ta = document.createElement('textarea');
			ta.value = value;
			document.body.append(ta);
			ta.select();
			document.execCommand('copy');
			ta.remove();
		}
		copied = true;
		setTimeout(() => (copied = false), 1500);
	}
</script>

<div class="copy" class:large>
	<code>{value}</code>
	<button class="small" type="button" onclick={copy}>{copied ? 'Copied' : 'Copy'}</button>
</div>

<style>
	.copy {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 6px 6px 12px;
		background: var(--surface-2);
		border-radius: var(--radius-sm);
	}
	code {
		flex: 1;
		min-width: 0;
		font-family: var(--mono);
		font-size: 0.9rem;
		overflow-wrap: anywhere;
	}
	.large code {
		font-size: 1.1rem;
		font-weight: 600;
	}
</style>
