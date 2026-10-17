// Browser flows under the real page CSP, with an isolated virtual passkey and slot.
import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { after, before, describe, it } from "node:test";
import { startBrowser, stopBrowser, request, load, sent, text, visible, settle } from "./page-harness.mjs";
const b64 = (b) => Buffer.from(b).toString("base64url");
before(startBrowser);
after(stopBrowser);

describe("the ceremony page, without a request", () => {
  it("is beam's homepage and offers no passkey", async () => {
    const t = await load("https://beam.n10.is/");
    assert.equal(await text(t.page, "h1"), "Connect this machine");
    assert.ok(await t.page.locator("#home").isVisible());
    assert.equal(await text(t.page, "#create-command"), "beam init");
    assert.doesNotMatch(await t.page.locator("body").innerText(), /n10|connected anywhere/i);
    await t.page.getByText("Joining an existing fleet?", { exact: true }).click();
    assert.ok(await t.page.getByText("beam join", { exact: true }).isVisible());
    await t.page.getByText("Using an app with beam?", { exact: true }).click();
    assert.match(await t.page.locator("#home").innerText(), /Start the create or join request in that app/);
    assert.equal(await t.page.getByRole("alert").count(), 0);
    assert.equal(await t.page.getByRole("button").count(), 0);
    assert.equal(await t.page.locator("details#compat").isVisible(), false);
    assert.equal(await t.page.locator("#summary").isVisible(), false);
    assert.equal(await t.calls(), 0);
    await t.close();
  });

  for (const scheme of ["light", "dark"]) {
    it(`keeps the action, comparison and help usable at phone width in ${scheme}`, async () => {
      const t = await load(request("a", { l: "long-machine-name-".repeat(3) }).url, { scheme });
      await t.page.setViewportSize({ width: 390, height: 844 });
      await t.page.getByRole("button", { name: "Authorize machine" }).waitFor();
      assert.equal(await t.page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      assert.doesNotMatch(await t.page.locator("body").innerText(), /n10|connected anywhere/i);
      assert.equal(await t.page.locator("#fingerprint").innerText(), "b7f3 9a21 0c4e 55d1");
      assert.ok((await t.page.locator("#go").boundingBox()).height >= 44);
      await t.page.getByText("Passkey compatibility", { exact: true }).click();
      assert.ok(await t.page.getByText("Browser PRF support detected.", { exact: false }).count());
      assert.equal(await t.page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      await t.close();
    });
  }

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
      assert.equal(await text(t.page, "[role=alert] #explain"), "Return to the terminal or app where you started and request a fresh link.");
      assert.equal(await t.page.locator("#home").isVisible(), false);
      assert.ok(await t.page.locator("details#compat").isVisible());
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
    assert.equal(await text(t.page, "h1"), "Authorize this machine");
    assert.equal(await t.page.locator("#home").isVisible(), false);
    assert.ok(await t.page.locator("details#compat").isVisible());
    await t.close();
  });

  it("accepts a label of 64 characters beyond the BMP", async () => {
    const t = await load(request("a", { l: "😀".repeat(64) }).url);
    assert.equal(await text(t.page, "#machine"), "😀".repeat(64));
    await t.close();
  });
});

describe("the ceremony page, given a request", () => {
  const ready = [
    ["c", "Create your fleet passkey",
      "Step 1 of 2. Save a passkey, then return to the machine where you started for the second prompt.",
      "Create fleet passkey for “homelab”", "Create passkey", { n: "homelab" }],
    ["a", "Authorize this machine",
      "Use your fleet’s passkey to add this machine. If you just created it, this is step 2 of 2.",
      "Add “buildbox” to fleet", "Authorize machine", {}],
    ["r", "Remove this machine from your fleet",
      "Permanently revoke this machine identity with your fleet’s passkey. Offline machines learn when they reconnect.",
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
        "Continue only if you started this request and all three details match. If anything differs, cancel the request.");
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
    assert.equal(await text(t.page, "#machine"), "<img src=x onerror=alert(1)>");
    assert.equal(await t.page.locator("main img").count(), 0);
    await t.close();
  });

  it("names the fleet beam when the create link names none", async () => {
    const t = await load(request("c", { n: undefined }).url);
    assert.equal(await text(t.page, "#action"), "Create fleet passkey for “beam”");
    await t.close();
  });
});

describe("the ceremony page's checks before any prompt", () => {
  const button = (page) => page.getByRole("button", { name: "Authorize machine" });

  it("says a browser without WebAuthn cannot run the request, and offers no prompt", async () => {
    const req = request("a");
    const t = await load(req.url, { webauthn: false });
    await t.page.getByRole("alert").filter({ hasText: "This browser cannot use passkeys." }).waitFor();
    assert.equal(await button(t.page).count(), 0);
    assert.ok(await visible(t.page, "Copy link"));
    assert.equal(await t.page.locator("#summary").isVisible(), false);
    assert.equal(await t.page.locator("#safety").isVisible(), false);
    assert.equal(await t.page.locator("#copy").getAttribute("class"), "");
    await t.page.getByRole("button", { name: "Cancel request" }).click();
    assert.equal(sent(req, t.posts).get("result"), "cancelled");
    await t.close();
  });

  it("says a browser without X25519 cannot encrypt the answer, and writes nothing", async () => {
    const req = request("a");
    const t = await load(req.url, { x25519: 0 });
    await t.page.getByRole("alert").filter({
      hasText: "This browser cannot complete the request.",
    }).waitFor();
    assert.match(await text(t.page, "[role=alert]"), /To cancel or restart, return to the machine where you started\.$/);
    assert.equal(await button(t.page).count(), 0);
    assert.equal(await t.page.getByRole("button", { name: "Cancel request" }).count(), 0);
    assert.ok(await visible(t.page, "Copy link"));
    assert.equal(await t.page.locator("#summary").isVisible(), false);
    assert.equal(await t.page.locator("#safety").isVisible(), false);
    assert.equal(await t.page.locator("#copy").getAttribute("class"), "");
    assert.equal(t.posts.length, 0);
    assert.equal(await t.calls(), 0);
    await t.close();
  });

  it("gives one recovery when passkeys and encryption are both unavailable", async () => {
    const t = await load(request("a").url, { webauthn: false, x25519: 0 });
    await t.page.locator("#intro.blocked").waitFor();
    assert.equal(await text(t.page, "h1"), "This browser cannot complete the request.");
    assert.equal(await text(t.page, "#explain"), "Open this link in a supported browser. To cancel or restart, return to the machine where you started.");
    assert.equal(await t.page.getByRole("button", { name: "Cancel request" }).count(), 0);
    assert.equal(await t.page.locator("#summary").isVisible(), false);
    await t.page.getByText("Request details", { exact: true }).click();
    assert.equal(await text(t.page, "#machine"), "buildbox");
    assert.equal(t.posts.length, 0);
    await t.close();
  });

  for (const op of ["c", "a", "r"]) {
    it(`moves the live instruction above details for o=${op}`, async () => {
      const t = await load(request(op).url, { credential: "hang" });
      await t.page.locator("#go:enabled").click();
      assert.equal(await text(t.page, "#intro #progress"), op === "c" ? "Create the passkey in your passkey manager." : "Choose your fleet passkey in the passkey prompt.");
      assert.equal(await t.page.locator("#compare").isVisible(), false);
      assert.equal(await t.page.locator("#safety").isVisible(), false);
      assert.ok(await visible(t.page, "Cancel request"));
      await t.close();
    });
  }

  it("copies the link", async () => {
    const req = request("a");
    const t = await load(req.url, { x25519: 0 });
    await t.page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    await t.page.getByRole("button", { name: "Copy link" }).click();
    assert.equal(await t.page.evaluate(() => navigator.clipboard.readText()), req.url);
    await t.close();
  });

  const prf = [
    ["reports PRF", "true"],
    ["cannot report PRF: no key", "absent"],
    ["cannot report PRF: not a boolean", "odd"],
    ["cannot report PRF: no method", "none"],
    ["cannot report PRF: it throws", "throws"],
    ["cannot report PRF: no answer in time", "hangs"],
  ];
  for (const [what, caps] of prf) {
    it(`lets a browser that ${what} approve`, async () => {
      const t = await load(request("a").url, { caps });
      const copy = caps === "true" ? "Browser PRF support detected. Your selected passkey provider must support PRF too."
        : "This browser cannot confirm PRF support in advance. You can try; beam will check the selected passkey’s result.";
      await t.page.waitForFunction((copy) => document.getElementById("capability").textContent === copy, copy, { timeout: 6000 });
      assert.ok(await button(t.page).isEnabled());
      assert.equal(await t.page.getByRole("button", { name: "Try anyway" }).count(), 0);
      await t.close();
    });
  }

  it("holds back approval in a browser that reports no PRF, until Try anyway", async () => {
    const req = request("a");
    const t = await load(req.url, { caps: "false", passkey: true });
    await t.page.locator("#problem").filter({ hasText: "This browser reports that the required PRF feature is unavailable. Your passkey provider may still support it. You can try; beam will check the result." }).waitFor();
    assert.equal(await button(t.page).count(), 0);
    assert.ok(await t.page.locator("details#compat").isVisible());
    assert.equal(await t.page.locator("details#compat").evaluate((d) => d.open), false);
    assert.equal(await text(t.page, "#anyway-note"), "Or copy this link to another browser.");
    assert.equal(await t.calls(), 0);
    assert.equal(t.posts.length, 0, "a false preflight writes nothing by itself");
    await t.page.getByRole("button", { name: "Try anyway" }).click();
    assert.deepEqual(await settle(t.page), ["Approval sent.", "Return to buildbox to check the result."]);
    assert.equal(sent(req, t.posts).get("result"), "ok");
    assert.equal(await t.page.getByRole("alert").count(), 0, "the preflight's alert is gone once overridden");
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
      "Return to the machine where you started."]);
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
    assert.equal(await t.page.locator("#intro").isVisible(), false);
    assert.equal(await t.page.locator("#summary").isVisible(), false);
    await t.page.getByText("Request details", { exact: true }).click();
    assert.ok(await t.page.locator("#summary").isVisible());
    await t.close();
  });

  for (const [o, heading, body] of [
    ["a", "Approval sent.", "Return to buildbox to check the result."],
    ["r", "Revocation approval sent.", "Return to the machine where you started to check that removal completed."],
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

  it("qualifies the fleet check and separates handoff from completion", async () => {
    const t = await load(request("a").url, { passkey: true });
    await t.page.getByRole("button", { name: "Authorize machine" }).click();
    await settle(t.page);
    assert.equal(await text(t.page, "#result-body"), "Return to buildbox to check the result.");
    assert.equal(await text(t.page, "#result-secondary"), "If you joined an existing fleet, compare its fleet fingerprint with a machine already in that fleet. This page cannot confirm completion.");
    assert.equal(await text(t.page, "#result-close"), "You can close this tab.");
    await t.close();
  });

  it("reports prf-unsupported when a create leaves PRF disabled", async () => {
    const req = request("c");
    const t = await load(req.url, { prf: false });
    await t.page.getByRole("button", { name: "Create passkey" }).click();
    assert.deepEqual(await settle(t.page), ["This passkey could not complete the request.",
      "Return to the machine where you started and request a new link. Try a browser and passkey provider that support beam's required PRF feature."]);
    assert.equal(await text(t.page, "#result-extra"), "A passkey may have been saved, but fleet creation is not complete.");
    assert.ok(await t.page.locator("details#compat").isVisible());
    assert.ok(await t.page.locator("details#compat").evaluate((d) => d.open));
    assert.match(await text(t.page, "#technical-text"), /prf\.enabled/);
    assert.equal(sent(req, t.posts).get("result"), "prf-unsupported");
    await t.close();
  });

  it("reports prf-unsupported when a get returns no PRF output", async () => {
    const req = request("r");
    const t = await load(req.url, { prf: false, passkey: true });
    await t.page.getByRole("button", { name: "Authorize removal" }).click();
    assert.equal((await settle(t.page))[0], "This passkey could not complete the request.");
    assert.equal(await text(t.page, "#result-extra"), "Use the same fleet passkey.");
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
    assert.equal((await settle(t.page))[0], "This passkey could not complete the request.");
    assert.equal(sent(req, t.posts).get("result"), "prf-unsupported");
    await t.close();
  });

  it("cancels the passkey call under way, and writes cancelled once", async () => {
    const req = request("a");
    const t = await load(req.url, { credential: "hang" });
    await t.page.getByRole("button", { name: "Authorize machine" }).click();
    await t.page.getByRole("status").filter({ hasText: "Choose your fleet passkey in the passkey prompt." }).waitFor();
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

  for (const [what, credential, detail] of [["another exception", "throw:InvalidStateError", /InvalidStateError/], ["no credential", "null", /TypeError/]]) {
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
    ["ok", { passkey: true }],
    ["prf-unsupported", { passkey: true, prf: false }],
    ["cancelled", { credential: "throw:NotAllowedError" }],
    ["failed", { credential: "throw:UnknownError" }],
  ];
  const deliveries = [
    [409, "This request was already answered.",
      "Another device, or an earlier attempt on this device, sent the first answer. Check the originating machine. If you did not approve that answer and it enrolled, reset the fleet on that machine. A reset does not undo a completed revocation."],
    ["abort", "Could not confirm delivery.",
      "Check the originating machine before starting again: it may have received your answer. If it is still waiting, cancel there and start a fresh request."],
    [429, "Too many requests.",
      "beam did not accept this answer. Wait a minute, then cancel the pending request on the originating machine and start again."],
    [500, "beam could not accept the response.", "Return to the machine where you started. If the request is still waiting, cancel it there, then start a fresh request."],
    [404, "beam could not accept the response.", "Return to the machine where you started. If the request is still waiting, cancel it there, then start a fresh request."],
  ];
  // Each result against the slot answers that must not be read as it (a
  // 409, no response); the others need only one.
  for (const [code, o] of results) {
    for (const [slot, heading, body] of deliveries.filter(([slot]) => slot === 409 || slot === "abort" || code === "ok")) {
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

  it("gives up on a write that hangs, as one without a response", async () => {
    const req = request("a");
    const t = await load(req.url, { passkey: true, slot: "hang" });
    await t.page.getByRole("button", { name: "Authorize machine" }).click();
    await t.page.locator("#result:not([hidden])").waitFor({ timeout: 25000 });
    assert.equal(await text(t.page, "#result-heading"), "Could not confirm delivery.");
    await t.close();
  });
});
