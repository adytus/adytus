<script lang="ts">
	import { goto } from '$app/navigation';
	import { tick } from 'svelte';
	import {
		api,
		ApiError,
		sendMessage,
		usd,
		bytes,
		type Attachment,
		type Chat,
		type FileItem,
		type Message,
		type ToolStep
	} from '../api.ts';
	import { app, handoff } from '../state.svelte.ts';
	import { isCodingQuery } from '../coding.ts';
	import ModelIcon from './ModelIcon.svelte';
	import ModelPicker from './ModelPicker.svelte';
	import ToolSteps from './ToolSteps.svelte';
	import Markdown from './Markdown.svelte';
	import Alert from './Alert.svelte';
	import Lightbox, { type GalleryImage } from './Lightbox.svelte';

	let { chatID }: { chatID: string | null } = $props();

	let chat = $state<Chat | null>(null);
	let messages = $state<Message[]>([]);
	let loading = $state(false);
	let loadError = $state('');

	let draft = $state('');
	let attached = $state<Attachment[]>([]);
	let pickerOpen = $state(false);
	let pickable = $state<FileItem[]>([]);
	// Files on their way up, shown as chips until they're attached.
	let pending = $state<{ key: number; name: string }[]>([]);
	const uploading = $derived(pending.length > 0);
	// Drag events fire per child element, so entries are counted.
	let dragDepth = $state(0);
	let gallery = $state<{ images: GalleryImage[]; index: number } | null>(null);
	let noThumb = $state<Record<string, boolean>>({});
	// Any file can be attached when a tool that opens files is on.
	let pythonOn = $state(false);

	let streaming = $state(false);
	let liveText = $state('');
	let liveThinking = $state('');
	let liveSteps = $state<ToolStep[]>([]);
	let holdMicros = $state(0);
	let sendError = $state<{ code: string; message: string } | null>(null);
	let controller: AbortController | null = null;

	let modelID = $state(app.defaultModel);
	let scroller = $state<HTMLElement>();
	let input = $state<HTMLTextAreaElement>();

	const model = $derived(app.model(modelID));
	const maxHold = $derived(model ? model.max_output_tokens * (model.completion_usd_per_mtok ?? 0) : 0);

	$effect(() => {
		const id = chatID;
		if (!id) return;
		loading = true;
		api.chat(id).then(
			(r) => {
				chat = r.chat;
				messages = r.messages;
				// Single model mode runs every chat on its one model; older
				// chats are moved there on their next message.
				modelID = app.singleModel || r.chat.model;
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

	// question returns the user message the reply at index i answers. The tool
	// line reads it to tell a coding question from a plain one.
	function question(i: number): string {
		for (let j = i - 1; j >= 0; j--) if (messages[j].role === 'user') return messages[j].content;
		return '';
	}

	async function scrollDown() {
		await tick();
		scroller?.scrollTo({ top: scroller.scrollHeight });
	}

	async function submit(e?: SubmitEvent) {
		e?.preventDefault();
		const content = draft.trim();
		if (!content || streaming || uploading) return;
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
		liveThinking = '';
		liveSteps = [];
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
						// Single model mode moves an older chat onto the one
						// model; follow it so the header agrees.
						if (d.model) {
							modelID = d.model;
							if (chat) chat.model = d.model;
						}
						if (chat) {
							// The chat moves to the top of the list as soon as it's sent to.
							if (d.title && !chat.title) chat.title = d.title;
							chat.updated_at = new Date().toISOString();
							app.upsertChat({ ...chat });
						}
						app.refreshBalance();
					},
					title: (d) => {
						if (!chat) return;
						chat.title = d.title;
						app.upsertChat({ ...chat });
					},
					delta: (d) => {
						liveText += d.text;
						scrollDown();
					},
					reasoning: (d) => {
						liveThinking += d.text;
					},
					tool: (d) => {
						const i = liveSteps.findIndex((x) => x.id === d.id);
						if (i >= 0) liveSteps[i] = d;
						else liveSteps.push(d);
						scrollDown();
					},
					done: (d) => {
						messages = [...messages.filter((m) => m.id !== tempID), d.user_message, d.message];
						app.balance = d.balance;
						liveText = '';
						liveSteps = [];
						liveThinking = '';
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
		const prev = modelID;
		modelID = id;
		if (!chat) return;
		try {
			chat = (await api.updateChat(chat.id, { model: id })).chat;
			app.upsertChat(chat);
		} catch (err) {
			modelID = prev;
			sendError = { code: 'error', message: err instanceof ApiError ? err.message : 'The model could not be changed.' };
		}
	}

	async function rename(title: string) {
		if (!chat || title === chat.title) return;
		chat = (await api.updateChat(chat.id, { title })).chat;
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

	$effect(() => {
		api.settings().then(
			(s) =>
				(pythonOn = s.tools.some(
					(t) => (t.id === 'python' || t.id === 'read_document') && t.enabled
				)),
			() => {}
		);
	});
	const python = $derived(pythonOn && !!model?.tools);
	const canAttach = (f: FileItem) => f.text || (f.image && !!model?.vision) || python;

	function attach(f: FileItem) {
		if (attached.length >= maxAttached || attached.some((a) => a.id === f.id)) return;
		attached = [...attached, { id: f.id, name: f.name, image: f.image }];
		pickerOpen = false;
		input?.focus();
	}

	const maxAttached = 5;
	let uploadKey = 0;

	// uploadFiles uploads files side by side and attaches each as it lands,
	// up to the message's limit.
	async function uploadFiles(files: File[]) {
		if (!files.length) return;
		const room = maxAttached - attached.length - pending.length;
		if (files.length > room) {
			sendError = {
				code: 'validation',
				message:
					room > 0
						? `Only ${room} more file${room === 1 ? '' : 's'} can go on this message (up to ${maxAttached}); the rest weren't uploaded.`
						: `A message can carry up to ${maxAttached} files.`
			};
		}
		pickerOpen = false;
		await Promise.all(
			files.slice(0, Math.max(0, room)).map(async (file) => {
				const key = ++uploadKey;
				pending = [...pending, { key, name: file.name }];
				try {
					const { file: up } = await api.upload(file);
					if (!canAttach(up)) {
						const why = up.image
							? `${model?.name ?? 'This model'} can't see images; switch to a vision model or turn on the document reader or Python tool to attach it.`
							: 'only text files and images can be attached to a message, or any file when the document reader or Python tool is on.';
						sendError = { code: 'validation', message: `${up.name} was saved to Files, but ${why}` };
					} else {
						attach(up);
					}
				} catch (err) {
					sendError = {
						code: 'error',
						message: `${file.name}: ${err instanceof ApiError ? err.message : 'the upload failed.'}`
					};
				} finally {
					pending = pending.filter((p) => p.key !== key);
				}
			})
		);
	}

	function uploadPicked(e: Event) {
		const el = e.currentTarget as HTMLInputElement;
		const files = [...(el.files ?? [])];
		el.value = '';
		uploadFiles(files);
	}

	const carriesFiles = (e: DragEvent) => !!e.dataTransfer?.types.includes('Files');
	function dragEnter(e: DragEvent) {
		if (!carriesFiles(e)) return;
		e.preventDefault();
		dragDepth++;
	}
	function dragOver(e: DragEvent) {
		if (!carriesFiles(e)) return;
		e.preventDefault();
		e.dataTransfer!.dropEffect = loadError ? 'none' : 'copy';
	}
	function dragLeave(e: DragEvent) {
		if (carriesFiles(e)) dragDepth = Math.max(0, dragDepth - 1);
	}
	function drop(e: DragEvent) {
		if (!carriesFiles(e)) return;
		e.preventDefault();
		dragDepth = 0;
		if (loadError) return;
		uploadFiles([...(e.dataTransfer?.files ?? [])]);
		input?.focus();
	}

	const perMTok = (n: number) => (n < 1 ? `$${n.toFixed(2)}` : `$${n.toFixed(n % 1 ? 2 : 0)}`);

	// What the error is about, so the alert can lead with it.
	const errorTitles: Record<string, string> = {
		budget_exceeded: 'Not enough credit for this reply',
		validation: "That message can't be sent",
		rate_limited: 'Too many messages at once',
		upstream: 'The model did not answer',
		internal: 'The reply could not be saved'
	};
	const lineColor = (id?: string) => app.model(id ?? '')?.icon?.color;

	// Every image in the conversation, sent or made by a tool, oldest first,
	// so the viewer can step through them in order.
	const chatImages = $derived(
		messages.flatMap((m) =>
			[...(m.attachments ?? []), ...(m.steps ?? []).flatMap((s) => s.files ?? [])]
				.filter((a) => a.image)
				.map((a) => ({ id: a.id, name: a.name }))
		)
	);
	function view(list: GalleryImage[], id: string) {
		gallery = { images: list, index: Math.max(0, list.findIndex((x) => x.id === id)) };
	}
</script>

{#snippet lock()}
	<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
		<rect x="4" y="10.5" width="16" height="10.5" rx="2" fill="currentColor" />
		<path d="M8 10.5V7.5a4 4 0 0 1 8 0v3" fill="none" stroke="currentColor" stroke-width="2.25" stroke-linecap="round" />
	</svg>
{/snippet}

<section
	class="chat"
	ondragenter={dragEnter}
	ondragover={dragOver}
	ondragleave={dragLeave}
	ondrop={drop}
	aria-label="Chat"
>
	{#if dragDepth > 0 && !loadError}
		<div class="dropzone" aria-hidden="true">
			<p>Drop files to attach them</p>
			<p class="muted">
				Up to {maxAttached} per message. Files are encrypted and stored safely in Adytus.
			</p>
		</div>
	{/if}
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
			{#if app.singleModel}
				<span class="fixed-model">
					<ModelIcon icon={app.model(app.singleModel)?.icon} size={1.375} />
					{app.model(app.singleModel)?.name ?? app.singleModel}
				</span>
			{:else}
				<ModelPicker
					models={app.models}
					value={modelID}
					disabled={streaming}
					showPrices={app.can('backoffice')}
					onchange={changeModel}
				/>
			{/if}
			{#if chat}
				<button class="quiet danger" onclick={remove}>Delete chat</button>
			{/if}
		</div>
	</header>

	<div class="scroll" bind:this={scroller}>
		<div class="column">
			{#if loading}
				<p class="note">Loading chat…</p>
			{:else if loadError}
				<Alert title={loadError}>
					{#snippet action()}<a class="btn" href="/chat">Start a new chat</a>{/snippet}
				</Alert>
			{:else if messages.length === 0 && !streaming}
				<div class="blank">
					<p class="blank-head">Ask anything.</p>
					{#if model}
						<div class="rider">
							<ModelIcon icon={model.icon} size={2.5} />
							<div>
								<p class="rider-name">{model.name}</p>
								{#if model.description}<p class="muted rider-desc">{model.description}</p>{/if}
								{#if app.can('backoffice') || model.tools || model.vision}
									<p class="muted">
										{#if app.can('backoffice')}
											{perMTok(model.prompt_usd_per_mtok ?? 0)} per million tokens in, {perMTok(model.completion_usd_per_mtok ?? 0)} out.
										{/if}
										{#if model.tools}It can use the tools switched on in <a href="/settings">Settings</a>.{/if}
										{#if model.vision}It can read images you attach.{/if}
									</p>
								{/if}
							</div>
						</div>
					{/if}
					<p class="muted small">Messages and files are encrypted before they're stored in Adytus.</p>
				</div>
			{/if}

			{#each messages as m, i (m.id)}
				{#if m.role === 'user'}
					<article class="ticket q">
						<p class="text">{m.content}</p>
						{#if m.attachments?.some((a) => a.image)}
							<ul class="shots">
								{#each m.attachments.filter((a) => a.image) as a (a.id)}
									<li>
										<button type="button" class="shot" onclick={() => view(chatImages, a.id)} aria-label="View {a.name}">
											{#if noThumb[a.id]}<span>{a.name}</span>{:else}
												<img src={api.thumbURL(a.id)} alt="" loading="lazy" onerror={() => (noThumb[a.id] = true)} />
											{/if}
										</button>
									</li>
								{/each}
							</ul>
						{/if}
						{#if m.attachments?.some((a) => !a.image)}
							<ul class="att">
								{#each m.attachments.filter((a) => !a.image) as a (a.id)}<li>{a.name}</li>{/each}
							</ul>
						{/if}
						{#if m.attachments?.length}
							<p class="sealed">
								{@render lock()}
								Encrypted and stored safely in Adytus. Only you can read {m.attachments.length === 1 ? 'it' : 'them'}.
							</p>
						{/if}
					</article>
				{:else}
					<article class="a">
						{#if m.steps?.length}
							<ToolSteps
								steps={m.steps}
								color={lineColor(m.model)}
								coding={isCodingQuery(question(i))}
								onview={(id) => view(chatImages, id)}
							/>
						{/if}
						<Markdown text={m.content} />
						<p class="receipt">
							<span class="who">
								<ModelIcon icon={app.model(m.model ?? '')?.icon} size={1} />
								{app.model(m.model ?? '')?.name ?? m.model}
							</span>
							{#if m.usage}
								<span>{m.usage.prompt_tokens.toLocaleString()} in</span>
								<span>{m.usage.completion_tokens.toLocaleString()} out</span>
								{#if app.can('backoffice') && m.usage.cost_micros != null}
									<span class="fare">{usd(m.usage.cost_micros)}</span>
								{/if}
							{/if}
							{#if m.partial}<span class="cut">Stopped early</span>{/if}
						</p>
					</article>
				{/if}
			{/each}

			{#if streaming}
				<article class="a live" aria-live="polite">
					{#if liveSteps.length}
						<ToolSteps steps={liveSteps} color={lineColor(modelID)} coding={isCodingQuery(question(messages.length))} />
					{/if}
					{#if liveThinking && !liveText}
						<details class="thinking">
							<summary>Thinking…</summary>
							<p>{liveThinking}</p>
						</details>
					{/if}
					{#if liveText}<Markdown text={liveText} live />{:else}<p class="text"><span class="caret"></span></p>{/if}
					<p class="receipt">
						{#if app.can('backoffice') && holdMicros}<span>Holding up to {usd(holdMicros)}</span>{:else}<span>Reserving fare…</span>{/if}
					</p>
				</article>
			{/if}

			{#if sendError}
				{@const err = sendError}
				<Alert title={errorTitles[err.code] ?? 'Something went wrong'} ondismiss={() => (sendError = null)}>
					<p>{err.message}</p>
					{#snippet action()}
						{#if err.code === 'budget_exceeded'}<button class="btn" type="button" onclick={() => app.openTopUp()}>Add tokens</button>{/if}
					{/snippet}
				</Alert>
			{/if}
		</div>
	</div>

	<form class="composer" onsubmit={submit}>
		<div class="column">
			{#if attached.length || pending.length}
				<ul class="chips">
					{#each attached as a (a.id)}
						{#if a.image && !noThumb[a.id]}
							<li class="tile">
								<button type="button" class="shot" aria-label="View {a.name}" onclick={() => view(attached.filter((x) => x.image), a.id)}>
									<img src={api.thumbURL(a.id)} alt="" onerror={() => (noThumb[a.id] = true)} />
								</button>
								<button type="button" class="drop" aria-label="Remove {a.name}" onclick={() => (attached = attached.filter((x) => x.id !== a.id))}>×</button>
							</li>
						{:else}
							<li class:img={a.image}>
								{a.name}
								<button type="button" aria-label="Remove {a.name}" onclick={() => (attached = attached.filter((x) => x.id !== a.id))}>×</button>
							</li>
						{/if}
					{/each}
					{#each pending as p (p.key)}
						<li class="up" aria-live="polite">Uploading {p.name}…</li>
					{/each}
				</ul>
				<p class="sealed">
					{@render lock()}
					Stored safely in Adytus: files are encrypted before they're stored, and only you can read them.
				</p>
			{/if}
			{#if pickerOpen}
				<div class="picker">
					<label class="btn upload">
						Upload files
						<input type="file" class="sr-only" multiple onchange={uploadPicked} />
					</label>
					<p class="sealed">
						{@render lock()}
						Files you attach are encrypted and stored safely in Adytus. Only you can read them.
					</p>
					{#each pickable as f (f.id)}
						<button type="button" class="pick" onclick={() => attach(f)}>
							{#if f.thumb}<img src={api.thumbURL(f.id)} alt="" loading="lazy" />{/if}
							<span class="pick-name" class:img={f.image && !f.thumb}>{f.name}</span><span class="muted">{bytes(f.size)}</span>
						</button>
					{:else}
						<p class="muted">{python ? 'No files yet.' : model?.vision ? 'No text files or images yet.' : 'No text files yet.'} Upload one, or drop it here, to attach it.</p>
					{/each}
				</div>
			{/if}
			<div class="box">
				<button
					type="button"
					class="clip"
					aria-expanded={pickerOpen}
					aria-label="Attach files"
					title={attached.length + pending.length >= maxAttached ? `Up to ${maxAttached} files per message` : 'Attach files, or drop them here'}
					onclick={openPicker}
					disabled={attached.length + pending.length >= maxAttached}
				>
					<svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true">
						<path
							d="M20.5 11.5 12.3 19.7a5 5 0 0 1-7.1-7.1l8.5-8.5a3.3 3.3 0 0 1 4.7 4.7l-8.5 8.5a1.7 1.7 0 0 1-2.4-2.4l7.8-7.8"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
							stroke-linecap="round"
							stroke-linejoin="round"
						/>
					</svg>
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
					<button class="btn primary" disabled={!draft.trim() || uploading} title={uploading ? 'Waiting for uploads to finish' : undefined}>Send</button>
				{/if}
			</div>
			{#if model && app.can('backoffice')}
				<p class="hint">Each reply reserves up to {usd(maxHold)} and is charged by the token when it finishes.</p>
			{/if}
		</div>
	</form>
</section>

{#if gallery}
	<Lightbox images={gallery.images} bind:index={gallery.index} onclose={() => (gallery = null)} />
{/if}

<style>
	.chat {
		position: relative;
		display: grid;
		grid-template-rows: auto minmax(0, 1fr) auto;
		height: 100%;
	}
	.dropzone {
		position: absolute;
		inset: 0.75rem;
		z-index: 5;
		display: grid;
		place-content: center;
		gap: 0.25rem;
		text-align: center;
		border: 2px dashed var(--stock);
		border-radius: 12px;
		background: color-mix(in srgb, var(--black) 85%, transparent);
		pointer-events: none;
	}
	.dropzone p {
		margin: 0;
		font-weight: 800;
		font-size: 1.25rem;
		color: var(--headline);
	}
	.dropzone .muted {
		font-weight: 400;
		font-size: 0.875rem;
		color: var(--muted);
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
	/* Single model mode shows the model's name without a picker. */
	.fixed-model {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		background: var(--surface);
		border: 2px solid var(--rule);
		border-radius: 999px;
		padding: 0.25rem 0.75rem 0.25rem 0.3rem;
		font-size: 0.875rem;
		font-weight: 700;
	}
	.quiet {
		font: inherit;
		font-size: 0.8125rem;
		font-weight: 600;
		background: none;
		border: 0;
		color: var(--muted);
		padding: 0.375rem 0.25rem;
		cursor: pointer;
	}
	.quiet.danger:hover {
		color: var(--line-red);
	}
	.rider {
		display: flex;
		gap: 0.875rem;
		align-items: flex-start;
		margin-top: 1.25rem;
	}
	.rider-name {
		margin: 0.125rem 0 0.25rem;
		font-weight: 800;
		font-size: 1.125rem;
	}
	.rider .muted {
		margin: 0;
	}
	.rider .rider-desc {
		margin-bottom: 0.25rem;
	}
	.rider a {
		color: var(--paper);
	}
	.small {
		font-size: 0.8125rem;
		margin-top: 1.5rem;
	}
	.who {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		color: var(--text-soft);
		font-weight: 600;
	}
	.thinking {
		margin-bottom: 0.5rem;
		font-size: 0.875rem;
		color: var(--muted);
	}
	.thinking summary {
		cursor: pointer;
		font-weight: 600;
		animation: breathe 1.6s ease-in-out infinite;
	}
	.thinking[open] summary {
		animation: none;
	}
	.thinking p {
		margin: 0.5rem 0 0;
		padding-left: 0.75rem;
		border-left: 2px solid var(--rule);
		white-space: pre-wrap;
		line-height: 1.5;
		max-height: 12rem;
		overflow-y: auto;
	}
	@keyframes breathe {
		50% {
			opacity: 0.45;
		}
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
		color: var(--headline);
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
		align-items: center;
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
		background: var(--headline);
		animation: blink 1s steps(1) infinite;
	}
	@keyframes blink {
		50% {
			opacity: 0;
		}
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
		background: var(--deep);
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
		display: grid;
		place-items: center;
		width: 2.5rem;
		height: 2.5rem;
		background: none;
		border: 0;
		border-radius: 6px;
		color: var(--muted);
		padding: 0;
		cursor: pointer;
		flex: none;
	}
	.clip[aria-expanded='true'] {
		color: var(--headline);
		background: var(--surface-2);
	}
	.clip:disabled {
		opacity: 0.35;
		cursor: default;
	}
	.clip:hover:not(:disabled) {
		color: var(--paper);
	}
	.sealed {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		margin: 0;
		font-size: 0.75rem;
		color: var(--muted);
	}
	.sealed svg {
		flex: none;
	}
	/* On a sent message the note sits on the bubble, so it takes the
	   bubble's own ink instead of the page's muted grey. */
	.q .sealed {
		margin-top: 0.5rem;
		color: color-mix(in srgb, currentColor 72%, transparent);
	}
	.picker .sealed {
		align-items: flex-start;
		margin: 0 0 0.25rem;
		padding: 0 0.5rem;
		line-height: 1.4;
	}
	.chips li.up {
		background: var(--surface-2);
		color: var(--muted);
		padding-right: 0.625rem;
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
	/* An attached image shows as a small print with a remove tab. */
	.chips li.tile {
		position: relative;
		padding: 0;
		background: none;
		border-radius: 6px;
	}
	.chips .shot {
		padding: 0;
	}
	.chips .shot img {
		width: 4rem;
		height: 4rem;
	}
	.chips .drop {
		position: absolute;
		top: -0.4rem;
		right: -0.4rem;
		width: 1.375rem;
		height: 1.375rem;
		padding: 0;
		border-radius: 50%;
		background: var(--stock);
		color: var(--ink);
		font-weight: 800;
		line-height: 1;
		box-shadow: 0 0 0 2px var(--black);
	}
	.shots {
		list-style: none;
		margin: 0.5rem 0 0;
		padding: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}
	.shot {
		display: block;
		padding: 0;
		border: 0;
		border-radius: 6px;
		overflow: hidden;
		background: var(--deep);
		cursor: zoom-in;
		border-bottom: 3px solid var(--line-yellow);
	}
	.shot img {
		display: block;
		width: 7.5rem;
		height: 7.5rem;
		object-fit: cover;
	}
	.shot span {
		display: block;
		padding: 0.5rem 0.75rem;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--paper);
	}
	.shot:hover img {
		filter: brightness(1.1);
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
		align-items: center;
		gap: 0.75rem;
		text-align: left;
		background: none;
		border: 0;
		color: var(--paper);
		padding: 0.5rem;
		border-radius: 4px;
		cursor: pointer;
	}
	.pick img {
		width: 2rem;
		height: 2rem;
		object-fit: cover;
		border-radius: 3px;
		flex: none;
	}
	.pick-name {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.pick:hover {
		background: var(--surface-2);
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
