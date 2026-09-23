// A software ES256 passkey for beam.n10.is, as internal/identity's beamtest
// authenticator: WebAuthn-shaped get assertions, nothing more.
import { isoBase64URL } from "@simplewebauthn/server/helpers";

export interface Assertion {
  credentialId: string;
  clientDataJSON: string;
  authenticatorData: string;
  signature: string;
}

const b64 = (b: ArrayBuffer | Uint8Array) => isoBase64URL.fromBuffer(new Uint8Array(b));
const sha256 = async (b: Uint8Array) => new Uint8Array(await crypto.subtle.digest("SHA-256", new Uint8Array(b)));

export class Authenticator {
  private constructor(
    readonly credentialId: string,
    private readonly keys: CryptoKeyPair,
    readonly cose: Uint8Array,
  ) {}

  // create makes a passkey, under credentialId when given: another key
  // claiming a credential's id.
  static async create(credentialId = b64(crypto.getRandomValues(new Uint8Array(16)))): Promise<Authenticator> {
    const keys = (await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"])) as CryptoKeyPair;
    const raw = new Uint8Array((await crypto.subtle.exportKey("raw", keys.publicKey)) as ArrayBuffer);
    // COSE_Key {1: 2 (EC2), 3: -7 (ES256), -1: 1 (P-256), -2: x, -3: y}
    const cose = new Uint8Array([0xa5, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01, 0x21, 0x58, 0x20, ...raw.slice(1, 33), 0x22, 0x58, 0x20, ...raw.slice(33)]);
    return new Authenticator(credentialId, keys, cose);
  }

  // assert signs a get over challenge, by default for beam with user
  // presence (0x01) and verification (0x04); as overrides one of them.
  async assert(challenge: Uint8Array, flags = 0x05, as: { type?: string; origin?: string; rpId?: string } = {}): Promise<Assertion> {
    const { type = "webauthn.get", origin = "https://beam.n10.is", rpId = "beam.n10.is" } = as;
    const authData = new Uint8Array([...(await sha256(new TextEncoder().encode(rpId))), flags, 0, 0, 0, 0]);
    const clientData = new TextEncoder().encode(JSON.stringify({ type, challenge: b64(challenge), origin, crossOrigin: false }));
    const signed = new Uint8Array([...authData, ...(await sha256(clientData))]);
    const p1363 = new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, this.keys.privateKey, signed));
    return { credentialId: this.credentialId, clientDataJSON: b64(clientData), authenticatorData: b64(authData), signature: b64(der(p1363)) };
  }
}

// der turns an IEEE P1363 ECDSA signature (r ‖ s) into the ASN.1 DER that
// WebAuthn carries.
function der(sig: Uint8Array): Uint8Array {
  const int = (b: Uint8Array) => {
    let i = 0;
    while (i < b.length - 1 && b[i] === 0) i++;
    const v = b.slice(i);
    return v[0] & 0x80 ? [0x02, v.length + 1, 0, ...v] : [0x02, v.length, ...v];
  };
  const body = [...int(sig.slice(0, 32)), ...int(sig.slice(32))];
  return new Uint8Array([0x30, body.length, ...body]);
}
