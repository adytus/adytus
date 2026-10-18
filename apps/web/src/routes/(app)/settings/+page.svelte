<script lang="ts">
	import { api, ApiError, type Tool } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import Alert from '#lib/components/Alert.svelte';
	import ModelIcon from '#lib/components/ModelIcon.svelte';

	let tools = $state<Tool[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let saving = $state('');
	let autoTitle = $state(true);

	$effect(() => {
		api.settings().then(
			(r) => {
				tools = r.tools;
				autoTitle = r.auto_title;
				loaded = true;
			},
			(err) => (error = err instanceof ApiError ? err.message : 'Settings could not be loaded.')
		);
	});

	async function toggle(t: Tool) {
		error = '';
		saving = t.id;
		const want = !t.enabled;
		t.enabled = want; // optimistic; reverted on failure
		try {
			tools = (await api.setTool(t.id, want)).tools;
		} catch (err) {
			t.enabled = !want;
			error = err instanceof ApiError ? err.message : 'The change was not saved.';
		} finally {
			saving = '';
		}
	}

	async function toggleAutoTitle() {
		error = '';
		saving = 'auto_title';
		const want = !autoTitle;
		autoTitle = want; // optimistic; reverted on failure
		try {
			autoTitle = (await api.setAutoTitle(want)).auto_title;
		} catch (err) {
			autoTitle = !want;
			error = err instanceof ApiError ? err.message : 'The change was not saved.';
		} finally {
			saving = '';
		}
	}

	const glyphs: Record<string, { mark: string; line: string }> = {
		calculator: { mark: '=', line: 'var(--line-blue)' },
		web_search: { mark: '?', line: 'var(--line-yellow)' },
		python: { mark: 'Py', line: 'var(--line-green)' }
	};
	const withTools = $derived(app.models.filter((m) => m.tools));
</script>

<div class="page">
	<header>
		<h1>Settings</h1>
	</header>

	{#if error}
		<Alert title="Settings not saved" ondismiss={() => (error = '')}><p>{error}</p></Alert>
	{/if}

	<section aria-labelledby="chats-h">
		<h2 id="chats-h">Chats</h2>
		{#if loaded}
			<ul class="tools">
				<li class="tool">
					<span class="glyph" style:--line="var(--line-purple)" aria-hidden="true">Aa</span>
					<div class="about">
						<p class="name" id="auto-title">Name chats automatically</p>
						<p class="desc">
							After your first message, the chat's model writes a short title for it. It's charged
							like a tiny reply. Off, a chat is named after the start of your first message.
						</p>
					</div>
					<button
						class="switch"
						role="switch"
						aria-checked={autoTitle}
						aria-labelledby="auto-title"
						disabled={saving === 'auto_title'}
						onclick={toggleAutoTitle}
					>
						<span class="knob"></span>
						<span class="state">{autoTitle ? 'On' : 'Off'}</span>
					</button>
				</li>
			</ul>
		{/if}
	</section>

	<section aria-labelledby="tools-h">
		<h2 id="tools-h">Tools</h2>
		<p class="muted">
			Models that support tools can call these while writing a reply. You'll see each call in the
			chat, and the extra model time is charged like any other reply.
		</p>

		{#if loaded}
			<ul class="tools">
				{#each tools as t (t.id)}
					{@const g = glyphs[t.id] ?? { mark: t.name[0], line: 'var(--line-purple)' }}
					<li class="tool">
						<span class="glyph" style:--line={g.line} aria-hidden="true">{g.mark}</span>
						<div class="about">
							<p class="name" id="tool-{t.id}">{t.name}</p>
							<p class="desc">{t.description}</p>
							{#if t.leaves_server}
								<p class="leaves">Leaves this server: your search text goes to public search engines.</p>
							{/if}
						</div>
						<button
							class="switch"
							role="switch"
							aria-checked={t.enabled}
							aria-labelledby="tool-{t.id}"
							disabled={saving === t.id}
							onclick={() => toggle(t)}
						>
							<span class="knob"></span>
							<span class="state">{t.enabled ? 'On' : 'Off'}</span>
						</button>
					</li>
				{:else}
					<li class="muted">This server has no tools set up.</li>
				{/each}
			</ul>
		{/if}

		<div class="models">
			<p class="muted">
				{#if withTools.length}
					Tools work with:
				{:else}
					None of the available models can use tools.
				{/if}
			</p>
			<ul>
				{#each withTools as m (m.id)}
					<li><ModelIcon icon={m.icon} size={1.25} />{m.name}</li>
				{/each}
			</ul>
		</div>
	</section>
</div>

<style>
	.page {
		max-width: 48rem;
		margin: 0 auto;
		padding: 2rem 1.5rem 4rem;
		display: grid;
		gap: 2rem;
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
		margin: 0 0 0.5rem;
		font-size: 1.375rem;
		font-weight: 800;
		font-stretch: 85%;
	}
	section {
		display: grid;
		gap: 1rem;
	}
	.muted {
		color: var(--muted);
		margin: 0;
		line-height: 1.5;
		max-width: 60ch;
	}
	.tools {
		list-style: none;
		margin: 0;
		padding: 0;
		border-top: 1px solid var(--rule);
	}
	.tool {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		gap: 1rem;
		align-items: center;
		padding: 1rem 0;
		border-bottom: 1px solid var(--rule);
	}
	.glyph {
		display: grid;
		place-items: center;
		width: 2.5rem;
		height: 2.5rem;
		border-radius: 8px;
		background: var(--line);
		color: #000;
		font-weight: 900;
		font-stretch: 80%;
		font-size: 1.125rem;
	}
	.about {
		display: grid;
		gap: 0.25rem;
	}
	.name {
		margin: 0;
		font-weight: 700;
	}
	.desc {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-soft);
		line-height: 1.45;
	}
	.leaves {
		margin: 0.125rem 0 0;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--line-yellow);
	}
	.switch {
		font: inherit;
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		background: none;
		border: 0;
		padding: 0.25rem;
		color: var(--muted);
		cursor: pointer;
		font-size: 0.8125rem;
		font-weight: 700;
	}
	.switch:disabled {
		cursor: progress;
	}
	.knob {
		position: relative;
		width: 2.75rem;
		height: 1.5rem;
		border-radius: 999px;
		background: var(--surface-2);
		transition: background 0.15s;
	}
	.knob::after {
		content: '';
		position: absolute;
		top: 0.1875rem;
		left: 0.1875rem;
		width: 1.125rem;
		height: 1.125rem;
		border-radius: 50%;
		background: var(--muted);
		transition: transform 0.15s;
	}
	.switch[aria-checked='true'] {
		color: var(--paper);
	}
	.switch[aria-checked='true'] .knob {
		background: var(--line-green);
	}
	.switch[aria-checked='true'] .knob::after {
		background: var(--black);
		transform: translateX(1.25rem);
	}
	.state {
		width: 1.75rem;
		text-align: left;
	}
	.models ul {
		list-style: none;
		margin: 0.5rem 0 0;
		padding: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1.25rem;
	}
	.models li {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 600;
		font-size: 0.875rem;
	}
	@media (max-width: 820px) {
		.page {
			padding: 1.5rem 1rem 3rem;
		}
	}
	@media (max-width: 520px) {
		.tool {
			grid-template-columns: auto minmax(0, 1fr);
		}
		.switch {
			grid-column: 2;
			justify-self: start;
		}
	}
</style>
