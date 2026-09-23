package store

import (
	"encoding/hex"
	"encoding/json"

	"github.com/notaharness/beam/internal/identity"
)

// AddPending queues a record whose directory append has not landed, to be
// retried while the daemon runs (docs/02).
func (s *Store) AddPending(r identity.Record, now int64) error {
	b, _ := json.Marshal(r)
	_, err := s.db.Exec(`INSERT INTO pending (statement_hash, record, created_at) VALUES (?, ?, ?)
		ON CONFLICT (statement_hash) DO NOTHING`, pendingKey(r), b, now)
	return err
}

// Pending lists the queued records, oldest first.
func (s *Store) Pending() ([]identity.Record, error) {
	rows, err := s.db.Query(`SELECT record FROM pending ORDER BY created_at, rowid`)
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
		if json.Unmarshal([]byte(b), &r) == nil {
			rs = append(rs, r)
		}
	}
	return rs, rows.Err()
}

// DonePending removes a record from the queue.
func (s *Store) DonePending(r identity.Record) error {
	_, err := s.db.Exec(`DELETE FROM pending WHERE statement_hash = ?`, pendingKey(r))
	return err
}

func pendingKey(r identity.Record) string {
	h := r.StatementHash()
	return hex.EncodeToString(h[:])
}

// Reset forgets the fleet (docs/02, beam fleet reset): every peer and
// revocation, the pending directory writes, and the mail to and from the
// peers, which goes with them.
func (s *Store) Reset() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	for _, table := range []string{"peers", "revocations", "pending", "outbound", "inbound", "seen", "send_seq", "quarantine"} {
		if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
			return err
		}
	}
	return tx.Commit()
}
