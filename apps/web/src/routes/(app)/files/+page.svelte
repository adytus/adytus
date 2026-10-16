<script lang="ts">
	import { api, ApiError, bytes, type FileItem } from '#lib/api.ts';
	import Alert from '#lib/components/Alert.svelte';
	import Lightbox from '#lib/components/Lightbox.svelte';

	type Filter = 'all' | 'images' | 'text' | 'other';

	let files = $state<FileItem[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let uploading = $state(0);
	let dragging = $state(false);
	let filter = $state<Filter>('all');
	let broken = $state<Record<string, boolean>>({});
	let viewing = $state<number | null>(null);

	$effect(() => {
		api.files().then((r) => {
			files = r.files;
			loaded = true;
		}, report);
	});

	function report(err: unknown) {
		error = err instanceof ApiError ? err.message : 'The server could not be reached. Try again.';
	}

	async function upload(list: FileList | null | undefined) {
		if (!list?.length) return;
		error = '';
		for (const f of Array.from(list)) {
			uploading++;
			try {
				const { file } = await api.upload(f);
				files = [file, ...files];
			} catch (err) {
				report(err);
			} finally {
				uploading--;
			}
		}
	}

	async function remove(f: FileItem) {
		if (!confirm(`Delete ${f.name}? This cannot be undone.`)) return;
		try {
			await api.deleteFile(f.id);
			files = files.filter((x) => x.id !== f.id);
		} catch (err) {
			report(err);
		}
	}

	const kind = (f: FileItem): Exclude<Filter, 'all'> => (f.image ? 'images' : f.text ? 'text' : 'other');
	const counts = $derived({
		all: files.length,
		images: files.filter((f) => kind(f) === 'images').length,
		text: files.filter((f) => kind(f) === 'text').length,
		other: files.filter((f) => kind(f) === 'other').length
	});
	const shown = $derived(filter === 'all' ? files : files.filter((f) => kind(f) === filter));
	const images = $derived(shown.filter((f) => f.image).map((f) => ({ id: f.id, name: f.name })));
	const tabs: { id: Filter; label: string }[] = [
		{ id: 'all', label: 'All' },
		{ id: 'images', label: 'Images' },
		{ id: 'text', label: 'Text' },
		{ id: 'other', label: 'Other' }
	];

	// What the file is good for in a chat.
	const use = (f: FileItem) =>
		f.image ? 'Attach with image models' : f.text ? 'Attach to any chat' : 'Stored only; download to use';
	const ext = (f: FileItem) => {
		const e = f.name.includes('.') ? f.name.split('.').pop()! : '';
		return (e || f.mime.split('/').pop() || 'file').slice(0, 4).toUpperCase();
	};
	const date = (s: string) => new Date(s).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
</script>

<svelte:window
	ondragover={(e) => {
		if (e.dataTransfer?.types.includes('Files')) {
			e.preventDefault();
			dragging = true;
		}
	}}
	ondragleave={(e) => {
		if (!e.relatedTarget) dragging = false;
	}}
	ondrop={(e) => {
		e.preventDefault();
		dragging = false;
		upload(e.dataTransfer?.files);
	}}
/>

<div class="page">
	<header class="head">
		<div>
			<h1>Files</h1>
			<p class="muted">
				Encrypted before they're stored. Attach text files and images to chat messages from the composer.
			</p>
		</div>
		<label class="btn primary upload">
			{uploading ? `Encrypting ${uploading}…` : 'Upload files'}
			<input type="file" multiple class="sr-only" onchange={(e) => upload(e.currentTarget.files)} />
		</label>
	</header>

	{#if error}
		<Alert title="Upload or change failed" ondismiss={() => (error = '')}><p>{error}</p></Alert>
	{/if}

	{#if loaded && files.length === 0}
		<label class="empty">
			<input type="file" multiple class="sr-only" onchange={(e) => upload(e.currentTarget.files)} />
			<span class="empty-head">No files yet</span>
			<span class="muted">Drop files anywhere on this page, or choose some. Up to 25 MB each.</span>
		</label>
	{:else if files.length}
		<div class="tabs" role="tablist" aria-label="Filter files">
			{#each tabs as t (t.id)}
				<button role="tab" aria-selected={filter === t.id} onclick={() => (filter = t.id)} disabled={t.id !== 'all' && counts[t.id] === 0}>
					{t.label}<span class="count">{counts[t.id]}</span>
				</button>
			{/each}
		</div>

		<ul class="grid">
			{#each shown as f (f.id)}
				<li class="card">
					{#if f.image}
						<button class="preview images" onclick={() => (viewing = images.findIndex((x) => x.id === f.id))} aria-label="View {f.name}">
							{#if f.thumb && !broken[f.id]}
								<img src={api.thumbURL(f.id)} alt="" loading="lazy" onerror={() => (broken[f.id] = true)} />
							{:else}
								<span class="ext">{ext(f)}</span>
							{/if}
						</button>
					{:else}
						<a class="preview {kind(f)}" href={api.fileURL(f.id)} download={f.name} aria-label="Download {f.name}">
							<span class="ext">{ext(f)}</span>
						</a>
					{/if}
					<div class="label">
						<p class="name" title={f.name}>{f.name}</p>
						<p class="meta">{bytes(f.size)}, {date(f.created_at)}</p>
						<p class="use {kind(f)}">{use(f)}</p>
					</div>
					<div class="actions">
						<a class="quiet" href={api.fileURL(f.id)} download={f.name}>Download</a>
						<button class="quiet danger" onclick={() => remove(f)}>Delete</button>
					</div>
				</li>
			{/each}
		</ul>
	{/if}
</div>

{#if viewing !== null && images.length}
	<Lightbox {images} bind:index={viewing} onclose={() => (viewing = null)} />
{/if}

{#if dragging}
	<div class="dropzone" aria-hidden="true"><p>Drop to encrypt and upload</p></div>
{/if}

<style>
	.page {
		max-width: 68rem;
		margin: 0 auto;
		padding: 2rem 1.5rem 4rem;
		display: grid;
		gap: 1.25rem;
	}
	.head {
		display: flex;
		justify-content: space-between;
		align-items: flex-end;
		gap: 1rem;
		flex-wrap: wrap;
	}
	h1 {
		margin: 0;
		font-size: clamp(2.5rem, 6vw, 4rem);
		font-weight: 900;
		font-stretch: 70%;
		letter-spacing: -0.03em;
		line-height: 1;
		color: var(--stock);
	}
	.muted {
		color: var(--muted);
		margin: 0.5rem 0 0;
		line-height: 1.5;
		max-width: 56ch;
	}
	.upload {
		cursor: pointer;
	}
	.empty {
		display: grid;
		gap: 0.25rem;
		justify-items: center;
		text-align: center;
		padding: 4rem 1rem;
		border: 2px dashed var(--rule);
		border-radius: 8px;
		cursor: pointer;
	}
	.empty:hover,
	.empty:focus-within {
		border-color: var(--stock-dim);
	}
	.empty-head {
		font-weight: 800;
		font-size: 1.25rem;
	}
	.tabs {
		display: flex;
		gap: 0.25rem;
		flex-wrap: wrap;
	}
	.tabs button {
		font: inherit;
		font-size: 0.875rem;
		font-weight: 700;
		display: inline-flex;
		gap: 0.375rem;
		align-items: center;
		background: none;
		border: 2px solid var(--rule);
		color: var(--muted);
		border-radius: 999px;
		padding: 0.3rem 0.75rem;
		cursor: pointer;
	}
	.tabs button[aria-selected='true'] {
		background: var(--stock);
		border-color: var(--stock);
		color: var(--ink);
	}
	.tabs button:disabled {
		opacity: 0.4;
		cursor: default;
	}
	.count {
		font-weight: 600;
		opacity: 0.7;
		font-variant-numeric: tabular-nums;
	}
	.grid {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(12.5rem, 1fr));
		gap: 1rem;
	}
	.card {
		display: grid;
		grid-template-rows: auto 1fr auto;
		background: #121212;
		border-radius: 8px;
		overflow: hidden;
		border: 1px solid var(--rule);
	}
	.preview {
		display: grid;
		place-items: center;
		aspect-ratio: 4 / 3;
		background: #0a0a0a;
		border-bottom: 4px solid var(--cap);
		--cap: var(--line-red);
		text-decoration: none;
		overflow: hidden;
		width: 100%;
		padding: 0;
		border-top: 0;
		border-left: 0;
		border-right: 0;
	}
	button.preview {
		cursor: zoom-in;
	}
	.preview.images {
		--cap: var(--line-yellow);
	}
	.preview.text {
		--cap: var(--line-green);
	}
	.preview img {
		width: 100%;
		height: 100%;
		object-fit: cover;
	}
	.ext {
		font-size: 2.5rem;
		font-weight: 900;
		font-stretch: 65%;
		letter-spacing: -0.02em;
		color: var(--cap);
	}
	.label {
		padding: 0.75rem 0.875rem 0.25rem;
		display: grid;
		gap: 0.125rem;
		align-content: start;
		min-width: 0;
	}
	.name {
		margin: 0;
		font-weight: 700;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.meta {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--muted);
		font-variant-numeric: tabular-nums;
	}
	.use {
		margin: 0.375rem 0 0;
		font-size: 0.75rem;
		font-weight: 600;
		color: #b5b2ab;
	}
	.actions {
		display: flex;
		gap: 0.75rem;
		padding: 0.25rem 0.875rem 0.75rem;
	}
	.quiet {
		font: inherit;
		font-size: 0.8125rem;
		font-weight: 600;
		background: none;
		border: 0;
		padding: 0.25rem 0;
		color: var(--paper);
		cursor: pointer;
		text-decoration: underline;
		text-underline-offset: 3px;
		text-decoration-color: var(--rule);
	}
	.quiet:hover {
		text-decoration-color: currentColor;
	}
	.quiet.danger {
		color: var(--muted);
		margin-left: auto;
	}
	.quiet.danger:hover {
		color: var(--line-red);
	}
	.dropzone {
		position: fixed;
		inset: 0;
		z-index: 50;
		display: grid;
		place-items: center;
		background: rgb(0 0 0 / 0.8);
		border: 4px dashed var(--line-yellow);
		pointer-events: none;
	}
	.dropzone p {
		font-size: 2rem;
		font-weight: 900;
		font-stretch: 75%;
		color: var(--line-yellow);
	}
	@media (max-width: 820px) {
		.page {
			padding: 1.5rem 1rem 3rem;
		}
		.grid {
			grid-template-columns: repeat(auto-fill, minmax(9.5rem, 1fr));
		}
	}
</style>
