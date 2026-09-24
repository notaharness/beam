// The browser half of internal/ceremony's TestBrowserCeremony (docs/10):
// Chromium with a virtual authenticator that does PRF, beam.n10.is served
// from public/ with its _headers, its slot route forwarded to the worker
// named by the first argument, and each ceremony URL read from stdin opened
// and approved. It prints "done" once the page has written its sealed result,
// and for a get been refused a second.
import { readFile } from "node:fs/promises";
import { createInterface } from "node:readline";
import { chromium } from "playwright";

const worker = process.argv[2];
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
await page.route("https://beam.n10.is/**", async (route) => {
  const url = new URL(route.request().url());
  if (url.pathname === "/") return route.fulfill({ status: 200, contentType: "text/html; charset=utf-8", headers, body });
  const response = await route.fetch({ url: worker + url.pathname });
  return route.fulfill({ response });
});
page.on("console", (m) => m.type() === "error" && console.error("page:", m.text()));

// approve opens url afresh, approves, and waits for the page to say want.
async function approve(url, want) {
  await page.goto("about:blank"); // a URL that differs only in its fragment would not load the page again
  await page.goto(url);
  await page.click("#go");
  await page.locator("#status").filter({ hasText: want }).waitFor();
}

// Each ceremony is answered once. A get is then answered again, as a second
// device would: the slot is taken, and the page says so. (A second create
// would leave the authenticator a credential the later get might pick.)
for await (const url of createInterface({ input: process.stdin })) {
  await approve(url, "Done.");
  if (new URLSearchParams(new URL(url).hash.slice(1)).get("o") !== "c") {
    await approve(url, "already answered from another device");
  }
  console.log("done");
}
await browser.close();
