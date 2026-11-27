package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// `ammit import-sqlite -from /data/ammit.db`: the record as it was in SQLite,
// copied into the Postgres AMMIT_DSN names, in batches, as many times as it is
// run. See docs/postgres-migration.md for when to run it and in what order.
//
// The source is opened read-only and read one batch per statement, so the
// SQLite ammit can go on writing while the copy runs and its WAL is never held
// open by a long read. Every run copies only what the target does not have
// yet, which is what makes the cut-over short: the first run moves the
// fourteen gigabytes while the old service works, each later run moves the
// minutes since the one before, and the last runs while nothing writes.
//
// Three kinds of table, by how their rows change:
//
//	append  rows are written once and never touched: copied by key, above the
//	        highest key the target already holds.
//	tail    rows are written once and then updated for a while (an answer
//	        learns its model when a later turn names it): the last -recent of
//	        them is replaced on every run, the rest copied by key.
//	mirror  small tables whose rows are updated or deleted (runs, the queue,
//	        leases, the watchdog's cycles, the knowledge base): replaced whole,
//	        in one transaction, on every run.
//
// Nothing here writes to the source; nothing here deletes from the target
// outside the tail window and the mirrored tables.

type importTable struct {
	name   string
	mode   string // append | tail | mirror
	key    string // append/tail: the increasing key
	at     string // tail: the time column the window is measured on
	filter string // an extra WHERE on the source, e.g. to leave rows out
	order  int    // mirror: tables replaced together, parents first
}

var importTables = []importTable{
	{name: "events", mode: "append", key: "id"},
	{name: "search_text", mode: "append", key: "id"},
	{name: "calls", mode: "append", key: "id"},
	{name: "turns", mode: "append", key: "event_id"},
	{name: "suites", mode: "append", key: "event_id"},
	{name: "heal_laps", mode: "append", key: "event_id"},
	{name: "judgements", mode: "append", key: "id"},
	{name: "gates", mode: "append", key: "id"},
	{name: "limits", mode: "append", key: "id"},
	{name: "documents", mode: "append", key: "id"},
	{name: "deploys", mode: "append", key: "id"},
	{name: "model_answers", mode: "tail", key: "event_id", at: "at"},
	{name: "model_idle", mode: "tail", key: "event_id", at: "at"},
	{name: "runs", mode: "mirror"},
	{name: "flows", mode: "mirror"},
	{name: "queue", mode: "mirror"},
	{name: "leases", mode: "mirror"},
	{name: "watchdog_drops", mode: "mirror"},
	{name: "watchdog_cycles", mode: "mirror"},
	{name: "watchdog_meta", mode: "mirror"},
	{name: "coverage", mode: "mirror"},
	{name: "evidence", mode: "mirror"},
	{name: "kb", mode: "mirror"},
	{name: "kb_docs", mode: "mirror", order: 1},
	{name: "kb_chunks", mode: "mirror", order: 2},
	{name: "lift_marks", mode: "mirror"},
	{name: "model_streams", mode: "mirror"},
	{name: "model_of", mode: "mirror"},
	{name: "phase_model_times", mode: "mirror"},
	{name: "search_meta", mode: "mirror"},
}

// The tables that hand out ids, whose sequences must start above what was
// copied.
var identityTables = []string{"events", "search_text", "calls", "judgements", "gates", "limits",
	"documents", "deploys", "coverage", "evidence", "queue", "watchdog_cycles", "kb_docs", "kb_chunks"}

type importer struct {
	src     *sql.DB
	batch   int
	recent  float64
	coerced map[string]int
}

func importSQLite(args []string, dsn string) error {
	flags := flag.NewFlagSet("import-sqlite", flag.ContinueOnError)
	from := flags.String("from", "", "the SQLite file to copy from (opened read-only)")
	batch := flags.Int("batch", 5000, "rows per batch")
	recent := flags.Duration("recent", 48*time.Hour, "how far back a tail table is replaced on every run")
	only := flags.String("tables", "", "comma-separated tables to copy (default: all)")
	skipDocs := flags.Bool("skip-document-search", false,
		"leave search_text's document rows behind (they are not searched once documents leave the index)")
	verify := flags.Bool("verify", false, "copy nothing; compare row counts and highest keys, source against target")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return errors.New("-from is required: the SQLite file to copy")
	}
	source, err := sql.Open("sqlite", "file:"+*from+"?mode=ro&_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	source.SetMaxOpenConns(1)
	if err := source.PingContext(context.Background()); err != nil {
		return fmt.Errorf("could not open %s: %w", *from, err)
	}
	db, readDB, err = openPools(dsn)
	if err != nil {
		return err
	}
	if err := migrate(); err != nil {
		return err
	}
	imp := &importer{src: source, batch: *batch, recent: recent.Seconds(), coerced: map[string]int{}}
	tables := importTables
	if *only != "" {
		want := map[string]bool{}
		for _, table := range strings.Split(*only, ",") {
			want[strings.TrimSpace(table)] = true
		}
		var picked []importTable
		for _, table := range tables {
			if want[table.name] {
				picked = append(picked, table)
			}
		}
		tables = picked
	}
	for index := range tables {
		if tables[index].name == "search_text" && *skipDocs {
			tables[index].filter = "source <> 'document'"
		}
	}
	ctx := context.Background()
	if *verify {
		return imp.verify(ctx, tables)
	}
	if err := imp.partitions(ctx); err != nil {
		return err
	}
	started := time.Now()
	// The mirrored tables that belong together are replaced in one
	// transaction, parents first: kb_chunks points at kb_docs.
	var group []importTable
	flushGroup := func() error {
		if len(group) == 0 {
			return nil
		}
		err := imp.mirror(ctx, group)
		group = nil
		return err
	}
	for _, table := range tables {
		if !imp.has(ctx, table.name) {
			log.Printf("import: %s is not in the source; skipped", table.name)
			continue
		}
		var err error
		switch table.mode {
		case "append":
			err = imp.appendRows(ctx, table, false)
		case "tail":
			err = imp.appendRows(ctx, table, true)
		case "mirror":
			if table.order > 0 {
				group = append(group, table)
				continue
			}
			err = imp.mirror(ctx, []importTable{table})
		}
		if err != nil {
			return fmt.Errorf("%s: %w", table.name, err)
		}
	}
	if err := flushGroup(); err != nil {
		return err
	}
	if err := imp.sequences(ctx); err != nil {
		return err
	}
	keys := make([]string, 0, len(imp.coerced))
	for coercion := range imp.coerced {
		keys = append(keys, coercion)
	}
	sort.Strings(keys)
	for _, coercion := range keys {
		log.Printf("import: %d value(s) of %s did not fit their column and were coerced", imp.coerced[coercion], coercion)
	}
	log.Printf("import: done in %s", time.Since(started).Round(time.Second))
	return nil
}

func (im *importer) has(ctx context.Context, table string) bool {
	var n int
	_ = im.src.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`,
		table).Scan(&n)
	return n > 0
}

type column struct {
	name, typ string
	notNull   bool
}

// columns is what both sides have, in the target's order and with its types;
// a generated column (search_text.words, kb_chunks.words) is the target's own.
func (im *importer) columns(ctx context.Context, table string) ([]column, error) {
	have, err := im.sourceColumns(ctx, table)
	if err != nil {
		return nil, err
	}
	columnRows, err := db.DB.QueryContext(ctx, `SELECT column_name, data_type, is_nullable = 'NO'
	        FROM information_schema.columns
	        WHERE table_schema = current_schema() AND table_name = $1 AND is_generated = 'NEVER'
	        ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = columnRows.Close() }()
	var out []column
	for columnRows.Next() {
		var c column
		if err := columnRows.Scan(&c.name, &c.typ, &c.notNull); err != nil {
			return nil, err
		}
		if have[c.name] {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no column of %s is on both sides", table)
	}
	// A source column with nowhere to go is history this copy would drop
	// without a word; it is said, and the copy stops.
	for column := range have {
		listed := false
		for _, col := range out {
			listed = listed || col.name == column
		}
		if !listed {
			return nil, fmt.Errorf("%s.%s is in the source and not in the target", table, column)
		}
	}
	return out, columnRows.Err()
}

func (im *importer) sourceColumns(ctx context.Context, table string) (map[string]bool, error) {
	have := map[string]bool{}
	pragmaRows, err := im.src.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = pragmaRows.Close() }()
	for pragmaRows.Next() {
		var n string
		if pragmaRows.Scan(&n) == nil {
			have[n] = true
		}
	}
	return have, pragmaRows.Err()
}

// page reads one query's rows from the source, in the target's types.
func (im *importer) page(ctx context.Context, table string, cols []column, q string, args ...any) ([][]any, error) {
	pageRows, err := im.src.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = pageRows.Close() }()
	return im.read(pageRows, table, cols)
}

func names(cols []column) []string {
	colNames := make([]string, len(cols))
	for index, col := range cols {
		colNames[index] = col.name
	}
	return colNames
}

func quotedCols(cols []column) string {
	quoted := make([]string, len(cols))
	for index, col := range cols {
		quoted[index] = `"` + col.name + `"`
	}
	return strings.Join(quoted, ", ")
}

// withConn runs fn on one pgx connection of the target pool: COPY is pgx's,
// not database/sql's.
func withConn(ctx context.Context, fn func(*pgx.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.Raw(func(dc any) error {
		stdConn, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("the pool's connection is a %T, not pgx's", dc)
		}
		return fn(stdConn.Conn())
	})
}

// partitions makes a table for every month the source's events fall in, so a
// copied event lands in its month and not in the default partition.
func (im *importer) partitions(ctx context.Context) error {
	var lo, hi sql.NullFloat64
	if err := im.src.QueryRowContext(ctx, `SELECT min(at), max(at) FROM events
	        WHERE at > 946684800 AND at < 4102444800`).Scan(&lo, &hi); err != nil {
		return err
	}
	if !lo.Valid {
		return nil // no events to place
	}
	for month := time.Unix(int64(lo.Float64), 0).UTC(); ; month = month.AddDate(0, 1, 0) {
		first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
		if float64(first.Unix()) > hi.Float64 {
			break
		}
		if err := ensurePartition(ctx, first); err != nil {
			return err
		}
	}
	return ensurePartitions(ctx, time.Now())
}

// appendRows copies the rows above the target's highest key, a batch at a
// time, each batch one COPY in its own transaction - so a run stopped halfway
// leaves whole batches behind and the next run starts where it ended. A tail
// table first gives back its last -recent and takes it again.
func (im *importer) appendRows(ctx context.Context, t importTable, tail bool) error {
	cols, err := im.columns(ctx, t.name)
	if err != nil {
		return err
	}
	since := nowWall() - im.recent
	if tail {
		//nolint:gosec // the table and column are importTables', never input
		if _, err := db.DB.ExecContext(ctx, `DELETE FROM `+t.name+` WHERE `+t.at+` >= $1`, since); err != nil {
			return err
		}
	}
	var last int64
	if err := db.DB.QueryRowContext(ctx, `SELECT coalesce(max(`+t.key+`), 0) FROM `+t.name).Scan(&last); err != nil {
		return err
	}
	// Above the cursor, and either above what the target had or inside the
	// window a tail table just gave back.
	where := t.key + " > ?"
	if tail {
		where += " AND (" + t.key + " > " + strconv.FormatInt(last, 10) + " OR " + t.at + " >= " +
			strconv.FormatFloat(since, 'f', -1, 64) + ")"
	}
	if t.filter != "" {
		where += " AND " + t.filter
	}
	var total int64
	started := time.Now()
	keyAt := -1
	for index, col := range cols {
		if col.name == t.key {
			keyAt = index
		}
	}
	if keyAt < 0 {
		return fmt.Errorf("the key %s is not a column on both sides", t.key)
	}
	cursor := last
	if tail {
		cursor = math.MinInt64
	}
	for {
		query := `SELECT ` + quotedCols(cols) + ` FROM ` + t.name + ` WHERE ` + where +
			` ORDER BY ` + t.key + ` LIMIT ` + strconv.Itoa(im.batch)
		page, err := im.page(ctx, t.name, cols, query, cursor)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		if err := withConn(ctx, func(c *pgx.Conn) error {
			txn, err := c.Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = txn.Rollback(ctx) }()
			if _, err := txn.CopyFrom(ctx, pgx.Identifier{t.name}, names(cols), pgx.CopyFromRows(page)); err != nil {
				return err
			}
			return txn.Commit(ctx)
		}); err != nil {
			return im.rowByRow(ctx, t, cols, page, err)
		}
		total += int64(len(page))
		next, ok := page[len(page)-1][keyAt].(int64)
		if !ok {
			return fmt.Errorf("the key %s of %s is not an integer", t.key, t.name)
		}
		cursor = next
		if total%(int64(im.batch)*20) == 0 {
			log.Printf("import: %s: %d rows so far (%.0f/s), at key %d", t.name, total,
				float64(total)/time.Since(started).Seconds(), cursor)
		}
	}
	log.Printf("import: %s: %d row(s) copied (%s)", t.name, total, t.mode)
	return nil
}

// rowByRow is a batch COPY refused, taken one row at a time so the row that
// was refused is named and the ones beside it are not lost with it.
func (im *importer) rowByRow(ctx context.Context, t importTable, cols []column, page [][]any, cause error) error {
	log.Printf("import: %s: a batch was refused (%v); copying it a row at a time", t.name, cause)
	holes := make([]string, len(cols))
	for index := range holes {
		holes[index] = "$" + strconv.Itoa(index+1)
	}
	//nolint:gosec // the table is importTables', the columns the catalog's
	insert := `INSERT INTO ` + t.name + ` (` + quotedCols(cols) + `) VALUES (` + strings.Join(holes, ",") + `)
	      ON CONFLICT DO NOTHING`
	for _, pageRow := range page {
		if _, err := db.DB.ExecContext(ctx, insert, pageRow...); err != nil {
			return fmt.Errorf("the row %v could not be copied: %w", pageRow[0], err)
		}
	}
	return nil
}

// mirror replaces the target's rows of these tables with the source's, in one
// transaction: a reader of the target sees the old rows or the new, never an
// empty table.
func (im *importer) mirror(ctx context.Context, tables []importTable) error {
	type loaded struct {
		t    importTable
		cols []column
		rows [][]any
	}
	all := make([]loaded, 0, len(tables))
	for _, table := range tables {
		cols, err := im.columns(ctx, table.name)
		if err != nil {
			return err
		}
		page, err := im.page(ctx, table.name, cols, `SELECT `+quotedCols(cols)+` FROM `+table.name)
		if err != nil {
			return err
		}
		all = append(all, loaded{table, cols, page})
	}
	err := withConn(ctx, func(c *pgx.Conn) error {
		txn, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = txn.Rollback(ctx) }()
		for index := len(all) - 1; index >= 0; index-- {
			if _, err := txn.Exec(ctx, `DELETE FROM `+all[index].t.name); err != nil {
				return err
			}
		}
		for _, loaded := range all {
			if _, err := txn.CopyFrom(ctx, pgx.Identifier{loaded.t.name}, names(loaded.cols), pgx.CopyFromRows(loaded.rows)); err != nil {
				return fmt.Errorf("%s: %w", loaded.t.name, err)
			}
		}
		return txn.Commit(ctx)
	})
	if err != nil {
		return err
	}
	for _, loaded := range all {
		log.Printf("import: %s: %d row(s) (mirror)", loaded.t.name, len(loaded.rows))
	}
	return nil
}

// read takes a page of source rows into the target's types.
func (im *importer) read(rows *sql.Rows, table string, cols []column) ([][]any, error) {
	var out [][]any
	for rows.Next() {
		cells := make([]any, len(cols))
		holders := make([]any, len(cols))
		for index := range cells {
			holders[index] = &cells[index]
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, err
		}
		for index, col := range cols {
			converted, ok := convert(cells[index], col)
			if !ok {
				im.coerced[table+"."+col.name]++
			}
			cells[index] = converted
		}
		out = append(out, cells)
	}
	return out, rows.Err()
}

var loneSurrogate = regexp.MustCompile(`\\u[dD][89abAB][0-9a-fA-F]{2}(\\u[dD][c-fC-F][0-9a-fA-F]{2})?|\\u[dD][c-fC-F][0-9a-fA-F]{2}`)

// convert is one SQLite value as the target column takes it, and whether it
// went in as it was. SQLite keeps whatever it is given in any column; Postgres
// keeps a column's type, refuses NUL in text, and refuses \u0000 and a lone
// surrogate in jsonb.
func convert(v any, c column) (any, bool) {
	if cellBytes, ok := v.([]byte); ok && c.typ != "bytea" {
		v = string(cellBytes)
	}
	if v == nil {
		if !c.notNull {
			return nil, true
		}
		switch c.typ {
		case "bigint", "integer", "double precision":
			return zeroOf(c.typ), false
		case "jsonb":
			return "null", false
		}
		return "", false
	}
	switch c.typ {
	case "bigint", "integer":
		switch typed := v.(type) {
		case int64:
			return typed, true
		case float64:
			if typed == math.Trunc(typed) && math.Abs(typed) < 9e18 {
				return int64(typed), true
			}
			return int64(typed), false
		case string:
			if whole, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
				return whole, true
			}
			if number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
				return int64(number), false
			}
			return nullOr(c), false
		}
	case "double precision":
		switch typed := v.(type) {
		case float64:
			return typed, true
		case int64:
			return float64(typed), true
		case string:
			if number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
				return number, true
			}
			return nullOr(c), false
		}
	case "jsonb":
		str := textOf(v)
		ok := true
		if strings.Contains(str, `\u0000`) {
			str, ok = strings.ReplaceAll(str, `\u0000`, `�`), false
		}
		if loneSurrogate.MatchString(str) {
			str = loneSurrogate.ReplaceAllStringFunc(str, func(m string) string {
				if len(m) == 12 {
					return m // a pair is a character
				}
				ok = false
				return `�`
			})
		}
		if strings.IndexByte(str, 0) >= 0 {
			str, ok = strings.ReplaceAll(str, "\x00", ""), false
		}
		if !json.Valid([]byte(str)) {
			wrapped, _ := json.Marshal(map[string]string{"unparsed": str})
			return string(wrapped), false
		}
		return str, ok
	case "bytea":
		switch typed := v.(type) {
		case []byte:
			return typed, true
		case string:
			return []byte(typed), true
		}
		return nil, false
	default: // text
		str := textOf(v)
		fit := true
		// A body cut at a byte count can end inside a character; SQLite kept
		// the half, Postgres refuses the row (calls row 145190, 2026-11-06).
		if !utf8.ValidString(str) {
			str, fit = strings.ToValidUTF8(str, "�"), false
		}
		if strings.IndexByte(str, 0) >= 0 {
			return strings.ReplaceAll(str, "\x00", "�"), false
		}
		if !fit {
			return str, false
		}
		_, isString := v.(string)
		return str, isString
	}
	return nullOr(c), false
}

func zeroOf(typ string) any {
	if typ == "double precision" {
		return float64(0)
	}
	return int64(0)
}

func nullOr(c column) any {
	if c.notNull {
		return zeroOf(c.typ)
	}
	return nil
}

// textOf is a value as SQLite would print it.
func textOf(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case []byte:
		return string(typed)
	}
	return fmt.Sprint(v)
}

// sequences moves every id sequence above the highest id copied, so the next
// row the service writes does not collide with history.
func (im *importer) sequences(ctx context.Context) error {
	for _, table := range identityTables {
		//nolint:gosec // the table is identityTables', never input
		if _, err := db.DB.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence($1, 'id'),
		        greatest(m, 1), m > 0) FROM (SELECT coalesce(max(id), 0) AS m FROM `+table+`) AS t`, table); err != nil {
			return fmt.Errorf("the id sequence of %s: %w", table, err)
		}
	}
	return nil
}

// verify compares the two sides table by table: rows, and the highest key.
func (im *importer) verify(ctx context.Context, tables []importTable) error {
	differ := 0
	for _, table := range tables {
		if !im.has(ctx, table.name) {
			continue
		}
		where := ""
		if table.filter != "" {
			where = " WHERE " + table.filter
		}
		var sn, tn int64
		var sk, tk sql.NullInt64
		maxKey := "0"
		if table.key != "" {
			maxKey = "max(" + table.key + ")"
		}
		if err := im.src.QueryRowContext(ctx, `SELECT count(*), `+maxKey+` FROM `+table.name+where).Scan(&sn, &sk); err != nil {
			return fmt.Errorf("%s (source): %w", table.name, err)
		}
		if err := db.DB.QueryRowContext(ctx, `SELECT count(*), `+maxKey+` FROM `+table.name+where).Scan(&tn, &tk); err != nil {
			return fmt.Errorf("%s (target): %w", table.name, err)
		}
		mark := "same"
		if sn != tn || sk != tk {
			mark = "DIFFERENT"
			differ++
		}
		_, _ = fmt.Fprintf(logOut, "%-18s %-6s source %10d rows, key %-10v target %10d rows, key %-10v %s\n",
			table.name, table.mode, sn, sk.Int64, tn, tk.Int64, mark)
	}
	if differ > 0 {
		return fmt.Errorf("%d table(s) differ", differ)
	}
	return nil
}

var logOut io.Writer = os.Stdout
