<script lang="ts">
	// A decorative barcode derived from a string, so each object gets a
	// stable stripe pattern. It encodes nothing.
	let { value, height = 28, label = true }: { value: string; height?: number; label?: boolean } =
		$props();

	const bars = $derived.by(() => {
		let h = 2166136261;
		const out: { x: number; w: number }[] = [];
		let x = 0;
		for (let i = 0; i < 48; i++) {
			h ^= value.charCodeAt(i % Math.max(value.length, 1)) + i;
			h = Math.imul(h, 16777619) >>> 0;
			const w = 1 + (h % 3);
			if (i % 2 === 0) out.push({ x, w });
			x += w + ((h >> 3) % 2);
		}
		return { out, width: x };
	});
</script>

<figure class="barcode" aria-hidden="true">
	<svg viewBox="0 0 {bars.width} 10" preserveAspectRatio="none" style:height="{height}px">
		{#each bars.out as b, i (i)}
			<rect x={b.x} y="0" width={b.w} height="10" />
		{/each}
	</svg>
	{#if label}<figcaption>{value}</figcaption>{/if}
</figure>

<style>
	.barcode {
		margin: 0;
		display: inline-flex;
		flex-direction: column;
		gap: 2px;
	}
	svg {
		width: 9rem;
		display: block;
		fill: currentColor;
	}
	figcaption {
		font-size: 0.625rem;
		letter-spacing: 0.18em;
		font-variant-numeric: tabular-nums;
	}
</style>
