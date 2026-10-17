# worker

The directory API and the ceremony page at `https://beam.n10.is` ([docs/09](../docs/09-build-and-distribution.md)):
`src/index.ts` on D1, `public/` as static assets with the headers in `public/_headers`.

## Test

```sh
npm ci
npx playwright install chromium       # for the page's flows and internal/ceremony's TestBrowserCeremony
npm test                              # tsc, vitest in workerd, then the page's flows in Chromium
```

The Go directory client's and ceremony slot reader's tests (`internal/directory`,
`internal/ceremony`) also run against the worker under `wrangler dev`:

```sh
npx wrangler d1 execute beam --local --file schema.sql
npx wrangler dev --local --port 8799 &
BEAM_WORKER_URL=http://127.0.0.1:8799 go test -tags "$(cat ../build-tags.txt),beamtest" ../internal/directory ../internal/ceremony
```

## Deploy

A release deploys the worker: job `worker` of `.github/workflows/release.yml` runs
`wrangler d1 execute beam --remote --file schema.sql`, whose statements are all `IF NOT
EXISTS`, then `wrangler deploy` (CONTRIBUTING.md, "Releasing"). It runs in the GitHub
environment `cloudflare`, deployable only from `v*` tags, whose secret
`CLOUDFLARE_API_TOKEN` is a custom token with Account / Workers Scripts / Edit, Account /
D1 / Edit, Account / Account Settings / Read on the account, and Zone / Workers Routes /
Edit, Zone / Zone / Read on `n10.is`, and whose variable `CLOUDFLARE_ACCOUNT_ID` is the
account's id.

The worker and its database were created once, from this directory, on the account that
holds the `n10.is` zone:

```sh
npx wrangler login
npx wrangler d1 create beam           # its database_id goes in wrangler.toml
npx wrangler d1 execute beam --remote --file schema.sql
npx wrangler deploy                   # attaches the beam.n10.is custom domain
```

The D1 database `beam` (`7e0d3382-8f3d-45a9-bf67-b341360650da`, WEUR) and the worker
`beam` with its `beam.n10.is` custom domain were created this way on 2026-09-23.
