package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
)

// What a token costs. The SDK reports a bill per session and only when the
// session ends; a session ammit stops, or one that dies with its run, never
// reports one, and its money was invisible - eighteen sessions of run f4a30b19
// ran twenty-five minutes on ten cores and cost nothing on the chart. A price
// per token turns every turn event into money as it arrives.
//
// The prices are fitted to the SDK's own bills rather than copied from a list:
// the fit reproduces what was actually billed, and a list price for a model the
// list does not name yet would be a guess dressed as a fact.
//
//go:embed prices.json
var pricesJSON []byte

type priceRow struct {
	Family     string  `json:"family"`
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

type priceSheet struct {
	About    string     `json:"about"`
	Fitted   string     `json:"fitted"`
	Families []priceRow `json:"families"`
}

func prices() priceSheet {
	var s priceSheet
	_ = json.Unmarshal(pricesJSON, &s)
	return s
}

// seedPrices writes the sheet into the prices table, replacing what was there:
// the file is the source, the table is how a query reads it.
func seedPrices() {
	mu.Lock()
	defer mu.Unlock()
	if _, err := db.Exec(`DELETE FROM prices`); err != nil {
		log.Printf("ammit: prices: %v", err)
		return
	}
	for _, p := range prices().Families {
		if _, err := db.Exec(`INSERT INTO prices (family, input, output, cache_read, cache_write) VALUES (?,?,?,?,?)`,
			p.Family, p.Input, p.Output, p.CacheRead, p.CacheWrite); err != nil {
			log.Printf("ammit: prices: %v", err)
		}
	}
}

func servePrices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(pricesJSON)
}
