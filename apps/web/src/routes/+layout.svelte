<script lang="ts">
	import '../app.css';
	import '../mode2.css';
	import { UI_MODE } from '#lib/theme.ts';
	import favicon from '#lib/assets/favicon.svg';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();

	// The look is chosen in lib/theme.ts; mode 2's rules key off this.
	document.documentElement.dataset.ui = String(UI_MODE);
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
</svelte:head>

{@render children()}

<style>
	:global {
		:root {
			--black: #000;
			--stock: #cfcbc2;
			--stock-dim: #a9a59c;
			--ink: #141414;
			--paper: #e9e6df;
			--muted: #8d8a83;
			--rule: #262626;
			--line-blue: #1e5bff;
			--line-yellow: #ffc400;
			--line-red: #e8322b;
			--line-purple: #7b4bd6;
			--line-green: #2fbf71;
			/* Surfaces and text that differ between looks (see mode2.css). */
			--headline: var(--stock);
			--surface: #0d0d0d;
			--surface-2: #161616;
			--deep: #0a0a0a;
			--text-soft: #b5b2ab;
			--font: 'Archivo', 'Helvetica Neue', Arial, sans-serif;
			color-scheme: dark;
		}
		*,
		*::before,
		*::after {
			box-sizing: border-box;
		}
		html,
		body {
			margin: 0;
			background: var(--black);
			color: var(--paper);
			font-family: var(--font);
			font-size: 16px;
			-webkit-font-smoothing: antialiased;
		}
		a {
			color: inherit;
		}
		:focus-visible {
			outline: 2px solid var(--line-yellow);
			outline-offset: 2px;
		}
		.ticket {
			background: var(--stock);
			color: var(--ink);
			border-radius: 6px;
			position: relative;
		}
		/* Tiny technical copy printed on ticket stock. */
		.micro {
			font-size: 0.6875rem;
			letter-spacing: 0.08em;
			text-transform: uppercase;
			font-weight: 600;
			opacity: 0.75;
		}
		.btn {
			font: inherit;
			font-weight: 700;
			font-size: 0.875rem;
			border: 2px solid currentColor;
			background: transparent;
			color: inherit;
			padding: 0.5rem 0.875rem;
			border-radius: 999px;
			cursor: pointer;
			white-space: nowrap;
		}
		.btn:hover:not(:disabled) {
			background: rgb(127 127 127 / 0.18);
		}
		.btn:disabled {
			opacity: 0.4;
			cursor: not-allowed;
		}
		.btn.primary {
			background: var(--line-blue);
			border-color: var(--line-blue);
			color: #fff;
		}
		.btn.primary:hover:not(:disabled) {
			background: #3a70ff;
		}
		.btn.danger {
			color: var(--line-red);
		}
		.field {
			display: grid;
			gap: 0.375rem;
			font-size: 0.875rem;
			font-weight: 600;
		}
		.field input,
		.field select,
		textarea {
			font: inherit;
			font-weight: 400;
			font-size: 1rem;
			padding: 0.625rem 0.75rem;
			border-radius: 4px;
			border: 2px solid var(--rule);
			background: #0d0d0d;
			color: var(--paper);
		}
		.ticket .field input {
			background: #f2efe8;
			color: var(--ink);
			border-color: rgb(0 0 0 / 0.2);
		}
		.error {
			color: var(--line-red);
			font-weight: 600;
			font-size: 0.875rem;
		}
		.ticket .error {
			color: #b3170f;
		}
		.sr-only {
			position: absolute;
			width: 1px;
			height: 1px;
			overflow: hidden;
			clip-path: inset(50%);
		}
		@media (prefers-reduced-motion: reduce) {
			* {
				animation: none !important;
				transition: none !important;
			}
		}
	}
</style>
