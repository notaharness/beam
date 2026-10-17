import { env } from "cloudflare:workers";
import { isoBase64URL } from "@simplewebauthn/server/helpers";
import { beforeAll, describe, expect, it } from "vitest";
import schema from "../schema.sql?raw";
import worker from "../src/index";
import { Authenticator } from "./authenticator";

const b64 = (b: Uint8Array) => isoBase64URL.fromBuffer(new Uint8Array(b));
const random = (n: number) => crypto.getRandomValues(new Uint8Array(n));

beforeAll(async () => {
  await env.DB.batch(schema.split(";").filter((s) => s.trim()).map((s) => env.DB.prepare(s)));
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
