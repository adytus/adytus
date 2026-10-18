<script lang="ts">
	import type { Snippet } from 'svelte';

	// A notice that says what happened and, when there is one, what to do.
	let {
		tone = 'error',
		title,
		children,
		action,
		ondismiss
	}: {
		tone?: 'error' | 'ok' | 'info';
		title: string;
		children?: Snippet;
		action?: Snippet;
		ondismiss?: () => void;
	} = $props();
</script>

<div class="alert {tone}" role={tone === 'error' ? 'alert' : 'status'}>
	<span class="mark" aria-hidden="true">{tone === 'error' ? '!' : tone === 'ok' ? '✓' : 'i'}</span>
	<div class="body">
		<p class="title">{title}</p>
		{#if children}<div class="detail">{@render children()}</div>{/if}
	</div>
	{#if action || ondismiss}
		<div class="actions">
			{#if action}{@render action()}{/if}
			{#if ondismiss}
				<button class="close" type="button" aria-label="Dismiss" onclick={ondismiss}>×</button>
			{/if}
		</div>
	{/if}
</div>

<style>
	.alert {
		--tone: var(--line-red);
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		align-items: start;
		gap: 0.75rem;
		padding: 0.75rem 0.875rem;
		border-radius: 6px;
		background: color-mix(in srgb, var(--tone) 12%, var(--black));
		border-left: 4px solid var(--tone);
		color: var(--paper);
		text-align: left;
	}
	.ok {
		--tone: var(--line-green);
	}
	.info {
		--tone: var(--line-blue);
	}
	.mark {
		display: grid;
		place-items: center;
		width: 1.375rem;
		height: 1.375rem;
		border-radius: 50%;
		background: var(--tone);
		color: #000;
		font-weight: 900;
		font-size: 0.8125rem;
		line-height: 1;
	}
	.error .mark,
	.info .mark {
		color: #fff;
	}
	.body {
		display: grid;
		gap: 0.25rem;
		padding-top: 0.0625rem;
	}
	.title {
		margin: 0;
		font-weight: 700;
		line-height: 1.35;
	}
	.detail {
		font-size: 0.875rem;
		line-height: 1.45;
		color: var(--text-soft);
	}
	.detail :global(p) {
		margin: 0;
	}
	.actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.actions :global(.btn) {
		font-size: 0.8125rem;
		padding: 0.3rem 0.75rem;
		text-decoration: none;
	}
	.close {
		font: inherit;
		font-size: 1.25rem;
		line-height: 1;
		background: none;
		border: 0;
		color: var(--muted);
		cursor: pointer;
		padding: 0 0.25rem;
	}
	.close:hover {
		color: var(--paper);
	}
	@media (max-width: 520px) {
		.alert {
			grid-template-columns: auto minmax(0, 1fr);
		}
		.actions {
			grid-column: 2;
		}
	}
</style>
