import { describe, expect, it } from "vitest";
import headers from "../public/_headers?raw";
import page from "../public/index.html?raw";

async function hash(s: string): Promise<string> {
  const d = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)));
  return "sha256-" + btoa(String.fromCharCode(...d));
}

function only(tag: string): string {
  const all = [...page.matchAll(new RegExp(`<${tag}[^>]*>([\\s\\S]*?)</${tag}>`, "g"))];
  expect(all).toHaveLength(1);
  return all[0][1];
}

// docs/09 Ceremony page headers: the policy allows exactly the page's one
// inline script and one inline style, and a connection to the slot route
// alone.
describe("the ceremony page", () => {
  it("is served under a policy hashing its script and style", async () => {
    const csp = headers.match(/Content-Security-Policy: (.*)/)?.[1];
    expect(csp).toBe(`default-src 'none'; script-src '${await hash(only("script"))}'; style-src '${await hash(only("style"))}'; connect-src https://beam.n10.is/v1/slots/`);
    expect(headers).toMatch(/^\/\*\n/);
    for (const h of ["Referrer-Policy: no-referrer", "X-Frame-Options: DENY", "Cache-Control: public, max-age=300"]) {
      expect(headers).toContain(h);
    }
  });

  it("writes no markup from its script: names from the fragment are text", () => {
    expect(only("script")).not.toMatch(/innerHTML|outerHTML|insertAdjacentHTML|document\.write|createContextualFragment/);
  });

  it("loads nothing", () => {
    expect(page).not.toMatch(/\bsrc=|<link\b|@import|url\((?!#)/);
    // The one href is a navigation, not a fetch: the repository link in the footer.
    expect([...page.matchAll(/\bhref="([^"]*)"/g)].map((m) => m[1])).toEqual(["https://github.com/notaharness/beam"]);
  });
});
