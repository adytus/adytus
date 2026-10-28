<script lang="ts">
	import ModelIcon from '../../web/src/lib/components/ModelIcon.svelte';
	import { providerName, type AdminModel } from './api.ts';

	let {
		model,
		isNew = false,
		offered,
		saving,
		onsave,
		oncancel
	}: {
		model: AdminModel;
		isNew?: boolean;
		offered: Record<string, string[]>;
		saving: boolean;
		onsave: (m: AdminModel) => void;
		oncancel: () => void;
	} = $props();

	// The parent hands over a copy, so the form edits it in place.
	// svelte-ignore state_referenced_locally
	let m = $state(model);
	const listID = $derived(`upstream-${m.provider}`);
	const shapes = ['circle', 'square', 'diamond', 'hexagon'] as const;
	const idPattern = '[a-z0-9][a-z0-9._\\-]{0,63}';

	function submit(e: SubmitEvent) {
		e.preventDefault();
		const out = $state.snapshot(m);
		if (!out.vision) delete out.image_tokens;
		onsave(out);
	}

	// suggestIdentity writes a starting instruction from the name and
	// description; the backoffice can edit it.
	function suggestIdentity() {
		const name = m.name.trim() || m.id;
		const desc = (m.description ?? '').trim().replace(/[.\s]+$/, '');
		m.identity =
			`You are ${name}${desc ? `, ${desc}` : ''}. ` +
			`When asked who or what you are, say you are ${name}. ` +
			`Never name another model, provider or company.`;
	}
</script>

<form class="form" onsubmit={submit}>
	<div class="preview">
		<ModelIcon icon={m.icon.code ? m.icon : { ...m.icon, code: m.id.slice(0, 2).toUpperCase() || '?' }} size={2.5} />
		<h3>{isNew ? 'New model' : `Edit ${m.name || m.id}`}</h3>
	</div>

	<div class="grid">
		<label class="field">
			ID
			<input
				bind:value={m.id}
				required
				readonly={!isNew}
				pattern={idPattern}
				title="Lowercase letters, digits, dots, dashes or underscores"
				spellcheck="false"
			/>
			<span class="hint">{isNew ? 'Chats keep this id; it can’t change later.' : 'Fixed; chats refer to it.'}</span>
		</label>
		<label class="field">
			Name
			<input bind:value={m.name} required maxlength="80" />
		</label>
		<label class="field">
			Provider
			<select bind:value={m.provider}>
				{#each Object.entries(providerName) as [id, name] (id)}
					<option value={id}>{name}</option>
				{/each}
			</select>
		</label>
		<label class="field">
			Upstream model
			<input bind:value={m.upstream} required list={listID} spellcheck="false" placeholder={m.provider === 'ollama' ? 'gemma3:270m' : 'vendor/model-slug'} />
			<datalist id={listID}>
				{#each offered[m.provider] ?? [] as id (id)}<option value={id}></option>{/each}
			</datalist>
			<span class="hint">
				{offered[m.provider] ? 'Suggestions from the last connection test.' : 'Test the provider’s connection to get suggestions.'}
			</span>
		</label>
		<label class="field">
			Prompt price (USD / M tokens)
			<input type="number" bind:value={m.prompt_usd_per_mtok} min="0" step="any" required />
		</label>
		<label class="field">
			Completion price (USD / M tokens)
			<input type="number" bind:value={m.completion_usd_per_mtok} min="0" step="any" required />
		</label>
		<label class="field">
			Context window (tokens)
			<input type="number" bind:value={m.context_tokens} min="1" step="1" required />
		</label>
		<label class="field">
			Max reply (tokens)
			<input type="number" bind:value={m.max_output_tokens} min="1" max={m.context_tokens - 1} step="1" required />
		</label>
	</div>

	<fieldset>
		<legend>Abilities</legend>
		<label class="check"><input type="checkbox" bind:checked={m.tools} /> Calls tools</label>
		<label class="check"><input type="checkbox" bind:checked={m.vision} /> Sees images</label>
		{#if m.vision}
			<label class="field inline">
				Tokens held per image
				<input type="number" bind:value={m.image_tokens} min="1" step="1" required />
			</label>
		{/if}
	</fieldset>

	<fieldset class="rebrand">
		<legend>Rebrand</legend>
		<label class="field wide">
			Description
			<textarea
				bind:value={m.description}
				rows="2"
				maxlength="400"
				placeholder="A line for the model picker, e.g. Fast and good with numbers"
			></textarea>
			<span class="hint">Shown to users instead of the provider's blurb. Leave empty for none.</span>
		</label>
		<label class="field wide">
			Identity instruction
			<textarea
				bind:value={m.identity}
				rows="4"
				maxlength="2000"
				placeholder="You are … When asked who you are, say you are …"
			></textarea>
			<span class="hint">
				Added to the system prompt, so the model presents itself as this name and description rather
				than its upstream one. Leave empty to keep the provider's identity.
			</span>
		</label>
		<button class="btn" type="button" onclick={suggestIdentity}>Write it from the name</button>
	</fieldset>

	<fieldset>
		<legend>Badge</legend>
		<label class="field inline">
			Code
			<input bind:value={m.icon.code} maxlength="3" placeholder="Auto" />
		</label>
		<label class="field inline">
			Color
			<input type="color" bind:value={m.icon.color} />
		</label>
		<label class="field inline">
			Shape
			<select bind:value={m.icon.shape}>
				{#each shapes as s (s)}<option value={s}>{s}</option>{/each}
			</select>
		</label>
	</fieldset>

	<label class="check"><input type="checkbox" bind:checked={m.enabled} /> Offered to users</label>

	<div class="actions">
		<button class="btn primary" type="submit" disabled={saving}>{saving ? 'Saving…' : isNew ? 'Add model' : 'Save'}</button>
		<button class="btn" type="button" onclick={oncancel}>Cancel</button>
	</div>
</form>

<style>
	.form {
		display: grid;
		gap: 1rem;
		padding: 1.25rem;
		margin: 1rem 0;
		border: 1px solid var(--line-blue);
		border-radius: 16px;
		background: var(--deep);
		color: var(--paper);
	}
	.preview {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	h3 {
		margin: 0;
		font-size: 1.125rem;
		font-weight: 800;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 16rem), 1fr));
		gap: 0.875rem 1rem;
	}
	.field input,
	.field select {
		width: 100%;
	}
	.field input[readonly] {
		color: var(--muted);
	}
	.field input[type='color'] {
		padding: 0.125rem;
		height: 2.75rem;
		width: 4rem;
	}
	.hint {
		font-weight: 400;
		font-size: 0.75rem;
		color: var(--muted);
	}
	fieldset {
		display: flex;
		flex-wrap: wrap;
		align-items: end;
		gap: 0.75rem 1.5rem;
		margin: 0;
		padding: 0.75rem 1rem 1rem;
		border: 1px solid var(--rule);
		border-radius: 6px;
	}
	legend {
		padding: 0 0.375rem;
		font-size: 0.8125rem;
		font-weight: 700;
		color: var(--muted);
	}
	.inline input:not([type='color']),
	.inline select {
		width: 9rem;
	}
	.rebrand {
		display: grid;
		gap: 0.75rem;
	}
	.wide {
		width: 100%;
	}
	.wide textarea {
		font: inherit;
		width: 100%;
		padding: 0.5rem 0.625rem;
		resize: vertical;
	}
	.check {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 600;
		font-size: 0.9375rem;
		min-height: 2.75rem;
	}
	.check input {
		width: 1.125rem;
		height: 1.125rem;
		accent-color: var(--line-green);
	}
	.actions {
		display: flex;
		gap: 0.5rem;
	}
</style>
