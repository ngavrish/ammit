package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Evidence: the videos and screenshots that prove each test of a ticket.
//
// A test that passed is a line in a junit file, and a line in a junit file is
// the agent's word for it. What a reviewer actually trusts is the recording of
// the browser doing the thing - and those recordings lived in a run directory
// that the next run of the same ticket resets, or in a scratchpad on whichever
// laptop ran the suite. "Show me the video of FR-3.2 passing" could be answered
// for the run in front of you, on the machine in front of you, and for nothing
// else.
//
// So the files go to an object store (MinIO, the S3 API, versioning on, and a
// user that can write and read but cannot delete) and this service keeps the
// index: which ticket, which run, which junit testcase, which kind of proof,
// and the hash that names the bytes. ammit is the one door - a client never
// talks to the bucket, and a link handed to a person is a link to us.
//
// Content-addressed: the key is <ticket>/<sha256><ext>. The same recording
// uploaded twice - a retry after a timeout, the same screenshot attached to two
// rows of one Outline - is stored once, and a file that is in the store is the
// file whose hash the row names, because the name is the hash.

// evidenceMaxBytes is the most one file may be. A screen recording of a
// scenario that runs for ten minutes is a few tens of megabytes; half a
// gigabyte is a recording that never stopped, and the store is not where that
// should be discovered.
const evidenceMaxBytes = 512 << 20

// evidenceFieldMax is the most a form field other than the file may be. meta
// is the only one that is free text, and a meta of a megabyte is a log sent in
// the wrong part.
const evidenceFieldMax = 1 << 20

// evidenceKinds is the closed list. A kind outside it is a typo, and a typo
// that lands is a proof nobody will find again - the same rule as coverage's.
var evidenceKinds = map[string]bool{"video": true, "screenshot": true, "log": true}

// evidenceStatuses is what a junit testcase can say about itself. Optional: a
// screenshot taken before the verdict is known is still evidence.
var evidenceStatuses = map[string]bool{"passed": true, "failed": true, "skipped": true}

// evidenceFields is the form as this service reads it. Anything else is named
// back in "dropped", as the documents handler does: a field nobody stores is
// a field its sender believes was kept.
var evidenceFields = []string{"ticket", "run", "test", "classname", "feature",
	"status", "kind", "meta", "file"}

// evidenceMimes maps an extension to the type the file is served as. A
// browser plays a video only when it is told it is one; served as
// octet-stream, the proof is a download nobody opens.
var evidenceMimes = map[string]string{
	".webm": "video/webm",
	".mp4":  "video/mp4",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".txt":  "text/plain; charset=utf-8",
	".log":  "text/plain; charset=utf-8",
}

// evidenceTicket is what a ticket may look like. It is the first segment of an
// object key, so a "/" in it is a key in somebody else's folder and a ".."
// is worse; a ticket is a tracker key, and tracker keys are none of that.
var evidenceTicket = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// evidenceExt is the file's extension as it goes into the key and picks the
// type: lowercased, and only the extensions this service knows how to serve.
// Anything else is stored with no extension and served as octet-stream - kept,
// because refusing a proof over its file name is the store losing evidence.
func evidenceExt(filename string) (ext, mime string) {
	ext = strings.ToLower(path.Ext(filename))
	if m, ok := evidenceMimes[ext]; ok {
		return ext, m
	}
	return "", "application/octet-stream"
}

// evidenceKey is where a file lives: under its ticket, named by its bytes.
func evidenceKey(ticket, sum, ext string) string {
	return ticket + "/" + sum + ext
}

// evidenceStore is the bucket, as narrow as this service uses it. An
// interface so the handler can be tested without an object store; the one
// implementation that matters is minioStore below.
type evidenceStore interface {
	// stat answers whether key is there and, if so, its version.
	stat(ctx context.Context, key string) (version string, found bool, err error)
	put(ctx context.Context, key string, body io.Reader, size int64, mime string) (version string, err error)
	// open returns the object at that version ("" = the newest) and its size.
	open(ctx context.Context, key, version string) (io.ReadSeekCloser, int64, error)
}

// evidence is nil until AMMIT_EVIDENCE_S3_* say where the bucket is. Nil is
// answered with 503 - an upload that lands nowhere and replies 201 is the one
// failure this endpoint exists to prevent.
var evidence evidenceStore

// evidenceLocks serialises the stat-then-put of one key. Two branches that
// upload the same screenshot at the same instant would both see it missing
// and both write it, and in a versioned bucket that is two versions of one
// file and a row naming whichever finished last. A lock per key, never freed:
// it is a few dozen bytes per distinct file, and freeing it safely costs more
// code than the memory is worth.
var evidenceLocks sync.Map

type minioStore struct {
	client *minio.Client
	bucket string
}

func (m minioStore) stat(ctx context.Context, key string) (string, bool, error) {
	info, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if code := minio.ToErrorResponse(err).Code; code == minio.NoSuchKey || code == "NotFound" {
			return "", false, nil
		}
		return "", false, err
	}
	return info.VersionID, true, nil
}

func (m minioStore) put(ctx context.Context, key string, body io.Reader, size int64, mime string) (string, error) {
	info, err := m.client.PutObject(ctx, m.bucket, key, body, size,
		minio.PutObjectOptions{ContentType: mime})
	if err != nil {
		return "", err
	}
	return info.VersionID, nil
}

func (m minioStore) open(ctx context.Context, key, version string) (io.ReadSeekCloser, int64, error) {
	obj, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{VersionID: version})
	if err != nil {
		return nil, 0, err
	}
	// GetObject is lazy: a missing object is an error on the first read, which
	// inside http.ServeContent would be a 500 with half a header written. Stat
	// asks now, while there is still a status code to choose.
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, 0, err
	}
	return obj, info.Size, nil
}

// openEvidenceStore reads AMMIT_EVIDENCE_S3_* and answers the store, or nil
// with the reason when it is not configured. Half a configuration is a
// reason too, and it is said at boot rather than at the first upload.
func openEvidenceStore() (evidenceStore, string) {
	endpoint := os.Getenv("AMMIT_EVIDENCE_S3_ENDPOINT")
	bucket := os.Getenv("AMMIT_EVIDENCE_S3_BUCKET")
	user := os.Getenv("AMMIT_EVIDENCE_S3_USER")
	password := os.Getenv("AMMIT_EVIDENCE_S3_PASSWORD")
	if endpoint == "" && bucket == "" && user == "" && password == "" {
		return nil, "AMMIT_EVIDENCE_S3_* are not set"
	}
	var missing []string
	for name, v := range map[string]string{
		"AMMIT_EVIDENCE_S3_ENDPOINT": endpoint, "AMMIT_EVIDENCE_S3_BUCKET": bucket,
		"AMMIT_EVIDENCE_S3_USER": user, "AMMIT_EVIDENCE_S3_PASSWORD": password,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, "not set: " + strings.Join(missing, ", ")
	}
	// The endpoint is written as a URL, the way every other service in the
	// compose file is addressed; minio-go wants host:port and a flag.
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Sprintf("AMMIT_EVIDENCE_S3_ENDPOINT is not a URL: %q", endpoint)
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(user, password, ""),
		Secure: u.Scheme == "https",
		// The region is said rather than asked: the evidence user may put and
		// get and list, and GetBucketLocation is none of those.
		Region:       env("AMMIT_EVIDENCE_S3_REGION", "us-east-1"),
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, err.Error()
	}
	return minioStore{client: client, bucket: bucket}, ""
}

// evidenceUnconfigured is the reason evidence is nil, for the 503.
var evidenceUnconfigured = "AMMIT_EVIDENCE_S3_* are not set"

// errTooLarge is a file over evidenceMaxBytes.
var errTooLarge = errors.New("too large")

// spoolEvidence copies one file part to a temp file while hashing it, so a
// video is never held in memory and its name is known when the copy ends.
func spoolEvidence(part io.Reader) (*os.File, string, int64, error) {
	tmp, err := os.CreateTemp("", "ammit-evidence-*")
	if err != nil {
		return nil, "", 0, err
	}
	hash := sha256.New()
	n, err := io.CopyN(io.MultiWriter(tmp, hash), part, evidenceMaxBytes+1)
	if err != nil && !errors.Is(err, io.EOF) {
		dropTemp(tmp)
		return nil, "", 0, err
	}
	if n > evidenceMaxBytes {
		dropTemp(tmp)
		return nil, "", 0, errTooLarge
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		dropTemp(tmp)
		return nil, "", 0, err
	}
	return tmp, hex.EncodeToString(hash.Sum(nil)), n, nil
}

func dropTemp(f *os.File) {
	_ = f.Close()
	_ = os.Remove(f.Name())
}

func postEvidence(db *sql.DB, mu *sync.Mutex, store evidenceStore, w http.ResponseWriter, r *http.Request) {
	if store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "the evidence store is not configured: " + evidenceUnconfigured})
		return
	}
	// The body as a whole is capped a little above the file: the fields ride
	// in the same body, and a cap on the file alone is no cap on a request
	// that sends a gigabyte as a "meta".
	r.Body = http.MaxBytesReader(w, r.Body, evidenceMaxBytes+4*evidenceFieldMax)
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not multipart/form-data: " + err.Error()})
		return
	}
	fields := map[string]string{}
	var dropped []string
	var spool *os.File
	var sum, filename string
	var size int64
	defer func() {
		if spool != nil {
			dropTemp(spool)
		}
	}()
	tooLarge := func() {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
			"error": fmt.Sprintf("over %d MiB", evidenceMaxBytes>>20)})
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var big *http.MaxBytesError
			if errors.As(err, &big) {
				tooLarge()
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad multipart body: " + err.Error()})
			return
		}
		name := part.FormName()
		switch {
		case name == "file":
			if spool != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one file per upload"})
				return
			}
			filename = part.FileName()
			spool, sum, size, err = spoolEvidence(part)
			if err != nil {
				var big *http.MaxBytesError
				if errors.Is(err, errTooLarge) || errors.As(err, &big) {
					tooLarge()
					return
				}
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file: " + err.Error()})
				return
			}
		case isEvidenceField(name):
			value, err := readField(part)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": name + ": " + err.Error()})
				return
			}
			fields[name] = strings.TrimSpace(value)
		default:
			dropped = append(dropped, name)
		}
		_ = part.Close()
	}
	sort.Strings(dropped)

	row, problem := evidenceRowOf(fields, spool != nil)
	if problem != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": problem})
		return
	}
	ext, mime := evidenceExt(filename)
	key := evidenceKey(row.ticket, sum, ext)

	version, err := storeEvidence(r.Context(), store, key, spool, size, mime)
	if err != nil {
		// The store said no. 502: the request was fine and the bucket was not,
		// and a client that retries on 5xx should retry this.
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "evidence store: " + err.Error()})
		return
	}

	at := float64(time.Now().UnixNano()) / 1e9
	mu.Lock()
	// Written again rather than added again: a retried upload of the same
	// bytes for the same test is the same proof, and two rows for it would be
	// a test that looks proved twice.
	_, err = db.ExecContext(r.Context(), `
		INSERT INTO evidence (at, ticket, run, test, classname, feature, status, kind,
		                      sha256, bytes, mime, key, version_id, meta)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (run, test, kind, sha256) DO UPDATE SET
		  at=excluded.at, ticket=excluded.ticket, classname=excluded.classname,
		  feature=excluded.feature, status=excluded.status, bytes=excluded.bytes,
		  mime=excluded.mime, key=excluded.key, version_id=excluded.version_id,
		  meta=excluded.meta`,
		at, row.ticket, row.run, row.test, row.classname, row.feature, row.status,
		row.kind, sum, size, mime, key, version, row.meta)
	mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	reply := map[string]any{"sha256": sum, "key": key, "version_id": version, "bytes": size}
	if len(dropped) > 0 {
		reply["dropped"] = dropped
	}
	writeJSON(w, http.StatusCreated, reply)
}

// storeEvidence puts the file unless the store already has those bytes under
// that key, and answers the version the row should point at.
func storeEvidence(ctx context.Context, store evidenceStore, key string, body io.Reader, size int64, mime string) (string, error) {
	lock, _ := evidenceLocks.LoadOrStore(key, &sync.Mutex{})
	keyMu, _ := lock.(*sync.Mutex)
	keyMu.Lock()
	defer keyMu.Unlock()
	// The key is the hash, so a key that is there is these bytes. Writing
	// them again would only add a version to a bucket that keeps every one.
	version, found, err := store.stat(ctx, key)
	if err != nil {
		return "", err
	}
	if found {
		return version, nil
	}
	return store.put(ctx, key, body, size, mime)
}

func isEvidenceField(name string) bool {
	for _, f := range evidenceFields {
		if f == name && f != "file" {
			return true
		}
	}
	return false
}

func readField(part *multipart.Part) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(part, evidenceFieldMax+1))
	if err != nil {
		return "", err
	}
	if len(raw) > evidenceFieldMax {
		return "", fmt.Errorf("over %d bytes", evidenceFieldMax)
	}
	return string(raw), nil
}

type evidenceRow struct {
	ticket, run, test, classname, feature, status, kind, meta string
}

// evidenceRowOf checks the form and answers the row, or what is wrong with it
// in one sentence that names every missing field at once - a client fixing
// them one 400 at a time is a client that gives up at the second.
func evidenceRowOf(f map[string]string, hasFile bool) (evidenceRow, string) {
	row := evidenceRow{
		ticket: f["ticket"], run: f["run"], test: f["test"],
		classname: f["classname"], feature: f["feature"],
		status: strings.ToLower(f["status"]), kind: strings.ToLower(f["kind"]),
		meta: f["meta"],
	}
	var missing []string
	for _, need := range []struct{ name, value string }{
		{"ticket", row.ticket}, {"run", row.run}, {"test", row.test}, {"kind", row.kind},
	} {
		if need.value == "" {
			missing = append(missing, need.name)
		}
	}
	if !hasFile {
		missing = append(missing, "file")
	}
	if len(missing) > 0 {
		return row, "missing: " + strings.Join(missing, ", ")
	}
	if !evidenceKinds[row.kind] {
		return row, fmt.Sprintf("kind %q is not one of video, screenshot, log", row.kind)
	}
	if row.status != "" && !evidenceStatuses[row.status] {
		return row, fmt.Sprintf("status %q is not one of passed, failed, skipped", row.status)
	}
	if !evidenceTicket.MatchString(row.ticket) {
		return row, fmt.Sprintf("ticket %q is not a tracker key", row.ticket)
	}
	if row.meta != "" && !json.Valid([]byte(row.meta)) {
		return row, "meta is not JSON"
	}
	return row, ""
}

// getEvidence lists the proofs of one ticket or one run, optionally of one
// test. Whole statements, one per question, for the reason getCoverage gives:
// a query built by concatenation reads the same whether it holds a caller's
// text or not.
func getEvidence(db *sql.DB, mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ticket, run, test := strings.TrimSpace(q.Get("ticket")),
		strings.TrimSpace(q.Get("run")), q.Get("test")
	const cols = `SELECT at, ticket, run, test, classname, feature, status, kind,
	                     sha256, bytes, mime FROM evidence `
	const order = ` ORDER BY test, kind, at`
	var (
		rows *sql.Rows
		err  error
	)
	ctx := r.Context()
	mu.Lock()
	switch {
	case run != "" && test != "":
		rows, err = db.QueryContext(ctx, cols+`WHERE run = ? AND test = ?`+order, run, test)
	case run != "":
		rows, err = db.QueryContext(ctx, cols+`WHERE run = ?`+order, run)
	case ticket != "" && test != "":
		rows, err = db.QueryContext(ctx, cols+`WHERE ticket = ? AND test = ?`+order, ticket, test)
	case ticket != "":
		rows, err = db.QueryContext(ctx, cols+`WHERE ticket = ?`+order, ticket)
	default:
		mu.Unlock()
		// No "everything": the table grows by a row per file per run, and an
		// unfiltered list is a question nobody means.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ticket or run is required"})
		return
	}
	mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer func() { _ = rows.Close() }()
	out := []map[string]any{}
	for rows.Next() {
		var at float64
		var bytes int64
		var tk, rn, ts, cls, feat, status, kind, sum, mime sql.NullString
		if err := rows.Scan(&at, &tk, &rn, &ts, &cls, &feat, &status, &kind,
			&sum, &bytes, &mime); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"at": at, "ticket": tk.String, "run": rn.String, "test": ts.String,
			"classname": cls.String, "feature": feat.String, "status": status.String,
			"kind": kind.String, "sha256": sum.String, "bytes": bytes, "mime": mime.String,
			"url": "/evidence/file/" + sum.String,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out})
}

// serveEvidenceFile streams one file back by its hash, with Range: a browser
// seeks in a video by asking for the bytes it wants, and a server that answers
// every seek with the whole file is a video that cannot be scrubbed.
func serveEvidenceFile(db *sql.DB, mu *sync.Mutex, store evidenceStore, w http.ResponseWriter, r *http.Request) {
	sum := r.PathValue("sha256")
	if !sha256Hex.MatchString(sum) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a sha256 (64 lowercase hex)"})
		return
	}
	var key, mime, version string
	mu.Lock()
	// Any row will do: the same bytes under two tickets are two keys holding
	// one file, and either of them is that file.
	err := db.QueryRowContext(r.Context(), `SELECT key, mime, coalesce(version_id, '')
		FROM evidence WHERE sha256 = ? ORDER BY id LIMIT 1`, sum).Scan(&key, &mime, &version)
	mu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such evidence"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "the evidence store is not configured: " + evidenceUnconfigured})
		return
	}
	// The version the row recorded, not the newest: the row is the proof of
	// a test, and it is a proof of exactly the bytes it was written with.
	obj, _, err := store.open(r.Context(), key, version)
	if err != nil {
		log.Printf("ammit: evidence %s: %v", key, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "evidence store: " + err.Error()})
		return
	}
	defer func() { _ = obj.Close() }()
	w.Header().Set("Content-Type", mime)
	// Content-addressed: what is at this URL today is what is at it forever.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+sum+`"`)
	http.ServeContent(w, r, "", time.Time{}, obj)
}
