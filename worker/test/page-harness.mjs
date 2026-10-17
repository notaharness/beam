// docs/02 Ceremonies, the page's side: public/index.html in Chromium under its
// own _headers, a virtual authenticator for the WebAuthn calls, and the slot
// route answered here, each write opened with the daemon's key so a test sees
// the result the page sealed. Browser capabilities the authenticator cannot
// vary are replaced before the page loads. Run with node --test; it needs
// `npx playwright install chromium`.
import assert from "node:assert/strict";
import { createDecipheriv, createHmac, createPublicKey, diffieHellman, generateKeyPairSync, randomBytes } from "node:crypto";
import { readFile } from "node:fs/promises";
import { chromium } from "playwright";

const pub = new URL("../public/", import.meta.url);
const body = await readFile(new URL("index.html", pub), "utf8");
const headers = Object.fromEntries(
  (await readFile(new URL("_headers", pub), "utf8"))
    .split("\n")
    .filter((l) => /^\s+\S/.test(l))
    .map((l) => [l.slice(0, l.indexOf(":")).trim(), l.slice(l.indexOf(":") + 1).trim()]),
);

const b64 = (b) => Buffer.from(b).toString("base64url");

// open is the daemon's HPKE open (docs/02): DHKEM(X25519, HKDF-SHA256),
// HKDF-SHA256, AES-128-GCM, info "beam-ceremony:v1" ‖ slot.
function open(key, slot, sealed) {
  const cat = (...p) => Buffer.concat(p.map((x) => (typeof x === "string" ? Buffer.from(x) : Buffer.from(x))));
  const u16 = (n) => [n >> 8, n & 255];
  const kem = cat("KEM", u16(0x20)), suite = cat("HPKE", u16(0x20), u16(1), u16(1));
  const mac = (k, d) => createHmac("sha256", k).update(d).digest();
  const extract = (s, salt, label, ikm) => mac(salt.length ? salt : Buffer.alloc(32), cat("HPKE-v1", s, label, ikm));
  const expand = (s, prk, label, info, n) => mac(prk, cat(u16(n), "HPKE-v1", s, label, info, [1])).subarray(0, n);
  const enc = sealed.subarray(0, 32);
  const dh = diffieHellman({ privateKey: key.privateKey, publicKey: createPublicKey({ key: { kty: "OKP", crv: "X25519", x: b64(enc) }, format: "jwk" }) });
  const shared = expand(kem, extract(kem, [], "eae_prk", dh), "shared_secret", cat(enc, Buffer.from(key.k, "base64url")), 32);
  const context = cat([0], extract(suite, [], "psk_id_hash", []), extract(suite, [], "info_hash", cat("beam-ceremony:v1", slot)));
  const secret = extract(suite, shared, "secret", []);
  const d = createDecipheriv("aes-128-gcm", expand(suite, secret, "key", context, 16), expand(suite, secret, "base_nonce", context, 12));
  d.setAuthTag(sealed.subarray(sealed.length - 16));
  return new URLSearchParams(Buffer.concat([d.update(sealed.subarray(32, sealed.length - 16)), d.final()]).toString());
}

// request is a ceremony's fragment as the daemon writes it, keys sorted.
function request(o, extra = {}) {
  const { publicKey, privateKey } = generateKeyPairSync("x25519");
  const k = publicKey.export({ format: "jwk" }).x;
  const f = { o, s: b64(randomBytes(16)), k, c: b64(randomBytes(32)), l: "buildbox", f: "b7f39a210c4e55d1", ...(o === "c" ? { n: "beam" } : {}), ...extra };
  const frag = new URLSearchParams(Object.entries(f).filter(([, v]) => v !== undefined).sort(([a], [b]) => (a < b ? -1 : 1))).toString();
  return { url: "https://beam.n10.is/#" + frag, slot: f.s, key: { privateKey, k } };
}

// Replacements run before the page, in its realm. Each is off unless the
// test's options name it; calls counts the WebAuthn calls the page makes.
function replace(o) {
  if (o.clipboard === false) Object.defineProperty(navigator, "clipboard", {
    value: { writeText: () => Promise.reject(new DOMException("denied", "NotAllowedError")) },
  });
  if (o.preflight === "hang") {
    SubtleCrypto.prototype.generateKey = () => new Promise(() => {});
  }
  window.__calls = 0;
  window.__violations = [];
  addEventListener("securitypolicyviolation", (e) => window.__violations.push(e.violatedDirective));
  const cc = CredentialsContainer.prototype;
  for (const name of ["create", "get"]) {
    const real = cc[name];
    Object.defineProperty(cc, name, {
      configurable: true,
      value(...args) {
        window.__calls++;
        const opts = args[0] || {};
        if (o.credential === "null") return Promise.resolve(null);
        if (o.credential?.startsWith("prf:")) {
          // the real credential, its PRF output cut to this many bytes
          return real.apply(this, args).then((cred) => {
            const results = cred.getClientExtensionResults();
            results.prf.results.first = results.prf.results.first.slice(0, Number(o.credential.slice(4)));
            cred.getClientExtensionResults = () => results;
            return cred;
          });
        }
        if (o.credential?.startsWith("throw:")) return Promise.reject(new DOMException("replaced", o.credential.slice(6)));
        if (o.credential === "hang") {
          return new Promise((_, reject) => opts.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))));
        }
        return real.apply(this, args);
      },
    });
  }
  if (o.webauthn === false) delete window.PublicKeyCredential;
  if (o.caps !== undefined && window.PublicKeyCredential) {
    const caps = {
      true: () => Promise.resolve({ "extension:prf": true }),
      false: () => Promise.resolve({ "extension:prf": false }),
      odd: () => Promise.resolve({ "extension:prf": "true" }),
      absent: () => Promise.resolve({}),
      throws: () => Promise.reject(new DOMException("no", "NotSupportedError")),
      hangs: () => new Promise(() => {}),
    }[o.caps];
    if (caps) PublicKeyCredential.getClientCapabilities = caps;
    else delete PublicKeyCredential.getClientCapabilities;
  }
  if (o.x25519 !== undefined) {
    // x25519 is how many X25519 key generations succeed; later ones fail.
    const gen = SubtleCrypto.prototype.generateKey;
    let left = o.x25519;
    SubtleCrypto.prototype.generateKey = function (alg, ...rest) {
      if ((alg?.name ?? alg) === "X25519" && left-- <= 0) return Promise.reject(new DOMException("no X25519", "NotSupportedError"));
      return gen.call(this, alg, ...rest);
    };
  }
}

let browser;
export async function startBrowser() { browser = await chromium.launch(); }
export async function stopBrowser() { await browser?.close(); }

// load opens the page at url in a fresh browser context, with a virtual
// authenticator (prf: whether it does PRF; present: whether it answers
// without a touch) and the slot route answering slot, which is a status or
// "abort" for a request that gets no response, "hang" for one never answered.
async function load(url, o = {}) {
  const context = await browser.newContext({ reducedMotion: o.motion ?? "no-preference", colorScheme: o.scheme ?? "light", viewport: o.viewport ?? { width: 1280, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(8000);
  const cdp = await context.newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true,
      hasPrf: o.prf ?? true, automaticPresenceSimulation: true,
    },
  });
  const posts = [];
  await page.route("**/*", async (route) => {
    const u = new URL(route.request().url());
    if (u.origin !== "https://beam.n10.is") throw new Error("Unexpected test request: " + u.origin);
    if (u.pathname === "/") return route.fulfill({ status: 200, contentType: "text/html; charset=utf-8", headers, body });
    if (u.pathname.startsWith("/v1/slots/") && route.request().method() === "POST") {
      posts.push({ path: u.pathname, body: JSON.parse(route.request().postData()) });
      const slot = o.slot ?? 201;
      if (slot === "abort") return route.abort("connectionreset");
      if (slot === "hang") return; // never answered
      return route.fulfill({ status: slot, contentType: "application/json", body: slot === 201 ? "{}" : '{"error":"x"}' });
    }
    return route.fulfill({ status: 404 });
  });
  if (o.passkey) {
    // a passkey for beam.n10.is already on the authenticator, as a get needs
    await page.goto("https://beam.n10.is/");
    await page.evaluate(async () => {
      await navigator.credentials.create({ publicKey: {
        rp: { id: "beam.n10.is", name: "beam" }, user: { id: new Uint8Array(16), name: "beam", displayName: "beam" },
        challenge: new Uint8Array(32), pubKeyCredParams: [{ type: "public-key", alg: -7 }],
        authenticatorSelection: { residentKey: "required", userVerification: "required" }, extensions: { prf: {} },
      } });
    });
    await page.goto("about:blank");
  }
  await page.addInitScript(replace, o);
  await page.goto(url);
  const calls = () => page.evaluate(() => window.__calls);
  return { page, posts, calls, close: () => context.close() };
}

// sent is the one result the page wrote, opened.
function sent(req, posts) {
  assert.equal(posts.length, 1, "one slot write");
  assert.equal(posts[0].path, "/v1/slots/" + req.slot);
  return open(req.key, req.slot, Buffer.from(posts[0].body.sealed, "base64url"));
}

const text = async (page, sel) => (await page.locator(sel).textContent())?.trim();
const visible = (page, name) => page.getByRole("button", { name, exact: true }).isVisible();

// settle waits for the page's terminal result and returns its heading and
// body.
async function settle(page) {
  await page.locator("#result:not([hidden])").waitFor();
  return [await text(page, "#result-heading"), await text(page, "#result-body")];
}

export { request, load, sent, text, visible, settle };
