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
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_txlock=immediate"+common)
	if err != nil {
		return nil, nil, err
	}
	writer.SetMaxOpenConns(8)
	writer.SetConnMaxIdleTime(5 * time.Minute)
	if err := writer.Ping(); err != nil { //nolint:noctx // once, at start
		return nil, nil, fmt.Errorf("no database at %s: %w", path, err)
	}
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro"+common)
	if err != nil {
		return nil, nil, err
	}
	reader.SetMaxOpenConns(4)
	reader.SetConnMaxIdleTime(5 * time.Minute)
	_ = os.Chmod(path, 0o600)
	return writer, reader, nil
}

// Migrate makes the tables from ammit's own DDL, statement by statement. A
// column that is already there, or a statement only Postgres has, is not an
// error: the schema is the same one, brought as far as SQLite can carry it.
func Migrate(db *sql.DB, ddl ...string) error {
	for _, block := range ddl {
		for _, stmt := range splitStatements(block) {
			translated := translateDDL(stmt)
			if strings.TrimSpace(translated) == "" {
				continue
			}
			if _, err := db.Exec(translated); err != nil { //nolint:noctx // once, at start
				msg := err.Error()
				if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
					continue
				}
				return fmt.Errorf("%s: %w", firstLine(translated), err)
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
	if newline := strings.IndexByte(s, '\n'); newline > 0 {
		return s[:newline]
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
	bare := stripComments(stmt)
	upper := strings.ToUpper(strings.Join(strings.Fields(bare), " "))
	switch {
	case strings.HasPrefix(upper, "CREATE OR REPLACE FUNCTION"),
		strings.HasPrefix(upper, "CREATE SCHEMA"),
		strings.Contains(upper, " PARTITION OF "),
		strings.Contains(upper, " USING GIN"),
		strings.HasPrefix(upper, "SELECT PG_"):
		return ""
	}
	if reIdentity.MatchString(bare) && rePKIdAt.MatchString(bare) {
		bare = reIdentity.ReplaceAllString(bare, "INTEGER PRIMARY KEY AUTOINCREMENT")
		bare = rePKIdAt.ReplaceAllString(bare, "")
	}
	bare = reIdentityPK.ReplaceAllString(bare, "INTEGER PRIMARY KEY AUTOINCREMENT")
	bare = reIdentity.ReplaceAllString(bare, "INTEGER")
	bare = rePartition.ReplaceAllString(strings.TrimSpace(bare), ")")
	bare = reTSVector.ReplaceAllString(bare, "")
	bare = reAddIfNot.ReplaceAllString(bare, "ADD COLUMN")
	bare = reBytea.ReplaceAllString(bare, "BLOB")
	bare = reJSONBType.ReplaceAllString(bare, "TEXT")
	return Translate(bare)
}

// stripComments drops -- comments outside quotes.
func stripComments(q string) string {
	var b strings.Builder
	for pos := 0; pos < len(q); pos++ {
		char := q[pos]
		switch {
		case char == '\'' || char == '"':
			quoteEnd := skipQuoted(q, pos)
			b.WriteString(q[pos:quoteEnd])
			pos = quoteEnd - 1
		case char == '-' && pos+1 < len(q) && q[pos+1] == '-':
			for pos < len(q) && q[pos] != '\n' {
				pos++
			}
			b.WriteByte('\n')
		default:
			b.WriteByte(char)
		}
	}
	return b.String()
}

// splitStatements cuts a block of DDL at the semicolons outside quotes and
// $$ bodies.
func splitStatements(block string) []string {
	var out []string
	start := 0
	for pos := 0; pos < len(block); pos++ {
		char := block[pos]
		switch {
		case char == '\'' || char == '"':
			pos = skipQuoted(block, pos) - 1
		case char == '-' && pos+1 < len(block) && block[pos+1] == '-':
			for pos < len(block) && block[pos] != '\n' {
				pos++
			}
		case char == '$' && pos+1 < len(block) && block[pos+1] == '$':
			dollarEnd := strings.Index(block[pos+2:], "$$")
			if dollarEnd < 0 {
				pos = len(block)
			} else {
				pos += dollarEnd + 3
			}
		case char == ';':
			out = append(out, block[start:pos])
			start = pos + 1
		}
	}
	if strings.TrimSpace(block[start:]) != "" {
		out = append(out, block[start:])
	}
	return out
}

// skipQuoted is the index just past the quoted string or identifier at i.
func skipQuoted(q string, i int) int {
	quote := q[i]
	pos := i + 1
	for pos < len(q) {
		if q[pos] == quote {
			if pos+1 < len(q) && q[pos+1] == quote {
				pos += 2
				continue
			}
			return pos + 1
		}
		pos++
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
	translated, ok := cache[q]
	cacheMu.RUnlock()
	if ok {
		return translated
	}
	translated = translate(q)
	cacheMu.Lock()
	if len(cache) > 4096 {
		cache = map[string]string{}
	}
	cache[q] = translated
	cacheMu.Unlock()
	return translated
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
	rewritten := q
	// jsonb_array_elements(x) WITH ORDINALITY AS r(v, ord): json_each's
	// value is v, its key (from 0) is ord - 1.
	if match := reArrayElems.FindStringSubmatch(rewritten); match != nil {
		alias, elems, ord := match[2], match[3], match[4]
		rewritten = strings.Replace(rewritten, match[0], "json_each("+match[1]+") AS "+alias, 1)
		rewritten = strings.ReplaceAll(rewritten, alias+"."+elems+"-", alias+".value-")
		rewritten = strings.ReplaceAll(rewritten, alias+"."+ord, "("+alias+".key+1)")
	}
	rewritten = arrows(casts(rewritten))
	rewritten = codeOnly(rewritten, func(c string) string {
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
	if match := reUpdateAlias.FindStringSubmatch(rewritten); match != nil {
		rewritten = reUpdateAlias.ReplaceAllString(rewritten, "$1 AS $2 SET")
		// RETURNING names the modified table's columns bare.
		if returning := strings.LastIndex(strings.ToUpper(rewritten), "RETURNING "); returning > 0 {
			rewritten = rewritten[:returning] + strings.ReplaceAll(rewritten[returning:], match[2]+".", "")
		}
	}
	rewritten = reDeleteAlias.ReplaceAllString(rewritten, "$1 AS $2 WHERE")
	rewritten = reStringAggD.ReplaceAllString(rewritten, "group_concat(DISTINCT $1)")
	rewritten = reStringAgg.ReplaceAllString(rewritten, "group_concat(")
	rewritten = reToChar.ReplaceAllString(rewritten, "strftime('%Y-%m-%d', $1, 'unixepoch')")
	// DISTINCT ON (k) cols FROM t WHERE w ORDER BY k, o DESC) - the last row of
	// each k by o: SQLite's bare columns beside max(o) are that row's.
	rewritten = reDistinctOn.ReplaceAllString(rewritten, "SELECT $2, max($6) FROM $3 WHERE $4 GROUP BY $1)")
	// INSERT ... SELECT ... ON CONFLICT DO NOTHING: SQLite cannot parse the
	// clause after a SELECT with no WHERE; OR IGNORE says the same.
	if reInsertInto.MatchString(rewritten) && reConflictNil.MatchString(rewritten) {
		rewritten = reConflictNil.ReplaceAllString(rewritten, "")
		rewritten = reInsertInto.ReplaceAllString(rewritten, "INSERT OR IGNORE INTO")
	}
	return rewritten
}

// codeOnly applies f to the parts of q outside quoted strings and identifiers.
func codeOnly(q string, f func(string) string) string {
	var b strings.Builder
	start := 0
	for pos := 0; pos < len(q); pos++ {
		char := q[pos]
		if char == '\'' || char == '"' {
			b.WriteString(f(q[start:pos]))
			quoteEnd := skipQuoted(q, pos)
			b.WriteString(q[pos:quoteEnd])
			start = quoteEnd
			pos = quoteEnd - 1
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
	rewritten := []byte{}
	for pos := 0; pos < len(q); pos++ {
		char := q[pos]
		if char == '\'' || char == '"' {
			quoteEnd := skipQuoted(q, pos)
			rewritten = append(rewritten, q[pos:quoteEnd]...)
			pos = quoteEnd - 1
			continue
		}
		if char == ':' && pos+1 < len(q) && q[pos+1] == ':' {
			typ, castLen := castType(q[pos+2:])
			if typ == "" {
				rewritten = append(rewritten, char)
				continue
			}
			operandAt := operandStart(rewritten)
			operand := string(rewritten[operandAt:])
			rewritten = append(rewritten[:operandAt], []byte("CAST("+operand+" AS "+typ+")")...)
			pos += 1 + castLen
			continue
		}
		rewritten = append(rewritten, char)
	}
	return string(rewritten)
}

// arrows turns every x->'key' (not ->>) into json_extract(x, '$."key"'):
// Postgres's -> hands over a jsonb that ammit reads the way it read SQLite's
// json_extract (db.go sqlCell), and json_extract is that answer in SQLite,
// where -> would be the JSON text.
func arrows(q string) string {
	if !strings.Contains(q, "->'") {
		return q
	}
	rewritten := []byte{}
	for pos := 0; pos < len(q); pos++ {
		char := q[pos]
		if char == '\'' || char == '"' {
			quoteEnd := skipQuoted(q, pos)
			rewritten = append(rewritten, q[pos:quoteEnd]...)
			pos = quoteEnd - 1
			continue
		}
		if char == '-' && pos+2 < len(q) && q[pos+1] == '>' && q[pos+2] == '\'' {
			quoteEnd := skipQuoted(q, pos+2)
			jsonKey := strings.ReplaceAll(q[pos+3:quoteEnd-1], "''", "'")
			operandAt := operandStart(rewritten)
			operand := strings.TrimRight(string(rewritten[operandAt:]), " ")
			path := "$.\"" + strings.ReplaceAll(jsonKey, "'", "''") + "\""
			rewritten = append(rewritten[:operandAt], []byte("json_extract("+operand+", '"+path+"')")...)
			pos = quoteEnd - 1
			continue
		}
		rewritten = append(rewritten, char)
	}
	return string(rewritten)
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
	for _, cast := range castTypes {
		if strings.HasPrefix(low, cast.pg) {
			pgLen := len(cast.pg)
			if pgLen < len(low) && (isWord(low[pgLen])) {
				continue
			}
			return cast.lite, pgLen
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
	pos := len(out) - 1
	for pos >= 0 && out[pos] == ' ' {
		pos--
	}
	if pos < 0 {
		return 0
	}
	switch out[pos] {
	case ')':
		depth := 0
	scan:
		for ; pos >= 0; pos-- {
			switch out[pos] {
			case ')':
				depth++
			case '(':
				depth--
				if depth == 0 {
					break scan
				}
			}
		}
		for pos > 0 && (isWord(out[pos-1]) || out[pos-1] == '.') {
			pos--
		}
		return max(pos, 0)
	case '\'':
		quotePos := pos - 1
		for quotePos >= 0 {
			if out[quotePos] == '\'' {
				if quotePos > 0 && out[quotePos-1] == '\'' {
					quotePos -= 2
					continue
				}
				return quotePos
			}
			quotePos--
		}
		return 0
	}
	for pos >= 0 && (isWord(out[pos]) || out[pos] == '.' || out[pos] == '?' || out[pos] == '$') {
		pos--
	}
	return pos + 1
}

// ---- functions SQLite has no word for -----------------------------------

func registerFunctions() {
	sqlite.MustRegisterDeterministicScalarFunction("jnum", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		return jnum(a[0]), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("trunc", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		number, ok := num(a[0])
		if !ok {
			return nil, nil
		}
		return math.Trunc(number), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("lead_int", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		str, ok := text(a[0])
		if !ok {
			return nil, nil
		}
		match := leadInt.FindStringSubmatch(str)
		if match == nil {
			return int64(0), nil
		}
		lead, _ := strconv.ParseInt(match[1], 10, 64)
		return lead, nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("regexp", 2, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		pat, _ := text(a[0])
		subject, ok := text(a[1])
		if !ok {
			return nil, nil
		}
		pattern, err := compiled(pat)
		if err != nil {
			return nil, err
		}
		if pattern.MatchString(subject) {
			return int64(1), nil
		}
		return int64(0), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("jsonb_typeof", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		jsonText, ok := text(a[0])
		if !ok {
			return nil, nil
		}
		var x any
		if json.Unmarshal([]byte(jsonText), &x) != nil {
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
	if cachedPattern, ok := reCached[p]; ok {
		return cachedPattern, nil
	}
	pattern, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	reCached[p] = pattern
	return pattern, nil
}

func text(v driver.Value) (string, bool) {
	switch typed := v.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), true
	}
	return "", false
}

func num(v driver.Value) (float64, bool) {
	switch typed := v.(type) {
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number, err == nil
	case []byte:
		number, err := strconv.ParseFloat(strings.TrimSpace(string(typed)), 64)
		return number, err == nil
	}
	return 0, false
}

// jnum is schema.go's jnum on a JSON value as SQLite's -> hands it over (its
// JSON text), or on a plain SQL value: a number is itself, a boolean 1 or 0,
// a string that reads as a number that number, anything else NULL.
func jnum(v driver.Value) driver.Value {
	switch typed := v.(type) {
	case nil:
		return nil
	case int64:
		return float64(typed)
	case float64:
		return typed
	}
	jsonText, _ := text(v)
	var j any
	if json.Unmarshal([]byte(jsonText), &j) != nil {
		return nil
	}
	switch decoded := j.(type) {
	case float64:
		return decoded
	case bool:
		if decoded {
			return float64(1)
		}
		return float64(0)
	case string:
		if numText.MatchString(decoded) {
			number, err := strconv.ParseFloat(strings.TrimSpace(decoded), 64)
			if err == nil {
				return number
			}
		}
	}
	return nil
}
