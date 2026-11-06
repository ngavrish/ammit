package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"
)

// compatSchema is the handful of functions the queries lean on where SQLite
// and Postgres disagree about a value, written once here rather than spelled
// out in a hundred queries.
//
//	jnum(jsonb)          json_extract's number: a JSON number is itself, true
//	                     and false are 1 and 0 (SQLite's json_extract says so),
//	                     a string that is a number is that number, anything
//	                     else is NULL. ->> gives text, and text in a sum is an
//	                     error in Postgres where SQLite quietly read it.
//	round(float8, int)   Postgres rounds numerics to places and floats only to
//	                     whole numbers, half to even; every ROUND(x, 1) in this
//	                     service and in every saved chart is over a float. This
//	                     one rounds as SQLite did: the double's exact value,
//	                     half away from zero (round(x, 0) for SQLite's round(x)).
//	lead_int(text)       CAST(text AS INTEGER) as SQLite does it: the leading
//	                     digits, 0 when there are none, NULL for NULL. Postgres
//	                     refuses the cast instead.
const compatSchema = `
CREATE OR REPLACE FUNCTION jnum(j jsonb) RETURNS double precision
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
  SELECT CASE jsonb_typeof(j)
    WHEN 'number' THEN (j #>> '{}')::double precision
    WHEN 'boolean' THEN CASE WHEN j = 'true'::jsonb THEN 1 ELSE 0 END
    WHEN 'string' THEN CASE WHEN (j #>> '{}') ~ '^\s*[-+]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][-+]?[0-9]+)?\s*$'
                            THEN (j #>> '{}')::double precision END
  END
$$;
CREATE OR REPLACE FUNCTION round(x double precision, n integer) RETURNS double precision
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
  k integer;
  m bigint;
  exact numeric;
BEGIN
  IF x IS NULL OR n IS NULL THEN RETURN NULL; END IF;
  IF x = 0 OR x <> x OR abs(x) = 'Infinity'::float8 OR abs(x) >= 4503599627370496 THEN RETURN x; END IF;
  IF n > 30 THEN n := 30; END IF;
  IF n <= 0 THEN
    RETURN trunc(x + CASE WHEN x < 0 THEN -0.5 ELSE 0.5 END);
  END IF;
  -- The double's exact decimal value, as SQLite rounds it: m * 2^-k with m
  -- a 54-bit integer, carried to thirty places. x::numeric keeps fifteen
  -- digits, and 114.55 - which is 114.5499999999999972 - rounded up.
  k := 53 - floor(ln(abs(x)) / ln(2))::integer;
  m := (x * power(2::float8, k))::bigint;
  exact := m::numeric * 1e30 / (2::numeric ^ k) / 1e30;
  RETURN round(exact, n)::double precision;
END
$$;
CREATE OR REPLACE FUNCTION lead_int(t text) RETURNS bigint
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
  SELECT CASE WHEN t IS NULL THEN NULL
              ELSE coalesce(substring(t from '^\s*([-+]?[0-9]{1,18})')::bigint, 0) END
$$;
-- Which schema this database was brought to, and when: written on every start
-- that changed it, read by nobody but a person asking.
CREATE TABLE IF NOT EXISTS ammit_schema (
    version bigint PRIMARY KEY,
    at      double precision NOT NULL
);
-- Where archive() moves finished runs: the same tables, in their own schema,
-- in the same database. Nothing is deleted; a run older than retention.days is
-- one "SELECT ... FROM archive.events" away.
CREATE SCHEMA IF NOT EXISTS archive;`

// schemaVersion is what openDB brings a database to. Every statement it runs
// is idempotent, so a start of the same version changes nothing; a change to a
// table that CREATE IF NOT EXISTS cannot make goes in schemaSteps with the
// next number.
const schemaVersion = 1

// schemaSteps are the changes that are not idempotent DDL, by the version they
// bring the database to. Empty: version 1 is the first Postgres schema.
var schemaSteps = map[int]string{}

// migrate makes the tables, the functions and this month's partitions, under
// an advisory lock so two processes starting at once (the service and the
// copy tool) do not race each other's CREATE OR REPLACE.
func migrate() error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	const lockKey = 0x616d6d6974 // "ammit"
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, lockKey) }()
	for _, ddl := range []struct{ name, sql string }{
		{"compat", compatSchema},
		{"tables", schema},
		{"watchdog", watchdogSchema},
		{"knowledge base", kbSchema},
		{"knowledge base documents", kbDocsSchema},
		{"search", searchSchema},
	} {
		if _, err := conn.ExecContext(ctx, ddl.sql); err != nil {
			return fmt.Errorf("could not make the %s: %w", ddl.name, err)
		}
	}
	// Columns added to a table that already exists. CREATE TABLE IF NOT EXISTS
	// does nothing to a database that has the table, so a new column arrives
	// only this way, and IF NOT EXISTS makes every start but the first a
	// no-op rather than an error to ignore.
	for _, add := range []string{
		`ALTER TABLE calls ADD COLUMN IF NOT EXISTS why TEXT DEFAULT ''`,
		// The wait the call happened in (sid#seq), so a call and the turn that
		// asked for it are one trace.
		`ALTER TABLE calls ADD COLUMN IF NOT EXISTS request TEXT DEFAULT ''`,
		// Whether the guard read this call as arbitrary python: 1, 0, or
		// NULL from a client that does not say. The gate that audits python
		// reasons asked a regex over the signature, which is whitespace
		// folded and cut short, and on run 4314fb3c it counted a call the
		// guard had read differently. One judge, recorded once.
		`ALTER TABLE calls ADD COLUMN IF NOT EXISTS adhoc bigint`,
	} {
		if _, err := conn.ExecContext(ctx, add); err != nil {
			return fmt.Errorf("%s: %w", add, err)
		}
	}
	var have int
	_ = conn.QueryRowContext(ctx, `SELECT coalesce(max(version),0) FROM ammit_schema`).Scan(&have)
	for v := have + 1; v <= schemaVersion; v++ {
		if step, ok := schemaSteps[v]; ok {
			if _, err := conn.ExecContext(ctx, step); err != nil {
				return fmt.Errorf("schema step %d: %w", v, err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO ammit_schema (version, at) VALUES ($1, $2)
		                                   ON CONFLICT (version) DO NOTHING`, v, nowWall()); err != nil {
			return err
		}
		log.Printf("ammit: database brought to schema %d", v)
	}
	return ensurePartitions(ctx, time.Now())
}

// The months that have a table, so the tick asks the catalog once a month.
var (
	partMu   sync.Mutex
	partMade = map[string]bool{}
)

// monthOf is the UTC month an instant falls in: its table's name and bounds.
func monthOf(t time.Time) (string, float64, float64) {
	t = t.UTC()
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	next := first.AddDate(0, 1, 0)
	return fmt.Sprintf("events_y%04dm%02d", first.Year(), int(first.Month())),
		float64(first.Unix()), float64(next.Unix())
}

// ensurePartitions makes the table for the month of t and for the month after,
// so an event never waits on DDL and the default partition stays empty.
func ensurePartitions(ctx context.Context, t time.Time) error {
	for _, m := range []time.Time{t, t.UTC().AddDate(0, 1, 0)} {
		if err := ensurePartition(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// ensurePartition makes one month's table if it is not there. It is made
// beside the events table and attached to it, after moving into it anything the
// default partition caught for that month - CREATE ... PARTITION OF would
// refuse while such a row exists, and an event dated a month ahead is one a
// client is allowed to send.
func ensurePartition(ctx context.Context, month time.Time) error {
	name, from, to := monthOf(month)
	partMu.Lock()
	defer partMu.Unlock()
	if partMade[name] {
		return nil
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_inherits i
	                        JOIN pg_class c ON c.oid = i.inhrelid
	                        JOIN pg_class p ON p.oid = i.inhparent
	                        WHERE p.relname = 'events' AND c.relname = ?`, name).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		lo, hi := strconv.FormatFloat(from, 'f', -1, 64), strconv.FormatFloat(to, 'f', -1, 64)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, q := range []string{
			`CREATE TABLE IF NOT EXISTS ` + name + ` (LIKE events INCLUDING DEFAULTS INCLUDING CONSTRAINTS)`,
			`INSERT INTO ` + name + ` SELECT * FROM events_default WHERE at >= ` + lo + ` AND at < ` + hi,
			`DELETE FROM events_default WHERE at >= ` + lo + ` AND at < ` + hi,
			`ALTER TABLE events ATTACH PARTITION ` + name + ` FOR VALUES FROM (` + lo + `) TO (` + hi + `)`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("could not make the events table %s: %w", name, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		log.Printf("ammit: events for %s go to %s", time.Unix(int64(from), 0).UTC().Format("2006-01"), name)
	}
	partMade[name] = true
	return nil
}
