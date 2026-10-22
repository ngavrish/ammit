package main

// The product under test - its services, repositories, suite, credentials'
// names - is configuration, and ammit holds the one instance of it, as it
// holds the limits. Everyone asks for it here: GET /product (JSON) or
// GET /product.yml (the document, comments and all); an operator changes it
// with PUT /product.yml. Nothing else keeps a copy.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// productRequired are the fields every reader of the product depends on; a
// document without one is refused rather than served.
var productRequired = []string{"name", "repos", "services", "suite"}

// productMaxBytes bounds a PUT: the document is tens of kilobytes.
const productMaxBytes = 1 << 20

// productBroken is why a document cannot be the product, or nil.
func productBroken(body []byte) (map[string]any, []string) {
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, []string{"does not parse as YAML: " + err.Error()}
	}
	if doc == nil {
		return nil, []string{"is not a mapping"}
	}
	var bad []string
	for _, key := range productRequired {
		if _, ok := doc[key]; !ok {
			bad = append(bad, "missing "+key)
		}
	}
	return doc, bad
}

// jsonable turns YAML's map[any]any leftovers into what encoding/json takes.
func jsonable(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = jsonable(x)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[fmt.Sprint(k)] = jsonable(x)
		}
		return out
	case []any:
		for i, x := range t {
			t[i] = jsonable(x)
		}
		return t
	}
	return v
}

func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// serveProduct wires the product's routes. The ETag lets a reader that asks
// on every question skip re-parsing an unchanged document; it never lets one
// skip asking.
func serveProduct(mux *http.ServeMux, path string) {
	read := func(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
		body, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "ammit holds no product: "+err.Error(), http.StatusServiceUnavailable)
			return nil, false
		}
		tag := etagOf(body)
		w.Header().Set("ETag", tag)
		if strings.Contains(r.Header.Get("If-None-Match"), tag) {
			w.WriteHeader(http.StatusNotModified)
			return nil, false
		}
		return body, true
	}
	mux.HandleFunc("GET /product.yml", func(w http.ResponseWriter, r *http.Request) {
		body, ok := read(w, r)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		if _, err := w.Write(body); err != nil {
			log.Printf("ammit: product not sent: %v", err)
		}
	})
	mux.HandleFunc("GET /product", func(w http.ResponseWriter, r *http.Request) {
		body, ok := read(w, r)
		if !ok {
			return
		}
		doc, bad := productBroken(body)
		if len(bad) > 0 {
			http.Error(w, "the product ammit holds is broken: "+strings.Join(bad, "; "), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, jsonable(doc))
	})
	mux.HandleFunc("PUT /product.yml", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, productMaxBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, bad := productBroken(body); len(bad) > 0 {
			http.Error(w, "refused, this is not a product:\n"+strings.Join(bad, "\n"), http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("ammit: product replaced (%d bytes)", len(body))
		w.WriteHeader(http.StatusNoContent)
	})
}
