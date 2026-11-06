// Package selfschema holds the three frozen self-authentication DDL constants.
// Independently extracted, byte-for-byte, from this project's self_store.go for
// docs/restored-self-state-contract.md. No external implementation source.
package selfschema

const Credentials = `CREATE TABLE employee_self_credentials (
		employee_id TEXT PRIMARY KEY REFERENCES employees(id) ON DELETE CASCADE,
		password_hash BLOB,
		enrollment_digest BLOB,
		enrollment_expires_at TEXT,
		updated_at TEXT NOT NULL,
		CHECK ((enrollment_digest IS NULL) = (enrollment_expires_at IS NULL)),
		CHECK (password_hash IS NULL OR enrollment_digest IS NULL)
	)`

const Sessions = `CREATE TABLE employee_self_sessions (
		selector TEXT PRIMARY KEY,
		employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE CASCADE,
		verifier_digest BLOB NOT NULL,
		csrf_token TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`

const ExpiryIndex = `CREATE INDEX employee_self_sessions_expiry_idx ON employee_self_sessions(expires_at)`
