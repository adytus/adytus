<script lang="ts">
	import { goto } from '$app/navigation';
	import { tick } from 'svelte';
	import { api, ApiError, sendMessage, usd, bytes, type Attachment, type Chat, type FileItem, type Message } from '../api.ts';
	import { app, handoff } from '../state.svelte.ts';

	let { chatID }: { chatID: string | null } = $props();

	let chat = $state<Chat | null>(null);
	let messages = $state<Message[]>([]);
	let loading = $state(false);
	let loadError = $state('');

	let draft = $state('');
	let attached = $state<Attachment[]>([]);
	let pickerOpen = $state(false);
	let pickable = $state<FileItem[]>([]);
	let uploading = $state(false);

	let streaming = $state(false);
	let liveText = $state('');
	let holdMicros = $state(0);
	let sendError = $state<{ code: string; message: string } | null>(null);
	let controller: AbortController | null = null;

	let modelID = $state(app.defaultModel);
	let scroller = $state<HTMLElement>();
	let input = $state<HTMLTextAreaElement>();

	const model = $derived(app.model(modelID));
	const maxHold = $derived(model ? (model.max_output_tokens * model.completion_usd_per_mtok) : 0);

	$effect(() => {
		const id = chatID;
		if (!id) return;
		loading = true;
		api.chat(id).then(
			(r) => {
				chat = r.chat;
				messages = r.messages;
				modelID = r.chat.model;
				loading = false;
				scrollDown();
				const p = handoff.pending;
				if (p && p.chatID === id) {
					handoff.pending = null;
					send(p.content, p.files);
				}
			},
			(err) => {
				loading = false;
				loadError = err instanceof ApiError && err.status === 404 ? 'This chat no longer exists.' : String(err.message ?? err);
			}
		);
		return () => controller?.abort();
	});

	async function scrollDown() {
		await tick();
		scroller?.scrollTo({ top: scroller.scrollHeight });
	}

	async function submit(e?: SubmitEvent) {
		e?.preventDefault();
		const content = draft.trim();
		if (!content || streaming) return;
		const files = attached;
		if (!chatID) {
			try {
				const { chat: created } = await api.createChat(modelID);
				app.upsertChat(created);
				handoff.pending = { chatID: created.id, content, files };
				draft = '';
				attached = [];
				await goto(`/chat/${created.id}`);
			} catch (err) {
				sendError = { code: 'error', message: err instanceof ApiError ? err.message : 'Could not start a chat.' };
			}
			return;
		}
		draft = '';
		attached = [];
		await send(content, files);
	}

	async function send(content: string, files: { id: string; name: string }[]) {
		if (!chatID) return;
		sendError = null;
		streaming = true;
		liveText = '';
		holdMicros = 0;
		const tempID = `pending-${Date.now()}`;
		messages.push({ id: tempID, role: 'user', content, attachments: files, created_at: new Date().toISOString() });
		scrollDown();
		controller = new AbortController();
		try {
			await sendMessage(
				chatID,
				content,
				files.map((f) => f.id),
				{
					start: (d) => {
						holdMicros = d.hold_micros;
						if (chat && d.title && !chat.title) {
							chat.title = d.title;
						}
						app.refreshBalance();
					},
					delta: (d) => {
						liveText += d.text;
						scrollDown();
					},
					done: (d) => {
						messages = [...messages.filter((m) => m.id !== tempID), d.user_message, d.message];
						app.balance = d.balance;
						liveText = '';
						if (chat) app.upsertChat({ ...chat, updated_at: new Date().toISOString() });
					},
					error: (d) => {
						sendError = { code: d.error, message: d.message };
					}
				},
				controller.signal
			);
		} catch (err) {
			if (err instanceof DOMException && err.name === 'AbortError') {
				// The server bills what was generated; reload to show the saved partial reply.
				await reload();
			} else {
				messages = messages.filter((m) => m.id !== tempID);
				draft = content;
				attached = files;
				sendError =
					err instanceof ApiError
						? { code: err.code, message: err.message }
						: { code: 'error', message: 'The connection dropped before the reply finished.' };
			}
		} finally {
			streaming = false;
			holdMicros = 0;
			controller = null;
			app.refreshBalance();
			scrollDown();
		}
	}

	async function reload() {
		if (!chatID) return;
		const r = await api.chat(chatID);
		chat = r.chat;
		messages = r.messages;
	}

	function stop() {
		controller?.abort();
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			submit();
		}
	}

	async function changeModel(id: string) {
		modelID = id;
		if (chat) {
			chat = (await api.updateChat(chat.id, { model: id })).chat;
		}
	}

	async function rename(title: string) {
		if (!chat || title === chat.title) return;
		chat = (await api.updateChat(chat.id, { title })).chat;
		app.upsertChat(chat);
	}

	async function toggleOffline() {
		if (!chat) return;
		chat = (await api.setChatOffline(chat.id, !chat.offline)).chat;
		app.upsertChat(chat);
	}

	async function remove() {
		if (!chat || !confirm('Delete this chat and all its messages? This cannot be undone.')) return;
		await api.deleteChat(chat.id);
		app.removeChat(chat.id);
		goto('/chat');
	}

	async function openPicker() {
		pickerOpen = !pickerOpen;
		if (pickerOpen) pickable = (await api.files()).files.filter(canAttach);
	}

	const canAttach = (f: FileItem) => f.text || (f.image && !!model?.vision);

	function attach(f: FileItem) {
		if (attached.length >= 5 || attached.some((a) => a.id === f.id)) return;
		attached = [...attached, { id: f.id, name: f.name, image: f.image }];
		pickerOpen = false;
		input?.focus();
	}

	async function uploadAndAttach(e: Event) {
		const el = e.currentTarget as HTMLInputElement;
		const file = el.files?.[0];
		el.value = '';
		if (!file) return;
		uploading = true;
		try {
			const { file: up } = await api.upload(file);
			if (!canAttach(up)) {
				const why = up.image ? `${model?.name ?? 'This model'} can't see images; switch to a vision model to attach it.` : 'only text files and images can be attached to a message.';
				sendError = { code: 'validation', message: `${up.name} was saved to Files, but ${why}` };
			} else {
				attach(up);
			}
		} catch (err) {
			sendError = { code: 'error', message: err instanceof ApiError ? err.message : 'Upload failed.' };
		} finally {
			uploading = false;
		}
	}

	const perMTok = (n: number) => (n < 1 ? `$${n.toFixed(2)}` : `$${n.toFixed(n % 1 ? 2 : 0)}`);
</script>

<section class="chat">
	<header class="bar">
		{#if chat}
			<input
				class="title"
				value={chat.title}
				placeholder="Untitled chat"
				aria-label="Chat title"
				maxlength="200"
				onchange={(e) => rename(e.currentTarget.value)}
			/>
		{:else}
			<h1 class="title static">New chat</h1>
		{/if}
		<div class="controls">
			<label class="sr-only" for="model">Model</label>
			<select id="model" value={modelID} onchange={(e) => changeModel(e.currentTarget.value)} disabled={streaming}>
				{#each app.models as m (m.id)}
					<option value={m.id}>{m.name}</option>
				{/each}
			</select>
			{#if chat}
				<button
					class="toggle"
					class:on={chat.offline}
					aria-pressed={chat.offline}
					onclick={toggleOffline}
					title="Let the server read this chat while you're signed out, for scheduled and background jobs"
				>
					<span class="dot"></span>Offline access
				</button>
				<button class="btn danger" onclick={remove}>Delete</button>
			{/if}
		</div>
	</header>

	<div class="scroll" bind:this={scroller}>
		<div class="column">
			{#if loading}
				<p class="micro note">Loading chat…</p>
			{:else if loadError}
				<p class="error">{loadError}</p>
			{:else if messages.length === 0 && !streaming}
				<div class="blank">
					<p class="blank-head">Ask anything.</p>
					<p class="muted">
						Your message is encrypted before it's stored. {#if model}{model.name} costs
						{perMTok(model.prompt_usd_per_mtok)} per million input tokens and
						{perMTok(model.completion_usd_per_mtok)} per million output tokens.{/if}
					</p>
				</div>
			{/if}

			{#each messages as m (m.id)}
				{#if m.role === 'user'}
					<article class="ticket q">
						<p class="text">{m.content}</p>
						{#if m.attachments?.length}
							<ul class="att">
								{#each m.attachments as a (a.id)}<li class:img={a.image}>{a.name}</li>{/each}
							</ul>
						{/if}
					</article>
				{:else}
					<article class="a">
						<p class="text">{m.content}</p>
						<p class="receipt">
							{app.model(m.model ?? '')?.name ?? m.model}
							{#if m.usage}
								<span>{m.usage.prompt_tokens.toLocaleString()} in</span>
								<span>{m.usage.completion_tokens.toLocaleString()} out</span>
								<span class="fare">{usd(m.usage.cost_micros)}</span>
							{/if}
							{#if m.partial}<span class="cut">Stopped early</span>{/if}
						</p>
					</article>
				{/if}
			{/each}

			{#if streaming}
				<article class="a live" aria-live="polite">
					<p class="text">{liveText}<span class="caret"></span></p>
					<p class="receipt">
						{#if holdMicros}<span>Holding up to {usd(holdMicros)}</span>{:else}<span>Reserving fare…</span>{/if}
					</p>
				</article>
			{/if}

			{#if sendError}
				<div class="alert" role="alert">
					<p>{sendError.message}</p>
					{#if sendError.code === 'budget_exceeded'}
						<a class="btn" href="/account">Add credit</a>
					{/if}
				</div>
			{/if}
		</div>
	</div>

	<form class="composer" onsubmit={submit}>
		<div class="column">
			{#if attached.length}
				<ul class="chips">
					{#each attached as a (a.id)}
						<li class:img={a.image}>
							{a.name}
							<button type="button" aria-label="Remove {a.name}" onclick={() => (attached = attached.filter((x) => x.id !== a.id))}>×</button>
						</li>
					{/each}
				</ul>
			{/if}
			{#if pickerOpen}
				<div class="picker">
					<label class="btn upload">
						{uploading ? 'Uploading…' : 'Upload a file'}
						<input type="file" class="sr-only" onchange={uploadAndAttach} disabled={uploading} />
					</label>
					{#each pickable as f (f.id)}
						<button type="button" class="pick" onclick={() => attach(f)}>
							<span class:img={f.image}>{f.name}</span><span class="muted">{bytes(f.size)}</span>
						</button>
					{:else}
						<p class="muted">{model?.vision ? 'No text files or images yet.' : 'No text files yet.'} Upload one to attach it.</p>
					{/each}
				</div>
			{/if}
			<div class="box">
				<button type="button" class="clip" aria-expanded={pickerOpen} onclick={openPicker} disabled={attached.length >= 5}>
					Attach
				</button>
				<textarea
					bind:this={input}
					bind:value={draft}
					onkeydown={onKey}
					rows="1"
					placeholder="Message {model?.name ?? 'the model'}"
					aria-label="Message"
				></textarea>
				{#if streaming}
					<button type="button" class="btn" onclick={stop}>Stop</button>
				{:else}
					<button class="btn primary" disabled={!draft.trim()}>Send</button>
				{/if}
			</div>
			{#if model}
				<p class="hint">Each reply reserves up to {usd(maxHold)} and is charged by the token when it finishes.</p>
			{/if}
		</div>
	</form>
</section>

<style>
	.chat {
		display: grid;
		grid-template-rows: auto minmax(0, 1fr) auto;
		height: 100%;
	}
	.bar {
		display: flex;
		align-items: center;
		gap: 1rem;
		padding: 0.875rem 1.5rem;
		border-bottom: 1px solid var(--rule);
		flex-wrap: wrap;
	}
	.title {
		flex: 1;
		min-width: 10rem;
		margin: 0;
		font: inherit;
		font-size: 1.25rem;
		font-weight: 800;
		font-stretch: 85%;
		background: transparent;
		border: 0;
		color: var(--paper);
		padding: 0.25rem 0;
	}
	.title:not(.static):hover {
		box-shadow: 0 1px 0 var(--rule);
	}
	.controls {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	select {
		font: inherit;
		font-size: 0.875rem;
		font-weight: 600;
		background: #0d0d0d;
		color: var(--paper);
		border: 2px solid var(--rule);
		border-radius: 999px;
		padding: 0.4rem 0.75rem;
	}
	.toggle {
		font: inherit;
		font-size: 0.875rem;
		font-weight: 600;
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		background: none;
		border: 2px solid var(--rule);
		color: var(--muted);
		border-radius: 999px;
		padding: 0.4rem 0.75rem;
		cursor: pointer;
	}
	.toggle .dot {
		width: 0.625rem;
		height: 0.625rem;
		border-radius: 50%;
		border: 2px solid currentColor;
	}
	.toggle.on {
		color: var(--line-yellow);
		border-color: var(--line-yellow);
	}
	.toggle.on .dot {
		background: currentColor;
	}
	.scroll {
		overflow-y: auto;
		padding: 1.5rem;
	}
	.column {
		max-width: 46rem;
		margin: 0 auto;
		display: grid;
		gap: 1.25rem;
	}
	.blank {
		padding: 12vh 0 0;
	}
	.blank-head {
		margin: 0 0 0.75rem;
		font-size: clamp(2.5rem, 7vw, 4.5rem);
		font-weight: 900;
		font-stretch: 70%;
		letter-spacing: -0.03em;
		line-height: 0.95;
		color: var(--stock);
	}
	.muted {
		color: var(--muted);
		line-height: 1.5;
		max-width: 60ch;
	}
	.text {
		margin: 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		line-height: 1.55;
	}
	.q {
		justify-self: end;
		max-width: 85%;
		padding: 0.75rem 1rem;
		border-left: 6px solid var(--line-blue);
	}
	.att {
		list-style: none;
		margin: 0.5rem 0 0;
		padding: 0.5rem 0 0;
		border-top: 2px dashed rgb(0 0 0 / 0.3);
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.75rem;
		font-size: 0.8125rem;
		font-weight: 600;
	}
	.a .text {
		font-size: 1.0625rem;
	}
	.receipt {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.875rem;
		margin: 0.5rem 0 0;
		font-size: 0.75rem;
		color: var(--muted);
		font-variant-numeric: tabular-nums;
	}
	.fare {
		color: var(--line-yellow);
		font-weight: 700;
	}
	.cut {
		color: var(--line-red);
	}
	.caret {
		display: inline-block;
		width: 0.55em;
		height: 1.1em;
		vertical-align: text-bottom;
		margin-left: 1px;
		background: var(--stock);
		animation: blink 1s steps(1) infinite;
	}
	@keyframes blink {
		50% {
			opacity: 0;
		}
	}
	.alert {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		border: 2px solid var(--line-red);
		border-radius: 6px;
		padding: 0.75rem 1rem;
	}
	.alert p {
		margin: 0;
	}
	.alert .btn {
		text-decoration: none;
	}
	.composer {
		padding: 0.75rem 1.5rem 1rem;
		border-top: 1px solid var(--rule);
	}
	.composer .column {
		gap: 0.5rem;
	}
	.box {
		display: flex;
		align-items: end;
		gap: 0.5rem;
		border: 2px solid var(--rule);
		border-radius: 8px;
		padding: 0.375rem;
		background: #0a0a0a;
	}
	.box:focus-within {
		border-color: var(--stock-dim);
	}
	textarea {
		flex: 1;
		resize: none;
		border: 0;
		background: transparent;
		field-sizing: content;
		max-height: 40vh;
		min-height: 2.5rem;
		padding: 0.5rem;
	}
	textarea:focus-visible {
		outline: none;
	}
	.clip {
		font: inherit;
		font-size: 0.8125rem;
		font-weight: 700;
		background: none;
		border: 0;
		color: var(--muted);
		padding: 0.6rem 0.5rem;
		cursor: pointer;
	}
	.clip:hover:not(:disabled) {
		color: var(--paper);
	}
	.hint {
		margin: 0;
		font-size: 0.75rem;
		color: var(--muted);
	}
	.chips {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}
	.chips li {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		background: var(--stock);
		color: var(--ink);
		font-size: 0.8125rem;
		font-weight: 600;
		padding: 0.25rem 0.25rem 0.25rem 0.625rem;
		border-radius: 999px;
	}
	.chips button {
		font: inherit;
		background: none;
		border: 0;
		cursor: pointer;
		padding: 0 0.375rem;
		color: inherit;
	}
	/* Images get the yellow cap their drives have in Files. */
	.img::before {
		content: '';
		display: inline-block;
		width: 0.5rem;
		height: 0.5rem;
		margin-right: 0.375rem;
		border-radius: 1px;
		background: var(--line-yellow);
		vertical-align: 0.0625rem;
	}
	.picker {
		display: grid;
		gap: 2px;
		max-height: 14rem;
		overflow-y: auto;
		border: 2px solid var(--rule);
		border-radius: 8px;
		padding: 0.5rem;
	}
	.upload {
		justify-self: start;
		margin-bottom: 0.25rem;
	}
	.pick {
		font: inherit;
		display: flex;
		justify-content: space-between;
		gap: 1rem;
		text-align: left;
		background: none;
		border: 0;
		color: var(--paper);
		padding: 0.5rem;
		border-radius: 4px;
		cursor: pointer;
	}
	.pick:hover {
		background: #151515;
	}
	.picker .muted {
		margin: 0.25rem 0.5rem;
		font-size: 0.875rem;
	}
	.note {
		color: var(--muted);
	}
	@media (max-width: 820px) {
		.bar,
		.scroll,
		.composer {
			padding-left: 1rem;
			padding-right: 1rem;
		}
		.q {
			max-width: 100%;
		}
	}
</style>
