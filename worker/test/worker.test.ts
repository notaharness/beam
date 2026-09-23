import { env, exports } from "cloudflare:workers";
import { isoBase64URL } from "@simplewebauthn/server/helpers";
import { beforeAll, describe, expect, it } from "vitest";
import schema from "../schema.sql?raw";
import worker from "../src/index";
import { Authenticator } from "./authenticator";

const b64 = (b: Uint8Array) => isoBase64URL.fromBuffer(new Uint8Array(b));
const sha256 = async (b: Uint8Array) => new Uint8Array(await crypto.subtle.digest("SHA-256", new Uint8Array(b)));
const hex = (b: Uint8Array) => [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
const random = (n: number) => crypto.getRandomValues(new Uint8Array(n));

beforeAll(async () => {
  await env.DB.batch(schema.split(";").filter((s) => s.trim()).map((s) => env.DB.prepare(s)));
});

function call(method: string, path: string, body?: unknown, token?: Uint8Array | string): Promise<Response> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token !== undefined) headers.Authorization = "Bearer " + (typeof token === "string" ? token : b64(token));
  return exports.default.fetch(new Request("https://beam.n10.is" + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) }));
}

// fleet is an owner's passkey and read token.
async function fleet() {
  const auth = await Authenticator.create();
  const token = random(32);
  return { auth, token, id: hex(await sha256(auth.cose)) };
}

// entry is a statement of kind signed by auth: its hash, a blob and the
// assertion over SHA-256("beam-<kind>:v1" ‖ hash).
async function entry(auth: Authenticator, kind: string, blob = random(200), flags?: number) {
  const hash = random(32);
  const challenge = await sha256(new Uint8Array([...new TextEncoder().encode(`beam-${kind}:v1`), ...hash]));
  return { kind, statementHash: b64(hash), blob: b64(blob), assertion: await auth.assert(challenge, flags) };
}

async function register(f: Awaited<ReturnType<typeof fleet>>, first?: Awaited<ReturnType<typeof entry>>) {
  first ??= await entry(f.auth, "member");
  return call("POST", "/v1/fleets", { credentialId: f.auth.credentialId, credentialPublicKey: b64(f.auth.cose), readToken: b64(f.token), first });
}

describe("docs/09 routes", () => {
  it("creates a fleet once and reads it by its token alone", async () => {
    const f = await fleet();
    const res = await register(f);
    expect(res.status).toBe(201);
    expect(await res.json()).toEqual({ fleetId: f.id });
    expect((await register(f)).status).toBe(409);
    const page: any = await (await call("GET", "/v1/entries?since=0", undefined, f.token)).json();
    expect(page.fleetId).toBe(f.id);
    expect(page.credentialId).toBe(f.auth.credentialId);
    expect(page.credentialPublicKey).toBe(b64(f.auth.cose));
    expect(page.entries.map((e: any) => e.seq)).toEqual([1]);
    expect(page.next).toBeUndefined();
  });

  it("answers every token that opens nothing alike", async () => {
    const bodies = new Set<string>();
    for (const token of [random(32), random(3), "not base64!", undefined]) {
      const res = await call("GET", "/v1/entries", undefined, token);
      expect(res.status).toBe(401);
      bodies.add(await res.text());
    }
    expect(bodies.size).toBe(1);
  });

  it("registers only a member statement its credential signed", async () => {
    const f = await fleet();
    const other = await fleet();
    for (const first of [await entry(f.auth, "revoke"), await entry(other.auth, "member")]) {
      const res = await call("POST", "/v1/fleets", { credentialId: f.auth.credentialId, credentialPublicKey: b64(f.auth.cose), readToken: b64(f.token), first });
      expect(res.status).toBe(403);
    }
  });

  it("appends a signed record of its kind, once", async () => {
    const f = await fleet();
    await register(f);
    const member = await entry(f.auth, "member");
    const append = (e: unknown, id = f.id) => call("POST", `/v1/fleets/${id}/entries`, e);
    let res = await append(member);
    expect([res.status, await res.json()]).toEqual([201, { seq: 2 }]);
    res = await append(await entry(f.auth, "revoke"));
    expect([res.status, await res.json()]).toEqual([201, { seq: 3 }]);
    res = await append(member);
    expect([res.status, await res.json()]).toEqual([200, { seq: 2 }]);
    expect((await append({ ...(await entry(f.auth, "revoke")), kind: "member" })).status).toBe(403);
    expect((await append(await entry((await fleet()).auth, "member"))).status).toBe(403);
    expect((await append(await entry(f.auth, "member", random(8193)))).status).toBe(413);
    expect((await append(await entry(f.auth, "member", random(10), 0x01))).status).toBe(403); // no user verification
    expect((await append({ ...member, kind: undefined })).status).toBe(400);
    expect((await append(member, (await fleet()).id)).status).toBe(404);
  });
});

describe("docs/09 caps", () => {
  async function seeded(n: number) {
    const base = await fleet();
    const f = { ...base, first: await entry(base.auth, "member") };
    await register(f, f.first);
    const rows = Array.from({ length: n - 1 }, (_, i) =>
      env.DB.prepare("INSERT INTO entries VALUES (?, ?, ?, ?, '{}', 0)").bind(f.id, i + 2, random(32), random(10)));
    for (let i = 0; i < rows.length; i += 500) await env.DB.batch(rows.slice(i, i + 500));
    return f;
  }

  it("pages at 500 entries", async () => {
    const f = await seeded(501);
    const first: any = await (await call("GET", "/v1/entries", undefined, f.token)).json();
    expect([first.entries.length, first.next]).toEqual([500, 500]);
    const rest: any = await (await call("GET", "/v1/entries?since=500", undefined, f.token)).json();
    expect(rest.entries.map((e: any) => e.seq)).toEqual([501]);
    expect(rest.next).toBeUndefined();
  });

  it("holds at most 5,000 entries per fleet", async () => {
    const f = await seeded(5000);
    expect((await call("POST", `/v1/fleets/${f.id}/entries`, await entry(f.auth, "member"))).status).toBe(413);
    const again = await call("POST", `/v1/fleets/${f.id}/entries`, f.first);
    expect([again.status, await again.json()]).toEqual([200, { seq: 1 }]);
  });

  // D1 answering slowly lets concurrent appends interleave anywhere between
  // their statements; the cap holds only if one statement checks and inserts.
  it("holds the cap against appends at once", async () => {
    const slow = new Proxy(env.DB, {
      get: (db, k) =>
        k !== "prepare" ? Reflect.get(db, k).bind(db) : (sql: string) => {
          const wrap = (s: D1PreparedStatement): D1PreparedStatement => new Proxy(s, {
            get: (st, m) => {
              const f = Reflect.get(st, m).bind(st);
              if (m === "bind") return (...a: unknown[]) => wrap(f(...a));
              return async (...a: unknown[]) => { const r = await f(...a); await new Promise((ok) => setTimeout(ok, 20)); return r; };
            },
          });
          return wrap(db.prepare(sql));
        },
    });
    const post = (id: string, e: unknown) =>
      worker.fetch(new Request(`https://beam.n10.is/v1/fleets/${id}/entries`, { method: "POST", body: JSON.stringify(e) }), { ...env, DB: slow });
    const count = async (id: string) => (await env.DB.prepare("SELECT count(*) AS n FROM entries WHERE fleet_id = ?").bind(id).first<number>("n"));

    const distinct = await seeded(4999);
    const entries = await Promise.all(Array.from({ length: 12 }, () => entry(distinct.auth, "member")));
    const codes = (await Promise.all(entries.map((e) => post(distinct.id, e)))).map((r) => r.status).sort();
    expect(codes).toEqual([201, ...Array(11).fill(413)]);
    expect(await count(distinct.id)).toBe(5000);

    const same = await seeded(4999);
    const e = await entry(same.auth, "member");
    const answers = await Promise.all(Array.from({ length: 6 }, async () => { const r = await post(same.id, e); return [r.status, await r.json()]; }));
    expect(answers.filter(([s]) => s === 201)).toEqual([[201, { seq: 5000 }]]);
    expect(answers.filter(([s]) => s !== 201)).toEqual(Array(5).fill([200, { seq: 5000 }]));
  });

  // A request body is refused past 16 KiB as it arrives, whatever its
  // Content-Length says, before anything parses it.
  it("reads at most 16 KiB of a request", async () => {
    const f = await fleet();
    const padded = { credentialId: f.auth.credentialId, credentialPublicKey: b64(f.auth.cose), readToken: b64(f.token), first: await entry(f.auth, "member"), padding: "x".repeat(2 << 20) };
    const text = JSON.stringify(padded);
    const stream = () => new Blob([text]).stream();
    for (const init of [
      { body: text },
      { body: stream() },
      { body: stream(), headers: { "Content-Length": "100" } },
    ] as RequestInit[]) {
      const res = await exports.default.fetch(new Request("https://beam.n10.is/v1/fleets", { method: "POST", ...init }));
      expect([res.status, await res.json()]).toEqual([413, { error: "too-large" }]);
    }
    expect((await call("GET", "/v1/entries", undefined, f.token)).status).toBe(401);
  });
});

describe("docs/09 rate", () => {
  it("allows 120 requests a minute per fleet", async () => {
    const f = await fleet();
    await register(f);
    const codes = [];
    for (let i = 0; i < 121; i++) codes.push((await call("GET", "/v1/entries", undefined, f.token)).status);
    expect(codes.slice(0, 119).every((c) => c === 200)).toBe(true);
    expect(codes[120]).toBe(429);
  });
});
