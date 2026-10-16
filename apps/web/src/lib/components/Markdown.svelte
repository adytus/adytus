<script lang="ts">
	import { Streamdown } from 'svelte-streamdown';

	// A model reply as Markdown. `live` marks a reply still streaming: code
	// blocks are capped to a peek that follows the newest lines, and a caret
	// trails the text.
	//
	// Model output is untrusted (a page or file it read can steer it), so raw
	// HTML is shown as text and images aren't loaded: a remote image URL could
	// carry chat contents out in its query string.
	let { text, live = false }: { text: string; live?: boolean } = $props();
</script>

<div class="md" class:live>
	<Streamdown
		content={text}
		baseTheme="shadcn"
		highlightTheme="github-dark"
		allowedImagePrefixes={[]}
		codeBlockMaxHeight={live ? '16rem' : undefined}
	/>
</div>

<style>
	.md {
		font-size: 1.0625rem;
		line-height: 1.6;
		overflow-wrap: anywhere;
	}
	.md :global {
		> * > :first-child {
			margin-top: 0;
		}
		> * > :last-child {
			margin-bottom: 0;
		}
		p {
			margin: 0 0 0.875rem;
		}
		/* No preflight: give the library's buttons a blank slate. */
		button {
			font: inherit;
			border: 0;
			background: none;
			color: inherit;
		}
		a {
			text-decoration-color: var(--line-yellow);
			text-underline-offset: 0.15em;
		}
		/* A block still being written breathes until its fence closes. */
		[data-incomplete] {
			border-color: var(--stock-dim);
		}
		[data-incomplete] > :first-child > :first-child {
			color: var(--paper);
			animation: md-breathe 1.6s ease-in-out infinite;
		}
	}

	/* The caret trails the last bit of text while the reply streams. */
	.md.live > :global(* > :is(p, h1, h2, h3, h4, h5, h6):last-child)::after,
	.md.live > :global(* > :is(ul, ol):last-child > li:last-child)::after {
		content: '';
		display: inline-block;
		width: 0.55em;
		height: 1.1em;
		vertical-align: text-bottom;
		margin-left: 2px;
		background: var(--stock);
		animation: md-blink 1s steps(1) infinite;
	}
	@keyframes -global-md-blink {
		50% {
			opacity: 0;
		}
	}
	@keyframes -global-md-breathe {
		50% {
			opacity: 0.45;
		}
	}
</style>
