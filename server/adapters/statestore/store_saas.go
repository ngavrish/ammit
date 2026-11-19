//go:build !sealed

// Package statestore is ammit's StateStore port: where the record is kept.
//
// SaaS (this file, every build without the sealed tag) keeps it in Postgres,
// exactly as ammit always has: Embedded is false, so the service opens its
// pools on AMMIT_DSN, runs its own schema and partitions, and nothing here is
// called. The sealed binary (store_sealed.go) keeps it in one SQLite file in
// the data directory, inside the revisore process (revisore design, section 3).
package statestore

import (
	"database/sql"
	"errors"
)

// Embedded says the record is in the embedded store, not Postgres.
const Embedded = false

// Open is the embedded store's two pools; a SaaS build has none.
func Open(string) (*sql.DB, *sql.DB, error) {
	return nil, nil, errors.New("statestore: this build keeps the record in Postgres")
}

// Migrate makes the embedded store's tables; a SaaS build has none.
func Migrate(*sql.DB, ...string) error {
	return errors.New("statestore: this build keeps the record in Postgres")
}

// Translate is the query as the store reads it: unchanged.
func Translate(q string) string { return q }

// UniqueViolation is never asked of a SaaS build (ammit reads pgconn).
func UniqueViolation(error) bool { return false }

// Label is where the record is, for /health and the log; the SaaS build
// names its Postgres itself.
func Label() string { return "" }
