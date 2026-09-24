CREATE TABLE IF NOT EXISTS fleets (
  fleet_id       TEXT PRIMARY KEY,
  credential_id  TEXT NOT NULL,
  credential_pk  BLOB NOT NULL,
  read_hash      BLOB NOT NULL UNIQUE,
  created_at     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS entries (
  fleet_id       TEXT NOT NULL REFERENCES fleets,
  seq            INTEGER NOT NULL,
  statement_hash BLOB NOT NULL,
  blob           BLOB NOT NULL,
  assertion      TEXT NOT NULL,
  created_at     INTEGER NOT NULL,
  PRIMARY KEY (fleet_id, seq),
  UNIQUE (fleet_id, statement_hash)
);
CREATE TABLE IF NOT EXISTS slots (
  slot_id        TEXT PRIMARY KEY,
  sealed         BLOB,
  created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS slots_created ON slots (created_at);
