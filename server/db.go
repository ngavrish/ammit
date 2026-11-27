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

	"github.com/ngavrish/ammit/adapters/statestore"
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
func (d *DB) Begin() (*Tx, error) {
	txn, err := d.DB.Begin() //nolint:noctx // the callers' choice, as before
	if err != nil {
		return nil, err
	}
	return &Tx{txn}, nil
}
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	txn, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{txn}, nil
}

func (t *Tx) Exec(q string, args ...any) (sql.Result, error) {
	return t.Tx.Exec(rebind(q), clean(args)...) //nolint:noctx // the callers' choice, as before
}
func (t *Tx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.Tx.Query(rebind(q), clean(args)...) //nolint:noctx // the callers' choice, as before
}
func (t *Tx) QueryRow(q string, args ...any) *sql.Row {
	return t.Tx.QueryRow(rebind(q), clean(args)...) //nolint:noctx // the callers' choice, as before
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
	// The sealed build keeps the record in its embedded store, which reads
	// SQLite (adapters/statestore); a SaaS build goes on to Postgres below.
	if statestore.Embedded {
		return statestore.Translate(q)
	}
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	placeholder := 0
	for pos := 0; pos < len(q); pos++ {
		char := q[pos]
		switch {
		case char == '\'' || char == '"':
			quoteEnd := pos + 1
			for quoteEnd < len(q) {
				if q[quoteEnd] == char {
					if quoteEnd+1 < len(q) && q[quoteEnd+1] == char { // '' or "" inside the quotes
						quoteEnd += 2
						continue
					}
					break
				}
				quoteEnd++
			}
			if quoteEnd >= len(q) {
				quoteEnd = len(q) - 1
			}
			b.WriteString(q[pos : quoteEnd+1])
			pos = quoteEnd
		case char == '-' && pos+1 < len(q) && q[pos+1] == '-':
			lineEnd := strings.IndexByte(q[pos:], '\n')
			if lineEnd < 0 {
				b.WriteString(q[pos:])
				return b.String()
			}
			b.WriteString(q[pos : pos+lineEnd])
			pos += lineEnd - 1
		case char == '?':
			digitsEnd := pos + 1
			for digitsEnd < len(q) && q[digitsEnd] >= '0' && q[digitsEnd] <= '9' {
				digitsEnd++
			}
			if digitsEnd > pos+1 {
				b.WriteString("$" + q[pos+1:digitsEnd])
				pos = digitsEnd - 1
			} else {
				placeholder++
				b.WriteString("$" + strconv.Itoa(placeholder))
			}
		default:
			b.WriteByte(char)
		}
	}
	return b.String()
}

// clean keeps a NUL out of every text argument. SQLite stored one; Postgres
// refuses the whole statement ("invalid byte sequence for encoding UTF8:
// 0x00"), and an event refused for one invisible character is an event lost.
func clean(args []any) []any {
	for index, arg := range args {
		if str, ok := arg.(string); ok && strings.IndexByte(str, 0) >= 0 {
			args[index] = strings.ReplaceAll(str, "\x00", "�")
		}
	}
	return args
}

// noNUL is clean for a value about to become JSON: jsonb refuses \u0000 the
// same way text refuses the byte.
func noNUL(v any) any {
	switch typed := v.(type) {
	case string:
		if strings.IndexByte(typed, 0) >= 0 {
			return strings.ReplaceAll(typed, "\x00", "�")
		}
		return typed
	case map[string]any:
		for field, fieldValue := range typed {
			typed[field] = noNUL(fieldValue)
		}
		return typed
	case event:
		for field, fieldValue := range typed {
			typed[field] = noNUL(fieldValue)
		}
		return typed
	case []any:
		for index, member := range typed {
			typed[index] = noNUL(member)
		}
		return typed
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
		switch typed := v.(type) {
		case string:
			s = typed
		case []byte:
			s = string(typed)
		default:
			return v
		}
		if whole, err := strconv.ParseInt(s, 10, 64); err == nil {
			return whole
		}
		if number, err := strconv.ParseFloat(s, 64); err == nil {
			if number == float64(int64(number)) && !strings.ContainsAny(s, "eE") && len(s) < 19 {
				return int64(number)
			}
			return number
		}
		return s
	case "BOOL":
		if flag, ok := v.(bool); ok {
			if flag {
				return int64(1)
			}
			return int64(0)
		}
	case "JSON", "JSONB":
		var raw []byte
		switch typed := v.(type) {
		case []byte:
			raw = typed
		case string:
			raw = []byte(typed)
		default:
			return v
		}
		return jsonCell(raw)
	}
	if cellBytes, ok := v.([]byte); ok {
		return string(cellBytes)
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
	switch decoded := x.(type) {
	case nil:
		return nil
	case bool:
		if decoded {
			return int64(1)
		}
		return int64(0)
	case json.Number:
		if whole, err := decoded.Int64(); err == nil {
			return whole
		}
		if number, err := decoded.Float64(); err == nil {
			return number
		}
		return decoded.String()
	case string:
		return decoded
	}
	encoded, err := json.Marshal(x)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

// scanCells reads one row of any shape through cell.
func scanCells(rows *sql.Rows, types []string) ([]any, error) {
	cells := make([]any, len(types))
	holders := make([]any, len(types))
	for index := range cells {
		holders[index] = &cells[index]
	}
	if err := rows.Scan(holders...); err != nil {
		return nil, err
	}
	for index := range cells {
		cells[index] = sqlCell(cells[index], types[index])
	}
	return cells, nil
}

func columnTypes(rows *sql.Rows) []string {
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil
	}
	types := make([]string, len(cts))
	for index, colType := range cts {
		types[index] = colType.DatabaseTypeName()
	}
	return types
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
	pgConf, err := pgx.ParseConfig(dsn)
	if err != nil {
		// The parser's message can quote the string it was given.
		return nil, errors.New("AMMIT_DSN is not a Postgres connection string")
	}
	if pgConf.Password != "" {
		return nil, errors.New("AMMIT_DSN carries a password: the password comes from Vault " +
			"(AMMIT_PG_VAULT), never from the environment")
	}
	if path := os.Getenv("AMMIT_PG_VAULT"); path != "" {
		vaultKey := env("AMMIT_PG_VAULT_KEY", "POSTGRES_PASSWORD")
		var pw string
		var verr error
		// Vault may be coming up beside this container; give it a minute
		// before calling it absent.
		for try := 0; try < 30; try++ {
			if pw, verr = vaultValue(path, vaultKey); verr == nil {
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
		pgConf.Password = pw
	}
	if pgConf.RuntimeParams == nil {
		pgConf.RuntimeParams = map[string]string{}
	}
	pgConf.RuntimeParams["application_name"] = "ammit"
	return pgConf, nil
}

// dbLabel is where the record is, for /health and the log: host, port and
// database, and never a credential.
func dbLabel(dsn string) string {
	if statestore.Embedded {
		return statestore.Label()
	}
	pgConf, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "postgres"
	}
	return fmt.Sprintf("postgres://%s:%d/%s", pgConf.Host, pgConf.Port, pgConf.Database)
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
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
	if json.Unmarshal(respBody, &out) != nil || out.Data.Data.Content == "" {
		return "", fmt.Errorf("the Vault secret sut/%s has no content", path)
	}
	for _, envLine := range strings.Split(out.Data.Data.Content, "\n") {
		envName, envValue, ok := strings.Cut(strings.TrimSpace(envLine), "=")
		if ok && strings.TrimSpace(envName) == key {
			return envValue, nil
		}
	}
	return "", fmt.Errorf("the Vault secret sut/%s has no %s line", path, key)
}

// openPools opens the writer's pool and the reader's. The reader is the same
// database with every transaction read-only: /query and the charts run SQL a
// caller wrote, and the database refusing a write is the only refusal that
// holds however the statement is spelled.
func openPools(dsn string) (*DB, *DB, error) {
	if statestore.Embedded {
		writer, reader, err := statestore.Open(strings.TrimSuffix(env("AMMIT_DATA", "/data"), "/"))
		if err != nil {
			return nil, nil, err
		}
		return &DB{writer}, &DB{reader}, nil
	}
	pgConf, err := pgConfig(dsn)
	if err != nil {
		return nil, nil, err
	}
	writer := stdlib.OpenDB(*pgConf)
	writer.SetMaxOpenConns(8)
	writer.SetConnMaxIdleTime(5 * time.Minute)
	rcfg := pgConf.Copy()
	rcfg.RuntimeParams["default_transaction_read_only"] = "on"
	rcfg.RuntimeParams["application_name"] = "ammit-read"
	reader := stdlib.OpenDB(*rcfg)
	reader.SetMaxOpenConns(4)
	reader.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		if err = writer.PingContext(ctx); err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("no answer from Postgres at %s:%d/%s: %w",
			pgConf.Host, pgConf.Port, pgConf.Database, err)
	}
	return &DB{writer}, &DB{reader}, nil
}

// readOnly runs one caller's query in a read-only transaction of its own, so
// nothing the query does to its session (a set_config, a SET) outlives it.
func readOnly(ctx context.Context, q string, each func(*sql.Rows) error) error {
	txn, err := readDB.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = txn.Rollback() }()
	queried, err := txn.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer func() { _ = queried.Close() }()
	if err := each(queried); err != nil {
		return err
	}
	return queried.Err()
}
