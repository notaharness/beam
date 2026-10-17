import { env } from "cloudflare:workers";
import { isoBase64URL } from "@simplewebauthn/server/helpers";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import schema from "../schema.sql?raw";
import worker from "../src/index";
import { Authenticator } from "./authenticator";

const b64 = (b: Uint8Array) => isoBase64URL.fromBuffer(new Uint8Array(b));
const random = (n: number) => crypto.getRandomValues(new Uint8Array(n));

beforeAll(async () => {
  await env.DB.batch(schema.split(";").filter((s) => s.trim()).map((s) => env.DB.prepare(s)));
});

afterEach(() => {
  vi.restoreAllMocks();
});

// call is one request from an address of its own, so rates do not add up
// across tests.
function call(method: string, path: string, body?: unknown, token?: Uint8Array | string, ip = crypto.randomUUID()): Promise<Response> {
  const headers: Record<string, string> = { "CF-Connecting-IP": ip, "Content-Type": "application/json" };
  if (token !== undefined) headers.Authorization = "Bearer " + (typeof token === "string" ? token : b64(token));
  return worker.fetch(new Request("https://beam.n10.is" + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) }), env);
}

async function registration() {
  const auth = await Authenticator.create();
  const hash = random(32);
  const challenge = new Uint8Array(await crypto.subtle.digest("SHA-256", new Uint8Array([...new TextEncoder().encode("beam-member:v1"), ...hash])));
  const first = { statementHash: b64(hash), blob: b64(random(100)), assertion: await auth.assert(challenge) };
  return { credentialId: auth.credentialId, credentialPublicKey: b64(auth.cose), readToken: b64(random(32)), first };
}

// docs/09 "API responses": no cache keeps a directory page or a slot, and no
// browser sniffs one into something other than JSON.
describe("docs/09 API responses", () => {
  it("are no-store and nosniff, whatever the answer", async () => {
    const reg = await registration();
    const slot = b64(random(16));
    const responses = [
      await call("POST", "/v1/fleets", reg),
      await call("POST", "/v1/fleets", reg),
      await call("GET", "/v1/entries", undefined, random(32)),
      await call("POST", "/v1/fleets/" + "0".repeat(64) + "/entries", { kind: "member" }),
      await call("POST", "/v1/slots/" + slot, { sealed: b64(random(10)) }),
      await call("GET", "/v1/nothing"),
    ];
    expect(responses.map((r) => r.status)).toEqual([201, 409, 401, 404, 201, 404]);
    for (const r of responses) {
      expect(r.headers.get("Cache-Control")).toBe("no-store");
      expect(r.headers.get("X-Content-Type-Options")).toBe("nosniff");
    }
  });
});

// docs/09 "Logs": each refusal on a route is one structured line of a fixed
// set of fields, none of them a token, a slot or a body.
describe("docs/09 logs", () => {
  it("log each refusal by route, status and reason alone", async () => {
    const log = vi.spyOn(console, "log").mockImplementation(() => {});
    const token = random(32);
    const reg = await registration();
    const forged = { ...reg, first: { ...reg.first, statementHash: b64(random(32)) } };
    const slot = b64(random(16));
    const ip = crypto.randomUUID();
    await call("GET", "/v1/entries", undefined, token);
    await call("POST", "/v1/fleets", forged);
    for (let i = 0; i < 121; i++) await call("GET", "/v1/slots/" + slot, undefined, token, ip);
    await call("GET", "/v1/nothing");
    const lines = log.mock.calls.map((c) => c[0]);
    expect(lines[0]).toEqual({ event: "refused", route: "GET /v1/entries", status: 401, reason: "unauthorized" });
    expect(lines[1]).toEqual({ event: "refused", route: "POST /v1/fleets", status: 403, reason: "bad-assertion" });
    expect(lines.at(-1)).toEqual({ event: "refused", route: "GET /v1/slots/:slot", status: 429, reason: "rate-limited" });
    expect(lines.length).toBe(123); // an unknown route is not logged
    const all = JSON.stringify(lines);
    for (const secret of [b64(token), slot, reg.readToken, reg.first.assertion.signature]) expect(all).not.toContain(secret);
  });
});
