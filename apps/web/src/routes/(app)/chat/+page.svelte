<script lang="ts">
	import ChatView from '#lib/components/ChatView.svelte';
	import { app } from '#lib/state.svelte.ts';
</script>

{#key app.newChat}
	<ChatView chatID={null} />
{/key}
