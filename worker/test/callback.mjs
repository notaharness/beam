// The browser half of internal/ceremony's TestBrowserCallback (docs/10):
// Chromium with a virtual authenticator that does PRF, beam.n10.is served
// from public/ with its _headers, and each ceremony URL read from stdin
// opened and approved. It prints "done" once /cb has handed the result on.
import { readFile } from "node:fs/promises";
import { createInterface } from "node:readline";
import { chromium } from "playwright";

const pub = new URL("../public/", import.meta.url);
const body = await readFile(new URL("index.html", pub), "utf8");
const headers = Object.fromEntries(
  (await readFile(new URL("_headers", pub), "utf8"))
    .split("\n")
    .filter((l) => /^\s+\S/.test(l))
    .map((l) => [l.slice(0, l.indexOf(":")).trim(), l.slice(l.indexOf(":") + 1).trim()]),
);

const browser = await chromium.launch();
const page = await browser.newPage();
const cdp = await page.context().newCDPSession(page);
await cdp.send("WebAuthn.enable");
await cdp.send("WebAuthn.addVirtualAuthenticator", {
  options: {
    protocol: "ctap2",
    transport: "internal",
    hasResidentKey: true,
    hasUserVerification: true,
    isUserVerified: true,
    hasPrf: true,
    automaticPresenceSimulation: true,
  },
});
await page.route("https://beam.n10.is/**", (route) =>
  route.fulfill({ status: 200, contentType: "text/html; charset=utf-8", headers, body }),
);
page.on("console", (m) => m.type() === "error" && console.error("page:", m.text()));

for await (const url of createInterface({ input: process.stdin })) {
  await page.goto(url);
  await page.click("#go");
  await page.waitForURL(/^http:\/\/127\.0\.0\.1:\d+\/cb/);
  await page.locator("#out").filter({ hasText: "done, close this tab" }).waitFor();
  console.log("done");
}
await browser.close();
