<script lang="ts">
	// The tholos: a round temple seen from above. Twelve columns, a wall and
	// the adyton, the solid centre. Below 32px the small mark has eight bolder
	// columns so it stays crisp.
	let { size = 2, small = false }: { size?: number; small?: boolean } = $props();

	const n = $derived(small ? 8 : 12);
	const dots = $derived(
		Array.from({ length: n }, (_, i) => {
			const a = (i / n) * Math.PI * 2 - Math.PI / 2;
			return { x: 50 + 43 * Math.cos(a), y: 50 + 43 * Math.sin(a) };
		})
	);
</script>

<svg class="mark" viewBox="0 0 100 100" width="{size}rem" height="{size}rem" aria-hidden="true">
	{#each dots as d}
		<circle cx={d.x} cy={d.y} r={small ? 6.5 : 4.2} fill="currentColor" />
	{/each}
	<circle cx="50" cy="50" r="27" fill="none" stroke="currentColor" stroke-width={small ? 9 : 7} />
	<circle cx="50" cy="50" r="12" fill="currentColor" />
</svg>

<style>
	.mark {
		flex: none;
		display: block;
	}
</style>
