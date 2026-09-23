import { env, exports } from "cloudflare:workers";
import { isoBase64URL } from "@simplewebauthn/server/helpers";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import schema from "../schema.sql?raw";
import worker from "../src/index";

const b64 = (b: Uint8Array) => isoBase64URL.fromBuffer(new Uint8Array(b));
const random = (n: number) => crypto.getRandomValues(new Uint8Array(n));

beforeAll(async () => {
  await env.DB.batch(schema.split(";").filter((s) => s.trim()).map((s) => env.DB.prepare(s)));
});

afterEach(() => {
  vi.useRealTimers();
});

// slot is a daemon's read key and the slot it names: SHA-256(readKey)[0:16].
async function slot() {
  const key = random(32);
  return { key, id: b64(new Uint8Array(await crypto.subtle.digest("SHA-256", key)).slice(0, 16)) };
}

// Each test speaks from an address of its own, so rates do not add up across
// tests. A client in this isolate (inline) sees its fake timers; the others go
// through a request of their own, as a real one does.
function client(inline = false, ip = crypto.randomUUID()) {
  const send = (method: string, path: string, init: { body?: unknown; key?: Uint8Array | string } = {}) => {
    const headers: Record<string, string> = { "CF-Connecting-IP": ip, "Content-Type": "application/json" };
    if (init.key !== undefined) headers.Authorization = "Bearer " + (typeof init.key === "string" ? init.key : b64(init.key));
    const body = init.body === undefined ? undefined : typeof init.body === "string" ? init.body : JSON.stringify(init.body);
    const req = new Request("https://beam.n10.is" + path, { method, headers, body });
    return inline ? worker.fetch(req, env) : exports.default.fetch(req);
  };
  return {
    write: (id: string, sealed: unknown) => send("POST", `/v1/slots/${id}`, { body: { sealed } }),
    read: (id: string, key?: Uint8Array | string) => send("GET", `/v1/slots/${id}`, { key }),
    send,
  };
}

async function answer(res: Response) {
  return [res.status, res.status === 204 ? null : await res.json()];
}

describe("docs/09 slots", () => {
  it("hands one write to one read, and stays taken after it", async () => {
    const c = client();
    const s = await slot();
    const sealed = b64(random(300));
    expect(await answer(await c.write(s.id, sealed))).toEqual([201, {}]);
    expect(await answer(await c.write(s.id, b64(random(300))))).toEqual([409, { error: "answered" }]);
    expect(await answer(await c.read(s.id, s.key))).toEqual([200, { sealed }]);
    expect(await answer(await c.read(s.id, s.key))).toEqual([410, { error: "read" }]);
    expect(await answer(await c.write(s.id, sealed))).toEqual([409, { error: "answered" }]);
    const row = await env.DB.prepare("SELECT sealed FROM slots WHERE slot_id = ?").bind(s.id).first();
    expect(row).toEqual({ sealed: null });
  });

  it("opens a slot only to the key it hashes from", async () => {
    const c = client();
    const s = await slot();
    await c.write(s.id, b64(random(10)));
    const bodies = new Set<string>();
    for (const key of [random(32), s.key.slice(0, 16), "not base64!", undefined]) {
      const res = await c.read(s.id, key);
      expect(res.status).toBe(401);
      bodies.add(await res.text());
    }
    expect(bodies.size).toBe(1);
    expect((await c.read(s.id, s.key)).status).toBe(200);
  });

  it("holds a read until the write arrives", async () => {
    const c = client();
    const s = await slot();
    const sealed = b64(random(64));
    const reading = c.read(s.id, s.key);
    await new Promise((ok) => setTimeout(ok, 600));
    expect((await c.write(s.id, sealed)).status).toBe(201);
    expect(await answer(await reading)).toEqual([200, { sealed }]);
  });

  it("answers 204 after 25 seconds without a write", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "Date"] });
    const c = client(true);
    const s = await slot();
    let done = false;
    const reading = c.read(s.id, s.key).then((r) => { done = true; return r; });
    await vi.advanceTimersByTimeAsync(24_000);
    expect(done).toBe(false);
    await vi.advanceTimersByTimeAsync(2_000);
    expect((await reading).status).toBe(204);
  });

  it("forgets a slot five minutes after its write", async () => {
    const c = client(true);
    const old = await slot();
    const sealed = random(20);
    await env.DB.prepare("INSERT INTO slots (slot_id, sealed, created_at) VALUES (?, ?, ?)").bind(old.id, sealed, Date.now() - 5 * 60_000 - 1).run();
    vi.useFakeTimers({ toFake: ["setTimeout", "Date"] });
    const reading = c.read(old.id, old.key);
    await vi.advanceTimersByTimeAsync(26_000);
    expect((await reading).status).toBe(204);
    vi.useRealTimers();
    const fresh = await slot();
    expect((await c.write(fresh.id, b64(random(10)))).status).toBe(201);
    expect(await env.DB.prepare("SELECT count(*) AS n FROM slots WHERE slot_id = ?").bind(old.id).first("n")).toBe(0);
    expect((await c.write(old.id, b64(random(10)))).status).toBe(201);
  });

  it("caps a sealed result at 8 KiB and a body at 16 KiB", async () => {
    const c = client();
    expect((await c.write((await slot()).id, b64(random(8192)))).status).toBe(201);
    expect(await answer(await c.write((await slot()).id, b64(random(8193))))).toEqual([413, { error: "sealed-too-large" }]);
    const res = await c.send("POST", `/v1/slots/${(await slot()).id}`, { body: { sealed: "A", padding: "x".repeat(16384) } });
    expect(await answer(res)).toEqual([413, { error: "too-large" }]);
  });

  it("refuses a malformed slot or result", async () => {
    const c = client();
    for (const id of ["short", "A".repeat(23), "A".repeat(21) + "!"]) {
      expect((await c.write(id, b64(random(10)))).status).toBe(404);
    }
    const s = await slot();
    for (const sealed of ["", "not base64!", 42, undefined]) {
      expect((await c.write(s.id, sealed)).status).toBe(400);
    }
    expect((await c.send("POST", `/v1/slots/${s.id}`, { body: "{" })).status).toBe(400);
    expect((await c.write(s.id, b64(random(10)))).status).toBe(201);
  });

  it("allows 120 requests a minute per client address", async () => {
    const c = client();
    const codes = [];
    for (let i = 0; i < 121; i++) codes.push((await c.write((await slot()).id, b64(random(10)))).status);
    expect(codes.slice(0, 119).every((code) => code === 201)).toBe(true);
    expect(codes[120]).toBe(429);
    expect((await client().write((await slot()).id, b64(random(10)))).status).toBe(201);
  });
});
