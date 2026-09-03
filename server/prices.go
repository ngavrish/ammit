package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"math"
	"net/http"
)

// What a token costs, per model: the catalog inside the Claude Code CLI the
// runner's SDK bundles, which is the code that writes the bill. The SDK
// reports that bill per session and only when the session ends; a session
// ammit stops, or one that dies with its run, never reports one, and its
// money was invisible - eighteen sessions of run f4a30b19 ran twenty-five
// minutes on ten cores and cost nothing on the chart. With the catalog and the
// SDK's own formula, every turn is money the moment it is reported, and the
// sum over a session that did get a bill comes to the bill.
//
// The formula (Claude Code, CRe): input*in + output*out + cache_read*read +
// (cache writes at 5 minutes)*write + (cache writes at 1 hour)*write_1h, all
// times 1.1 when usage.inference_geo is "us". Copied, not fitted: a fitted
// price reproduces the totals and lies about every turn.
//
//go:embed prices.json
var pricesJSON []byte

type priceRow struct {
	Model        string  `json:"model"`
	Name         string  `json:"name"`
	Tier         string  `json:"tier"`
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite   float64 `json:"cache_write"`
	CacheWrite1h float64 `json:"cache_write_1h"`
}

type priceSheet struct {
	About       string     `json:"about"`
	Source      string     `json:"source"`
	USSurcharge float64    `json:"us_surcharge"`
	Models      []priceRow `json:"models"`
}

func prices() priceSheet {
	var s priceSheet
	_ = json.Unmarshal(pricesJSON, &s)
	return s
}

// dropOldPrices removes the first shape of the table (keyed by family), so the
// schema can make the one keyed by model. The sheet is re-seeded on every
// start; nothing in it is worth migrating.
func dropOldPrices() {
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM pragma_table_info('prices') WHERE name = 'family'`).Scan(&n)
	if n > 0 {
		if _, err := db.Exec(`DROP TABLE prices`); err != nil {
			log.Printf("ammit: prices: %v", err)
		}
	}
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
	s := prices()
	for _, p := range s.Models {
		if _, err := db.Exec(`INSERT INTO prices (model, name, tier, input, output, cache_read, cache_write, cache_write_1h, us_surcharge) VALUES (?,?,?,?,?,?,?,?,?)`,
			p.Model, p.Name, p.Tier, p.Input, p.Output, p.CacheRead, p.CacheWrite, p.CacheWrite1h, s.USSurcharge); err != nil {
			log.Printf("ammit: prices: %v", err)
		}
	}
}

// The SDK's formula, in SQL, over the lifted turns: what a run's turns come
// to at the catalog. The one expression every chart and the judge read.
const countedSQL = `SELECT coalesce(sum(
	(coalesce(t.tokens_in,0)*p.input
	 + max(coalesce(t.tokens_out,0), coalesce(t.out_est,0))*p.output
	 + coalesce(t.cache_read,0)*p.cache_read
	 + (coalesce(t.cache_write,0) - coalesce(t.cache_write_1h,0))*p.cache_write
	 + coalesce(t.cache_write_1h,0)*p.cache_write_1h)/1e6
	* CASE WHEN t.geo='us' THEN p.us_surcharge ELSE 1 END), 0)
	FROM turns t JOIN prices p ON p.model = t.model
	WHERE t.run = ? AND t.tokens_in IS NOT NULL`

// countedUSD is what a run's turns come to so far, priced as they arrived.
func countedUSD(run string) float64 {
	mu.Lock()
	defer mu.Unlock()
	var usd float64
	if err := db.QueryRow(countedSQL, run).Scan(&usd); err != nil {
		log.Printf("ammit: counted usd: %v", err)
	}
	return usd
}

// spentUSD is a run's spend as far as anything knows it: the bill where the
// SDK has sent one, the count of its turns where it has not, whichever is
// larger. A session this service stopped never sends a bill.
func spentUSD(r openRun) float64 {
	return math.Max(r.usd, countedUSD(r.run))
}

func servePrices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(pricesJSON)
}
