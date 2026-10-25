# Demo server (Cloudflare Worker)

The public Health Relay demo server, https://health-relay-demo.leblancfg.com, for App Review
and anyone trying the app without a server of their own. It is the demo mode of
healthkit-relay (`-demo-code-file`, `server/demo.go`) written again as a Cloudflare Worker, so
it runs on Cloudflare's free plan, on no computer of ours:

- The shared setup code (secret `DEMO_CODE`, in the App Review notes) pairs any number of
  iPhones with signed tokens, valid 30 days (secret `DEMO_KEY` signs them). No device list:
  every device is "Demo iPhone".
- Uploads are checked like on any server (`src/batch.js`, the rules of `server/model.go`),
  answered as stored, and dropped. The server counts records received, all devices
  together, for the app's "Records on server", and resets the count every hour.
- 256 MB of uploads and 500 pairings per hour, 4 uploads at once; beyond that, 429 with
  Retry-After, and the app tries again later.
- No logs: Workers Logs are off (`wrangler.toml`). Cloudflare relays the requests, as any
  host would.

`src/index.js` has the routes; one Durable Object serves every request, which keeps the hourly
counts exact. `src/setup.js` is the setup page (`/setup?code=...`) with its QR code
(`src/qr.js`, a port of `server/qr.go`). The product's address,
https://health-relay.leblancfg.com, is the documentation site on GitHub Pages.

## Same answers as the Go server

`server/demo_contract_test.go` runs one request suite (pairing, status, uploads accepted and
refused, heartbeats, erase, setup page, quota) against the Go server's demo mode in `go test`,
and in CI against this Worker under `wrangler dev`. A change to what the demo answers goes in
both, and in the suite. Against the deployed Worker:

```sh
cd server
HKR_DEMO_URL=https://health-relay-demo.leblancfg.com HKR_DEMO_CODE=<code> go test -count=1 -run TestDemoContract -v .
```

Locally, under `wrangler dev`, with small limits so the quota checks run too:

```sh
cd demo/worker
npm ci
printf 'DEMO_CODE=ABCD-EFGH-JKLM-NPQR\nDEMO_KEY=%s\n' "$(openssl rand -hex 32)" > .dev.vars
npx wrangler dev --port 8787 --var DEMO_QUOTA:1048576 --var MAX_BODY_BYTES:131072 &
cd ../../server
HKR_DEMO_URL=http://127.0.0.1:8787 HKR_DEMO_CODE=ABCD-EFGH-JKLM-NPQR HKR_DEMO_PUBLIC_URL=https://health-relay-demo.leblancfg.com \
  HKR_DEMO_QUOTA=1048576 HKR_DEMO_MAX_BODY=131072 go test -count=1 -run TestDemoContract -v .
```

`npm test` runs the Worker's own tests (`test/`).

## Deploying

```sh
CLOUDFLARE_API_TOKEN=... demo/worker/deploy.sh
```

`wrangler deploy` creates the Worker, its Durable Object and both custom domains (DNS records
and certificates). The first time, set the two secrets:

```sh
cd demo/worker
npx wrangler secret put DEMO_CODE                   # 16+ characters of A-Z and 2-7
openssl rand -hex 32 | npx wrangler secret put DEMO_KEY
```

A new `DEMO_KEY` signs out every demo device; a new `DEMO_CODE` needs new App Review notes.
