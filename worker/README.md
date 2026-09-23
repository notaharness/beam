# worker

The directory API and the ceremony page at `https://beam.n10.is` ([docs/09](../docs/09-build-and-distribution.md)):
`src/index.ts` on D1, `public/` as static assets with the headers in `public/_headers`.

## Test

```sh
npm ci
npm test                              # tsc, then vitest in workerd
npx playwright install chromium       # for internal/ceremony's TestBrowserCeremony
```

The Go directory client's and ceremony slot reader's tests (`internal/directory`,
`internal/ceremony`) also run against the worker under `wrangler dev`:

```sh
npx wrangler d1 execute beam --local --file schema.sql
npx wrangler dev --local --port 8799 &
BEAM_WORKER_URL=http://127.0.0.1:8799 go test -tags "$(cat ../build-tags.txt),beamtest" ../internal/directory ../internal/ceremony
```

## Deploy

Done once, from this directory, on the account that holds the `n10.is` zone:

```sh
npx wrangler login
npx wrangler d1 create beam           # its database_id goes in wrangler.toml
npx wrangler d1 execute beam --remote --file schema.sql
npx wrangler deploy                   # attaches the beam.n10.is custom domain
```

Later deploys re-run the `schema.sql` step above, whose statements are all `IF NOT
EXISTS`, then `npx wrangler deploy`. The D1 database `beam` (`7e0d3382-8f3d-45a9-bf67-b341360650da`, WEUR) and the worker
`beam` with its `beam.n10.is` custom domain were created this way on 2026-09-23.
