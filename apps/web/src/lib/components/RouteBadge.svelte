<script lang="ts">
	// Colored line badge, like a metro route marker.
	type Line = 'blue' | 'yellow' | 'red' | 'purple' | 'green';
	let { line = 'blue', children, size = 1.75 }: { line?: Line; children: import('svelte').Snippet; size?: number } =
		$props();
</script>

<span class="badge {line}" style:--s="{size}rem">{@render children()}</span>

<style>
	.badge {
		display: inline-grid;
		place-items: center;
		flex: none;
		width: var(--s);
		height: var(--s);
		border-radius: 50%;
		font-weight: 800;
		font-stretch: 85%;
		font-size: calc(var(--s) * 0.5);
		line-height: 1;
		color: #fff;
	}
	.blue {
		background: var(--line-blue);
	}
	.yellow {
		background: var(--line-yellow);
		color: #000;
	}
	.red {
		background: var(--line-red);
	}
	.purple {
		background: var(--line-purple);
	}
	.green {
		background: var(--line-green);
		color: #000;
	}
</style>
