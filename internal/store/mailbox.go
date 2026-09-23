package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// The mailbox tables (docs/05). inbound adds to the listed columns the
// envelope's id and topic, which msg.ack and subscribe filters select on, and
// who deferred it and why, for redelivery and the refused list.
const mailboxSchema = `
CREATE TABLE IF NOT EXISTS outbound (
	peer       TEXT NOT NULL,
	seq        INTEGER NOT NULL,
	envelope   TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (peer, seq)
);
CREATE TABLE IF NOT EXISTS inbound (
	peer        TEXT NOT NULL,
	seq         INTEGER NOT NULL,
	id          TEXT NOT NULL,
	topic       TEXT NOT NULL,
	envelope    TEXT NOT NULL,
	received_at INTEGER NOT NULL,
	inflight_to INTEGER,
	deferred_to INTEGER,
	deferred    TEXT,
	PRIMARY KEY (peer, seq)
);
CREATE TABLE IF NOT EXISTS seen (
	peer     TEXT PRIMARY KEY,
	high_seq INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS send_seq (
	peer     TEXT PRIMARY KEY,
	next_seq INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS quarantine (
	peer     TEXT NOT NULL,
	seq      INTEGER NOT NULL,
	envelope TEXT NOT NULL,
	reason   TEXT NOT NULL,
	PRIMARY KEY (peer, seq)
);
UPDATE inbound SET inflight_to = NULL, deferred_to = NULL;`

// Queue bounds, per peer and direction (docs/05); outbound counts what the
// recipient refused for good.
const (
	MaxQueued      = 10000
	MaxQueuedBytes = 64 << 20
)

// Mailbox errors; their text is the docs/05 reason.
var (
	ErrQueueFull = errors.New("queue-full")
	ErrNoSendSeq = errors.New("storage-failure")
)

// Receive outcomes besides acceptance.
const (
	Duplicate = "duplicate"
	QueueFull = "queue-full"
)

// Enqueue is msg.send's transaction: the next seq for peer, the envelope built
// with it, its outbound row and the bumped send_seq, together.
func (s *Store) Enqueue(peer string, now int64, build func(seq int64) ([]byte, error)) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	seq, err := nextSeq(tx, peer)
	if err != nil {
		return 0, err
	}
	env, err := build(seq)
	if err != nil {
		return 0, err
	}
	var n, size int64
	if err := tx.QueryRow(`SELECT count(*), coalesce(sum(length(envelope)), 0) FROM
		(SELECT envelope FROM outbound WHERE peer = ? UNION ALL SELECT envelope FROM quarantine WHERE peer = ?)`, peer, peer).Scan(&n, &size); err != nil {
		return 0, err
	}
	if n >= MaxQueued || size+int64(len(env)) > MaxQueuedBytes {
		return 0, ErrQueueFull
	}
	if _, err := tx.Exec(`INSERT INTO outbound (peer, seq, envelope, created_at) VALUES (?, ?, ?, ?)`, peer, seq, env, now); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE send_seq SET next_seq = ? WHERE peer = ?`, seq+1, peer); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

// nextSeq reads send_seq, which Pin creates with the peer's row: a peer
// without one lost its counter.
func nextSeq(tx *sql.Tx, peer string) (int64, error) {
	var seq int64
	err := tx.QueryRow(`SELECT next_seq FROM send_seq WHERE peer = ?`, peer).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoSendSeq
	}
	return seq, err
}

// Head is the oldest outbound envelope for peer.
func (s *Store) Head(peer string) (seq int64, env []byte, ok bool, err error) {
	err = s.db.QueryRow(`SELECT seq, envelope FROM outbound WHERE peer = ? ORDER BY seq LIMIT 1`, peer).Scan(&seq, &env)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, false, nil
	}
	return seq, env, err == nil, err
}

// Delivered deletes an outbound envelope the recipient acked as accepted or
// duplicate.
func (s *Store) Delivered(peer string, seq int64) error {
	_, err := s.db.Exec(`DELETE FROM outbound WHERE peer = ? AND seq = ?`, peer, seq)
	return err
}

// Quarantine moves an outbound envelope the recipient refused for good out of
// the queue.
func (s *Store) Quarantine(peer string, seq int64, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	if _, err := tx.Exec(`INSERT INTO quarantine (peer, seq, envelope, reason)
		SELECT peer, seq, envelope, ? FROM outbound WHERE peer = ? AND seq = ?`, reason, peer, seq); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM outbound WHERE peer = ? AND seq = ?`, peer, seq); err != nil {
		return err
	}
	return tx.Commit()
}

// Receive is the receiver's transaction for an envelope from peer. It returns
// "" once the envelope and its seq as high_seq are committed together, or
// Duplicate or QueueFull having written nothing.
func (s *Store) Receive(peer string, seq int64, id, topic string, env []byte, now int64) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	var high, n, size int64
	err = tx.QueryRow(`SELECT high_seq FROM seen WHERE peer = ?`, peer).Scan(&high)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if seq <= high {
		return Duplicate, nil
	}
	if err := tx.QueryRow(`SELECT count(*), coalesce(sum(length(envelope)), 0) FROM inbound WHERE peer = ?`, peer).Scan(&n, &size); err != nil {
		return "", err
	}
	if n >= MaxQueued || size+int64(len(env)) > MaxQueuedBytes {
		return QueueFull, nil
	}
	if _, err := tx.Exec(`INSERT INTO inbound (peer, seq, id, topic, envelope, received_at) VALUES (?, ?, ?, ?, ?, ?)`,
		peer, seq, id, topic, env, now); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO seen (peer, high_seq) VALUES (?, ?)
		ON CONFLICT (peer) DO UPDATE SET high_seq = excluded.high_seq`, peer, seq); err != nil {
		return "", err
	}
	return "", tx.Commit()
}

// Take hands subscriber sub the oldest inbound envelope that matches topic
// (when set) and one of from (when any), is in flight to no one and was not
// deferred by sub.
func (s *Store) Take(sub int64, topic *string, from []string) ([]byte, bool, error) {
	q := `UPDATE inbound SET inflight_to = ? WHERE rowid = (SELECT rowid FROM inbound
		WHERE inflight_to IS NULL AND deferred_to IS NOT ? AND (? IS NULL OR topic = ?)`
	args := []any{sub, sub, topic, topic}
	if len(from) > 0 {
		q += ` AND peer IN (SELECT value FROM json_each(?))`
		b, _ := json.Marshal(from)
		args = append(args, string(b))
	}
	var env []byte
	err := s.db.QueryRow(q+` ORDER BY rowid LIMIT 1) RETURNING envelope`, args...).Scan(&env)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return env, err == nil, err
}

// AckInbound deletes the envelope id in flight to sub: the application has it.
func (s *Store) AckInbound(sub int64, id string) (bool, error) {
	return s.changed(`DELETE FROM inbound WHERE inflight_to = ? AND id = ?`, sub, id)
}

// DeferInbound releases the envelope id in flight to sub unacked, recording
// reason; sub is not offered it again.
func (s *Store) DeferInbound(sub int64, id, reason string) (bool, error) {
	return s.changed(`UPDATE inbound SET inflight_to = NULL, deferred_to = ?, deferred = ?
		WHERE inflight_to = ? AND id = ?`, sub, reason, sub, id)
}

// Release returns every envelope in flight to sub, whose connection is gone.
func (s *Store) Release(sub int64) error {
	_, err := s.db.Exec(`UPDATE inbound SET inflight_to = NULL WHERE inflight_to = ?`, sub)
	return err
}

func (s *Store) changed(q string, args ...any) (bool, error) {
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
