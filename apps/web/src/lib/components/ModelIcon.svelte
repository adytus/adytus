<script lang="ts">
	import type { ModelIcon } from '../api.ts';

	// A model's line marker: shape and color tell models apart at a glance,
	// the code names it, like a metro route sign.
	let { icon, size = 1.75, title }: { icon?: ModelIcon; size?: number; title?: string } = $props();

	const shape = $derived(icon?.shape ?? 'circle');
	const color = $derived(icon?.color ?? '#8d8a83');
	const code = $derived(icon?.code ?? '?');

	// Dark text on light fills (yellow, green), white otherwise.
	const ink = $derived.by(() => {
		const m = /^#?([0-9a-f]{6})$/i.exec(color);
		if (!m) return '#fff';
		const n = parseInt(m[1], 16);
		const lum = (0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255;
		return lum > 0.6 ? '#000' : '#fff';
	});
	const fontSize = $derived(code.length > 2 ? 12 : code.length === 2 ? 15 : 19);
</script>

<svg
	class="mi"
	viewBox="0 0 40 40"
	width="{size}rem"
	height="{size}rem"
	role={title ? 'img' : undefined}
	aria-label={title}
	aria-hidden={title ? undefined : 'true'}
>
	{#if shape === 'square'}
		<rect x="2" y="2" width="36" height="36" rx="7" fill={color} />
	{:else if shape === 'diamond'}
		<rect x="7" y="7" width="26" height="26" rx="4" fill={color} transform="rotate(45 20 20)" />
	{:else if shape === 'hexagon'}
		<polygon points="20,1.5 36.5,11 36.5,29 20,38.5 3.5,29 3.5,11" fill={color} />
	{:else}
		<circle cx="20" cy="20" r="19" fill={color} />
	{/if}
	<text x="20" y="20.5" fill={ink} font-size={fontSize} text-anchor="middle" dominant-baseline="central">{code}</text>
</svg>

<style>
	.mi {
		flex: none;
		display: block;
	}
	text {
		font-family: 'Archivo', 'Helvetica Neue', Arial, sans-serif;
		font-weight: 800;
		font-stretch: 80%;
		letter-spacing: -0.02em;
	}
</style>
