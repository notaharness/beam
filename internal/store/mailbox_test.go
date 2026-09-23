package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/stream"
)

const peer = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// pinned is a store that knows peer.
func pinned(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	if _, err := s.Pin(identity.Record{V: 1, Kind: identity.Member, PeerID: peer, Label: "b"}, 1); err != nil {
		t.Fatal(err)
	}
	return s
}

func enqueue(s *Store, env string) (int64, error) {
	return s.Enqueue(peer, 1, func(int64) ([]byte, error) { return []byte(env), nil })
}

// docs/05 Sequence numbers: send_seq exists from the peer's pinning on, so a
// lost counter is storage-failure even once every message sent was delivered,
// never a restart at 1.
func TestLostSendCounter(t *testing.T) {
	s := pinned(t)
	for want := int64(1); want <= 2; want++ {
		seq, err := enqueue(s, "{}")
		if err != nil || seq != want {
			t.Fatalf("seq %d, %v; want %d", seq, err, want)
		}
		if err := s.Delivered(peer, seq); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM send_seq`); err != nil {
		t.Fatal(err)
	}
	if seq, err := enqueue(s, "{}"); !errors.Is(err, ErrNoSendSeq) {
		t.Fatalf("seq %d, %v; want storage-failure", seq, err)
	}
	if _, err := open(t).Enqueue(peer, 1, nil); !errors.Is(err, ErrNoSendSeq) {
		t.Errorf("a peer never pinned: %v, want storage-failure", err)
	}
}

// docs/05 Store: a peer's outbound bound counts what the recipient refused
// for good, as well as what waits.
func TestQuarantineCounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows int
		size int
	}{
		{"envelopes", MaxQueued, 2},
		{"bytes", 64, MaxQueuedBytes / 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := pinned(t)
			tx, _ := s.db.Begin()
			env := strings.Repeat("x", tc.size)
			for i := range tc.rows {
				if _, err := tx.Exec(`INSERT INTO quarantine VALUES (?, ?, ?, 'invalid-envelope')`, peer, i+1, env); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := enqueue(s, "{}"); !errors.Is(err, ErrQueueFull) {
				t.Errorf("%v, want queue-full", err)
			}
		})
	}
}

// docs/06 msg.queue: a page's envelopes fit a control line, however large
// they are, and the pages list each envelope once.
func TestQueuePagesFitALine(t *testing.T) {
	s := pinned(t)
	big := `{"payload":"` + strings.Repeat("<", 300<<10) + `"}` // three to a page, as sent
	for range 5 {
		if _, err := enqueue(s, big); err != nil {
			t.Fatal(err)
		}
	}
	seen, pages := 0, 1
	for cursor := int64(0); ; pages++ {
		items, next, err := s.Queue(Outbound, "", cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := stream.Marshal(items)
		if len(b) > MaxPage {
			t.Fatalf("a page of %d bytes", len(b))
		}
		seen += len(items)
		if next == 0 {
			break
		}
		cursor = next
	}
	if seen != 5 || pages != 2 {
		t.Errorf("listed %d envelopes in %d pages, want 5 in 2", seen, pages)
	}
}
