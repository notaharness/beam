// Capture the real page, never a mockup: isolated virtual WebAuthn + intercepted slots.
// Run from worker/: node test/page-screenshots.mjs [output-directory]
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { startBrowser, stopBrowser, load, request } from "./page-harness.mjs";

const out = resolve(process.argv[2] ?? "../docs/review/guided-fleet-browser/screenshots");
const click = async (t, id) => t.page.locator(id).click();
const ready = async (t) => t.page.waitForFunction(() => !document.getElementById("go").disabled);
const result = async (t) => t.page.locator("#result:not([hidden])").waitFor();
const approve = async (t) => { await ready(t); await click(t, "#go"); await result(t); };
const disclose = (name) => async (t) => t.page.getByText(name, { exact: true }).click();
const cases = [
  { name: "start", url: "https://beam.n10.is/" },
  { name: "start-join", url: "https://beam.n10.is/", act: disclose("Joining an existing fleet?") },
  { name: "start-app", url: "https://beam.n10.is/", act: disclose("Using an app with beam?") },
  { name: "invalid", url: "https://beam.n10.is/#invalid" },
  ...["c", "a", "r"].map((op) => ({ name: `ready-${op}`, op, act: ready })),
  { name: "checking-browser", options: { preflight: "hang" }, act: async (t) => t.page.getByText("Checking this browser…", { exact: true }).waitFor() },
  { name: "compatibility", options: { caps: "true" }, act: async (t) => { await ready(t); await disclose("Passkey compatibility")(t); } },
  { name: "prf-unknown", options: { caps: "absent" }, act: async (t) => { await ready(t); await disclose("Passkey compatibility")(t); } },
  { name: "prf-reported-false", options: { caps: "false" }, act: async (t) => t.page.locator("#anyway").waitFor() },
  { name: "no-webauthn", options: { webauthn: false }, act: async (t) => t.page.locator("#problem:not([hidden])").waitFor() },
  { name: "no-encryption", options: { x25519: 0 }, act: async (t) => t.page.locator("#problem:not([hidden])").waitFor() },
  { name: "no-webauthn-or-encryption", options: { webauthn: false, x25519: 0 }, act: async (t) => t.page.locator("#problem:not([hidden])").waitFor() },
  ...["c", "a", "r"].map((op) => ({ name: `waiting-${op}`, op, options: { credential: "hang" }, act: async (t) => { await ready(t); await click(t, "#go"); await t.page.waitForFunction(() => document.getElementById("progress").textContent.startsWith("Waiting")); } })),
  ...["c", "a", "r"].map((op) => ({ name: `sending-${op}`, op, options: { slot: "hang", passkey: op !== "c" }, act: async (t) => { await ready(t); await click(t, "#go"); await t.page.getByText("Sending the encrypted answer to your machine…", { exact: true }).waitFor(); } })),
  ...["c", "a", "r"].map((op) => ({ name: `sent-${op}`, op, options: { passkey: op !== "c" }, act: approve })),
  ...["c", "a", "r"].map((op) => ({ name: `prf-unsupported-${op}`, op, options: { passkey: op !== "c", prf: false }, act: approve })),
  { name: "cancelled", options: { credential: "throw:NotAllowedError" }, act: approve },
  { name: "failed", options: { credential: "throw:InvalidStateError" }, act: approve },
  { name: "failed-details", options: { credential: "null" }, act: async (t) => { await approve(t); await disclose("Technical details")(t); } },
  { name: "encryption-failed", options: { passkey: true, x25519: 1 }, act: approve },
  ...[409, 429, 400, 404, 413, 500].map((slot) => ({ name: `delivery-${slot}`, options: { passkey: true, slot }, act: approve })),
  { name: "delivery-unknown", options: { passkey: true, slot: "abort" }, act: approve },
  { name: "sent-request-details", options: { passkey: true }, act: async (t) => { await approve(t); await disclose("Request details")(t); } },
  { name: "sent-technical-details", options: { passkey: true }, act: async (t) => { await approve(t); await disclose("Technical details")(t); } },
  { name: "copied", act: async (t) => { await ready(t); await t.page.context().grantPermissions(["clipboard-read", "clipboard-write"]); await click(t, "#copy"); await t.page.getByText("Link copied.", { exact: true }).waitFor(); } },
  { name: "copy-denied", options: { clipboard: false }, act: async (t) => { await ready(t); await click(t, "#copy"); await t.page.getByText("Copy the link from the address bar.", { exact: true }).waitFor(); } },
  { name: "long-name", extra: { l: "Long machine name with <text> & Unicode — ".repeat(1) + "界".repeat(20) }, act: ready },
];

await mkdir(out, { recursive: true });
await startBrowser();
try {
  for (const [size, viewport] of [["desktop", { width: 1280, height: 900 }], ["phone", { width: 390, height: 844 }]]) {
    for (const scheme of ["light", "dark"]) {
      for (const spec of cases) {
        const req = spec.url ?? request(spec.op ?? "a", spec.extra).url;
        const t = await load(req, { scheme, viewport, caps: "true", ...spec.options });
        try {
          await spec.act?.(t);
          assert.equal(await t.page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${spec.name}: horizontal overflow`);
          assert.deepEqual(await t.page.evaluate(() => window.__violations), [], `${spec.name}: CSP violation`);
          assert.doesNotMatch(await t.page.locator("body").innerText(), /n10 Desktop|n10 Fleet|connected anywhere/);
          await t.page.screenshot({ path: resolve(out, `${spec.name}-${size}-${scheme}.png`), fullPage: true });
        } finally { await t.close(); }
      }
      console.log(`${size}/${scheme}: ${cases.length} states captured`);
    }
  }
} finally { await stopBrowser(); }
const groups = cases.map(({ name }) => `| ${name} | ${["desktop-light", "desktop-dark", "phone-light", "phone-dark"].map((variant) => `[${variant}](${name}-${variant}.png)`).join(" | ")} |`);
await writeFile(resolve(out, "README.md"), `# Browser state captures\n\nActual worker asset under its CSP; virtual passkeys and intercepted slot responses. No live daemon or service.\n\nReproduce from worker/: \`node test/page-screenshots.mjs\`. c = create, a = authorize, r = revoke.\n\n${cases.length} distinct visible states, each at 1280px and 390px in light/dark (${cases.length * 4} PNGs). Equivalent errors (for example cancelled via the button vs NotAllowedError) share a frame; protocol combinations are exercised by flows.test.mjs.\n\n| State | Desktop light | Desktop dark | Phone light | Phone dark |\n| --- | --- | --- | --- | --- |\n${groups.join("\n")}\n`);
console.log(`Captured ${cases.length * 4} screenshots.`);
