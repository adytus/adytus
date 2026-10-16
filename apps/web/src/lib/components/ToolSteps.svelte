<script lang="ts">
	import { api, bytes, type ToolStep } from '../api.ts';

	// The tool calls behind a reply, drawn as stops along a line. Files a
	// tool saved show under its stop; onview opens an image in the viewer.
	let {
		steps,
		color = 'var(--line-purple)',
		onview
	}: { steps: ToolStep[]; color?: string; onview?: (id: string) => void } = $props();

	let noThumb = $state<Record<string, boolean>>({});

	const labels: Record<string, string> = {
		calculator: 'Calculated',
		python: 'Ran Python',
		web_search: 'Searched the web'
	};
	const pending: Record<string, string> = {
		calculator: 'Calculating',
		python: 'Running Python',
		web_search: 'Searching the web'
	};

	function args(s: ToolStep): string {
		try {
			const a = JSON.parse(s.input);
			return a.expression ?? a.query ?? a.code ?? s.input;
		} catch {
			return s.input;
		}
	}

	// Search results come back as JSON; show them as a list of links.
	function results(s: ToolStep): { title: string; url: string }[] | null {
		if (s.tool !== 'web_search' || !s.output?.startsWith('[')) return null;
		try {
			return JSON.parse(s.output);
		} catch {
			return null;
		}
	}
</script>

<ol class="line" style:--c={color}>
	{#each steps as s (s.id)}
		{@const arg = args(s)}
		{@const found = results(s)}
		<li class="stop" class:live={!s.done}>
			<span class="dot" aria-hidden="true"></span>
			<details>
				<summary>
					<span class="what">{s.done ? (labels[s.tool] ?? s.tool) : `${pending[s.tool] ?? s.tool}…`}</span>
					{#if s.tool !== 'python'}<span class="arg">{arg}</span>{/if}
					{#if s.done && s.ms}<span class="ms">{s.ms < 1000 ? `${s.ms} ms` : `${(s.ms / 1000).toFixed(1)} s`}</span>{/if}
				</summary>
				<div class="io">
					{#if s.tool === 'python'}
						<pre class="code">{arg}</pre>
					{/if}
					{#if s.done}
						{#if found}
							<ul class="links">
								{#each found as r (r.url)}
									<li><a href={r.url} target="_blank" rel="noopener noreferrer">{r.title || r.url}</a></li>
								{/each}
							</ul>
						{:else}
							<pre class="out">{s.output || '(no output)'}</pre>
						{/if}
					{/if}
				</div>
			</details>
			{#if s.tool === 'python' && !s.done}<pre class="code peek">{arg}</pre>{/if}
			{#if s.files?.length}
				<ul class="made">
					{#each s.files as f (f.id)}
						<li>
							{#if f.image && !noThumb[f.id]}
								<button type="button" class="thumb" onclick={() => onview?.(f.id)} aria-label="View {f.name}">
									<img src={api.thumbURL(f.id)} alt="" loading="lazy" onerror={() => (noThumb[f.id] = true)} />
								</button>
							{/if}
							<a class="file" href={api.fileURL(f.id)} download={f.name}>
								<span class="name">{f.name}</span><span class="size">{bytes(f.size)}</span>
							</a>
						</li>
					{/each}
				</ul>
			{/if}
		</li>
	{/each}
</ol>

<style>
	.line {
		list-style: none;
		margin: 0 0 0.75rem;
		padding: 0;
		display: grid;
	}
	.stop {
		position: relative;
		padding: 0 0 0.5rem 1.5rem;
		min-width: 0;
	}
	/* The track between stops. */
	.stop::before {
		content: '';
		position: absolute;
		left: 0.3125rem;
		top: 0.5rem;
		bottom: -0.5rem;
		width: 4px;
		background: var(--c);
	}
	.stop:last-child::before {
		display: none;
	}
	.dot {
		position: absolute;
		left: 0;
		top: 0.25rem;
		width: 0.875rem;
		height: 0.875rem;
		border-radius: 50%;
		background: #000;
		border: 3px solid var(--c);
		z-index: 1;
	}
	.live .dot {
		animation: pulse 1.2s ease-in-out infinite;
	}
	@keyframes pulse {
		50% {
			background: var(--c);
		}
	}
	summary {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		cursor: pointer;
		font-size: 0.875rem;
		list-style: none;
		min-width: 0;
	}
	summary::-webkit-details-marker {
		display: none;
	}
	summary:hover .what {
		text-decoration: underline;
	}
	.what {
		font-weight: 700;
		flex: none;
	}
	.arg {
		color: var(--muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.ms {
		margin-left: auto;
		flex: none;
		font-size: 0.75rem;
		color: var(--muted);
		font-variant-numeric: tabular-nums;
	}
	.io {
		display: grid;
		gap: 0.375rem;
		margin-top: 0.375rem;
	}
	pre {
		margin: 0;
		padding: 0.5rem 0.625rem;
		border-radius: 4px;
		background: #0d0d0d;
		border: 1px solid var(--rule);
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 0.8125rem;
		line-height: 1.45;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		max-height: 16rem;
		overflow-y: auto;
	}
	.out {
		color: #c9c6bf;
	}
	.peek {
		margin-top: 0.375rem;
		max-height: 6rem;
		opacity: 0.7;
	}
	.links {
		margin: 0;
		padding-left: 1.125rem;
		font-size: 0.8125rem;
		display: grid;
		gap: 0.25rem;
	}
	.links a {
		color: var(--paper);
	}
	.made {
		list-style: none;
		margin: 0.375rem 0 0;
		padding: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
	.made li {
		display: grid;
		gap: 0.25rem;
		max-width: 10rem;
	}
	.thumb {
		display: block;
		padding: 0;
		border: 0;
		border-radius: 6px;
		overflow: hidden;
		background: #0a0a0a;
		cursor: zoom-in;
		border-bottom: 3px solid var(--c);
	}
	.thumb img {
		display: block;
		width: 10rem;
		height: 7.5rem;
		object-fit: cover;
	}
	.file {
		display: flex;
		gap: 0.375rem;
		align-items: baseline;
		font-size: 0.8125rem;
		color: var(--paper);
		min-width: 0;
	}
	.file .name {
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.file .size {
		flex: none;
		font-size: 0.75rem;
		color: var(--muted);
	}
</style>
