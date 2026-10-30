<script lang="ts">
	import { UI_MODE } from '#lib/theme.ts';
	import Mark from './Mark.svelte';

	// Mode 1 prints the name in capitals; mode 2 pairs the mark with the
	// lowercase wordmark.
	let { size = 1.75 }: { size?: number } = $props();
</script>

{#if UI_MODE === 2}
	<span class="lockup" style:--s="{size}rem">
		<Mark size={size * 1.28} small={size < 1.5} />
		<span class="word">adytus</span>
	</span>
{:else}
	ADYTUS
{/if}

<style>
	.lockup {
		display: inline-flex;
		align-items: center;
		gap: 0.36em;
		font-size: var(--s);
	}
	.word {
		font-weight: 580;
		letter-spacing: -0.045em;
		line-height: 1;
		text-transform: lowercase;
	}
</style>
