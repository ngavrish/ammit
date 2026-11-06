package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// The record lives in Postgres.
//
// It lived in one SQLite file for its first year, and the file reached
// fourteen gigabytes with a single writer lock in front of every event the
// pipeline sent: an archive pass or a long chart query held that lock and the
// runs' records queued behind it. Postgres takes the writes and the readers
// apart, and the events table is cut by month so a year of history is a set of
// tables nobody has to scan to answer a question about today.
//
// Every query in this service was written for SQLite, with ? placeholders.
// They stay written that way: DB and Tx below turn ? (and SQLite's numbered
// ?1, ?2) into $1, $2 on the way to the driver, outside quoted strings, and
// that is the only translation done at run time. Everything else that differs
// between the two dialects is rewritten in the query itself, where a reader
// can see it.

// DB is the pool, with placeholders translated.
type DB struct{ *sql.DB }

// Tx is a transaction on it, with the same translation.
type Tx struct{ *sql.Tx }

// The calls without a context are the ones this service always made, passed
// through as they were: whether a caller ought to hand down its request's
// context is a question about the caller, and the baseline counts it there.
func (d *DB) Exec(q string, args ...any) (sql.Result, error) {
	return d.DB.Exec(rebind(q), clean(args)...) //nolint:noctx // the callers' choice, as before
}
func (d *DB) Query(q string, args ...any) (*sql.Rows, error) {
	return d.DB.Query(rebind(q), clean(args)...) //nolint:noctx // the callers' choice, as before
}
func (d *DB) QueryRow(q string, args ...any) *sql.Row {
	return d.DB.QueryRow(rebind(q), clean(args)...) //nolint:noctx // the callers' choice, as before
}
func (d *DB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, rebind(q), clean(args)...)
}
func (d *DB) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, rebind(q), clean(args)...)
}
func (d *DB) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, rebind(q), clean(args)...)
}
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{tx}, nil
}

func (t *Tx) Exec(q string, args ...any) (sql.Result, error) {
	return t.Tx.Exec(rebind(q), clean(args)...) //nolint:noctx // the callers' choice, as before
}
func (t *Tx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.Tx.Query(rebind(q), clean(args)...) //nolint:noctx // the callers' choice, as before
}
func (t *Tx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, rebind(q), clean(args)...)
}
func (t *Tx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, rebind(q), clean(args)...)
}
func (t *Tx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, rebind(q), clean(args)...)
}

// rebind turns SQLite's placeholders into Postgres's: each bare ? is the next
// $n, and ?N is $N. Quoted strings, quoted identifiers and comments are copied
// as they are, so a '?' inside a literal stays a question mark.
func rebind(q string) string {
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	n := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(q) {
				if q[j] == c {
					if j+1 < len(q) && q[j+1] == c { // '' or "" inside the quotes
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(q) {
				j = len(q) - 1
			}
			b.WriteString(q[i : j+1])
			i = j
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				b.WriteString(q[i:])
				return b.String()
			}
			b.WriteString(q[i : i+j])
			i += j - 1
		case c == '?':
			j := i + 1
			for j < len(q) && q[j] >= '0' && q[j] <= '9' {
				j++
			}
			if j > i+1 {
				b.WriteString("$" + q[i+1:j])
				i = j - 1
			} else {
				n++
				b.WriteString("$" + strconv.Itoa(n))
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// clean keeps a NUL out of every text argument. SQLite stored one; Postgres
// refuses the whole statement ("invalid byte sequence for encoding UTF8:
// 0x00"), and an event refused for one invisible character is an event lost.
func clean(args []any) []any {
	for i, a := range args {
		if s, ok := a.(string); ok && strings.IndexByte(s, 0) >= 0 {
			args[i] = strings.ReplaceAll(s, "\x00", "�")
		}
	}
	return args
}

// noNUL is clean for a value about to become JSON: jsonb refuses \u0000 the
// same way text refuses the byte.
func noNUL(v any) any {
	switch x := v.(type) {
	case string:
		if strings.IndexByte(x, 0) >= 0 {
			return strings.ReplaceAll(x, "\x00", "�")
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = noNUL(e)
		}
		return x
	case event:
		for k, e := range x {
			x[k] = noNUL(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = noNUL(e)
		}
		return x
	}
	return v
}

// sqlCell is one value of a free-form result as SQLite would have handed it over,
// so /query, the charts and every listing answer with the same JSON they did:
//
//   - numeric (sum and avg of integers, round of a numeric) is a number, not
//     the string the driver carries it in;
//   - a boolean (a comparison in a select list) is 1 or 0;
//   - jsonb is json_extract's answer: a scalar is itself (true is 1), and an
//     object or array is its text, compact and with its keys in the order Go
//     wrote them - which is the text the event was stored as;
//   - bytes are a string, as rows() always made them.
func sqlCell(v any, dbType string) any {
	switch dbType {
	case "NUMERIC":
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case []byte:
			s = string(x)
		default:
			return v
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			if f == float64(int64(f)) && !strings.ContainsAny(s, "eE") && len(s) < 19 {
				return int64(f)
			}
			return f
		}
		return s
	case "BOOL":
		if b, ok := v.(bool); ok {
			if b {
				return int64(1)
			}
			return int64(0)
		}
	case "JSON", "JSONB":
		var raw []byte
		switch x := v.(type) {
		case []byte:
			raw = x
		case string:
			raw = []byte(x)
		default:
			return v
		}
		return jsonCell(raw)
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func jsonCell(raw []byte) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return string(raw)
	}
	switch t := x.(type) {
	case nil:
		return nil
	case bool:
		if t {
			return int64(1)
		}
		return int64(0)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case string:
		return t
	}
	out, err := json.Marshal(x)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// scanCells reads one row of any shape through cell.
func scanCells(rows *sql.Rows, types []string) ([]any, error) {
	cells := make([]any, len(types))
	holders := make([]any, len(types))
	for i := range cells {
		holders[i] = &cells[i]
	}
	if err := rows.Scan(holders...); err != nil {
		return nil, err
	}
	for i := range cells {
		cells[i] = sqlCell(cells[i], types[i])
	}
	return cells, nil
}

func columnTypes(rows *sql.Rows) []string {
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil
	}
	out := make([]string, len(cts))
	for i, c := range cts {
		out[i] = c.DatabaseTypeName()
	}
	return out
}

// pgConfig is where the database is and who this service is there: AMMIT_DSN
// without a password, and the password from Vault, the one instance of every
// secret on this machine - the same sut/ store, the same token and the same
// KEY=value content every other service reads through vault-env.
//
//	AMMIT_DSN              postgres://ammit@postgres:5432/ammit?sslmode=disable
//	VAULT_ADDR             http://vault:8200
//	SUT_VAULT_TOKEN        the stack's read token
//	AMMIT_PG_VAULT         the sut path, e.g. ammit/postgres.env
//	AMMIT_PG_VAULT_KEY     the line in it (default POSTGRES_PASSWORD)
//
// A DSN that carries a password is refused rather than used: a password in an
// environment variable is a password in `docker inspect`, in every crash dump
// and in the compose file that set it.
func pgConfig(dsn string) (*pgx.ConnConfig, error) {
	if dsn == "" {
		return nil, errors.New("AMMIT_DSN is not set: ammit keeps its record in Postgres")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// The parser's message can quote the string it was given.
		return nil, errors.New("AMMIT_DSN is not a Postgres connection string")
	}
	if cfg.Password != "" {
		return nil, errors.New("AMMIT_DSN carries a password: the password comes from Vault " +
			"(AMMIT_PG_VAULT), never from the environment")
	}
	if path := os.Getenv("AMMIT_PG_VAULT"); path != "" {
		key := env("AMMIT_PG_VAULT_KEY", "POSTGRES_PASSWORD")
		var pw string
		var verr error
		// Vault may be coming up beside this container; give it a minute
		// before calling it absent.
		for try := 0; try < 30; try++ {
			if pw, verr = vaultValue(path, key); verr == nil {
				break
			}
			if try == 0 {
				log.Printf("ammit: waiting for Vault: %v", verr)
			}
			time.Sleep(2 * time.Second)
		}
		if verr != nil {
			return nil, verr
		}
		cfg.Password = pw
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = "ammit"
	return cfg, nil
}

// dbLabel is where the record is, for /health and the log: host, port and
// database, and never a credential.
func dbLabel(dsn string) string {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "postgres"
	}
	return fmt.Sprintf("postgres://%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
}

// vaultValue reads one KEY=value line of a sut/ secret, the way vault-env
// does. The value is returned and never logged.
func vaultValue(path, key string) (string, error) {
	addr, token := os.Getenv("VAULT_ADDR"), os.Getenv("SUT_VAULT_TOKEN")
	if addr == "" || token == "" {
		return "", errors.New("AMMIT_PG_VAULT is set but VAULT_ADDR or SUT_VAULT_TOKEN is not")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(addr, "/")+"/v1/sut/data/"+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Vault-Token", token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("no answer from Vault for sut/%s", path)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("no sut/%s in Vault (HTTP %d)", path, resp.StatusCode)
	}
	var out struct {
		Data struct {
			Data struct {
				Content string `json:"content"`
			} `json:"data"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil || out.Data.Data.Content == "" {
		return "", fmt.Errorf("the Vault secret sut/%s has no content", path)
	}
	for _, line := range strings.Split(out.Data.Data.Content, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(k) == key {
			return v, nil
		}
	}
	return "", fmt.Errorf("the Vault secret sut/%s has no %s line", path, key)
}

// openPools opens the writer's pool and the reader's. The reader is the same
// database with every transaction read-only: /query and the charts run SQL a
// caller wrote, and the database refusing a write is the only refusal that
// holds however the statement is spelled.
func openPools(dsn string) (*DB, *DB, error) {
	cfg, err := pgConfig(dsn)
	if err != nil {
		return nil, nil, err
	}
	w := stdlib.OpenDB(*cfg)
	w.SetMaxOpenConns(8)
	w.SetConnMaxIdleTime(5 * time.Minute)
	rcfg := cfg.Copy()
	rcfg.RuntimeParams["default_transaction_read_only"] = "on"
	rcfg.RuntimeParams["application_name"] = "ammit-read"
	r := stdlib.OpenDB(*rcfg)
	r.SetMaxOpenConns(4)
	r.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		if err = w.PingContext(ctx); err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("no answer from Postgres at %s:%d/%s: %w",
			cfg.Host, cfg.Port, cfg.Database, err)
	}
	return &DB{w}, &DB{r}, nil
}

// readOnly runs one caller's query in a read-only transaction of its own, so
// nothing the query does to its session (a set_config, a SET) outlives it.
func readOnly(ctx context.Context, q string, each func(*sql.Rows) error) error {
	tx, err := readDB.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rs, err := tx.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer func() { _ = rs.Close() }()
	if err := each(rs); err != nil {
		return err
	}
	return rs.Err()
}
