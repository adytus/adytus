<script lang="ts">
	import { api, ApiError, bytes, shortID, type FileItem } from '#lib/api.ts';
	import Barcode from '#lib/components/Barcode.svelte';

	let files = $state<FileItem[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let uploading = $state(0);
	let dragging = $state(false);

	$effect(() => {
		api.files().then((r) => {
			files = r.files;
			loaded = true;
		}, report);
	});

	function report(err: unknown) {
		error = err instanceof ApiError ? err.message : 'Something went wrong. Try again.';
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

	async function toggleOffline(f: FileItem) {
		try {
			const { file } = await api.setFileOffline(f.id, !f.offline);
			files = files.map((x) => (x.id === f.id ? file : x));
		} catch (err) {
			report(err);
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

	// Drive cap color by kind: text files can ride along in chats.
	const cap = (f: FileItem) => (f.text ? 'green' : f.mime.startsWith('image/') ? 'yellow' : 'red');
	const sizeParts = (n: number) => {
		const [num, unit] = bytes(n).split(' ');
		return { num, unit };
	};
</script>

<div class="page">
	<header>
		<h1>Files</h1>
		<p class="muted">
			Files are encrypted before they reach storage. Text files can be attached to a chat message.
		</p>
	</header>

	<label
		class="drop"
		class:dragging
		ondragover={(e) => {
			e.preventDefault();
			dragging = true;
		}}
		ondragleave={() => (dragging = false)}
		ondrop={(e) => {
			e.preventDefault();
			dragging = false;
			upload(e.dataTransfer?.files);
		}}
	>
		<input type="file" multiple class="sr-only" onchange={(e) => upload(e.currentTarget.files)} />
		<span class="drop-main">{uploading ? `Encrypting ${uploading} file${uploading > 1 ? 's' : ''}…` : 'Drop files here or choose files'}</span>
		<span class="muted">Up to 25 MB each</span>
	</label>

	{#if error}<p class="error" role="alert">{error}</p>{/if}

	{#if loaded && files.length === 0}
		<p class="muted">No files yet.</p>
	{/if}

	<ul class="drives">
		{#each files as f (f.id)}
			{@const s = sizeParts(f.size)}
			<li class="drive">
				<div class="cap {cap(f)}">
					<span class="led" class:lit={f.offline} title={f.offline ? 'Offline access on' : 'Offline access off'}></span>
				</div>
				<div class="label">
					<p class="size"><span>{s.num}</span><small>{s.unit}</small></p>
					<p class="name" title={f.name}>{f.name}</p>
					<p class="micro meta">
						{f.text ? 'Text · attachable' : f.image ? 'Image · vision models' : f.mime || 'Binary'} · {new Date(f.created_at).toLocaleDateString()}
					</p>
					<Barcode value={shortID(f.id)} height={16} />
				</div>
				<div class="actions">
					<a class="btn" href={api.fileURL(f.id)} download={f.name}>Download</a>
					<button class="btn" class:on={f.offline} aria-pressed={f.offline} onclick={() => toggleOffline(f)}>
						Offline access {f.offline ? 'on' : 'off'}
					</button>
					<button class="btn danger" onclick={() => remove(f)}>Delete</button>
				</div>
			</li>
		{/each}
	</ul>
</div>

<style>
	.page {
		max-width: 64rem;
		margin: 0 auto;
		padding: 2rem 1.5rem 4rem;
		display: grid;
		gap: 1.5rem;
	}
	h1 {
		margin: 0;
		font-size: clamp(2.5rem, 6vw, 4rem);
		font-weight: 900;
		font-stretch: 70%;
		letter-spacing: -0.03em;
		color: var(--stock);
	}
	.muted {
		color: var(--muted);
		margin: 0.25rem 0 0;
		line-height: 1.5;
	}
	.drop {
		display: grid;
		gap: 0.25rem;
		justify-items: center;
		padding: 2rem 1rem;
		border: 2px dashed var(--rule);
		border-radius: 8px;
		cursor: pointer;
		text-align: center;
	}
	.drop:hover,
	.drop.dragging,
	.drop:focus-within {
		border-color: var(--stock-dim);
	}
	.drop-main {
		font-weight: 700;
	}
	.drives {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(15rem, 1fr));
		gap: 1.25rem;
	}
	.drive {
		display: grid;
		grid-template-rows: auto 1fr auto;
		border-radius: 14px;
		background: #1a1a1a;
		overflow: hidden;
		box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.08);
	}
	.cap {
		height: 2.25rem;
		display: flex;
		justify-content: flex-end;
		align-items: center;
		padding: 0 0.875rem;
		background: linear-gradient(180deg, rgb(255 255 255 / 0.28), transparent 60%), var(--c);
	}
	.cap.green {
		--c: var(--line-green);
	}
	.cap.yellow {
		--c: var(--line-yellow);
	}
	.cap.red {
		--c: var(--line-red);
	}
	.led {
		width: 0.5rem;
		height: 0.5rem;
		border-radius: 50%;
		background: rgb(0 0 0 / 0.45);
	}
	.led.lit {
		background: #fff;
		box-shadow: 0 0 6px 2px rgb(255 255 255 / 0.7);
	}
	.label {
		margin: 0.75rem;
		padding: 0.75rem;
		background: #050505;
		border-radius: 6px;
		display: grid;
		gap: 0.375rem;
		color: var(--stock);
		min-width: 0;
	}
	.size {
		margin: 0;
		font-weight: 900;
		font-stretch: 65%;
		line-height: 0.9;
		font-variant-numeric: tabular-nums;
	}
	.size span {
		font-size: 3rem;
		letter-spacing: -0.02em;
	}
	.size small {
		font-size: 1.25rem;
		margin-left: 0.125rem;
	}
	.name {
		margin: 0;
		font-weight: 700;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--paper);
	}
	.meta {
		margin: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
		padding: 0 0.75rem 0.75rem;
	}
	.actions .btn {
		font-size: 0.75rem;
		padding: 0.3rem 0.625rem;
		text-decoration: none;
	}
	.actions .btn.on {
		color: var(--line-yellow);
	}
	@media (max-width: 820px) {
		.page {
			padding: 1.5rem 1rem 3rem;
		}
	}
</style>
