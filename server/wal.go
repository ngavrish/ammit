package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"
)

// One writer, many readers - what WAL is for.
//
// Every read and every write used to queue on one Go mutex, so one slow
// SELECT, or the writer busy for a while, held POST /events for as long as it
// lasted: ~400s on 2026-11-03 (run 6d42457b), and the late heartbeats behind
// it got live runs cancelled by this service's own sweeps. And the WAL never
// shrank, because the checkpoint needs a moment with no reader pinning an old
// snapshot and the lock never let go long enough to have one.
//
// Now: db is a pool of exactly one connection, so database/sql itself queues
// the writers (SQLite allows one at a time anyway), and readDB is a separate
// read-only pool that WAL lets run beside it. A read-then-write that has to be
// atomic is a transaction on the writer (inTx), never a lock around two
// statements. Nothing the writer runs may wait on db again from inside a
// transaction: the one connection is the transaction's, and the second call
// would wait for it forever.

// The read pool's size. Readers do not block each other or the writer in WAL;
// eight is more panels than the page draws at once.
const readConns = 8

// dbx is a statement's target: the writer, or a transaction on it. Helpers
// that run inside a caller's transaction take one of these rather than
// reaching for db.
type dbx interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readCtx bounds a read on readDB. A read holds a WAL snapshot for as long as
// it runs, and a snapshot nobody lets go is a WAL the checkpoint can never
// truncate: so every read ends, at the latest, at the same deadline /query and
// the charts have always had. parent is the caller's context: a request's,
// or bg on the service's own loops.
func readCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, _QUERY_DEADLINE)
}

// mu is no longer a lock anything in the service takes. It is the writer held
// by hand - Lock takes the one writer connection, Unlock gives it back - kept
// under the old name for absence_test.go, which holds the writer to make a
// record wait for it. Lock and Unlock from one goroutine at a time.
var mu writerHold

type writerHold struct{ conn *sql.Conn }

func (h *writerHold) Lock() {
	conn, err := db.Conn(bg)
	if err != nil {
		log.Printf("ammit: could not hold the writer: %v", err)
		return
	}
	h.conn = conn
}

func (h *writerHold) Unlock() {
	if h.conn != nil {
		_ = h.conn.Close()
		h.conn = nil
	}
}

// readTx runs fn over one snapshot on the read pool: every statement in it
// sees the database as the first one did, so facts gathered by several reads
// agree with each other, as they did when a global lock kept writes out from
// between them. fn's statements take ctx, which carries the read deadline.
func readTx(parent context.Context, fn func(ctx context.Context, x dbx) error) error {
	ctx, cancel := readCtx(parent)
	defer cancel()
	tx, err := readDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return fn(ctx, tx)
}

// inTx runs fn as one transaction on the writer. The DSN's _txlock makes it
// BEGIN IMMEDIATE, so the write lock is taken at the start and a read inside
// fn sees what nothing else can change before the commit. An error from fn,
// or a panic in it, rolls back: a transaction left open would keep the only
// writer connection forever.
func inTx(fn func(tx dbx) error) error {
	return inTxCtx(bg, fn)
}

// inTxCtx is inTx for a caller that has a context. A handler passes its
// request's without the cancel (context.WithoutCancel): a write the client
// hung up on is still kept, as it always was.
func inTxCtx(ctx context.Context, fn func(tx dbx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			if rbErr := tx.Rollback(); rbErr != nil {
				log.Printf("ammit: rollback: %v", rbErr)
			}
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	done = true
	return tx.Commit()
}

// The writer's DSN: WAL, a 30s wait for a lock held by another process (the
// sqlite3 shell, a backup), synchronous NORMAL - in WAL that is durable at
// every checkpoint and loses at most the last commits on a power cut, never
// the file - and BEGIN IMMEDIATE for every transaction.
func writerDSN(path string) string {
	return path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)" +
		"&_pragma=synchronous(NORMAL)&_txlock=immediate"
}

// The readers' DSN: the same file, read-only twice over - mode=ro for the
// connection, query_only for the statement - because /query and the charts
// run SQL anyone can save, and read-only must be the database's own refusal.
func readerDSN(path string) string {
	return "file:" + path + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(30000)"
}

// How often the WAL is folded back into the database and cut to nothing,
// unless retention.checkpoint_seconds says otherwise.
const checkpointEvery = 5 * time.Minute

// checkpointLoop runs PRAGMA wal_checkpoint(TRUNCATE) on the writer every
// retention.checkpoint_seconds. SQLite's own automatic checkpoint is PASSIVE:
// it copies what no reader still needs and never truncates, so a busy service
// keeps a WAL as large as its worst moment. TRUNCATE waits (busy_timeout) for
// readers to move off old snapshots, copies everything, and empties the file;
// when a reader is still holding one it cannot complete, and says so here
// with the size the file has reached.
func checkpointLoop(confPath, dbPath string) {
	for {
		every := checkpointEvery
		if s, ok := loadConfig(confPath).num("retention", "checkpoint_seconds"); ok && s > 0 {
			every = time.Duration(s * float64(time.Second))
		}
		time.Sleep(every)
		checkpoint(dbPath)
	}
}

func checkpoint(dbPath string) {
	// Bounded twice, because every record waits while the writer does this:
	// the copy by a context (an interrupted checkpoint is simply incomplete,
	// and the next one starts over), and the wait for readers to move off old
	// snapshots by a busy timeout of a second rather than the writer's thirty.
	ctx, cancel := context.WithTimeout(bg, 2*time.Minute)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		log.Printf("ammit: checkpoint: no writer (%v)", err)
		return
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout = 1000`); err != nil {
		log.Printf("ammit: checkpoint: %v", err)
		return
	}
	defer func() {
		if _, err := conn.ExecContext(bg, `PRAGMA busy_timeout = 30000`); err != nil {
			log.Printf("ammit: checkpoint: the writer's busy timeout was not restored: %v", err)
		}
	}()
	was := walBytes(dbPath)
	began := time.Now()
	var busy, logFrames, moved int
	err = conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &moved)
	took := time.Since(began).Round(time.Millisecond)
	now := walBytes(dbPath)
	switch {
	case err != nil:
		log.Printf("ammit: checkpoint failed after %s (%v); the WAL is %s", took, err, mb(now))
	case busy != 0:
		log.Printf("ammit: checkpoint could not complete - a reader holds an old snapshot "+
			"(%d of %d frames copied in %s); the WAL is %s", moved, logFrames, took, mb(now))
	case was >= walWorthSaying || took > time.Second:
		// A truncated WAL reports no frames at all, so the sizes are the news.
		log.Printf("ammit: checkpoint cut the WAL from %s to %s in %s", mb(was), mb(now), took)
	}
}

// A WAL this large before its checkpoint is worth a line; the ordinary few
// minutes of records are not.
const walWorthSaying = 64 << 20

func walBytes(dbPath string) int64 {
	st, err := os.Stat(dbPath + "-wal")
	if err != nil {
		return 0
	}
	return st.Size()
}

func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }
