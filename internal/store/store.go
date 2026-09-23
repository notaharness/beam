// Package store is state.db (docs/02, docs/05): pinned peers, revocations,
// pending directory writes and the mailbox, on SQLite in WAL mode.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/notaharness/beam/internal/identity"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS peers (
	peer_id      TEXT PRIMARY KEY,
	entry        TEXT NOT NULL,
	pinned_at    INTEGER NOT NULL,
	last_seen_at INTEGER,
	alias        TEXT,
	grant_       TEXT NOT NULL DEFAULT 'all'
);
CREATE TABLE IF NOT EXISTS revocations (
	peer_id    TEXT PRIMARY KEY,
	record     TEXT NOT NULL,
	revoked_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS pending (
	statement_hash TEXT PRIMARY KEY,
	record         TEXT NOT NULL,
	created_at     INTEGER NOT NULL
);`

// Grants (docs/04): what this machine lets a peer open here.
const (
	GrantAll  = "all"
	GrantMsg  = "msg"
	GrantNone = "none"
)

// Store is an open state.db.
type Store struct {
	db *sql.DB
}

// Open opens or creates state.db at path with mode 0600.
func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema + mailboxSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("state.db: %w", err)
	}
	return &Store{db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Peer is a pinned, verified member (docs/02, "The peer table").
type Peer struct {
	Entry      identity.Record
	PinnedAt   int64
	LastSeenAt *int64
	Revoked    bool
	RevokedAt  *int64
	Alias      *string
	Grant      string
}

// Pin stores a verified entry: a new peer, or a replacement its entry
// supersedes. It reports whether anything changed.
func (s *Store) Pin(e identity.Record, now int64) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	var old string
	err = tx.QueryRow(`SELECT entry FROM peers WHERE peer_id = ?`, e.PeerID).Scan(&old)
	b, _ := json.Marshal(e)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.Exec(`INSERT INTO peers (peer_id, entry, pinned_at) VALUES (?, ?, ?)`, e.PeerID, b, now)
		if err == nil { // the peer's send counter starts with it (docs/05)
			_, err = tx.Exec(`INSERT INTO send_seq (peer, next_seq) VALUES (?, 1) ON CONFLICT (peer) DO NOTHING`, e.PeerID)
		}
	case err != nil:
		return false, err
	default:
		var prev identity.Record
		if json.Unmarshal([]byte(old), &prev) == nil && !e.Supersedes(prev) {
			return false, nil
		}
		_, err = tx.Exec(`UPDATE peers SET entry = ? WHERE peer_id = ?`, b, e.PeerID)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Revoke stores a verified revocation. It reports whether it was new.
func (s *Store) Revoke(r identity.Record, now int64) (bool, error) {
	return revoke(s.db, r, now)
}

// execer is a database or a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func revoke(db execer, r identity.Record, now int64) (bool, error) {
	b, _ := json.Marshal(r)
	res, err := db.Exec(`INSERT INTO revocations (peer_id, record, revoked_at) VALUES (?, ?, ?)
		ON CONFLICT (peer_id) DO NOTHING`, r.PeerID, b, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// IsRevoked reports whether peerID has a stored revocation.
func (s *Store) IsRevoked(peerID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM revocations WHERE peer_id = ?`, peerID).Scan(&n)
	return n > 0, err
}

const peerColumns = `p.entry, p.pinned_at, p.last_seen_at, p.alias, p.grant_, r.revoked_at
	FROM peers p LEFT JOIN revocations r ON r.peer_id = p.peer_id`

func scanPeer(row interface{ Scan(...any) error }) (Peer, error) {
	var p Peer
	var entry string
	if err := row.Scan(&entry, &p.PinnedAt, &p.LastSeenAt, &p.Alias, &p.Grant, &p.RevokedAt); err != nil {
		return Peer{}, err
	}
	p.Revoked = p.RevokedAt != nil
	return p, json.Unmarshal([]byte(entry), &p.Entry)
}

// Peer returns one pinned peer.
func (s *Store) Peer(peerID string) (Peer, bool, error) {
	p, err := scanPeer(s.db.QueryRow(`SELECT `+peerColumns+` WHERE p.peer_id = ?`, peerID))
	if errors.Is(err, sql.ErrNoRows) {
		return Peer{}, false, nil
	}
	return p, err == nil, err
}

// Peers returns every pinned peer ordered by peer id.
func (s *Store) Peers() ([]Peer, error) {
	rows, err := s.db.Query(`SELECT ` + peerColumns + ` ORDER BY p.peer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ps []Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

// Records returns every entry and revocation held, revocations first: what a
// sync stream sends on open.
func (s *Store) Records() ([]identity.Record, error) {
	rows, err := s.db.Query(`SELECT record FROM revocations UNION ALL SELECT entry FROM peers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rs []identity.Record
	for rows.Next() {
		var b string
		var r identity.Record
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(b), &r); err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	return rs, rows.Err()
}

// SetAlias sets or, with nil, clears a peer's local alias.
func (s *Store) SetAlias(peerID string, alias *string) error {
	_, err := s.db.Exec(`UPDATE peers SET alias = ? WHERE peer_id = ?`, alias, peerID)
	return err
}

// SetGrant sets what peerID may open here.
func (s *Store) SetGrant(peerID, grant string) error {
	_, err := s.db.Exec(`UPDATE peers SET grant_ = ? WHERE peer_id = ?`, grant, peerID)
	return err
}

// Seen records contact with peerID.
func (s *Store) Seen(peerID string, now int64) error {
	_, err := s.db.Exec(`UPDATE peers SET last_seen_at = ? WHERE peer_id = ?`, now, peerID)
	return err
}
