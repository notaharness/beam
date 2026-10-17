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

// register asks for a fleet under cose, its first entry signed by auth.
async function register(auth: Authenticator, cose: Uint8Array): Promise<Response> {
  const hash = random(32);
  const challenge = new Uint8Array(await crypto.subtle.digest("SHA-256", new Uint8Array([...new TextEncoder().encode("beam-member:v1"), ...hash])));
  const body = { credentialId: auth.credentialId, credentialPublicKey: b64(cose), readToken: b64(random(32)), first: { statementHash: b64(hash), blob: b64(random(100)), assertion: await auth.assert(challenge) } };
  return worker.fetch(new Request("https://beam.n10.is/v1/fleets", { method: "POST", headers: { "CF-Connecting-IP": crypto.randomUUID() }, body: JSON.stringify(body) }), env);
}

// docs/02 "fleetId": a new fleet's key is the one COSE encoding CTAP2 gives
// it, so one public key and one signature register one fleet. Each vector
// re-encodes the same ES256 key, which the assertion verifies under.
describe("docs/02 fleetId", () => {
  it("registers a key in CTAP2's encoding alone", async () => {
    const auth = await Authenticator.create();
    const k = auth.cose;
    const encodings: [string, Uint8Array][] = [
      ["entries reordered", new Uint8Array([k[0], k[3], k[4], k[1], k[2], ...k.slice(5)])],
      ["an entry more", new Uint8Array([0xa6, ...k.slice(1), 0x04, 0x00])],
      ["an integer not minimal", new Uint8Array([0xa5, 0x01, 0x18, 0x02, ...k.slice(3)])],
    ];
    for (const [what, cose] of encodings) {
      const res = await register(auth, cose);
      expect([what, res.status, await res.json()]).toEqual([what, 400, { error: "params" }]);
    }
    expect((await register(auth, k)).status).toBe(201);
  });

  // The worker test authenticator signs with ES256 alone: a canonical
  // Ed25519 key passes the encoding check and fails only its assertion.
  it("takes an Ed25519 key in CTAP2's encoding as far as its assertion", async () => {
    const auth = await Authenticator.create();
    const ed25519 = new Uint8Array([0xa4, 0x01, 0x01, 0x03, 0x27, 0x20, 0x06, 0x21, 0x58, 0x20, ...random(32)]);
    const res = await register(auth, ed25519);
    expect([res.status, await res.json()]).toEqual([403, { error: "bad-assertion" }]);
  });
});
