<script lang="ts">
	import Alert from '../../web/src/lib/components/Alert.svelte';
	import ModelIcon from '../../web/src/lib/components/ModelIcon.svelte';
	import { api, providerName, type AdminModel, type State } from './api.ts';
	import ProviderCard from './ProviderCard.svelte';
	import ModelForm from './ModelForm.svelte';
	import Catalog from './Catalog.svelte';

	let st = $state<State | null>(null);
	let error = $state('');
	let busy = $state('');
	// Upstream model names each provider offered when last tested.
	let offered = $state<Record<string, string[]>>({});
	// The model being edited; a model without an id in the list is new.
	let editing = $state<AdminModel | null>(null);
	let browsing = $state(false);
	let filter = $state('');

	$effect(() => {
		api.state().then(
			(s) => (st = s),
			(err) => (error = err.message)
		);
	});

	async function run(key: string, fn: () => Promise<State>) {
		error = '';
		busy = key;
		try {
			st = await fn();
			return true;
		} catch (err) {
			error = (err as Error).message;
			return false;
		} finally {
			busy = '';
		}
	}

	const blank = (): AdminModel => ({
		id: '',
		provider: 'openrouter',
		upstream: '',
		name: '',
		prompt_usd_per_mtok: 0,
		completion_usd_per_mtok: 0,
		context_tokens: 128000,
		max_output_tokens: 4096,
		vision: false,
		tools: true,
		icon: { code: '', color: '#1e5bff', shape: 'circle' },
		enabled: true,
		default: false
	});

	const isNew = $derived(editing !== null && !st?.models.some((m) => m.id === editing!.id));

	async function save(m: AdminModel) {
		if (await run('save', () => api.putModel(m))) editing = null;
	}

	const add = (m: AdminModel) => run(`add:${m.id}`, () => api.putModel(m));

	const toggle = (m: AdminModel) => run(`en:${m.id}`, () => api.putModel({ ...m, enabled: !m.enabled }));

	const listed = $derived.by(() => {
		const words = filter.toLowerCase().split(/\s+/).filter(Boolean);
		return (st?.models ?? []).filter((m) =>
			words.every((w) => `${m.name} ${m.id} ${m.upstream}`.toLowerCase().includes(w))
		);
	});
	const enabledCount = $derived(st?.models.filter((m) => m.enabled).length ?? 0);

	function remove(m: AdminModel) {
		if (!confirm(`Delete ${m.name || m.id}? Chats on it can't continue until they switch model. Disabling it is undoable.`)) return;
		run(`del:${m.id}`, () => api.deleteModel(m.id));
	}

	const price = (n: number) => `$${n.toLocaleString(undefined, { maximumFractionDigits: 4 })}`;
	const tokens = (n: number) => (n >= 1000 ? `${Math.round(n / 1000)}K` : `${n}`);
</script>

<div class="page">
	<header>
		<h1>Backoffice</h1>
		<p class="muted">
			Server-wide settings: which models everyone can pick, and the keys the server uses to reach
			them. Changes apply to the next message sent. Nothing here opens anyone's chats or files.
		</p>
	</header>

	{#if error}
		<Alert title="Not saved" ondismiss={() => (error = '')}><p>{error}</p></Alert>
	{/if}

	{#if !st}
		{#if !error}<p class="muted">Loading…</p>{/if}
	{:else}
		<section aria-labelledby="prov-h">
			<h2 id="prov-h">Providers</h2>
			<p class="muted">
				Keys are sealed with the server key and never sent back; only their last four characters are
				shown. Every OpenRouter request is restricted to zero-data-retention providers.
			</p>
			<div class="providers">
				{#each st.providers as p (p.id)}
					<ProviderCard
						provider={p}
						models={st.models.filter((m) => m.provider === p.id && m.enabled).length}
						busy={busy === `prov:${p.id}`}
						onsave={(url, key) => run(`prov:${p.id}`, () => api.putProvider(p.id, url, key))}
						ontested={(ids) => (offered[p.id] = ids)}
					/>
				{/each}
			</div>
		</section>

		<section aria-labelledby="models-h">
			<div class="row">
				<h2 id="models-h">Models</h2>
				<div class="actions">
					<button class="btn" disabled={editing !== null} onclick={() => (editing = blank())}>Add manually</button>
					<button class="btn primary" disabled={browsing} onclick={() => (browsing = true)}>Browse models</button>
				</div>
			</div>
			<p class="muted">
				The whitelist users pick from. Prices are what users are charged, in USD per million tokens;
				OpenRouter's reported cost is billed instead when it sends one.
			</p>

			{#if browsing}
				<Catalog models={st.models} {busy} onadd={add} onclose={() => (browsing = false)} />
			{/if}

			{#if st.models.length > 0}
				<div class="row">
					<input class="filter" type="search" placeholder="Filter your models" bind:value={filter} spellcheck="false" />
					<span class="count">{enabledCount} of {st.models.length} offered</span>
				</div>
			{/if}

			{#if editing && isNew}
				<ModelForm
					model={editing}
					isNew
					offered={offered}
					saving={busy === 'save'}
					onsave={save}
					oncancel={() => (editing = null)}
				/>
			{/if}

			<ul class="models">
				{#each listed as m (m.id)}
					{#if editing && !isNew && editing.id === m.id}
						<li>
							<ModelForm
								model={editing}
								offered={offered}
								saving={busy === 'save'}
								onsave={save}
								oncancel={() => (editing = null)}
							/>
						</li>
					{:else}
						<li class="model" class:off={!m.enabled}>
							<button
								class="switch"
								role="switch"
								aria-checked={m.enabled}
								aria-label="Offer {m.name || m.id} to users"
								title={m.enabled ? 'Offered to users; click to disable' : 'Disabled; click to offer it'}
								disabled={busy !== ''}
								onclick={() => toggle(m)}
							>
								<span class="knob"></span>
							</button>
							<ModelIcon icon={m.icon} size={2} />
							<div class="about">
								<p class="name">
									{m.name || m.id}
									{#if m.default}<span class="tag default">Default</span>{/if}
									{#if !m.enabled}<span class="tag">Disabled</span>{/if}
								</p>
								<p class="meta">
									<code>{m.id}</code> → {providerName[m.provider]} <code>{m.upstream}</code>
								</p>
								<p class="meta">
									{price(m.prompt_usd_per_mtok)} in · {price(m.completion_usd_per_mtok)} out ·
									{tokens(m.context_tokens)} context · {tokens(m.max_output_tokens)} max reply
									{#if m.vision}· vision{/if}
									{#if m.tools}· tools{/if}
								</p>
							</div>
							<div class="actions">
								{#if m.enabled && !m.default}
									<button class="btn" disabled={busy !== ''} onclick={() => run('default', () => api.setDefault(m.id))}>
										Make default
									</button>
								{/if}
								<button class="btn" disabled={editing !== null} onclick={() => (editing = $state.snapshot(m))}>
									Edit
								</button>
								<button class="btn danger" disabled={busy !== ''} onclick={() => remove(m)}>Delete</button>
							</div>
						</li>
					{/if}
				{/each}
			</ul>
			{#if filter && listed.length === 0}
				<p class="muted">No model matches “{filter}”.</p>
			{/if}
		</section>
	{/if}
</div>

<style>
	.page {
		max-width: 60rem;
		margin: 0 auto;
		padding: 2rem 1.5rem 4rem;
		display: grid;
		gap: 2rem;
	}
	header {
		display: grid;
		gap: 0.75rem;
	}
	h1 {
		margin: 0;
		font-size: clamp(2.5rem, 6vw, 4rem);
		font-weight: 900;
		font-stretch: 70%;
		letter-spacing: -0.03em;
		line-height: 1;
		color: var(--headline);
	}
	h2 {
		margin: 0;
		font-size: 1.375rem;
		font-weight: 800;
		font-stretch: 85%;
	}
	section {
		display: grid;
		gap: 1rem;
	}
	.row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
	}
	.muted {
		color: var(--muted);
		margin: 0;
		line-height: 1.5;
		max-width: 65ch;
	}
	.providers {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 22rem), 1fr));
		gap: 1rem;
	}
	.models {
		list-style: none;
		margin: 0;
		padding: 0;
		border-top: 1px solid var(--rule);
	}
	.models > li {
		border-bottom: 1px solid var(--rule);
	}
	.model {
		display: grid;
		grid-template-columns: auto auto minmax(0, 1fr) auto;
		gap: 1rem;
		align-items: center;
		padding: 1rem 0;
	}
	.model.off > :global(svg),
	.model.off .about {
		opacity: 0.5;
	}
	.about {
		display: grid;
		gap: 0.25rem;
		min-width: 0;
	}
	.name {
		margin: 0;
		font-weight: 700;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
	}
	.meta {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-soft);
		overflow-wrap: anywhere;
	}
	code {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 0.8125rem;
		color: inherit;
	}
	.tag {
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		padding: 0.125rem 0.5rem;
		border-radius: 999px;
		border: 1px solid var(--muted);
		color: var(--muted);
	}
	.tag.default {
		border-color: var(--line-green);
		background: var(--line-green);
		color: var(--black);
	}
	.filter {
		font: inherit;
		flex: 1;
		max-width: 24rem;
		padding: 0.5rem 0.875rem;
		border: 1px solid var(--rule);
		border-radius: 999px;
		background: var(--surface);
		color: var(--paper);
	}
	.count {
		font-size: 0.875rem;
		color: var(--muted);
	}
	.switch {
		position: relative;
		width: 2.75rem;
		height: 1.5rem;
		padding: 0;
		border: 0;
		border-radius: 999px;
		background: var(--stock-dim);
		cursor: pointer;
		transition: background 0.15s;
	}
	.switch[aria-checked='true'] {
		background: var(--line-green);
	}
	.switch:disabled {
		cursor: not-allowed;
		opacity: 0.6;
	}
	.knob {
		position: absolute;
		top: 0.1875rem;
		left: 0.1875rem;
		width: 1.125rem;
		height: 1.125rem;
		border-radius: 50%;
		background: #fff;
		box-shadow: 0 1px 2px rgb(0 0 0 / 0.25);
		transition: transform 0.15s;
	}
	.switch[aria-checked='true'] .knob {
		transform: translateX(1.25rem);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: 0.5rem;
	}
	@media (max-width: 820px) {
		.page {
			padding: 1.5rem 1rem 3rem;
		}
		.model {
			grid-template-columns: auto auto minmax(0, 1fr);
		}
		.actions {
			grid-column: 1 / -1;
			justify-content: flex-start;
		}
	}
</style>
