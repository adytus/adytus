<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import FareCard from '#lib/components/FareCard.svelte';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();
	let ready = $state(false);
	let failure = $state('');
	let railOpen = $state(false);

	$effect(() => {
		app.load().then(
			() => (ready = true),
			(err) => {
				if (err instanceof ApiError && err.status === 401) goto('/login');
				else failure = err instanceof ApiError ? err.message : 'The API is not reachable.';
			}
		);
	});

	// Close the drawer after navigating on small screens.
	$effect(() => {
		void page.url.pathname;
		railOpen = false;
	});

	async function logout() {
		await api.logout().catch(() => {});
		app.clear();
		goto('/login');
	}

	const activeChat = $derived(page.params.id);
</script>

{#if failure}
	<div class="center">
		<p class="error">{failure}</p>
		<button class="btn" onclick={() => location.reload()}>Retry</button>
	</div>
{:else if !ready}
	<div class="center"><p class="micro">Validating ticket…</p></div>
{:else}
	<div class="shell">
		<header class="topbar">
			<a class="wordmark" href="/chat">ADYTUS</a>
			<button class="btn" aria-expanded={railOpen} aria-controls="rail" onclick={() => (railOpen = !railOpen)}>
				{railOpen ? 'Close' : 'Menu'}
			</button>
		</header>

		<nav id="rail" class="rail" class:open={railOpen} aria-label="Main">
			<a class="wordmark desktop" href="/chat">ADYTUS</a>
			<a class="btn primary new" href="/chat">New chat</a>

			<div class="chats">
				{#each app.chats as c (c.id)}
					<a
						href="/chat/{c.id}"
						class="chat"
						class:active={activeChat === c.id}
						aria-current={activeChat === c.id ? 'page' : undefined}
					>
						<span class="title">{c.title || 'Untitled chat'}</span>
						{#if c.offline}<span class="pip" title="Offline processing enabled"></span>{/if}
					</a>
				{:else}
					<p class="empty">Your chats will appear here.</p>
				{/each}
			</div>

			<div class="links">
				<a href="/files" aria-current={page.url.pathname === '/files' ? 'page' : undefined}>Files</a>
				<a href="/account" aria-current={page.url.pathname === '/account' ? 'page' : undefined}>Account</a>
			</div>

			<FareCard balance={app.balance} userID={app.user?.id ?? ''} />

			<div class="me">
				<span title={app.user?.email}>{app.user?.email}</span>
				<button class="link" onclick={logout}>Sign out</button>
			</div>
		</nav>

		<main class="stage">
			{@render children()}
		</main>
	</div>
{/if}

<style>
	.center {
		min-height: 100dvh;
		display: grid;
		place-content: center;
		gap: 1rem;
		justify-items: center;
	}
	.shell {
		display: grid;
		grid-template-columns: 17.5rem minmax(0, 1fr);
		height: 100dvh;
	}
	.topbar {
		display: none;
	}
	.wordmark {
		font-weight: 900;
		font-stretch: 70%;
		font-size: 2rem;
		letter-spacing: -0.03em;
		text-decoration: none;
		color: var(--stock);
		line-height: 1;
	}
	.rail {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		padding: 1.25rem 1rem;
		border-right: 1px solid var(--rule);
		min-height: 0;
	}
	.new {
		text-align: center;
		text-decoration: none;
	}
	.chats {
		flex: 1;
		overflow-y: auto;
		display: grid;
		align-content: start;
		gap: 2px;
	}
	.chat {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.5rem;
		border-radius: 4px;
		text-decoration: none;
		font-size: 0.9375rem;
		color: var(--muted);
	}
	.chat:hover {
		color: var(--paper);
		background: #111;
	}
	.chat.active {
		color: var(--ink);
		background: var(--stock);
	}
	.title {
		flex: 1;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.pip {
		width: 0.5rem;
		height: 0.5rem;
		border-radius: 50%;
		background: var(--line-yellow);
		flex: none;
	}
	.empty {
		color: var(--muted);
		font-size: 0.875rem;
		padding: 0 0.5rem;
	}
	.links {
		display: flex;
		gap: 1.25rem;
		font-weight: 700;
	}
	.links a {
		text-decoration: none;
		color: var(--muted);
	}
	.links a[aria-current='page'],
	.links a:hover {
		color: var(--paper);
	}
	.me {
		display: flex;
		justify-content: space-between;
		gap: 0.5rem;
		font-size: 0.8125rem;
		color: var(--muted);
	}
	.me span {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.link {
		font: inherit;
		background: none;
		border: 0;
		padding: 0;
		color: var(--paper);
		text-decoration: underline;
		cursor: pointer;
		flex: none;
	}
	.stage {
		min-width: 0;
		min-height: 0;
		overflow-y: auto;
	}
	@media (max-width: 820px) {
		.shell {
			grid-template-columns: 1fr;
			grid-template-rows: auto minmax(0, 1fr);
		}
		.topbar {
			display: flex;
			justify-content: space-between;
			align-items: center;
			padding: 0.75rem 1rem;
			border-bottom: 1px solid var(--rule);
		}
		.desktop {
			display: none;
		}
		.rail {
			display: none;
			position: fixed;
			inset: 3.75rem 0 0;
			z-index: 10;
			background: var(--black);
			border-right: 0;
		}
		.rail.open {
			display: flex;
		}
	}
</style>
