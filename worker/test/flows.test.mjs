// docs/02 Ceremonies, the page's side: public/index.html in Chromium under its
// own _headers, a virtual authenticator for the WebAuthn calls, and the slot
// route answered here, each write opened with the daemon's key so a test sees
// the result the page sealed. Browser capabilities the authenticator cannot
// vary are replaced before the page loads. Run with node --test; it needs
// `npx playwright install chromium`.
import assert from "node:assert/strict";
import { createDecipheriv, createHmac, createPublicKey, diffieHellman, generateKeyPairSync, randomBytes } from "node:crypto";
import { readFile } from "node:fs/promises";
import { after, before, describe, it } from "node:test";
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
before(async () => {
  browser = await chromium.launch();
});
after(async () => {
  await browser?.close();
});

// load opens the page at url in a fresh browser context, with a virtual
// authenticator (prf: whether it does PRF; present: whether it answers
// without a touch) and the slot route answering slot, which is a status or
// "abort" for a request that gets no response.
async function load(url, o = {}) {
  const context = await browser.newContext();
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
  await page.route("https://beam.n10.is/**", async (route) => {
    const u = new URL(route.request().url());
    if (u.pathname === "/") return route.fulfill({ status: 200, contentType: "text/html; charset=utf-8", headers, body });
    if (u.pathname.startsWith("/v1/slots/") && route.request().method() === "POST") {
      posts.push({ path: u.pathname, body: JSON.parse(route.request().postData()) });
      const slot = o.slot ?? 201;
      if (slot === "abort") return route.abort("connectionreset");
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

describe("the ceremony page, without a request", () => {
  it("says where to start and offers no passkey", async () => {
    const t = await load("https://beam.n10.is/");
    assert.equal(await text(t.page, "h1"), "Connect your machines with beam");
    assert.equal(await text(t.page, "#explain"), "Start in n10 Desktop → Fleet, or run beam init or beam join. Open the link or scan the QR code shown there.");
    assert.equal(await t.page.getByRole("button").count(), 0);
    assert.equal(await t.page.locator("details#compat summary").textContent(), "Passkey compatibility");
    assert.equal(await t.calls(), 0);
    await t.close();
  });
});

describe("the ceremony page, given an invalid request", () => {
  const b = (n) => b64(randomBytes(n));
  // the second part of each is merged into a valid a (or c) request
  const cases = [
    ["an unknown operation", { o: "x" }],
    ["no operation", { o: undefined }],
    ["no slot", { s: undefined }],
    ["no key", { k: undefined }],
    ["no challenge", { c: undefined }],
    ["no label", { l: undefined }],
    ["no fingerprint", { f: undefined }],
    ["a short slot", { s: b(15) }],
    ["a slot of 23 characters", { s: b(16) + "A" }],
    ["a slot with a padding character", { s: b(15) + "=" }],
    ["a slot that is not canonical base64url", { s: b(16).slice(0, 21) + "B" }],
    ["a key of 31 bytes", { k: b(31) }],
    ["a key of 33 bytes", { k: b(33) }],
    ["a challenge of 31 bytes", { c: b(31) }],
    ["an uppercase fingerprint", { f: "B7F39A210C4E55D1" }],
    ["a fingerprint of 15 characters", { f: "b7f39a210c4e55d" }],
    ["an empty label", { l: "" }],
    ["a label of 65 characters", { l: "x".repeat(65) }],
    ["a label with a slash", { l: "a/b" }],
    ["a label with a brace", { l: "a{b" }],
    ["a label with a C0 control", { l: "a\tb" }],
    ["a label with a C1 control", { l: "a\u0085b" }],
    ["an empty fleet name", { o: "c", n: "" }],
    ["a fleet name with a backslash", { o: "c", n: "a\\b" }],
  ];
  for (const [what, change] of cases) {
    it(`refuses ${what}`, async () => {
      const req = request(change.o ?? "a", change);
      const t = await load(req.url);
      assert.equal(await text(t.page, "[role=alert] h1"), "This link is incomplete or invalid.");
      assert.equal(await text(t.page, "[role=alert] #explain"), "Return to n10 Desktop or your terminal and start again for a fresh link.");
      assert.equal(await t.page.getByRole("button").count(), 0);
      assert.equal(await t.calls(), 0);
      assert.equal(t.posts.length, 0);
      await t.close();
    });
  }

  it("refuses a parameter given twice", async () => {
    const req = request("a");
    const t = await load(req.url + "&l=other");
    assert.equal(await text(t.page, "h1"), "This link is incomplete or invalid.");
    assert.equal(await t.page.getByRole("button").count(), 0);
    await t.close();
  });

  it("ignores a parameter it does not know", async () => {
    const t = await load(request("a").url + "&z=1");
    assert.equal(await text(t.page, "h1"), "Authorize buildbox");
    await t.close();
  });

  it("accepts a label of 64 characters beyond the BMP", async () => {
    const t = await load(request("a", { l: "😀".repeat(64) }).url);
    assert.equal(await text(t.page, "h1"), "Authorize " + "😀".repeat(64));
    await t.close();
  });
});

describe("the ceremony page, given a request", () => {
  const ready = [
    ["c", "Step 1 of 2 · Create your fleet passkey",
      "Save a passkey for homelab at beam.n10.is. This creates the fleet’s credential. A second prompt will authorize buildbox and unlock its encrypted directory.",
      "Create fleet passkey for “homelab”", "Create passkey", { n: "homelab" }],
    ["a", "Authorize buildbox",
      "Use your fleet’s passkey to sign this machine’s membership and unlock the encrypted directory. If you just created a fleet passkey, this is step 2 of 2.",
      "Add “buildbox” to fleet", "Authorize machine", {}],
    ["r", "Remove buildbox from your fleet",
      "Use your fleet’s passkey to permanently revoke this machine identity. Offline machines learn when they reconnect.",
      "Remove “buildbox” from fleet", "Authorize removal", {}],
  ];
  for (const [o, heading, explain, action, button, extra] of ready) {
    it(`shows what o=${o} approves and waits for a click`, async () => {
      const t = await load(request(o, extra).url, { caps: "true" });
      assert.equal(await text(t.page, "h1"), heading);
      assert.equal(await text(t.page, "#explain"), explain);
      assert.deepEqual(await t.page.locator("#summary dt").allTextContents(), ["Action", "Machine", "Machine fingerprint"]);
      assert.deepEqual(await t.page.locator("#summary dd").allTextContents(), [action, "buildbox", "b7f3 9a21 0c4e 55d1"]);
      assert.equal(await text(t.page, "#safety"),
        "Continue only if you started this request just now. Compare the action, machine name and machine fingerprint with n10 Desktop or your terminal.");
      assert.equal(await text(t.page, "#expiry"), "Requests expire after five minutes. The originating machine shows whether this request is still active.");
      await t.page.getByRole("button", { name: button, exact: true }).waitFor();
      assert.ok(await t.page.getByRole("button", { name: button }).isEnabled());
      assert.equal(await t.calls(), 0, "no passkey prompt before the click");
      assert.equal(t.posts.length, 0);
      assert.deepEqual(await t.page.evaluate(() => window.__violations), []);
      await t.close();
    });
  }

  it("marks removal as destructive", async () => {
    const t = await load(request("r").url);
    assert.match(await t.page.getByRole("button", { name: "Authorize removal" }).getAttribute("class"), /\bdanger\b/);
    await t.close();
  });

  it("renders a label as text, never markup", async () => {
    const t = await load(request("a", { l: "<img src=x onerror=alert(1)>" }).url);
    assert.equal(await text(t.page, "h1"), "Authorize <img src=x onerror=alert(1)>");
    assert.equal(await t.page.locator("main img").count(), 0);
    await t.close();
  });

  it("names the fleet beam when the create link names none", async () => {
    const t = await load(request("c", { n: undefined }).url);
    assert.match(await text(t.page, "#explain"), /^Save a passkey for beam at beam\.n10\.is\./);
    assert.equal(await text(t.page, "#action"), "Create fleet passkey for “beam”");
    await t.close();
  });
});

describe("the ceremony page's checks before any prompt", () => {
  const button = (page) => page.getByRole("button", { name: "Authorize machine" });

  it("says a browser without WebAuthn cannot run the request, and offers no prompt", async () => {
    const req = request("a");
    const t = await load(req.url, { webauthn: false });
    await t.page.getByRole("alert").filter({ hasText: "This browser cannot run a WebAuthn passkey request. Open this link in an up-to-date browser." }).waitFor();
    assert.equal(await button(t.page).count(), 0);
    assert.ok(await visible(t.page, "Copy link"));
    await t.page.getByRole("button", { name: "Cancel request" }).click();
    assert.equal(sent(req, t.posts).get("result"), "cancelled");
    await t.close();
  });

  it("says a browser without X25519 cannot encrypt the answer, and writes nothing", async () => {
    const req = request("a");
    const t = await load(req.url, { x25519: 0 });
    await t.page.getByRole("alert").filter({
      hasText: "This browser cannot encrypt beam’s answer. It needs X25519 in Web Crypto. Use Safari 18.4+, Chrome 133+, or Firefox 130+; passkey PRF support is also required.",
    }).waitFor();
    assert.match(await text(t.page, "[role=alert]"), /Return to the originating machine to cancel or restart\.$/);
    assert.equal(await button(t.page).count(), 0);
    assert.equal(await t.page.getByRole("button", { name: "Cancel request" }).count(), 0);
    assert.ok(await visible(t.page, "Copy link"));
    assert.equal(t.posts.length, 0);
    assert.equal(await t.calls(), 0);
    await t.close();
  });

  it("copies the link", async () => {
    const req = request("a");
    const t = await load(req.url, { x25519: 0 });
    await t.page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    await t.page.getByRole("button", { name: "Copy link" }).click();
    assert.equal(await t.page.evaluate(() => navigator.clipboard.readText()), req.url);
    await t.close();
  });

  const prf = [
    ["reports PRF", "true", "Browser PRF support detected. Your selected passkey provider must support PRF too.", "status"],
    ["cannot report PRF: no key", "absent", "This browser cannot confirm PRF support in advance. You can try; beam will check the selected passkey’s result.", "status"],
    ["cannot report PRF: not a boolean", "odd", "This browser cannot confirm PRF support in advance. You can try; beam will check the selected passkey’s result.", "status"],
    ["cannot report PRF: no method", "none", "This browser cannot confirm PRF support in advance. You can try; beam will check the selected passkey’s result.", "status"],
    ["cannot report PRF: it throws", "throws", "This browser cannot confirm PRF support in advance. You can try; beam will check the selected passkey’s result.", "status"],
    ["cannot report PRF: no answer in time", "hangs", "This browser cannot confirm PRF support in advance. You can try; beam will check the selected passkey’s result.", "status"],
  ];
  for (const [what, caps, copy, role] of prf) {
    it(`lets a browser that ${what} approve`, async () => {
      const t = await load(request("a").url, { caps });
      await t.page.getByRole(role).filter({ hasText: copy }).waitFor({ timeout: 6000 });
      assert.ok(await button(t.page).isEnabled());
      assert.equal(await t.page.getByRole("button", { name: "Try anyway" }).count(), 0);
      await t.close();
    });
  }

  it("holds back approval in a browser that reports no PRF, until Try anyway", async () => {
    const req = request("a");
    const t = await load(req.url, { caps: "false", passkey: true });
    await t.page.getByRole("alert").filter({ hasText: "This browser reports that WebAuthn PRF is unavailable. Choose a supported browser before continuing." }).waitFor();
    assert.ok(await button(t.page).isDisabled());
    assert.ok(await t.page.locator("details#compat").evaluate((d) => d.open));
    assert.equal(await text(t.page, "#anyway-note"), "Your provider may handle passkeys separately from the browser. beam still requires a valid PRF result.");
    assert.equal(await t.calls(), 0);
    assert.equal(t.posts.length, 0, "a false preflight writes nothing by itself");
    await t.page.getByRole("button", { name: "Try anyway" }).click();
    assert.deepEqual(await settle(t.page), ["Approval sent.", "Return to buildbox to check whether it joined. beam still needs to verify the answer and finish directory work. After joining, compare the fleet fingerprint with a machine already in your fleet."]);
    assert.equal(sent(req, t.posts).get("result"), "ok");
    await t.close();
  });

  it("cancels from a browser that reports no PRF", async () => {
    const req = request("a");
    const t = await load(req.url, { caps: "false" });
    await t.page.getByRole("button", { name: "Cancel request" }).click();
    assert.equal((await settle(t.page))[0], "Passkey request cancelled or not allowed.");
    assert.equal(sent(req, t.posts).get("result"), "cancelled");
    await t.close();
  });
});

describe("the ceremony page's passkey calls", () => {
  it("creates the fleet's passkey with PRF and seals it for the daemon", async () => {
    const req = request("c", { n: "homelab" });
    const t = await load(req.url);
    await t.page.getByRole("button", { name: "Create passkey" }).click();
    assert.deepEqual(await settle(t.page), ["Passkey created. One more step.",
      "Return to n10 Desktop or your terminal. Open the new link or scan the new QR code, then use this same passkey to authorize buildbox. This page cannot open the second step."]);
    const f = sent(req, t.posts);
    assert.equal(f.get("result"), "ok");
    for (const k of ["credentialId", "clientDataJSON", "attestationObject"]) assert.ok(f.get(k), k);
    const client = JSON.parse(Buffer.from(f.get("clientDataJSON"), "base64url"));
    assert.equal(client.type, "webauthn.create");
    assert.equal(client.challenge, new URLSearchParams(new URL(req.url).hash.slice(1)).get("c"));
    assert.equal(await t.page.evaluate(() => document.activeElement.id), "result-heading");
    assert.equal(await t.page.locator("#result").getAttribute("role"), "status");
    assert.equal(await t.page.getByRole("button").count(), 0, "no same-slot retry");
    assert.deepEqual(await t.page.evaluate(() => window.__violations), []);
    await t.close();
  });

  for (const [o, heading, body] of [
    ["a", "Approval sent.", "Return to buildbox to check whether it joined. beam still needs to verify the answer and finish directory work. After joining, compare the fleet fingerprint with a machine already in your fleet."],
    ["r", "Revocation approval sent.", "Return to your originating machine to check that the revocation completed and see its publication and acknowledgement status."],
  ]) {
    it(`signs o=${o} with the passkey and sends its 32-byte PRF output`, async () => {
      const req = request(o);
      const t = await load(req.url, { passkey: true });
      await t.page.getByRole("button", { name: o === "a" ? "Authorize machine" : "Authorize removal" }).click();
      assert.deepEqual(await settle(t.page), [heading, body]);
      const f = sent(req, t.posts);
      assert.equal(f.get("result"), "ok");
      assert.equal(Buffer.from(f.get("prf"), "base64url").length, 32);
      for (const k of ["credentialId", "clientDataJSON", "authenticatorData", "signature"]) assert.ok(f.get(k), k);
      assert.equal(await t.calls(), 1);
      await t.close();
    });
  }

  it("asks for one passkey however often the button is clicked", async () => {
    const req = request("a");
    const t = await load(req.url, { passkey: true });
    await t.page.getByRole("button", { name: "Authorize machine" }).waitFor();
    await t.page.evaluate(() => { const go = document.getElementById("go"); go.click(); go.click(); });
    await settle(t.page);
    assert.equal(await t.calls(), 1);
    assert.equal(t.posts.length, 1);
    await t.close();
  });

  it("reports prf-unsupported when a create leaves PRF disabled", async () => {
    const req = request("c");
    const t = await load(req.url, { prf: false });
    await t.page.getByRole("button", { name: "Create passkey" }).click();
    assert.deepEqual(await settle(t.page), ["This passkey did not provide WebAuthn PRF.",
      "beam needs PRF to derive its encrypted directory key. The browser, OS and selected passkey provider must support it. Return to your machine and start a fresh request using a supported combination."]);
    assert.equal(await text(t.page, "#result-extra"), "A passkey may have been saved, but fleet creation is not complete.");
    assert.ok(await t.page.locator("details#compat").evaluate((d) => d.open));
    assert.match(await text(t.page, "#technical-text"), /prf\.enabled/);
    assert.equal(sent(req, t.posts).get("result"), "prf-unsupported");
    await t.close();
  });

  it("reports prf-unsupported when a get returns no PRF output", async () => {
    const req = request("r");
    const t = await load(req.url, { prf: false, passkey: true });
    await t.page.getByRole("button", { name: "Authorize removal" }).click();
    assert.equal((await settle(t.page))[0], "This passkey did not provide WebAuthn PRF.");
    assert.equal(await text(t.page, "#result-extra"), "Use the same fleet passkey; a new passkey creates a different fleet.");
    assert.match(await text(t.page, "#technical-text"), /prf\.results\.first/);
    const f = sent(req, t.posts);
    assert.equal(f.get("result"), "prf-unsupported");
    assert.equal(f.get("prf"), null);
    await t.close();
  });

  it("reports prf-unsupported for PRF output that is not 32 bytes", async () => {
    const req = request("a");
    const t = await load(req.url, { passkey: true, credential: "prf:16" });
    await t.page.getByRole("button", { name: "Authorize machine" }).click();
    assert.equal((await settle(t.page))[0], "This passkey did not provide WebAuthn PRF.");
    assert.equal(sent(req, t.posts).get("result"), "prf-unsupported");
    await t.close();
  });

  it("cancels the passkey call under way, and writes cancelled once", async () => {
    const req = request("a");
    const t = await load(req.url, { credential: "hang" });
    await t.page.getByRole("button", { name: "Authorize machine" }).click();
    await t.page.getByRole("status").filter({ hasText: "Waiting for your fleet passkey…" }).waitFor();
    await t.page.getByRole("button", { name: "Cancel request" }).click();
    assert.deepEqual(await settle(t.page), ["Passkey request cancelled or not allowed.",
      "The browser did not return a passkey. You may have cancelled, the prompt may have expired, or no eligible passkey was available. Return to your machine to try again with a fresh link."]);
    await t.page.waitForTimeout(200);
    assert.equal(sent(req, t.posts).get("result"), "cancelled");
    await t.close();
  });

  it("reports cancelled for NotAllowedError", async () => {
    const req = request("a");
    const t = await load(req.url, { credential: "throw:NotAllowedError" });
    await t.page.getByRole("button", { name: "Authorize machine" }).click();
    assert.equal((await settle(t.page))[0], "Passkey request cancelled or not allowed.");
    assert.equal(sent(req, t.posts).get("result"), "cancelled");
    await t.close();
  });

  for (const [what, credential, detail] of [["another exception", "throw:InvalidStateError", /InvalidStateError/], ["no credential", "null", /failed/]]) {
    it(`reports failed for ${what}`, async () => {
      const req = request("a");
      const t = await load(req.url, { credential });
      await t.page.getByRole("button", { name: "Authorize machine" }).click();
      assert.deepEqual(await settle(t.page), ["The passkey request failed.", "The browser could not complete the request. Return to your machine and try a fresh request."]);
      assert.match(await text(t.page, "#technical-text"), detail);
      assert.equal(sent(req, t.posts).get("result"), "failed");
      await t.close();
    });
  }

  it("says nothing was sent when sealing fails after approval", async () => {
    const req = request("a");
    const t = await load(req.url, { passkey: true, x25519: 1 }); // the check before the prompt takes the one that works
    await t.page.getByRole("button", { name: "Authorize machine" }).click();
    assert.deepEqual(await settle(t.page), ["Could not encrypt the answer.",
      "Nothing was sent from this attempt. Return to the originating machine, cancel this request and start again in a supported browser."]);
    assert.equal(t.posts.length, 0);
    await t.close();
  });
});

describe("the ceremony page's delivery", () => {
  // each result code, made by the authenticator or a replaced call
  const results = [
    ["ok", { passkey: true }, "Approval sent."],
    ["prf-unsupported", { passkey: true, prf: false }, "This passkey did not provide WebAuthn PRF."],
    ["cancelled", { credential: "throw:NotAllowedError" }, "Passkey request cancelled or not allowed."],
    ["failed", { credential: "throw:UnknownError" }, "The passkey request failed."],
  ];
  const deliveries = [
    [409, "This request was already answered.",
      "Another device, or an earlier attempt on this device, sent the first answer. Check the originating machine. If you did not approve that answer and it enrolled, reset the fleet on that machine. A reset does not undo a completed revocation."],
    ["abort", "Could not confirm delivery.",
      "Check the originating machine before starting again: it may have received your answer. If it is still waiting, cancel there and start a fresh request."],
    [429, "Too many requests.",
      "beam.n10.is did not accept this answer. Wait a minute, then cancel the pending request on the originating machine and start again."],
    [500, "beam.n10.is did not accept the answer (HTTP 500).", "Return to the originating machine and start a fresh request."],
    [404, "beam.n10.is did not accept the answer (HTTP 404).", "Return to the originating machine and start a fresh request."],
  ];
  for (const [code, o, on201] of results) {
    it(`reports a delivered ${code} as ${code}`, async () => {
      const req = request("a");
      const t = await load(req.url, o);
      await t.page.getByRole("button", { name: "Authorize machine" }).click();
      assert.equal((await settle(t.page))[0], on201);
      assert.equal(sent(req, t.posts).get("result"), code);
      await t.close();
    });
    for (const [slot, heading, body] of deliveries) {
      it(`reports ${slot} for ${code} as the slot's answer`, async () => {
        const req = request("a");
        const t = await load(req.url, { ...o, slot });
        await t.page.getByRole("button", { name: "Authorize machine" }).click();
        assert.deepEqual(await settle(t.page), [heading, body]);
        assert.equal(await t.page.locator("#result").getAttribute("role"), "alert");
        const tech = await text(t.page, "#technical-text");
        assert.match(tech, new RegExp(`Passkey result: ${code}`));
        assert.match(tech, slot === "abort" ? /Delivery: no response/ : new RegExp(`Delivery: HTTP ${slot}`));
        assert.equal(sent(req, t.posts).get("result"), code);
        assert.equal(await t.page.getByRole("button").count(), 0, "no automatic or same-slot retry");
        await t.close();
      });
    }
  }
});
