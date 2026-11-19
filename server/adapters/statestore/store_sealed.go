//go:build sealed

// Package statestore is ammit's StateStore port: where the record is kept.
//
// The sealed binary (this file) keeps the record in one SQLite file,
// AMMIT_DATA/ammit.db, inside the revisore process: no Postgres, no second
// process. ammit's queries are written for SQLite with ? placeholders and
// rewritten for Postgres where the two differ (db.go); Translate takes those
// Postgres spellings back to SQLite's, outside quoted strings, and Migrate
// makes the tables from the same DDL the SaaS build runs, minus what only
// Postgres has (partitions, tsvector, plpgsql). What SQLite has no word for
// - jnum, trunc, lead_int, regexp, jsonb_typeof - is a Go function registered
// on every connection.
package statestore

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Embedded says the record is in the embedded store, not Postgres.
const Embedded = true

var (
	label    string
	register sync.Once
)

// Label is where the record is, for /health and the log.
func Label() string { return label }

// Open is the two pools on AMMIT_DATA/ammit.db: one that writes, and one that
// only reads (the free-form /query and the charts run there; SQLite refuses a
// write on a read-only connection as Postgres did on its read-only one).
func Open(dataDir string) (*sql.DB, *sql.DB, error) {
	register.Do(registerFunctions)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dataDir, "ammit.db")
	label = "sqlite:" + path
	common := "&_pragma=busy_timeout(60000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	w, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_txlock=immediate"+common)
	if err != nil {
		return nil, nil, err
	}
	w.SetMaxOpenConns(8)
	w.SetConnMaxIdleTime(5 * time.Minute)
	if err := w.Ping(); err != nil { //nolint:noctx // once, at start
		return nil, nil, fmt.Errorf("no database at %s: %w", path, err)
	}
	r, err := sql.Open("sqlite", "file:"+path+"?mode=ro"+common)
	if err != nil {
		return nil, nil, err
	}
	r.SetMaxOpenConns(4)
	r.SetConnMaxIdleTime(5 * time.Minute)
	_ = os.Chmod(path, 0o600)
	return w, r, nil
}

// Migrate makes the tables from ammit's own DDL, statement by statement. A
// column that is already there, or a statement only Postgres has, is not an
// error: the schema is the same one, brought as far as SQLite can carry it.
func Migrate(db *sql.DB, ddl ...string) error {
	for _, block := range ddl {
		for _, stmt := range splitStatements(block) {
			q := translateDDL(stmt)
			if strings.TrimSpace(q) == "" {
				continue
			}
			if _, err := db.Exec(q); err != nil { //nolint:noctx // once, at start
				msg := err.Error()
				if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
					continue
				}
				return fmt.Errorf("%s: %w", firstLine(q), err)
			}
		}
	}
	return nil
}

// UniqueViolation is SQLite's word for Postgres's 23505.
func UniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

// ---- DDL ----------------------------------------------------------------

var (
	reIdentityPK = regexp.MustCompile(`(?i)\bbigint\s+GENERATED\s+BY\s+DEFAULT\s+AS\s+IDENTITY\s+PRIMARY\s+KEY`)
	reIdentity   = regexp.MustCompile(`(?i)\bbigint\s+GENERATED\s+BY\s+DEFAULT\s+AS\s+IDENTITY`)
	rePKIdAt     = regexp.MustCompile(`(?is),\s*PRIMARY\s+KEY\s*\(\s*id\s*,\s*at\s*\)`)
	rePartition  = regexp.MustCompile(`(?i)\)\s*PARTITION\s+BY\s+RANGE\s*\([^)]*\)\s*$`)
	reTSVector   = regexp.MustCompile(`(?is),\s*words\s+tsvector\s+GENERATED\s+ALWAYS\s+AS\s*\(.*?\)\s*STORED`)
	reAddIfNot   = regexp.MustCompile(`(?i)ADD\s+COLUMN\s+IF\s+NOT\s+EXISTS`)
	reBytea      = regexp.MustCompile(`(?i)\bbytea\b`)
	reJSONBType  = regexp.MustCompile(`(?i)\bjsonb\b`)
)

func translateDDL(stmt string) string {
	s := stripComments(stmt)
	t := strings.ToUpper(strings.Join(strings.Fields(s), " "))
	switch {
	case strings.HasPrefix(t, "CREATE OR REPLACE FUNCTION"),
		strings.HasPrefix(t, "CREATE SCHEMA"),
		strings.Contains(t, " PARTITION OF "),
		strings.Contains(t, " USING GIN"),
		strings.HasPrefix(t, "SELECT PG_"):
		return ""
	}
	if reIdentity.MatchString(s) && rePKIdAt.MatchString(s) {
		s = reIdentity.ReplaceAllString(s, "INTEGER PRIMARY KEY AUTOINCREMENT")
		s = rePKIdAt.ReplaceAllString(s, "")
	}
	s = reIdentityPK.ReplaceAllString(s, "INTEGER PRIMARY KEY AUTOINCREMENT")
	s = reIdentity.ReplaceAllString(s, "INTEGER")
	s = rePartition.ReplaceAllString(strings.TrimSpace(s), ")")
	s = reTSVector.ReplaceAllString(s, "")
	s = reAddIfNot.ReplaceAllString(s, "ADD COLUMN")
	s = reBytea.ReplaceAllString(s, "BLOB")
	s = reJSONBType.ReplaceAllString(s, "TEXT")
	return Translate(s)
}

// stripComments drops -- comments outside quotes.
func stripComments(q string) string {
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'' || c == '"':
			j := skipQuoted(q, i)
			b.WriteString(q[i:j])
			i = j - 1
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			for i < len(q) && q[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// splitStatements cuts a block of DDL at the semicolons outside quotes and
// $$ bodies.
func splitStatements(block string) []string {
	var out []string
	start := 0
	for i := 0; i < len(block); i++ {
		c := block[i]
		switch {
		case c == '\'' || c == '"':
			i = skipQuoted(block, i) - 1
		case c == '-' && i+1 < len(block) && block[i+1] == '-':
			for i < len(block) && block[i] != '\n' {
				i++
			}
		case c == '$' && i+1 < len(block) && block[i+1] == '$':
			j := strings.Index(block[i+2:], "$$")
			if j < 0 {
				i = len(block)
			} else {
				i += j + 3
			}
		case c == ';':
			out = append(out, block[start:i])
			start = i + 1
		}
	}
	if strings.TrimSpace(block[start:]) != "" {
		out = append(out, block[start:])
	}
	return out
}

// skipQuoted is the index just past the quoted string or identifier at i.
func skipQuoted(q string, i int) int {
	c := q[i]
	j := i + 1
	for j < len(q) {
		if q[j] == c {
			if j+1 < len(q) && q[j+1] == c {
				j += 2
				continue
			}
			return j + 1
		}
		j++
	}
	return len(q)
}

// ---- queries ------------------------------------------------------------

var (
	cacheMu sync.RWMutex
	cache   = map[string]string{}
)

// Translate is a Postgres spelling of an ammit query in SQLite's words. The
// placeholders stay SQLite's own (? and ?N, as the queries are written).
func Translate(q string) string {
	cacheMu.RLock()
	t, ok := cache[q]
	cacheMu.RUnlock()
	if ok {
		return t
	}
	t = translate(q)
	cacheMu.Lock()
	if len(cache) > 4096 {
		cache = map[string]string{}
	}
	cache[q] = t
	cacheMu.Unlock()
	return t
}

var (
	reInfoSchema  = regexp.MustCompile(`(?is)^\s*SELECT\s+count\(\*\)\s+FROM\s+information_schema\.columns.*$`)
	reAdvisory    = regexp.MustCompile(`(?is)^\s*SELECT\s+pg_advisory_(un)?lock\(.*\)\s*$`)
	reILike       = regexp.MustCompile(`(?i)\bILIKE\b`)
	reNotDistinct = regexp.MustCompile(`(?i)\bIS\s+NOT\s+DISTINCT\s+FROM\b`)
	reDistinct    = regexp.MustCompile(`(?i)\bIS\s+DISTINCT\s+FROM\b`)
	reGreatest    = regexp.MustCompile(`(?i)\bgreatest\s*\(`)
	reLeast       = regexp.MustCompile(`(?i)\bleast\s*\(`)
	reRegexOp     = regexp.MustCompile(`\s!~\s`)
	reRegexOp2    = regexp.MustCompile(`\s~\s`)
	reForUpdate   = regexp.MustCompile(`(?i)\s+FOR\s+UPDATE\b`)
	reStringAggD  = regexp.MustCompile(`(?i)string_agg\(\s*DISTINCT\s+([^,()]+?)\s*,\s*','\s*\)`)
	reStringAgg   = regexp.MustCompile(`(?i)string_agg\(`)
	reToChar      = regexp.MustCompile(`(?i)to_char\(\s*to_timestamp\(([^()]*)\)\s+AT\s+TIME\s+ZONE\s+'UTC'\s*,\s*'YYYY-MM-DD'\s*\)`)
	reArrayElems  = regexp.MustCompile(`(?i)jsonb_array_elements\(([^()]*)\)\s+WITH\s+ORDINALITY\s+AS\s+(\w+)\(\s*(\w+)\s*,\s*(\w+)\s*\)`)
	reDistinctOn  = regexp.MustCompile(`(?is)SELECT\s+DISTINCT\s+ON\s*\(\s*(\w+)\s*\)\s+(.*?)\s+FROM\s+(\w+)\s+WHERE\s+(.*?)\s+ORDER\s+BY\s+(\w+)\s*,\s*(\w+)\s+DESC\s*\)`)
	reConflictNil = regexp.MustCompile(`(?is)\s+ON\s+CONFLICT\s*(\([^)]*\))?\s*DO\s+NOTHING\s*$`)
	reInsertInto  = regexp.MustCompile(`(?i)^\s*INSERT\s+INTO\b`)
	reCurSchema   = regexp.MustCompile(`(?i)\bpublic\.`)
	reStrpos      = regexp.MustCompile(`(?i)\bstrpos\s*\(`)
	reUpdateAlias = regexp.MustCompile(`(?i)^(\s*UPDATE\s+\w+)\s+(\w+)\s+SET\b`)
	reDeleteAlias = regexp.MustCompile(`(?i)^(\s*DELETE\s+FROM\s+\w+)\s+(\w+)\s+WHERE\b`)
)

func translate(q string) string {
	if reInfoSchema.MatchString(q) {
		return "SELECT 0"
	}
	if reAdvisory.MatchString(q) {
		return "SELECT 1"
	}
	s := q
	// jsonb_array_elements(x) WITH ORDINALITY AS r(v, ord): json_each's
	// value is v, its key (from 0) is ord - 1.
	if m := reArrayElems.FindStringSubmatch(s); m != nil {
		alias, v, ord := m[2], m[3], m[4]
		s = strings.Replace(s, m[0], "json_each("+m[1]+") AS "+alias, 1)
		s = strings.ReplaceAll(s, alias+"."+v+"-", alias+".value-")
		s = strings.ReplaceAll(s, alias+"."+ord, "("+alias+".key+1)")
	}
	s = arrows(casts(s))
	s = codeOnly(s, func(c string) string {
		c = reILike.ReplaceAllString(c, "LIKE")
		c = reNotDistinct.ReplaceAllString(c, "IS")
		c = reDistinct.ReplaceAllString(c, "IS NOT")
		c = reGreatest.ReplaceAllString(c, "max(")
		c = reLeast.ReplaceAllString(c, "min(")
		c = reRegexOp.ReplaceAllString(c, " NOT REGEXP ")
		c = reRegexOp2.ReplaceAllString(c, " REGEXP ")
		c = reForUpdate.ReplaceAllString(c, "")
		c = reCurSchema.ReplaceAllString(c, "")
		c = reStrpos.ReplaceAllString(c, "instr(")
		return c
	})
	// UPDATE t a SET / DELETE FROM t a WHERE: SQLite names an alias with AS.
	if m := reUpdateAlias.FindStringSubmatch(s); m != nil {
		s = reUpdateAlias.ReplaceAllString(s, "$1 AS $2 SET")
		// RETURNING names the modified table's columns bare.
		if i := strings.LastIndex(strings.ToUpper(s), "RETURNING "); i > 0 {
			s = s[:i] + strings.ReplaceAll(s[i:], m[2]+".", "")
		}
	}
	s = reDeleteAlias.ReplaceAllString(s, "$1 AS $2 WHERE")
	s = reStringAggD.ReplaceAllString(s, "group_concat(DISTINCT $1)")
	s = reStringAgg.ReplaceAllString(s, "group_concat(")
	s = reToChar.ReplaceAllString(s, "strftime('%Y-%m-%d', $1, 'unixepoch')")
	// DISTINCT ON (k) cols FROM t WHERE w ORDER BY k, o DESC) - the last row of
	// each k by o: SQLite's bare columns beside max(o) are that row's.
	s = reDistinctOn.ReplaceAllString(s, "SELECT $2, max($6) FROM $3 WHERE $4 GROUP BY $1)")
	// INSERT ... SELECT ... ON CONFLICT DO NOTHING: SQLite cannot parse the
	// clause after a SELECT with no WHERE; OR IGNORE says the same.
	if reInsertInto.MatchString(s) && reConflictNil.MatchString(s) {
		s = reConflictNil.ReplaceAllString(s, "")
		s = reInsertInto.ReplaceAllString(s, "INSERT OR IGNORE INTO")
	}
	return s
}

// codeOnly applies f to the parts of q outside quoted strings and identifiers.
func codeOnly(q string, f func(string) string) string {
	var b strings.Builder
	start := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' || c == '"' {
			b.WriteString(f(q[start:i]))
			j := skipQuoted(q, i)
			b.WriteString(q[i:j])
			start = j
			i = j - 1
		}
	}
	b.WriteString(f(q[start:]))
	return b.String()
}

// casts turns every operand::type into CAST(operand AS type), outside quotes.
func casts(q string) string {
	if !strings.Contains(q, "::") {
		return q
	}
	out := []byte{}
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' || c == '"' {
			j := skipQuoted(q, i)
			out = append(out, q[i:j]...)
			i = j - 1
			continue
		}
		if c == ':' && i+1 < len(q) && q[i+1] == ':' {
			typ, n := castType(q[i+2:])
			if typ == "" {
				out = append(out, c)
				continue
			}
			st := operandStart(out)
			operand := string(out[st:])
			out = append(out[:st], []byte("CAST("+operand+" AS "+typ+")")...)
			i += 1 + n
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// arrows turns every x->'key' (not ->>) into json_extract(x, '$."key"'):
// Postgres's -> hands over a jsonb that ammit reads the way it read SQLite's
// json_extract (db.go sqlCell), and json_extract is that answer in SQLite,
// where -> would be the JSON text.
func arrows(q string) string {
	if !strings.Contains(q, "->'") {
		return q
	}
	out := []byte{}
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' || c == '"' {
			j := skipQuoted(q, i)
			out = append(out, q[i:j]...)
			i = j - 1
			continue
		}
		if c == '-' && i+2 < len(q) && q[i+1] == '>' && q[i+2] == '\'' {
			j := skipQuoted(q, i+2)
			key := strings.ReplaceAll(q[i+3:j-1], "''", "'")
			st := operandStart(out)
			operand := strings.TrimRight(string(out[st:]), " ")
			path := "$.\"" + strings.ReplaceAll(key, "'", "''") + "\""
			out = append(out[:st], []byte("json_extract("+operand+", '"+path+"')")...)
			i = j - 1
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

var castTypes = []struct{ pg, lite string }{
	{"double precision", "REAL"}, {"float8", "REAL"}, {"float4", "REAL"}, {"numeric", "REAL"},
	{"real", "REAL"}, {"bigint", "INTEGER"}, {"integer", "INTEGER"}, {"int8", "INTEGER"},
	{"int4", "INTEGER"}, {"int", "INTEGER"}, {"smallint", "INTEGER"}, {"boolean", "INTEGER"},
	{"bool", "INTEGER"}, {"jsonb", "TEXT"}, {"json", "TEXT"}, {"text", "TEXT"},
	{"varchar", "TEXT"}, {"bytea", "BLOB"},
}

// castType reads the type after ::, and how many bytes it took.
func castType(s string) (string, int) {
	low := strings.ToLower(s)
	for _, t := range castTypes {
		if strings.HasPrefix(low, t.pg) {
			n := len(t.pg)
			if n < len(low) && (isWord(low[n])) {
				continue
			}
			return t.lite, n
		}
	}
	return "", 0
}

func isWord(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// operandStart is where the operand that ends out begins: a parenthesised
// group with the function name before it, a quoted literal, or a run of name
// characters (identifier, number, placeholder, a.b).
func operandStart(out []byte) int {
	i := len(out) - 1
	for i >= 0 && out[i] == ' ' {
		i--
	}
	if i < 0 {
		return 0
	}
	switch out[i] {
	case ')':
		depth := 0
	scan:
		for ; i >= 0; i-- {
			switch out[i] {
			case ')':
				depth++
			case '(':
				depth--
				if depth == 0 {
					break scan
				}
			}
		}
		for i > 0 && (isWord(out[i-1]) || out[i-1] == '.') {
			i--
		}
		return max(i, 0)
	case '\'':
		j := i - 1
		for j >= 0 {
			if out[j] == '\'' {
				if j > 0 && out[j-1] == '\'' {
					j -= 2
					continue
				}
				return j
			}
			j--
		}
		return 0
	}
	for i >= 0 && (isWord(out[i]) || out[i] == '.' || out[i] == '?' || out[i] == '$') {
		i--
	}
	return i + 1
}

// ---- functions SQLite has no word for -----------------------------------

func registerFunctions() {
	sqlite.MustRegisterDeterministicScalarFunction("jnum", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		return jnum(a[0]), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("trunc", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		f, ok := num(a[0])
		if !ok {
			return nil, nil
		}
		return math.Trunc(f), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("lead_int", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		s, ok := text(a[0])
		if !ok {
			return nil, nil
		}
		m := leadInt.FindStringSubmatch(s)
		if m == nil {
			return int64(0), nil
		}
		n, _ := strconv.ParseInt(m[1], 10, 64)
		return n, nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("regexp", 2, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		pat, _ := text(a[0])
		s, ok := text(a[1])
		if !ok {
			return nil, nil
		}
		re, err := compiled(pat)
		if err != nil {
			return nil, err
		}
		if re.MatchString(s) {
			return int64(1), nil
		}
		return int64(0), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("jsonb_typeof", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		s, ok := text(a[0])
		if !ok {
			return nil, nil
		}
		var x any
		if json.Unmarshal([]byte(s), &x) != nil {
			return nil, nil
		}
		switch x.(type) {
		case map[string]any:
			return "object", nil
		case []any:
			return "array", nil
		case string:
			return "string", nil
		case float64:
			return "number", nil
		case bool:
			return "boolean", nil
		}
		return "null", nil
	})
}

var (
	leadInt  = regexp.MustCompile(`^\s*([-+]?[0-9]{1,18})`)
	numText  = regexp.MustCompile(`^\s*[-+]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][-+]?[0-9]+)?\s*$`)
	reMu     sync.Mutex
	reCached = map[string]*regexp.Regexp{}
)

func compiled(p string) (*regexp.Regexp, error) {
	reMu.Lock()
	defer reMu.Unlock()
	if re, ok := reCached[p]; ok {
		return re, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	reCached[p] = re
	return re, nil
}

func text(v driver.Value) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), true
	}
	return "", false
}

func num(v driver.Value) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case []byte:
		f, err := strconv.ParseFloat(strings.TrimSpace(string(x)), 64)
		return f, err == nil
	}
	return 0, false
}

// jnum is schema.go's jnum on a JSON value as SQLite's -> hands it over (its
// JSON text), or on a plain SQL value: a number is itself, a boolean 1 or 0,
// a string that reads as a number that number, anything else NULL.
func jnum(v driver.Value) driver.Value {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		return float64(x)
	case float64:
		return x
	}
	s, _ := text(v)
	var j any
	if json.Unmarshal([]byte(s), &j) != nil {
		return nil
	}
	switch t := j.(type) {
	case float64:
		return t
	case bool:
		if t {
			return float64(1)
		}
		return float64(0)
	case string:
		if numText.MatchString(t) {
			f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
			if err == nil {
				return f
			}
		}
	}
	return nil
}
