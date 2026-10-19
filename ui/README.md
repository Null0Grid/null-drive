# ui/: Null Drive web UI and shared component library

Svelte 5 + Vite + TypeScript.

| Path | What |
|---|---|
| `src/lib/` | **Shared library** (`@nulldrive/ui`): components, design tokens (`styles/tokens.css`), typed API client, pure helpers. Reused by the desktop app in Phase 4, so: no routing, no `window.location`, no global fetch, styling only via CSS variables. |
| `src/web/` | The web app: router, session, pages (Setup, Login, Files, Settings), page-specific dialogs, upload queue. |
| `e2e/` | Playwright: `flow.spec.ts` (acceptance flow) and `screenshots.spec.ts` (writes `docs/screenshots`). |
| `public/` | Copied verbatim: favicon, `theme-init.js` (applies the saved theme before first paint; external because the CSP forbids inline scripts). |

`npm run build` writes to `../server/internal/webui/dist`, which the Go server embeds.

```bash
npm ci
npm run dev          # http://localhost:5173, proxies /api to :8080 (run the Go server too, or `make dev` from the root)
npm test             # Vitest unit + component tests
npm run check        # svelte-check (types + a11y warnings as errors)
npm run e2e          # Playwright e2e (needs ../bin/nulldrive: `make build`)
npm run screenshots  # regenerate ../docs/screenshots
```

Playwright is pinned to 1.56.0; run `npx playwright install chromium` once on a new machine.
