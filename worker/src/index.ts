// The beam directory (docs/02, docs/09): an append-only log of sealed records
// per fleet. Appends carry a passkey assertion over the record's statement
// hash, verified here; reads carry the fleet's read token. The ceremony page
// is a static asset.
import { verifyAuthenticationResponse } from "@simplewebauthn/server";
import { isoBase64URL } from "@simplewebauthn/server/helpers";

interface Env {
  DB: D1Database;
  LIMITER: RateLimit;
}

const RP_ID = "beam.n10.is";
const ORIGIN = "https://beam.n10.is";
const MAX_BLOB = 8192;
const MAX_ENTRIES = 5000;
const PAGE = 500;

class Refusal extends Error {
  constructor(
    readonly status: number,
    readonly reason: string,
  ) {
    super(reason);
  }
}

interface Assertion {
  credentialId: string;
  clientDataJSON: string;
  authenticatorData: string;
  signature: string;
}

interface Entry {
  kind?: string;
  statementHash: string;
  blob: string;
  assertion: Assertion;
}

interface Fleet {
  fleet_id: string;
  credential_id: string;
  credential_pk: ArrayBuffer;
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    const append = url.pathname.match(/^\/v1\/fleets\/([0-9a-f]{64})\/entries$/);
    try {
      if (req.method === "POST" && url.pathname === "/v1/fleets") return await register(env, await body(req));
      if (req.method === "GET" && url.pathname === "/v1/entries") return await read(env, req, url);
      if (req.method === "POST" && append) return await appendEntry(env, append[1], await body(req));
      throw new Refusal(404, "not-found");
    } catch (e) {
      if (e instanceof Refusal) return json(e.status, { error: e.reason });
      throw e;
    }
  },
} satisfies ExportedHandler<Env>;

// POST /v1/fleets: create a fleet with its first entry, a member statement
// signed by the credential it registers.
async function register(env: Env, req: any): Promise<Response> {
  const pk = bytes(req.credentialPublicKey);
  const token = bytes(req.readToken);
  if (typeof req.credentialId !== "string" || !req.credentialId || token.length !== 32 || typeof req.first !== "object") {
    throw new Refusal(400, "params");
  }
  const fleetId = hex(await sha256(pk));
  await limit(env, fleetId);
  const first = await verified(req.credentialId, pk, { ...req.first, kind: "member" });
  const now = Date.now();
  try {
    await env.DB.batch([
      env.DB.prepare("INSERT INTO fleets (fleet_id, credential_id, credential_pk, read_hash, created_at) VALUES (?, ?, ?, ?, ?)").bind(
        fleetId, req.credentialId, pk, await sha256(token), now),
      env.DB.prepare("INSERT INTO entries (fleet_id, seq, statement_hash, blob, assertion, created_at) VALUES (?, 1, ?, ?, ?, ?)").bind(
        fleetId, first.hash, first.blob, JSON.stringify(first.assertion), now),
    ]);
  } catch (e) {
    if (String(e).includes("UNIQUE")) throw new Refusal(409, "exists");
    throw e;
  }
  return json(201, { fleetId });
}

// GET /v1/entries?since=: the fleet the bearer token opens, from since on.
// Every token that opens nothing gets the same 401.
async function read(env: Env, req: Request, url: URL): Promise<Response> {
  const token = bytes((req.headers.get("Authorization") ?? "").replace(/^Bearer /, ""));
  const fleet = await env.DB.prepare("SELECT fleet_id, credential_id, credential_pk FROM fleets WHERE read_hash = ?")
    .bind(await sha256(token)).first<Fleet>();
  if (!fleet) throw new Refusal(401, "unauthorized");
  await limit(env, fleet.fleet_id);
  const since = Number(url.searchParams.get("since") ?? "0");
  if (!Number.isSafeInteger(since) || since < 0) throw new Refusal(400, "params");
  const { results } = await env.DB.prepare(
    "SELECT seq, statement_hash, blob, assertion FROM entries WHERE fleet_id = ? AND seq > ? ORDER BY seq LIMIT ?",
  ).bind(fleet.fleet_id, since, PAGE + 1).all<{ seq: number; statement_hash: ArrayBuffer; blob: ArrayBuffer; assertion: string }>();
  const page = results.slice(0, PAGE);
  return json(200, {
    fleetId: fleet.fleet_id,
    credentialId: fleet.credential_id,
    credentialPublicKey: b64(fleet.credential_pk),
    entries: page.map((r) => ({ seq: r.seq, statementHash: b64(r.statement_hash), blob: b64(r.blob), assertion: JSON.parse(r.assertion) })),
    ...(results.length > PAGE ? { next: page[PAGE - 1].seq } : {}),
  });
}

// POST /v1/fleets/:id/entries: append a record signed by the fleet's
// credential in the domain of its kind; appending one held already answers
// its seq.
async function appendEntry(env: Env, fleetId: string, req: any): Promise<Response> {
  if (req.kind !== "member" && req.kind !== "revoke") throw new Refusal(400, "params");
  const fleet = await env.DB.prepare("SELECT fleet_id, credential_id, credential_pk FROM fleets WHERE fleet_id = ?").bind(fleetId).first<Fleet>();
  if (!fleet) throw new Refusal(404, "no-fleet");
  await limit(env, fleetId);
  const e = await verified(fleet.credential_id, new Uint8Array(fleet.credential_pk), req);
  const held = () => env.DB.prepare("SELECT seq FROM entries WHERE fleet_id = ? AND statement_hash = ?").bind(fleetId, e.hash).first<number>("seq");
  let seq = await held();
  if (seq !== null) return json(200, { seq });
  const count = await env.DB.prepare("SELECT count(*) AS n FROM entries WHERE fleet_id = ?").bind(fleetId).first<number>("n");
  if ((count ?? 0) >= MAX_ENTRIES) throw new Refusal(413, "fleet-full");
  try {
    seq = await env.DB.prepare(
      `INSERT INTO entries (fleet_id, seq, statement_hash, blob, assertion, created_at)
       SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ? FROM entries WHERE fleet_id = ? RETURNING seq`,
    ).bind(fleetId, e.hash, e.blob, JSON.stringify(e.assertion), Date.now(), fleetId).first<number>("seq");
  } catch (err) {
    if (!String(err).includes("UNIQUE")) throw err;
    return json(200, { seq: await held() }); // a concurrent append of the same record
  }
  return json(201, { seq });
}

// verified checks an entry's shape and its assertion: by credentialId, under
// pk, over SHA-256("beam-<kind>:v1" ‖ statementHash), with user verification.
async function verified(credentialId: string, pk: Uint8Array<ArrayBuffer>, e: Entry) {
  const hash = bytes(e.statementHash);
  const blob = bytes(e.blob);
  const a = e.assertion;
  if (hash.length !== 32 || typeof e.blob !== "string" || typeof a !== "object" || a === null) throw new Refusal(400, "params");
  if (blob.length > MAX_BLOB) throw new Refusal(413, "blob-too-large");
  const challenge = await sha256(concat(new TextEncoder().encode(`beam-${e.kind}:v1`), hash));
  let ok = false;
  try {
    ok = a.credentialId === credentialId && (await verifyAuthenticationResponse({
      response: {
        id: a.credentialId,
        rawId: a.credentialId,
        type: "public-key",
        clientExtensionResults: {},
        response: { clientDataJSON: a.clientDataJSON, authenticatorData: a.authenticatorData, signature: a.signature },
      },
      expectedChallenge: b64(challenge),
      expectedOrigin: ORIGIN,
      expectedRPID: RP_ID,
      credential: { id: credentialId, publicKey: pk, counter: 0 },
      requireUserVerification: true,
    })).verified;
  } catch {
    ok = false;
  }
  if (!ok) throw new Refusal(403, "bad-assertion");
  const assertion: Assertion = {
    credentialId: a.credentialId, clientDataJSON: a.clientDataJSON, authenticatorData: a.authenticatorData, signature: a.signature,
  };
  return { hash, blob, assertion };
}

async function limit(env: Env, fleetId: string) {
  if (!(await env.LIMITER.limit({ key: fleetId })).success) throw new Refusal(429, "rate-limited");
}

async function body(req: Request): Promise<any> {
  try {
    return await req.json();
  } catch {
    throw new Refusal(400, "params");
  }
}

function json(status: number, v: unknown): Response {
  return new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
}

// bytes decodes unpadded base64url, anything else being empty.
function bytes(s: unknown): Uint8Array<ArrayBuffer> {
  return typeof s === "string" && /^[A-Za-z0-9_-]*$/.test(s) && s.length % 4 !== 1 ? isoBase64URL.toBuffer(s) : new Uint8Array();
}

function b64(b: ArrayBuffer | Uint8Array): string {
  return isoBase64URL.fromBuffer(new Uint8Array(b));
}

async function sha256(b: Uint8Array<ArrayBuffer>): Promise<Uint8Array<ArrayBuffer>> {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", b));
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
}

function hex(b: Uint8Array): string {
  return [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}
