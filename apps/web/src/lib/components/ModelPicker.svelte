<script lang="ts">
	import type { Model } from '../api.ts';
	import ModelIcon from './ModelIcon.svelte';

	let {
		models,
		value,
		disabled = false,
		onchange
	}: { models: Model[]; value: string; disabled?: boolean; onchange: (id: string) => void } = $props();

	let open = $state(false);
	let root = $state<HTMLElement>();
	const current = $derived(models.find((m) => m.id === value));

	const price = (n: number) => `$${n < 1 ? n.toFixed(2) : n % 1 ? n.toFixed(2) : n.toFixed(0)}`;

	function pick(id: string) {
		open = false;
		if (id !== value) onchange(id);
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Escape' && open) {
			open = false;
			(root?.querySelector('.trigger') as HTMLElement | null)?.focus();
		}
		if (!open || (e.key !== 'ArrowDown' && e.key !== 'ArrowUp')) return;
		e.preventDefault();
		const opts = Array.from(root?.querySelectorAll<HTMLElement>('[role=option]') ?? []);
		const i = opts.indexOf(document.activeElement as HTMLElement);
		const next = e.key === 'ArrowDown' ? Math.min(opts.length - 1, i + 1) : Math.max(0, i - 1);
		opts[next]?.focus();
	}
</script>

<svelte:window
	onclick={(e) => {
		if (open && root && !root.contains(e.target as Node)) open = false;
	}}
/>

<div class="picker" bind:this={root} onkeydown={onKey} role="presentation">
	<button
		class="trigger"
		type="button"
		aria-haspopup="listbox"
		aria-expanded={open}
		aria-label="Model: {current?.name ?? 'none'}"
		{disabled}
		onclick={() => (open = !open)}
	>
		<ModelIcon icon={current?.icon} size={1.375} />
		<span class="name">{current?.name ?? 'Choose a model'}</span>
		<span class="chev" aria-hidden="true"></span>
	</button>
	{#if open}
		<ul class="menu" role="listbox" aria-label="Models">
			{#each models as m (m.id)}
				<li>
					<button
						type="button"
						role="option"
						aria-selected={m.id === value}
						class="opt"
						onclick={() => pick(m.id)}
					>
						<ModelIcon icon={m.icon} size={2} />
						<span class="info">
							<span class="oname">{m.name}</span>
							<span class="meta">
								{price(m.prompt_usd_per_mtok)} in, {price(m.completion_usd_per_mtok)} out per million tokens
							</span>
							<span class="caps">
								{#if m.vision}<span>Sees images</span>{/if}
								{#if m.tools}<span>Uses tools</span>{/if}
								<span>{Math.round(m.context_tokens / 1000)}K context</span>
							</span>
						</span>
					</button>
				</li>
			{/each}
		</ul>
	{/if}
</div>

<style>
	.picker {
		position: relative;
	}
	.trigger {
		font: inherit;
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		background: #0d0d0d;
		color: var(--paper);
		border: 2px solid var(--rule);
		border-radius: 999px;
		padding: 0.25rem 0.75rem 0.25rem 0.3rem;
		cursor: pointer;
		font-size: 0.875rem;
		font-weight: 700;
		max-width: 100%;
	}
	.trigger:hover:not(:disabled) {
		border-color: var(--stock-dim);
	}
	.trigger:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.name {
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.chev {
		width: 0.4rem;
		height: 0.4rem;
		border-right: 2px solid currentColor;
		border-bottom: 2px solid currentColor;
		transform: translateY(-2px) rotate(45deg);
		margin-left: 0.125rem;
		flex: none;
	}
	.menu {
		position: absolute;
		right: 0;
		top: calc(100% + 0.375rem);
		z-index: 20;
		list-style: none;
		margin: 0;
		padding: 0.375rem;
		width: min(24rem, calc(100vw - 2rem));
		background: #0d0d0d;
		border: 2px solid var(--rule);
		border-radius: 8px;
		box-shadow: 0 12px 32px rgb(0 0 0 / 0.6);
	}
	.opt {
		font: inherit;
		width: 100%;
		display: flex;
		gap: 0.75rem;
		align-items: flex-start;
		text-align: left;
		background: none;
		border: 0;
		color: var(--paper);
		padding: 0.625rem;
		border-radius: 6px;
		cursor: pointer;
	}
	.opt:hover,
	.opt:focus-visible {
		background: #1a1a1a;
		outline-offset: -2px;
	}
	.opt[aria-selected='true'] {
		background: #1a1a1a;
		box-shadow: inset 3px 0 0 var(--line-yellow);
	}
	.info {
		display: grid;
		gap: 0.2rem;
		min-width: 0;
	}
	.oname {
		font-weight: 700;
	}
	.meta {
		font-size: 0.75rem;
		color: var(--muted);
	}
	.caps {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
	}
	.caps span {
		font-size: 0.6875rem;
		font-weight: 600;
		padding: 0.0625rem 0.375rem;
		border-radius: 3px;
		background: #222;
		color: #c9c6bf;
	}
</style>
