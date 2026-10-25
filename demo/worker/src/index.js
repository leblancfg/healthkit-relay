// The Health Relay demo server as a Cloudflare Worker: the public API of healthkit-relay's
// demo mode (server/demo.go), for App Review and anyone trying the app. It keeps nothing
// about the people who use it:
//
//   - Health data: each batch is checked like on any server (batch.js), answered as stored,
//     then dropped. The counts of records received (all devices together, for the app's
//     "Records on server"), pairings and upload bytes go back to zero every hour.
//   - Devices: pairing with the shared code returns a signed token (HMAC-SHA256 with
//     DEMO_KEY) valid for 30 days; no device list, every device is "Demo iPhone".
//   - Logs: none (Workers Logs stay off, see wrangler.toml).
//
// Per hour it accepts at most DEMO_QUOTA bytes of uploads and 500 pairings, and 4 uploads at
// once, answering 429 beyond that. One Durable Object (DemoState) serves every request, so
// these counts are exact and batch validation gets a Durable Object's CPU time.
//
// The contract test (server/demo_contract_test.go) runs the same requests against this and
// against the Go server's demo mode.
//
// Requests for PRODUCT_HOST (the product's address) are sent to the documentation.

import { BadRequest, checkBatch, fields, heartbeatSchema, pairSchema, WireVersion } from "./batch.js";
import { loginPage, pairPage } from "./setup.js";

const TokenLifetime = 30 * 24 * 3600; // seconds (DemoTokenLifetime)
const DemoPairings = 500;
const DemoUploads = 4;
const ResetEvery = 3600 * 1000;
const badCodeMessage = "This setup code expired or was already used. Get a new one from the server's setup page, " +
  "or run healthkit-relay pair on the server.";

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (env.PRODUCT_HOST && url.hostname === env.PRODUCT_HOST) {
      return new Response(null, { status: 301, headers: {
        Location: env.PRODUCT_URL + url.pathname.replace(/^\//, "") + url.search, "Strict-Transport-Security": "max-age=31536000" } });
    }
    const stub = env.DEMO_STATE.get(env.DEMO_STATE.idFromName("demo"));
    return stub.fetch(request);
  },
};

export class DemoState {
  constructor(ctx, env) {
    this.ctx = ctx;
    this.env = env;
    this.uploads = 0; // uploads being read
    this.ctx.blockConcurrencyWhile(async () => {
      this.state = (await ctx.storage.get("state")) ?? fresh();
    });
  }

  async fetch(request) {
    const res = await this.route(request);
    // An answer sent before the body was read (401, 429...) leaves it unread: drop it.
    if (request.body && !request.bodyUsed) await request.body.cancel().catch(() => {});
    res.headers.set("Strict-Transport-Security", "max-age=31536000");
    return res;
  }

  async route(request) {
    const url = new URL(request.url);
    const routes = {
      "/healthz": { GET: () => new Response("ok\n", { headers: { "Content-Type": "text/plain" } }) },
      "/metrics": { GET: () => httpError(403, "/metrics answers only private networks: this computer, the local network or Tailscale") },
      "/v1/samples": { POST: () => this.samples(request) },
      "/v1/heartbeat": { POST: () => this.heartbeat(request) },
      "/v1/status": { GET: () => this.status(request) },
      "/v1/server": { GET: () => writeJSON(200, { server_id: this.env.SERVER_ID, wire_version: WireVersion }) },
      "/v1/data": { DELETE: () => this.erase(request, url) },
      "/v1/pair": { POST: () => this.pair(request) },
      "/v1/pairing-codes": { POST: unauthorized },
      "/v1/devices": { GET: unauthorized },
      "/v1/tokens": { POST: unauthorized },
      "/setup": { GET: () => this.setupPage(url), POST: () => setupForm(request, url, () => loginPage(401, this.env.VERSION, "That is not this server's admin token.")) },
      "/setup/revoke": { POST: () => setupForm(request, url, () => redirect("/setup")) },
      "/setup/tokens": { POST: () => setupForm(request, url, () => redirect("/setup")) },
      "/setup/status": { GET: () => writeJSON(200, { state: this.isCode(url.searchParams.get("code")) ? "pending" : "invalid" }) },
      "/": { GET: () => redirect("/setup") },
    };
    let methods = routes[url.pathname];
    if (!methods && /^\/v1\/devices\/[^/]+$/.test(url.pathname)) methods = { DELETE: unauthorized };
    if (!methods) return text(404, "404 page not found\n");
    const method = request.method === "HEAD" && methods.GET ? "GET" : request.method;
    if (!methods[method]) {
      const allow = Object.keys(methods).flatMap((m) => (m === "GET" ? ["GET", "HEAD"] : [m])).sort();
      const res = text(405, "Method Not Allowed\n");
      res.headers.set("Allow", allow.join(", "));
      return res;
    }
    return methods[method]();
  }

  // period starts a new period when the last one is over, like demoReset.
  period() {
    if (Date.now() >= this.state.nextReset) {
      this.state = fresh();
      this.save();
    }
    return this.state;
  }

  save() {
    // Counts only; written in the background, in order (output gates hold the answer).
    this.ctx.storage.put("state", this.state);
  }

  isCode(code) {
    const want = normalizeCode(this.env.DEMO_CODE ?? "");
    return code != null && want.length >= 16 && normalizeCode(code) === want; // minDemoCode
  }

  async pair(request) {
    let req;
    try {
      req = fields(await readJSON(request, 4096), pairSchema);
    } catch (e) {
      return httpError(400, "invalid JSON: " + e.message);
    }
    const name = (req.name ?? "").trim() || "iPhone";
    const model = req.model ?? "";
    if (byteLen(name) > 100 || byteLen(model) > 100) {
      return httpError(400, "name and model must be at most 100 characters each");
    }
    if (/\p{Cc}/u.test(name) || /\p{Cc}/u.test(model)) {
      return httpError(400, "name and model must be printable: line breaks, tabs and other control characters are refused");
    }
    if (!this.isCode(req.code ?? "")) return httpError(403, badCodeMessage);
    const st = this.period();
    if (st.pairings >= DemoPairings) {
      const res = httpError(429, "this demo server has paired many iPhones in the last hour: try again later");
      res.headers.set("Retry-After", "600");
      return res;
    }
    st.pairings++;
    this.save();
    const { token, device } = await this.mint();
    return writeJSON(200, { token, device, server: this.statusOf(device) });
  }

  async status(request) {
    const device = await this.authorize(request);
    if (!device) return unauthorized();
    return writeJSON(200, this.statusOf(device));
  }

  statusOf(device) {
    return { version: this.env.VERSION, wire_version: WireVersion, records: this.period().received, device, ...this.handoff() };
  }

  handoff() {
    return { server_id: this.env.SERVER_ID, preferred_url: this.env.PUBLIC_URL };
  }

  async heartbeat(request) {
    if (!(await this.authorize(request))) return unauthorized();
    try {
      fields(await readJSON(request, 16 << 10), heartbeatSchema);
    } catch (e) {
      return httpError(400, "invalid JSON: " + e.message);
    }
    return writeJSON(200, this.handoff());
  }

  async erase(request, url) {
    const device = await this.authorize(request);
    if (!device) return unauthorized();
    const confirm = url.searchParams.get("confirm") ?? "";
    if (confirm !== "" && confirm !== "erase") return httpError(400, 'confirm must be "erase"; nothing was erased');
    // A demo server stores no records, and nothing sent with any token: for it, Erase and
    // Disconnect (disconnect=1) leaves the device disconnected.
    if (url.searchParams.get("disconnect") === "1") return writeJSON(200, { device: device.id, erased: 0, disconnected: true });
    return writeJSON(200, { device: device.id, erased: 0 });
  }

  async samples(request) {
    if (!(await this.authorize(request))) return unauthorized();
    // demoAdmit: a slot among DemoUploads, and room left in the quota.
    if (this.uploads >= DemoUploads) return busy(10, "this demo server is busy: try again in a few seconds");
    const quota = Number(this.env.DEMO_QUOTA);
    const maxBody = Number(this.env.MAX_BODY_BYTES);
    const st = this.period();
    const left = quota - st.used;
    if (left <= 0) {
      const wait = Math.max(st.nextReset - Date.now(), 1000);
      return busy(Math.floor(wait / 1000) + 1, `this demo server accepts ${Math.floor(quota / 2 ** 20)} MB of uploads per 1h0m0s: try again in ${goMinutes(wait)}`);
    }
    this.uploads++;
    try {
      let body;
      try {
        body = await readBody(request, Math.min(left, maxBody), (n) => { st.used += n; });
      } catch (e) {
        if (e instanceof TooLarge) return httpError(413, `body too large: this server accepts at most ${maxBody} bytes (-max-body-bytes)`);
        throw e;
      } finally {
        this.save();
      }
      let batch;
      try {
        batch = parseJSON(body);
      } catch (e) {
        if (e instanceof Incomplete) return httpError(400, "incomplete body");
        return httpError(400, "invalid JSON: " + e.message);
      }
      let answer;
      try {
        answer = checkBatch(batch);
      } catch (e) {
        if (e instanceof BadRequest) return httpError(400, e.message);
        throw e;
      }
      st.batches++;
      st.received += answer.received;
      this.save();
      return new Response(JSON.stringify(answer) + "\n", { headers: { "Content-Type": "application/json" } });
    } finally {
      this.uploads--;
    }
  }

  async setupPage(url) {
    const code = url.searchParams.get("code") ?? "";
    if (code === "") return loginPage(200, this.env.VERSION, "");
    if (!this.isCode(code)) {
      return loginPage(410, this.env.VERSION, "This setup code expired or was already used. For a new one, sign in with the admin token, or run healthkit-relay pair on the server.");
    }
    return pairPage(this.env.VERSION, this.env.PUBLIC_URL, code);
  }

  // Tokens: "demo." + base64url(8 random bytes, 8 bytes of expiry in Unix seconds) + "." +
  // base64url(HMAC-SHA256 of those 16 bytes), as in demo.go.
  async key() {
    this.hmacKey ??= await crypto.subtle.importKey("raw", hex(this.env.DEMO_KEY), { name: "HMAC", hash: "SHA-256" }, false, ["sign", "verify"]);
    return this.hmacKey;
  }

  async mint() {
    const payload = new Uint8Array(16);
    crypto.getRandomValues(payload.subarray(0, 8));
    new DataView(payload.buffer).setBigUint64(8, BigInt(Math.floor(Date.now() / 1000) + TokenLifetime));
    const sig = new Uint8Array(await crypto.subtle.sign("HMAC", await this.key(), payload));
    return { token: "demo." + b64url(payload) + "." + b64url(sig), device: demoDevice(payload) };
  }

  // authorize returns the device of a valid demo token, or null.
  async authorize(request) {
    const auth = request.headers.get("Authorization") ?? "";
    const token = (auth.startsWith("Bearer ") ? auth.slice(7) : "").trim();
    const m = /^demo\.([A-Za-z0-9_-]*)\.([A-Za-z0-9_-]*)$/.exec(token);
    if (!m) return null;
    const payload = unb64url(m[1]), sig = unb64url(m[2]);
    if (!payload || !sig || payload.length !== 16) return null;
    if (!(await crypto.subtle.verify("HMAC", await this.key(), sig, payload))) return null;
    if (Date.now() / 1000 > Number(new DataView(payload.buffer).getBigUint64(8))) return null;
    return demoDevice(payload);
  }
}

function fresh() {
  return { nextReset: Date.now() + ResetEvery, used: 0, pairings: 0, batches: 0, received: 0 };
}

function demoDevice(payload) {
  const id = [...payload.subarray(0, 4)].map((b) => b.toString(16).padStart(2, "0")).join("");
  const created = new Date(Math.floor(Date.now() / 1000) * 1000).toISOString().replace(".000Z", "Z");
  return { id, name: "Demo iPhone", created_at: created, last_seen_at: "0001-01-01T00:00:00Z", scope: "upload" };
}

const normalizeCode = (c) => c.trim().replaceAll("-", "").toUpperCase();
const byteLen = (s) => new TextEncoder().encode(s).length;

function hex(s) {
  const b = new Uint8Array(s.length / 2);
  for (let i = 0; i < b.length; i++) b[i] = parseInt(s.substr(i * 2, 2), 16);
  return b;
}

function b64url(bytes) {
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function unb64url(s) {
  if (s.length % 4 === 1) return null;
  try {
    return Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
  } catch {
    return null;
  }
}

// goMinutes is Go's Duration.Round(time.Minute).String(): "37m0s", "1h0m0s".
function goMinutes(ms) {
  const m = Math.round(ms / 60000);
  return m >= 60 ? `${Math.floor(m / 60)}h${m % 60}m0s` : m === 0 ? "0s" : `${m}m0s`;
}

class TooLarge extends Error {}
class Incomplete extends Error {}

// readBody reads at most limit bytes, counting each chunk with count; more is TooLarge.
async function readBody(request, limit, count) {
  if (!request.body) return new Uint8Array();
  const reader = request.body.getReader();
  const chunks = [];
  let n = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    n += value.length;
    count(value.length);
    if (n > limit) {
      reader.cancel();
      throw new TooLarge();
    }
    chunks.push(value);
  }
  const out = new Uint8Array(n);
  let off = 0;
  for (const c of chunks) {
    out.set(c, off);
    off += c.length;
  }
  return out;
}

// parseJSON decodes a body like json.Decoder.Decode: empty is "EOF", a document that ends
// before its value does (V8 says where it stopped: the end) is Incomplete, and anything after
// the first value is left unread.
function parseJSON(bytes) {
  const s = new TextDecoder().decode(bytes);
  if (s.trim() === "") throw new Error("EOF");
  try {
    return JSON.parse(s);
  } catch (e) {
    const at = /at position (\d+)/.exec(e.message);
    if (/end of JSON input/.test(e.message) || (at && Number(at[1]) >= s.trimEnd().length)) throw new Incomplete();
    // The decoder reads the first value and leaves what follows.
    if (at && /non-whitespace character after JSON/.test(e.message)) return JSON.parse(s.slice(0, Number(at[1])));
    throw new Error(e.message);
  }
}

async function readJSON(request, limit) {
  let bytes;
  try {
    bytes = await readBody(request, limit, () => {});
  } catch (e) {
    if (e instanceof TooLarge) throw new Error("http: request body too large");
    throw e;
  }
  try {
    return parseJSON(bytes);
  } catch (e) {
    throw new Error(e instanceof Incomplete ? "unexpected EOF" : e.message);
  }
}

// setupForm checks a form post like setup.go's setupForm, then answers with then().
async function setupForm(request, url, then) {
  const site = request.headers.get("Sec-Fetch-Site") ?? "";
  const origin = request.headers.get("Origin") ?? "";
  if (!(site === "same-origin" || site === "none" || (site === "" && (origin === "" || origin === url.origin)))) {
    return text(403, "This form was sent from another site. Open the setup page and try again from there.\n");
  }
  try {
    await readBody(request, 16 << 10, () => {});
  } catch {
    return text(400, "The server could not read this form. Reload the setup page and try again.\n");
  }
  return then();
}

function writeJSON(code, v) {
  return new Response(JSON.stringify(v) + "\n", { status: code, headers: { "Content-Type": "application/json", "Cache-Control": "no-store" } });
}

function httpError(code, msg) {
  return new Response(JSON.stringify({ error: msg }) + "\n", { status: code, headers: { "Content-Type": "application/json" } });
}

function unauthorized() {
  const res = httpError(401, "unauthorized");
  res.headers.set("WWW-Authenticate", "Bearer");
  return res;
}

function busy(seconds, msg) {
  const res = httpError(429, msg);
  res.headers.set("Retry-After", String(seconds));
  return res;
}

function text(code, body) {
  return new Response(body, { status: code, headers: { "Content-Type": "text/plain; charset=utf-8", "X-Content-Type-Options": "nosniff" } });
}

function redirect(location) {
  return new Response(null, { status: 303, headers: { Location: location } });
}
