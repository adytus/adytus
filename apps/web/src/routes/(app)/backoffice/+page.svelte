<script lang="ts">
	import { app } from '#lib/state.svelte.ts';
	import Alert from '#lib/components/Alert.svelte';

	// The backoffice isn't part of this app's bundle. The API serves its code
	// to admins only, so this page just fetches and mounts it; for anyone
	// else the request is refused and there's nothing to load.
	const src = '/api/backoffice/ui/backoffice.js';

	let target = $state<HTMLElement>();
	let failed = $state(false);

	$effect(() => {
		if (!target || !app.can('backoffice')) return;
		let unmount: (() => void) | undefined;
		let gone = false;
		import(/* @vite-ignore */ src).then(
			(mod: { mount: (el: HTMLElement) => () => void }) => {
				if (!gone) unmount = mod.mount(target!);
			},
			() => (failed = true)
		);
		return () => {
			gone = true;
			unmount?.();
		};
	});
</script>

<svelte:head><title>Backoffice · Adytus</title></svelte:head>

{#if !app.can('backoffice') || failed}
	<div class="center">
		<Alert title={failed ? 'The backoffice did not load' : 'Page not found'}>
			<p>
				{failed
					? 'The server did not send it. It may not be built yet, or your access was removed.'
					: 'There is nothing at this address.'}
			</p>
			{#snippet action()}<a class="btn" href="/chat">Back to chat</a>{/snippet}
		</Alert>
	</div>
{:else}
	<div bind:this={target}></div>
{/if}

<style>
	.center {
		padding: 3rem 1.5rem;
		max-width: 36rem;
		margin: 0 auto;
	}
	.btn {
		text-decoration: none;
	}
</style>
