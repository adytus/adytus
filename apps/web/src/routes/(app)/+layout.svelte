<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import Wordmark from '#lib/components/Wordmark.svelte';
	import FareCard from '#lib/components/FareCard.svelte';
	import ModelIcon from '#lib/components/ModelIcon.svelte';
	import Alert from '#lib/components/Alert.svelte';
	import TopUpDialog from '#lib/components/TopUpDialog.svelte';
	import type { LayoutProps } from './$types';
	import { differenceInCalendarDays, format, isThisYear, isToday, isYesterday } from 'date-fns';
	import type { Chat } from '#lib/api.ts';

	let { children }: LayoutProps = $props();
	let ready = $state(false);
	let failure = $state('');
	let railOpen = $state(false);

	$effect(() => {
		app.load().then(
			() => (ready = true),
			(err) => {
				if (err instanceof ApiError && err.status === 401) goto('/login');
				else failure = err instanceof ApiError ? err.message : 'The API did not answer.';
			}
		);
	});

	// Nothing works without tokens: offer to add some once, when an unpaid
	// account first arrives.
	let offered = false;
	$effect(() => {
		if (ready && !offered && app.balance && app.balance.purchased_micros === 0) {
			offered = true;
			app.openTopUp();
		}
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

	// Re-read the clock each minute so chats roll into Yesterday at midnight.
	let now = $state(new Date());
	$effect(() => {
		const t = setInterval(() => (now = new Date()), 60_000);
		return () => clearInterval(t);
	});

	function dayGroup(at: Date): string {
		if (isToday(at)) return 'Today';
		if (isYesterday(at)) return 'Yesterday';
		const days = differenceInCalendarDays(now, at);
		if (days < 7) return 'Previous 7 days';
		if (days < 30) return 'Previous 30 days';
		return format(at, isThisYear(at) ? 'MMMM' : 'MMMM yyyy');
	}

	// Chats are already newest first, so groups come out in order.
	const groups = $derived.by(() => {
		void now;
		const out: { label: string; chats: Chat[] }[] = [];
		for (const c of app.chats) {
			const label = dayGroup(new Date(c.updated_at));
			if (out.at(-1)?.label !== label) out.push({ label, chats: [] });
			out.at(-1)!.chats.push(c);
		}
		return out;
	});

	const activeChat = $derived(page.params.id);
	const here = (path: string) => (page.url.pathname.startsWith(path) ? 'page' : undefined);

	// Each section keeps its own line color, as on a metro map.
	const sections = $derived([
		{ href: '/files', label: 'Files', line: 'var(--line-yellow)' },
		{ href: '/settings', label: 'Settings', line: 'var(--line-green)' },
		{ href: '/account', label: 'Account and credit', line: 'var(--line-red)' },
		...(app.can('backoffice') ? [{ href: '/backoffice', label: 'Backoffice', line: 'var(--line-purple)' }] : [])
	]);
</script>

{#if failure}
	<div class="center">
		<Alert title="Adytus can't reach its server">
			<p>{failure} Check that the API is running, then try again.</p>
			{#snippet action()}<button class="btn" onclick={() => location.reload()}>Try again</button>{/snippet}
		</Alert>
	</div>
{:else if !ready}
	<div class="center"><p class="loading">Loading your account…</p></div>
{:else}
	<div class="shell">
		<header class="topbar">
			<a class="wordmark" href="/chat"><Wordmark size={1.5} /></a>
			<button class="btn" aria-expanded={railOpen} aria-controls="rail" onclick={() => (railOpen = !railOpen)}>
				{railOpen ? 'Close' : 'Menu'}
			</button>
		</header>

		<nav id="rail" class="rail" class:open={railOpen} aria-label="Main">
			<a class="wordmark desktop" href="/chat"><Wordmark size={2} /></a>
			<a class="btn primary new" href="/chat">New chat</a>

			<h2 class="sr-only">Chats</h2>
			<div class="chats">
				{#each groups as g (g.label)}
					<h3 class="group">{g.label}</h3>
					{#each g.chats as c (c.id)}
						<a
							href="/chat/{c.id}"
							class="chat"
							class:active={activeChat === c.id}
							aria-current={activeChat === c.id ? 'page' : undefined}
						>
							<ModelIcon icon={app.model(c.model)?.icon} size={1.125} />
							<span class="title">{c.title || 'Untitled chat'}</span>
						</a>
					{/each}
				{:else}
					<p class="empty">Chats you start appear here.</p>
				{/each}
			</div>

			<ul class="sections">
				{#each sections as s (s.href)}
					<li>
						<a href={s.href} aria-current={here(s.href)} style:--line={s.line}>
							<span class="stop" aria-hidden="true"></span>{s.label}
						</a>
					</li>
				{/each}
			</ul>

			<FareCard balance={app.balance} />

			<div class="me">
				<span title={app.user?.email}>{app.user?.email}</span>
				<button class="link" onclick={logout}>Sign out</button>
			</div>
		</nav>

		<main class="stage">
			{@render children()}
		</main>
	</div>
	<TopUpDialog />
{/if}

<style>
	.center {
		min-height: 100dvh;
		display: grid;
		place-content: center;
		padding: 1rem;
	}
	.center > :global(*) {
		max-width: 32rem;
	}
	.loading {
		color: var(--muted);
	}
	.shell {
		display: grid;
		grid-template-columns: 16.5rem minmax(0, 1fr);
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
		color: var(--headline);
		line-height: 1;
		padding: 0 0.25rem;
	}
	.rail {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
		padding: 1.25rem 0.875rem 1rem;
		border-right: 1px solid var(--rule);
		min-height: 0;
	}
	.new {
		text-align: center;
		text-decoration: none;
	}
	.group {
		margin: 0.875rem 0 0.25rem;
		padding: 0 0.5rem;
		font-size: 0.75rem;
		font-weight: 700;
		color: var(--muted);
	}
	.group:first-child {
		margin-top: 0.25rem;
	}
	.chats {
		flex: 1;
		overflow-y: auto;
		overflow-x: hidden;
		display: grid;
		/* minmax(0, …) so a long title truncates instead of widening the rail. */
		grid-template-columns: minmax(0, 1fr);
		align-content: start;
		gap: 2px;
	}
	.chat {
		min-width: 0;
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.4375rem 0.5rem;
		border-radius: 4px;
		text-decoration: none;
		font-size: 0.9375rem;
		color: var(--muted);
	}
	.chat:hover {
		color: var(--paper);
		background: var(--surface-2);
	}
	.chat.active {
		color: var(--ink);
		background: var(--stock);
	}
	.title {
		flex: 1;
		min-width: 0;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.empty {
		color: var(--muted);
		font-size: 0.875rem;
		margin: 0.25rem 0;
		padding: 0 0.5rem;
	}
	.sections {
		list-style: none;
		margin: 0;
		padding: 0.625rem 0 0;
		border-top: 1px solid var(--rule);
		display: grid;
		gap: 2px;
	}
	.sections a {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		padding: 0.4375rem 0.5rem;
		border-radius: 4px;
		text-decoration: none;
		font-weight: 600;
		font-size: 0.9375rem;
		color: var(--muted);
	}
	.sections a:hover {
		color: var(--paper);
		background: var(--surface-2);
	}
	.sections a[aria-current='page'] {
		color: var(--paper);
		background: var(--surface-2);
	}
	.stop {
		width: 0.75rem;
		height: 0.75rem;
		border-radius: 50%;
		border: 3px solid var(--line);
		flex: none;
	}
	[aria-current='page'] .stop {
		background: var(--line);
	}
	.me {
		display: flex;
		justify-content: space-between;
		gap: 0.5rem;
		font-size: 0.8125rem;
		color: var(--muted);
		padding: 0 0.25rem;
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
