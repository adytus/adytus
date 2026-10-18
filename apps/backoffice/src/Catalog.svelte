<script lang="ts">
	import ModelIcon from '../../web/src/lib/components/ModelIcon.svelte';
	import { api, providerName, type AdminModel, type Offer, type Provider } from './api.ts';

	let {
		models,
		busy,
		onadd,
		onclose
	}: {
		models: AdminModel[];
		busy: string;
		onadd: (m: AdminModel) => Promise<boolean>;
		onclose: () => void;
	} = $props();

	const PAGE = 40;
	let provider = $state<Provider['id']>('openrouter');
	let offers = $state<Record<string, Offer[]>>({});
	let error = $state('');
	let query = $state('');
	let zdrOnly = $state(true);
	let toolsOnly = $state(false);
	let visionOnly = $state(false);
	let freeOnly = $state(false);
	let shown = $state(PAGE);

	$effect(() => {
		const id = provider;
		if (offers[id]) return;
		error = '';
		api.offers(id).then(
			(r) => (offers[id] = r.offers),
			(err) => (error = (err as Error).message)
		);
	});

	// Reset paging when the filter changes.
	$effect(() => {
		void [provider, query, zdrOnly, toolsOnly, visionOnly, freeOnly];
		shown = PAGE;
	});

	const added = $derived(new Set(models.filter((m) => m.provider === provider).map((m) => m.upstream)));

	// Rank is OpenRouter's weekly usage order, kept through filtering.
	const matches = $derived.by(() => {
		const words = query.toLowerCase().split(/\s+/).filter(Boolean);
		return (offers[provider] ?? [])
			.map((o, i) => ({ o, rank: i + 1 }))
			.filter(({ o }) => {
				if (zdrOnly && !o.zdr) return false;
				if (toolsOnly && !o.tools) return false;
				if (visionOnly && !o.vision) return false;
				if (freeOnly && (o.prompt_usd_per_mtok > 0 || o.completion_usd_per_mtok > 0)) return false;
				const hay = `${o.name} ${o.upstream}`.toLowerCase();
				return words.every((w) => hay.includes(w));
			});
	});

	const colors = ['#104a4f', '#1e5bff', '#b98a2e', '#b3412f', '#5a5f8f', '#2e7d5b', '#7b4bd6'];
	const shapes = ['circle', 'square', 'diamond', 'hexagon'] as const;
	const hash = (s: string) => [...s].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7);

	// "Anthropic: Claude Opus 5.5" reads as "Claude Opus 5.5" in the picker.
	const shortName = (o: Offer) => o.name.replace(/^[^:]{1,40}:\s+/, '').slice(0, 80) || o.upstream;

	function freshID(o: Offer) {
		const base =
			(o.upstream.split('/').pop() ?? o.upstream)
				.toLowerCase()
				.replace(/[^a-z0-9._-]+/g, '-')
				.replace(/^[^a-z0-9]+|-+$/g, '')
				.slice(0, 60) || 'model';
		const taken = new Set(models.map((m) => m.id));
		let id = base;
		for (let n = 2; taken.has(id); n++) id = `${base}-${n}`;
		return id;
	}

	function toModel(o: Offer): AdminModel {
		const name = shortName(o);
		const context = o.context_tokens || 128000;
		// The hold for a reply is priced at this many tokens, so keep it modest.
		const maxOut = Math.max(1, Math.min(o.max_output_tokens || 8192, 16384, Math.floor(context / 2)));
		const code = name
			.split(/[\s\-_.]+/)
			.filter((w) => /^[a-z0-9]/i.test(w))
			.map((w) => w[0])
			.join('')
			.toUpperCase()
			.slice(0, 2);
		const vendor = o.upstream.split('/')[0];
		return {
			id: freshID(o),
			provider,
			upstream: o.upstream,
			name,
			prompt_usd_per_mtok: o.prompt_usd_per_mtok,
			completion_usd_per_mtok: o.completion_usd_per_mtok,
			context_tokens: context,
			max_output_tokens: maxOut,
			vision: o.vision,
			...(o.vision ? { image_tokens: 1500 } : {}),
			tools: o.tools,
			icon: { code, color: colors[hash(vendor) % colors.length], shape: shapes[hash(o.upstream) % shapes.length] },
			enabled: true,
			default: false
		};
	}

	const price = (n: number) => (n === 0 ? 'free' : `$${n.toLocaleString(undefined, { maximumFractionDigits: 3 })}`);
	const tokens = (n?: number) =>
		!n ? '—' : n >= 1e6 ? `${+(n / 1e6).toFixed(1)}M` : n >= 1000 ? `${Math.round(n / 1000)}K` : `${n}`;
</script>

<div class="catalog">
	<div class="top">
		<div class="tabs" role="tablist" aria-label="Provider">
			{#each Object.entries(providerName) as [id, name] (id)}
				<button
					role="tab"
					aria-selected={provider === id}
					class:on={provider === id}
					onclick={() => (provider = id as Provider['id'])}>{name}</button
				>
			{/each}
		</div>
		<button class="btn" onclick={onclose}>Done</button>
	</div>

	<input
		class="search"
		type="search"
		placeholder="Search {providerName[provider]} models, e.g. claude, llama 70b, vision"
		bind:value={query}
		spellcheck="false"
	/>

	{#if provider === 'openrouter'}
		<div class="filters">
			<label class="chip" class:on={zdrOnly}
				><input type="checkbox" bind:checked={zdrOnly} /> Zero data retention</label
			>
			<label class="chip" class:on={toolsOnly}><input type="checkbox" bind:checked={toolsOnly} /> Tools</label>
			<label class="chip" class:on={visionOnly}><input type="checkbox" bind:checked={visionOnly} /> Vision</label>
			<label class="chip" class:on={freeOnly}><input type="checkbox" bind:checked={freeOnly} /> Free</label>
		</div>
	{/if}

	{#if error}
		<p class="err" role="alert">{error}</p>
	{:else if !offers[provider]}
		<p class="muted">Loading {providerName[provider]} models…</p>
	{:else}
		<p class="muted count">
			{matches.length} model{matches.length === 1 ? '' : 's'}{provider === 'openrouter'
				? ', most used this week first'
				: ''}. Prices are per million tokens{provider === 'openrouter'
				? ' at the cheapest zero-data-retention endpoint'
				: ''}; you can edit them after adding.
		</p>
		<ul class="offers">
			{#each matches.slice(0, shown) as { o, rank } (o.upstream)}
				{@const m = toModel(o)}
				{@const have = added.has(o.upstream)}
				<li class:dim={!o.zdr}>
					{#if provider === 'openrouter'}<span class="rank">{rank}</span>{/if}
					<ModelIcon icon={m.icon} size={1.75} />
					<div class="about">
						<p class="name">
							{shortName(o)}
							{#if o.vision}<span class="tag">vision</span>{/if}
							{#if o.tools}<span class="tag">tools</span>{/if}
							{#if !o.zdr}<span class="tag warn">no ZDR</span>{/if}
						</p>
						<p class="meta"><code>{o.upstream}</code></p>
						{#if provider === 'openrouter'}
							<p class="meta">
								{price(o.prompt_usd_per_mtok)} in · {price(o.completion_usd_per_mtok)} out · {tokens(o.context_tokens)}
								context
							</p>
						{/if}
						{#if o.description}<p class="desc">{o.description}</p>{/if}
					</div>
					{#if have}
						<span class="have">Added</span>
					{:else}
						<button
							class="btn primary"
							disabled={busy !== '' || !o.zdr}
							title={o.zdr ? `Add as ${m.id}` : 'No zero-data-retention endpoint serves it, so requests would be refused'}
							onclick={() => onadd(m)}
						>
							{busy === `add:${m.id}` ? 'Adding…' : 'Add'}
						</button>
					{/if}
				</li>
			{/each}
		</ul>
		{#if matches.length > shown}
			<button class="btn more" onclick={() => (shown += PAGE)}>Show {Math.min(PAGE, matches.length - shown)} more</button>
		{/if}
	{/if}
</div>

<style>
	.catalog {
		display: grid;
		gap: 0.875rem;
		padding: 1.25rem;
		border: 1px solid var(--rule);
		border-radius: 16px;
		background: var(--deep);
		color: var(--paper);
	}
	.top {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
	}
	.tabs {
		display: inline-flex;
		padding: 0.25rem;
		gap: 0.25rem;
		border-radius: 999px;
		background: var(--surface-2);
	}
	.tabs button {
		font: inherit;
		font-size: 0.875rem;
		font-weight: 600;
		padding: 0.375rem 0.875rem;
		border: 0;
		border-radius: 999px;
		background: transparent;
		color: var(--muted);
		cursor: pointer;
	}
	.tabs button.on {
		background: var(--surface);
		color: var(--paper);
		box-shadow: 0 0 0 1px var(--rule);
	}
	.search {
		font: inherit;
		font-size: 1rem;
		width: 100%;
		padding: 0.75rem 1rem;
		border: 1px solid var(--rule);
		border-radius: 12px;
		background: var(--surface);
		color: var(--paper);
	}
	.filters {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-size: 0.8125rem;
		font-weight: 600;
		padding: 0.375rem 0.75rem;
		border: 1px solid var(--rule);
		border-radius: 999px;
		color: var(--text-soft);
		cursor: pointer;
		user-select: none;
	}
	.chip input {
		position: absolute;
		opacity: 0;
		pointer-events: none;
	}
	.chip:has(:focus-visible) {
		outline: 2px solid var(--line-blue);
		outline-offset: 2px;
	}
	.chip.on {
		border-color: var(--paper);
		background: var(--paper);
		color: var(--black);
	}
	.muted {
		margin: 0;
		color: var(--muted);
		font-size: 0.875rem;
		line-height: 1.5;
	}
	.err {
		margin: 0;
		color: var(--line-red);
		font-weight: 600;
		font-size: 0.875rem;
	}
	.offers {
		list-style: none;
		margin: 0;
		padding: 0;
		max-height: 34rem;
		overflow-y: auto;
		border-top: 1px solid var(--rule);
	}
	.offers li {
		display: grid;
		grid-template-columns: auto auto minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.75rem;
		padding: 0.75rem 0.25rem;
		border-bottom: 1px solid var(--rule);
	}
	.offers li.dim .about,
	.offers li.dim > :global(svg) {
		opacity: 0.55;
	}
	.rank {
		min-width: 1.75rem;
		text-align: right;
		font-size: 0.8125rem;
		font-variant-numeric: tabular-nums;
		color: var(--muted);
	}
	.about {
		display: grid;
		gap: 0.125rem;
		min-width: 0;
	}
	.name {
		margin: 0;
		font-weight: 700;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.375rem;
	}
	.meta,
	.desc {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-soft);
		overflow-wrap: anywhere;
	}
	.desc {
		color: var(--muted);
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	code {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 0.8125rem;
	}
	.tag {
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		padding: 0.0625rem 0.4375rem;
		border-radius: 999px;
		border: 1px solid var(--rule);
		color: var(--muted);
	}
	.tag.warn {
		border-color: var(--line-red);
		color: var(--line-red);
	}
	.have {
		font-size: 0.8125rem;
		font-weight: 700;
		color: var(--line-green);
		padding: 0 0.5rem;
	}
	.more {
		justify-self: center;
	}
	@media (max-width: 640px) {
		.offers li {
			grid-template-columns: auto minmax(0, 1fr) auto;
		}
		.rank {
			display: none;
		}
	}
</style>
