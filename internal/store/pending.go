package store

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"

	"github.com/notaharness/beam/internal/identity"
)

// AddPending queues a record for the directory until an append of it lands
// (docs/02).
func (s *Store) AddPending(r identity.Record, now int64) error {
	return addPending(s.db, r, now)
}

func addPending(db execer, r identity.Record, now int64) error {
	b, _ := json.Marshal(r)
	_, err := db.Exec(`INSERT INTO pending (statement_hash, record, created_at) VALUES (?, ?, ?)
		ON CONFLICT (statement_hash) DO NOTHING`, pendingKey(r), b, now)
	return err
}

// RevokeQueued stores a revocation this machine signed and queues it for the
// directory in one transaction.
func (s *Store) RevokeQueued(r identity.Record, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	if _, err := revoke(tx, r, now); err != nil {
		return err
	}
	if err := addPending(tx, r, now); err != nil {
		return err
	}
	return tx.Commit()
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

// Reset forgets the fleet's machines (docs/02, beam fleet reset): every peer,
// and the mail to and from them, which goes with them. The revocations and
// the pending directory writes stay, for the next enrolment to keep those its
// credential signed (Keep): a re-join into the same fleet goes on refusing a
// machine revoked here, whose revocation may exist nowhere else yet. The
// message counters stay: they belong to the keys, which outlive the fleet,
// and one restarted at 1 would make a peer that remembers it drop new mail as
// duplicates.
func (s *Store) Reset() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	for _, table := range []string{"peers", "outbound", "inbound", "quarantine"} {
		if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Keep deletes the revocations and pending directory writes signed does not
// hold for: at an enrolment, those another fleet's credential signed.
func (s *Store) Keep(signed func(identity.Record) bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	for _, t := range []struct{ table, key string }{{"revocations", "peer_id"}, {"pending", "statement_hash"}} {
		drop, err := unsigned(tx, t.table, t.key, signed)
		for _, k := range drop {
			if err == nil {
				_, err = tx.Exec(`DELETE FROM `+t.table+` WHERE `+t.key+` = ?`, k)
			}
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// unsigned lists the keys of table's records signed does not hold for.
func unsigned(tx *sql.Tx, table, key string, signed func(identity.Record) bool) ([]string, error) {
	rows, err := tx.Query(`SELECT ` + key + `, record FROM ` + table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drop []string
	for rows.Next() {
		var k, b string
		var r identity.Record
		if err := rows.Scan(&k, &b); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(b), &r) != nil || !signed(r) {
			drop = append(drop, k)
		}
	}
	return drop, rows.Err()
}
