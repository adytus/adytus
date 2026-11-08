<script lang="ts">
	import lockup from '#lib/assets/lockup.png';

	// The supplied logo artwork, used as a mask so it takes the text colour.
	let { size = 1.75 }: { size?: number } = $props();
</script>

<span
	class="lockup"
	role="img"
	aria-label="adytus"
	style:--s="{size * 1.28}rem"
	style:--logo="url({lockup})"
></span>

<style>
	.lockup {
		display: inline-block;
		vertical-align: middle;
		height: var(--s);
		aspect-ratio: 934 / 270;
		background: currentColor;
		-webkit-mask: var(--logo) center / contain no-repeat;
		mask: var(--logo) center / contain no-repeat;
	}
</style>
