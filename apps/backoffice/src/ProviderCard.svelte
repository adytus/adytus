<script lang="ts">
	import { api, endpoints, providerName, type Provider } from './api.ts';

	let {
		provider,
		models,
		busy,
		onsave,
		ontested
	}: {
		provider: Provider;
		models: number;
		busy: boolean;
		onsave: (baseURL: string, key?: string) => Promise<boolean>;
		ontested: (ids: string[]) => void;
	} = $props();

	const CUSTOM = 'custom';
	const known = $derived(endpoints[provider.id] ?? []);

	let baseURL = $state('');
	// The picked endpoint's URL, or CUSTOM when the URL is typed in.
	let choice = $state('');
	let key = $state('');
	let test = $state<{ ok: boolean; text: string } | null>(null);
	let testing = $state(false);

	// Follow the saved URL until the admin edits it.
	$effect.pre(() => {
		baseURL = provider.base_url;
		choice = known.some((e) => e.url === provider.base_url) ? provider.base_url : CUSTOM;
	});

	function pick(value: string) {
		choice = value;
		if (value !== CUSTOM) baseURL = value;
	}

	const dirty = $derived(baseURL !== provider.base_url || key !== '');

	async function save() {
		test = null;
		if (await onsave(baseURL, key === '' ? undefined : key)) key = '';
	}

	async function clearKey() {
		if (!confirm(`Remove the ${providerName[provider.id]} key?`)) return;
		test = null;
		await onsave(provider.base_url, '');
	}

	async function runTest() {
		testing = true;
		test = null;
		try {
			const { models: ids } = await api.testProvider(provider.id);
			ontested(ids);
			test = { ok: true, text: `Reached. It offers ${ids.length} model${ids.length === 1 ? '' : 's'}; they're suggested when you add one.` };
		} catch (err) {
			test = { ok: false, text: (err as Error).message };
		} finally {
			testing = false;
		}
	}
</script>

<form class="card" onsubmit={(e) => (e.preventDefault(), save())}>
	<div class="head">
		<h3>{providerName[provider.id]}</h3>
		<span class="use">{models ? `${models} enabled model${models === 1 ? '' : 's'}` : 'Unused'}</span>
	</div>

	<label class="field">
		Endpoint
		<select value={choice} onchange={(e) => pick(e.currentTarget.value)}>
			{#each known as e (e.url)}<option value={e.url}>{e.label} — {e.url}</option>{/each}
			<option value={CUSTOM}>Custom URL…</option>
		</select>
	</label>

	{#if choice === CUSTOM}
		<label class="field">
			Base URL
			<input type="url" required bind:value={baseURL} autocomplete="off" spellcheck="false" placeholder="https://…/v1" />
		</label>
	{/if}

	<label class="field">
		API key
		<input
			type="password"
			bind:value={key}
			autocomplete="new-password"
			spellcheck="false"
			placeholder={provider.key_set
				? provider.key_hint
					? `Saved, ends in ${provider.key_hint}. Type to replace.`
					: 'Saved. Type to replace.'
				: provider.id === 'ollama'
					? 'Optional; only for an Ollama behind a proxy'
					: provider.id === 'nearai'
						? 'Not set; create one at cloud.near.ai'
						: 'Not set'}
		/>
	</label>

	{#if test}
		<p class="test" class:ok={test.ok} role="status">{test.text}</p>
	{/if}

	<div class="actions">
		<button class="btn primary" type="submit" disabled={busy || !dirty}>{busy ? 'Saving…' : 'Save'}</button>
		<button class="btn" type="button" disabled={testing || dirty} title={dirty ? 'Save first' : undefined} onclick={runTest}>
			{testing ? 'Testing…' : 'Test connection'}
		</button>
		{#if provider.key_set}
			<button class="btn danger" type="button" disabled={busy} onclick={clearKey}>Remove key</button>
		{/if}
	</div>
</form>

<style>
	.card {
		display: grid;
		gap: 0.875rem;
		align-content: start;
		padding: 1rem;
		border: 1px solid var(--rule);
		border-radius: 16px;
		background: var(--deep);
		color: var(--paper);
	}
	.head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 0.5rem;
	}
	h3 {
		margin: 0;
		font-size: 1.125rem;
		font-weight: 800;
	}
	.use {
		font-size: 0.8125rem;
		color: var(--muted);
	}
	.field input,
	.field select {
		width: 100%;
	}
	.test {
		margin: 0;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--line-red);
		overflow-wrap: anywhere;
	}
	.test.ok {
		color: var(--line-green);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
</style>
