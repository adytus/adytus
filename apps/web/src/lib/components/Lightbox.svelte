<script lang="ts">
	import { api } from '../api.ts';

	export type GalleryImage = { id: string; name: string };

	// Full-screen viewer for a set of images: arrows or swipe-free buttons to
	// move through them, click the image to zoom to its real size.
	let { images, index = $bindable(0), onclose }: { images: GalleryImage[]; index?: number; onclose: () => void } =
		$props();

	let zoomed = $state(false);
	let loading = $state(true);
	let dialog = $state<HTMLElement>();
	const current = $derived(images[index]);

	$effect(() => {
		void index;
		zoomed = false;
		loading = true;
	});

	$effect(() => {
		const prev = document.activeElement as HTMLElement | null;
		dialog?.focus();
		const overflow = document.body.style.overflow;
		document.body.style.overflow = 'hidden';
		return () => {
			document.body.style.overflow = overflow;
			prev?.focus();
		};
	});

	const go = (d: number) => (index = (index + d + images.length) % images.length);

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Escape') onclose();
		else if (e.key === 'ArrowRight' && images.length > 1) go(1);
		else if (e.key === 'ArrowLeft' && images.length > 1) go(-1);
		else return;
		e.preventDefault();
	}
</script>

<div
	class="lightbox"
	role="dialog"
	aria-modal="true"
	aria-label="Image viewer: {current?.name}"
	tabindex="-1"
	bind:this={dialog}
	onkeydown={onKey}
>
	<header class="top">
		<p class="name" title={current?.name}>{current?.name}</p>
		{#if images.length > 1}<p class="count">{index + 1} of {images.length}</p>{/if}
		<a class="act" href={api.fileURL(current.id)} download={current.name}>Download</a>
		<button class="act close" onclick={onclose} aria-label="Close viewer">×</button>
	</header>

	<!-- Clicking the backdrop closes; clicking the image zooms. -->
	<div class="stage" class:zoomed onclick={(e) => e.target === e.currentTarget && onclose()} role="presentation">
		{#if loading}<p class="wait">Decrypting…</p>{/if}
		{#key current.id}
			<button class="frame" onclick={() => (zoomed = !zoomed)} aria-label={zoomed ? 'Fit to screen' : 'Zoom to full size'}>
				<img src={api.viewURL(current.id)} alt={current.name} onload={() => (loading = false)} class:hidden={loading} />
			</button>
		{/key}
	</div>

	{#if images.length > 1}
		<button class="nav prev" onclick={() => go(-1)} aria-label="Previous image">‹</button>
		<button class="nav next" onclick={() => go(1)} aria-label="Next image">›</button>
		<ol class="strip" aria-label="All images">
			{#each images as im, i (im.id)}
				<li>
					<button class:on={i === index} onclick={() => (index = i)} aria-label="Show {im.name}" aria-current={i === index}>
						<img src={api.thumbURL(im.id)} alt="" loading="lazy" />
					</button>
				</li>
			{/each}
		</ol>
	{/if}
</div>

<style>
	.lightbox {
		position: fixed;
		inset: 0;
		z-index: 100;
		display: grid;
		grid-template-rows: auto minmax(0, 1fr) auto;
		background: rgb(0 0 0 / 0.94);
		outline: none;
	}
	.top {
		display: flex;
		align-items: center;
		gap: 1rem;
		padding: 0.75rem 1rem;
	}
	.name {
		margin: 0;
		flex: 1;
		font-weight: 700;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.count {
		margin: 0;
		color: var(--muted);
		font-size: 0.875rem;
		font-variant-numeric: tabular-nums;
	}
	.act {
		font: inherit;
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--paper);
		background: none;
		border: 0;
		cursor: pointer;
	}
	.close {
		font-size: 1.75rem;
		line-height: 1;
		padding: 0 0.25rem;
	}
	.stage {
		position: relative;
		display: grid;
		place-items: center;
		overflow: hidden;
		padding: 0 4rem;
	}
	.stage.zoomed {
		overflow: auto;
		place-items: start center;
		padding: 0;
	}
	.frame {
		background: none;
		border: 0;
		padding: 0;
		cursor: zoom-in;
		max-width: 100%;
		max-height: 100%;
		display: grid;
	}
	.zoomed .frame {
		cursor: zoom-out;
		max-width: none;
		max-height: none;
	}
	.frame img {
		max-width: 100%;
		max-height: calc(100dvh - 10rem);
		object-fit: contain;
		display: block;
	}
	.zoomed .frame img {
		max-width: none;
		max-height: none;
	}
	.hidden {
		visibility: hidden;
	}
	.wait {
		position: absolute;
		color: var(--muted);
	}
	.nav {
		position: fixed;
		top: 50%;
		translate: 0 -50%;
		width: 2.75rem;
		height: 2.75rem;
		border-radius: 50%;
		border: 0;
		background: rgb(255 255 255 / 0.12);
		color: var(--paper);
		font-size: 1.75rem;
		line-height: 1;
		cursor: pointer;
	}
	.nav:hover {
		background: rgb(255 255 255 / 0.22);
	}
	.prev {
		left: 0.75rem;
	}
	.next {
		right: 0.75rem;
	}
	.strip {
		list-style: none;
		margin: 0;
		padding: 0.75rem 1rem 1rem;
		display: flex;
		gap: 0.5rem;
		justify-content: center;
		overflow-x: auto;
	}
	.strip button {
		display: block;
		padding: 0;
		border: 2px solid transparent;
		border-radius: 4px;
		background: #111;
		cursor: pointer;
		opacity: 0.55;
		overflow: hidden;
	}
	.strip button.on {
		border-color: var(--line-yellow);
		opacity: 1;
	}
	.strip img {
		display: block;
		width: 3.5rem;
		height: 3.5rem;
		object-fit: cover;
	}
	@media (max-width: 640px) {
		.stage {
			padding: 0;
		}
		.nav {
			top: auto;
			bottom: 5.5rem;
			translate: none;
		}
	}
</style>
