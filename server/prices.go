package main

import (
	_ "embed"
	"encoding/json"
	"log"
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
// One arithmetic, not two.
//
// The SDK bills a session when it ends, and this service prices turns as they
// arrive. Taking the larger of the two over a whole run mixed them: on run
// 0ad6a8c6 the bill was $40.02 and the estimate $70.11, and the estimate won
// because output is priced from a four-characters-a-token guess and output
// costs five times input. The run was stopped over a number nobody was charged.
//
// So the two are added rather than compared, and each is used where it is the
// only one there is: a session that billed contributes its bill, a session that
// did not -- one this service stopped, one still running -- contributes the
// price of its own turns. Matched by (agent, phase, branch), which is what a
// session is here.
const spentSQL = `
WITH billed AS (
	SELECT coalesce(agent,'') a, coalesce(phase,'') ph, coalesce(branch,'') br,
	       sum(coalesce(json_extract(payload,'$.usd'), 0)) usd
	FROM events
	WHERE run = ? AND kind = 'session_end'
	  AND coalesce(json_extract(payload,'$.usd'), 0) > 0
	GROUP BY 1, 2, 3
), counted AS (
	SELECT coalesce(t.agent,'') a, coalesce(t.phase,'') ph, coalesce(t.branch,'') br,
	       sum((coalesce(t.tokens_in,0)*pr.input
	            + max(coalesce(t.tokens_out,0), coalesce(t.out_est,0))*pr.output
	            + coalesce(t.cache_read,0)*pr.cache_read
	            + (coalesce(t.cache_write,0) - coalesce(t.cache_write_1h,0))*pr.cache_write
	            + coalesce(t.cache_write_1h,0)*pr.cache_write_1h)/1e6
	           * CASE WHEN t.geo='us' THEN pr.us_surcharge ELSE 1 END) usd
	FROM turns t JOIN prices pr ON pr.model = t.model
	WHERE t.run = ? AND t.tokens_in IS NOT NULL
	GROUP BY 1, 2, 3
)
SELECT coalesce((SELECT sum(usd) FROM billed), 0)
     + coalesce((SELECT sum(c.usd) FROM counted c
                 WHERE NOT EXISTS (SELECT 1 FROM billed b
                                   WHERE b.a = c.a AND b.ph = c.ph AND b.br = c.br)), 0)`

// spentExpr is spentSQL as a scalar subquery against the row's own run, so a
// listing and a judgement cannot drift apart: one formula, two callers.
const spentExpr = `(
	coalesce((SELECT sum(coalesce(json_extract(e.payload,'$.usd'),0))
	          FROM events e
	          WHERE e.run = r.run AND e.kind = 'session_end'
	            AND coalesce(json_extract(e.payload,'$.usd'),0) > 0), 0)
	+ coalesce((SELECT sum(u.usd) FROM (
	     SELECT coalesce(t.agent,'') a, coalesce(t.phase,'') ph,
	            coalesce(t.branch,'') br,
	            sum((coalesce(t.tokens_in,0)*pr.input
	                 + max(coalesce(t.tokens_out,0), coalesce(t.out_est,0))*pr.output
	                 + coalesce(t.cache_read,0)*pr.cache_read
	                 + (coalesce(t.cache_write,0)-coalesce(t.cache_write_1h,0))*pr.cache_write
	                 + coalesce(t.cache_write_1h,0)*pr.cache_write_1h)/1e6
	                * CASE WHEN t.geo='us' THEN pr.us_surcharge ELSE 1 END) usd
	     FROM turns t JOIN prices pr ON pr.model = t.model
	     WHERE t.run = r.run AND t.tokens_in IS NOT NULL
	     GROUP BY 1,2,3) u
	   WHERE NOT EXISTS (
	     SELECT 1 FROM events e2
	     WHERE e2.run = r.run AND e2.kind = 'session_end'
	       AND coalesce(json_extract(e2.payload,'$.usd'),0) > 0
	       AND coalesce(json_extract(e2.payload,'$.agent'),'') = u.a
	       AND coalesce(json_extract(e2.payload,'$.phase'),'') = u.ph
	       AND coalesce(json_extract(e2.payload,'$.branch'),'') = u.br)), 0))`

// spentUSD is a run's spend, once: every session's bill where it sent one, and
// the price of its turns where it did not.
func spentUSD(r openRun) float64 {
	mu.Lock()
	defer mu.Unlock()
	var usd float64
	if err := db.QueryRow(spentSQL, r.run, r.run).Scan(&usd); err != nil {
		log.Printf("ammit: spent usd: %v", err)
		return r.usd
	}
	return usd
}

func servePrices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(pricesJSON)
}
