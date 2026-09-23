package identity

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// vectors is testdata/vectors.json, written by testdata/gen and cross-checked
// against an independent implementation when generated.
type vectors struct {
	Credential struct {
		ID        string `json:"credentialId"`
		PublicKey string `json:"publicKey"`
		FleetID   string `json:"fleetId"`
	} `json:"credential"`
	PRF struct {
		First string `json:"first"`
		KDir  string `json:"kDir"`
		TRead string `json:"tRead"`
	} `json:"prf"`
	Nodes []struct {
		NodePublic string `json:"nodePublic"`
		PeerID     string `json:"peerId"`
	} `json:"nodes"`
	Revoked []string `json:"revoked"`
	Records []struct {
		Name          string          `json:"name"`
		Record        json.RawMessage `json:"record"`
		Statement     string          `json:"statement"`
		StatementHash string          `json:"statementHash"`
		Challenge     string          `json:"challenge"`
		Want          string          `json:"want"`
	} `json:"records"`
}

func loadVectors(t *testing.T) (vectors, Credential) {
	t.Helper()
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	pk, _ := base64.RawURLEncoding.DecodeString(v.Credential.PublicKey)
	return v, Credential{ID: v.Credential.ID, PublicKey: pk}
}

func TestRecordVectors(t *testing.T) {
	v, cred := loadVectors(t)
	if cred.FleetID() != v.Credential.FleetID {
		t.Errorf("fleet id %s, want %s", cred.FleetID(), v.Credential.FleetID)
	}
	revoked := func(id string) bool { return slices.Contains(v.Revoked, id) }
	for _, rv := range v.Records {
		t.Run(rv.Name, func(t *testing.T) {
			r, err := ParseRecord(rv.Record)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(r.Statement()); got != rv.Statement {
				t.Errorf("statement\n got %s\nwant %s", got, rv.Statement)
			}
			if h := r.StatementHash(); hex.EncodeToString(h[:]) != rv.StatementHash {
				t.Errorf("statement hash %x, want %s", h, rv.StatementHash)
			}
			if c := base64.RawURLEncoding.EncodeToString(r.Challenge()); c != rv.Challenge {
				t.Errorf("challenge %s, want %s", c, rv.Challenge)
			}
			got := ""
			if err := cred.Verify(r, revoked); err != nil {
				got = err.Error()
			}
			if got != rv.Want {
				t.Errorf("verify: %q, want %q", got, rv.Want)
			}
		})
	}
}

func TestNodeVectors(t *testing.T) {
	v, _ := loadVectors(t)
	for _, n := range v.Nodes {
		pub, _ := base64.RawURLEncoding.DecodeString(n.NodePublic)
		if got := PeerID([32]byte(pub)); got != n.PeerID || !ValidPeerID(got) {
			t.Errorf("peer id %s, want %s", got, n.PeerID)
		}
	}
}

// The derivations are pinned: changing a salt or an info string fails here.
func TestDirectoryKeys(t *testing.T) {
	v, _ := loadVectors(t)
	first, _ := base64.RawURLEncoding.DecodeString(v.PRF.First)
	kDir, tRead := DirectoryKeys(first)
	if hex.EncodeToString(kDir) != v.PRF.KDir || hex.EncodeToString(tRead) != v.PRF.TRead {
		t.Fatalf("K_dir %x, T_read %x; want %s, %s", kDir, tRead, v.PRF.KDir, v.PRF.TRead)
	}
}

func TestParseRecordRejects(t *testing.T) {
	for _, in := range []string{
		`{"v":1,"kind":"member","peerId":"a","issuedAt":1,"extra":true}`,
		`{"v":1,"kind":"member","kind":"revoke","peerId":"a","issuedAt":1}`,
		`{"v":1,"kind":"member","peerId":"a","issuedAt":9007199254740992}`,
		`{"v":1,"kind":"member","peerId":"a","issuedAt":1.5}`,
		`[]`,
	} {
		if _, err := ParseRecord([]byte(in)); err != BadEntry {
			t.Errorf("%s: %v, want bad-entry", in, err)
		}
	}
}

func TestSupersedes(t *testing.T) {
	a := Record{V: 1, Kind: Member, PeerID: "x", Label: "a", IssuedAt: 2}
	b := Record{V: 1, Kind: Member, PeerID: "x", Label: "b", IssuedAt: 1}
	if !a.Supersedes(b) || b.Supersedes(a) {
		t.Error("the later issuedAt must win")
	}
	b.IssuedAt = 2
	ha, hb := a.StatementHash(), b.StatementHash()
	winner, loser := a, b
	if string(hb[:]) > string(ha[:]) {
		winner, loser = b, a
	}
	if !winner.Supersedes(loser) || loser.Supersedes(winner) {
		t.Error("on a tie the greater statement hash must win")
	}
}

func TestValidText(t *testing.T) {
	for _, tc := range []struct {
		s  string
		ok bool
	}{
		{"buildbox", true},
		{"Ströme ☃ 😀", true},
		{string(make([]rune, 64)), false}, // NULs are controls
		{"", false},
		{"a/b", false}, {`a\b`, false}, {"a{", false}, {"a}", false},
		{"tab\t", false}, {"del\x7f", false}, {"c1\u0085", false},
		{"\xff", false},
	} {
		if got := ValidLabel(tc.s); got != tc.ok {
			t.Errorf("ValidLabel(%q) = %v", tc.s, got)
		}
	}
	long := ""
	for range 64 {
		long += "é"
	}
	if !ValidLabel(long) || ValidLabel(long+"é") {
		t.Error("labels are counted in scalar values, 64 at most")
	}
}
