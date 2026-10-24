<script lang="ts">
	// Shows a freshly issued recovery code as a tear-off ticket stub. The code
	// is the only way back into encrypted data after a lost password.
	let { code, onclose }: { code: string; onclose: () => void } = $props();
	let copied = $state(false);
	let kept = $state(false);

	async function copy() {
		await navigator.clipboard.writeText(code);
		copied = true;
	}
</script>

<div class="ticket stub" role="dialog" aria-labelledby="stub-title">
	<div class="body">
		<p class="micro">Recovery code · keep this stub</p>
		<h2 id="stub-title">Save your recovery code</h2>
		<p>
			Your chats and files are encrypted with your password. If you forget it, this code is the
			only way to get them back. We can't show it again or reset it for you.
		</p>
		<code class="code">{code}</code>
		<div class="actions">
			<button class="btn" type="button" onclick={copy}>{copied ? 'Copied' : 'Copy code'}</button>
		</div>
		<label class="check">
			<input type="checkbox" bind:checked={kept} />
			I've stored the code somewhere safe
		</label>
	</div>
	<div class="tear">
		<button class="btn primary" type="button" disabled={!kept} onclick={onclose}>Continue</button>
	</div>
</div>

<style>
	.stub {
		max-width: 30rem;
	}
	.body {
		padding: 1.25rem 1.25rem 1rem;
	}
	h2 {
		margin: 0.25rem 0 0.5rem;
		font-size: 1.5rem;
		font-weight: 800;
		letter-spacing: -0.02em;
	}
	p {
		margin: 0 0 1rem;
		line-height: 1.45;
	}
	.code {
		display: block;
		font-family: inherit;
		font-size: 1.375rem;
		font-weight: 700;
		font-stretch: 85%;
		letter-spacing: 0.06em;
		word-spacing: 0.3em;
		background: var(--ink);
		color: var(--stock);
		padding: 0.75rem 1rem;
		border-radius: 4px;
		overflow-wrap: anywhere;
		font-variant-numeric: tabular-nums;
	}
	.actions {
		margin: 0.75rem 0;
	}
	.check {
		display: flex;
		gap: 0.5rem;
		align-items: center;
		font-size: 0.875rem;
	}
	.tear {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
		border-top: 2px dashed rgb(0 0 0 / 0.35);
		padding: 0.75rem 1.25rem;
	}
</style>
