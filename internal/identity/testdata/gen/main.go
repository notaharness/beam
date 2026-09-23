//go:build beamtest

// Command gen writes the identity vectors (docs/10) to stdout:
//
//	go run -tags beamtest ./internal/identity/testdata/gen > internal/identity/testdata/vectors.json
//
// Keys are fixed; ECDSA signatures are randomised, so a regeneration changes
// them and nothing else.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"

	"github.com/notaharness/beam/internal/identity"
	"github.com/tailscale/tailcat"
	"go4.org/mem"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

var b64 = base64.RawURLEncoding.EncodeToString

type node struct {
	Private    string `json:"private"`
	NodePublic string `json:"nodePublic"`
	PeerID     string `json:"peerId"`
	Address    string `json:"address"`
}

type recordVector struct {
	Name          string          `json:"name"`
	Record        json.RawMessage `json:"record"`
	Statement     string          `json:"statement"`
	StatementHash string          `json:"statementHash"`
	Challenge     string          `json:"challenge"`
	Want          string          `json:"want"`
}

type vectors struct {
	Credential struct {
		ID         string `json:"credentialId"`
		PrivateKey string `json:"privateKey"`
		PublicKey  string `json:"publicKey"`
		FleetID    string `json:"fleetId"`
	} `json:"credential"`
	PRF struct {
		Secret string `json:"secret"`
		Salt   string `json:"salt"`
		First  string `json:"first"`
		KDir   string `json:"kDir"`
		TRead  string `json:"tRead"`
	} `json:"prf"`
	Nodes   []node         `json:"nodes"`
	Revoked []string       `json:"revoked"`
	Records []recordVector `json:"records"`
}

var region = &tailcfg.DERPRegion{RegionID: 900, RegionCode: "vec", Nodes: []*tailcfg.DERPNode{
	{Name: "v1", RegionID: 900, HostName: "derp.example.net", DERPPort: 443},
}}

func fixed(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func makeNode(seed byte, psk bool) (node, [32]byte) {
	priv := key.NodePrivateFromRaw32(mem.B(fixed(seed)))
	ci := tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: priv.Public()},
		ServerDiscoPublic: tailcat.DiscoPublicForNode(priv),
		Region:            []*tailcfg.DERPRegion{region},
	}
	if psk {
		copy(ci.PresharedKey[:], fixed(seed+100))
	}
	pub := [32]byte(priv.Public().AppendTo(nil))
	text, _ := priv.MarshalText()
	return node{string(text), b64(pub[:]), identity.PeerID(pub), string(ci.Addr())}, pub
}

func authenticator(seed byte) *identity.Authenticator {
	k := new(ecdsa.PrivateKey)
	k.Curve = elliptic.P256()
	k.D = new(big.Int).SetBytes(fixed(seed))
	k.X, k.Y = k.Curve.ScalarBaseMult(k.D.Bytes())
	return &identity.Authenticator{CredentialID: b64(fixed(seed)[:16]), Key: k, PRFSecret: fixed(seed + 1)}
}

func main() {
	var v vectors
	a, other := authenticator(7), authenticator(8)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(a.Key)
	cred := a.Credential()
	v.Credential.ID, v.Credential.PrivateKey = cred.ID, b64(pkcs8)
	v.Credential.PublicKey, v.Credential.FleetID = b64(cred.PublicKey), cred.FleetID()

	first := a.PRF([]byte(identity.PRFSalt))
	kDir, tRead := identity.DirectoryKeys(first)
	v.PRF.Secret, v.PRF.Salt, v.PRF.First = b64(a.PRFSecret), identity.PRFSalt, b64(first)
	v.PRF.KDir, v.PRF.TRead = hex.EncodeToString(kDir), hex.EncodeToString(tRead)

	n0, _ := makeNode(1, true)
	n1, _ := makeNode(2, true)
	n2, _ := makeNode(3, true)
	noPSK, _ := makeNode(1, false)
	v.Nodes = []node{n0, n1, n2}
	v.Revoked = []string{n2.PeerID}

	member := func(n node) identity.Record {
		return identity.Record{V: 1, Kind: identity.Member, PeerID: n.PeerID, NodePublic: n.NodePublic,
			Address: n.Address, Label: "buildbox", IssuedAt: 1758570000000}
	}
	signed := func(r identity.Record) identity.Record { a.SignRecord(&r); return r }
	add := func(name string, r identity.Record, want identity.Refusal) {
		b, _ := json.Marshal(r)
		h := r.StatementHash()
		v.Records = append(v.Records, recordVector{name, b, string(r.Statement()),
			hex.EncodeToString(h[:]), b64(r.Challenge()), string(want)})
	}
	withAssertion := func(r identity.Record, authData []byte, typ, origin string) identity.Record {
		r.Assertion = a.Sign(authData, identity.ClientDataJSON(typ, r.Challenge(), origin))
		return r
	}
	uvData := identity.AuthenticatorData(identity.RPID, identity.FlagUP|identity.FlagUV)

	add("member", signed(member(n0)), "")
	add("member unicode label", signed(func() identity.Record { r := member(n1); r.Label = "Ströme ☃ 😀"; return r }()), "")
	revoke := identity.Record{V: 1, Kind: identity.Revoke, PeerID: n2.PeerID, IssuedAt: 1758570000001}
	add("revoke", signed(revoke), "")
	add("member with a revocation", signed(member(n2)), identity.Revoked)

	badID := member(n0)
	badID.PeerID = n1.PeerID
	add("peer id not derived from the node key", signed(badID), identity.BadEntry)
	mismatch := member(n0)
	mismatch.Address = n1.Address
	add("address names another key", signed(mismatch), identity.BadEntry)
	zero := member(n0)
	zero.Address = noPSK.Address
	add("address without a pre-shared key", signed(zero), identity.BadEntry)
	slash := member(n0)
	slash.Label = "a/b"
	add("label with a slash", signed(slash), identity.BadEntry)

	foreign := member(n0)
	other.SignRecord(&foreign)
	add("signed by another credential", foreign, identity.WrongPasskey)
	foreignRevoke := revoke
	other.SignRecord(&foreignRevoke)
	add("revoke signed by another credential", foreignRevoke, identity.WrongPasskey)

	add("wrong origin", withAssertion(member(n0), uvData, "webauthn.get", "https://beam.n10.is.example.com"), identity.BadAssertion)
	add("wrong rpIdHash", withAssertion(member(n0), identity.AuthenticatorData("n10.is", identity.FlagUP|identity.FlagUV), "webauthn.get", identity.Origin), identity.BadAssertion)
	add("user verification unset", withAssertion(member(n0), identity.AuthenticatorData(identity.RPID, identity.FlagUP), "webauthn.get", identity.Origin), identity.BadAssertion)
	add("create instead of get", withAssertion(member(n0), uvData, "webauthn.create", identity.Origin), identity.BadAssertion)
	badSig := signed(member(n0))
	sig, _ := base64.RawURLEncoding.DecodeString(badSig.Assertion.Signature)
	sig[len(sig)-1] ^= 1
	badSig.Assertion.Signature = b64(sig)
	add("bad signature", badSig, identity.BadAssertion)
	relabelled := signed(member(n0))
	relabelled.Label = "otherbox"
	add("statement changed after signing", relabelled, identity.BadAssertion)
	confused := revoke
	confused.Assertion = a.Assert(func() []byte { // the member domain over the same statement hash
		h := confused.StatementHash()
		c := sha256.Sum256(append([]byte("beam-member:v1"), h[:]...))
		return c[:]
	}())
	add("revoke signed with the member domain", confused, identity.BadAssertion)

	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		panic(err)
	}
}
