# Deploy

Where each part of Adytus runs, and how the monorepo is published.

## Push targets

The monorepo is published to four remotes:

| Remote | Repository | Contents |
| --- | --- | --- |
| `origin` | `gecenterprises/adytus` | full monorepo |
| `backend` | `reaganiwadha/adytus-backend` | full monorepo mirror (Railway) |
| `adytus` | `adytus/adytus` | full monorepo mirror |
| `ui` | `reaganiwadha/adytus-ui` | `apps/web` subtree only (Vercel) |

```sh
git remote add backend https://github.com/reaganiwadha/adytus-backend
git remote add adytus https://github.com/adytus/adytus
git remote add ui https://github.com/reaganiwadha/adytus-ui

git push origin main
git push backend main
git push adytus main
git subtree push --prefix apps/web ui main
```

Never push the whole monorepo to `ui`: Vercel builds that repository as the web app, so it must hold only `apps/web`.

## Pieces

- **API** (`apps/api`, image from `deploy/api/Dockerfile`): Railway, from the `backend` mirror. It runs migrations on start and serves the backoffice bundle.
- **Web** (`apps/web`): Vercel, from the `ui` subtree.
- **NEAR deposit contract** (`contracts/near-deposit`): deployed by hand with near-cli-rs; see its README. Its account is then set in the backoffice under **Payments → NEAR**.
