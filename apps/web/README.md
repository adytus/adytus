# Adytus UI

SvelteKit frontend for Adytus, the private, encrypted chatbot for enterprise.

This directory is also published on its own to
[`reaganiwadha/adytus-ui`](https://github.com/reaganiwadha/adytus-ui) for
Vercel. The source of truth is the `apps/web` subtree of the monorepo.

## Push targets

| Remote | Repository | Contents |
| --- | --- | --- |
| `origin` | `gecenterprises/adytus` | full monorepo |
| `backend` | `reaganiwadha/adytus-backend` | full monorepo mirror (Railway) |
| `ui` | `reaganiwadha/adytus-ui` | this `apps/web` subtree (Vercel) |

From the monorepo root:

```sh
git push origin main
git push backend main
git subtree push --prefix apps/web ui main
```

## Developing

```sh
bun install
bun run dev
```

## Building

```sh
bun run check
bun run build
```

The API URL is configured in `vite.config.ts` / `vercel.json`.
