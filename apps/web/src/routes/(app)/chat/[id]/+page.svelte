<script lang="ts">
	import { page } from '$app/state';
	import ChatView from '#lib/components/ChatView.svelte';
</script>

{#key page.params.id}
	<ChatView chatID={page.params.id ?? null} />
{/key}
