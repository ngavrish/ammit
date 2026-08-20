package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

// The page where the limits are set.
//
// They lived in a file, which is honest and unusable at the moment it matters:
// a run is going, it is costing more than it should, and the person who can
// change that is reading a compose file over somebody's shoulder. So the same
// file is editable here, next to what it is doing to the run in front of you —
// and it is still just a file underneath, so nothing is hidden from git.
//
// One page, no framework, no build step. It is a text area and three tables.
const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>ammit</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root{--bg:#0b0e14;--panel:#141924;--edge:#26303f;--ink:#e8ecf3;--dim:#8a94a6;
      --ok:#7fb98a;--bad:#e8705a;--gold:#c9a227}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);
     font:14px/1.6 ui-sans-serif,system-ui,-apple-system,sans-serif}
header{display:flex;align-items:baseline;gap:1rem;padding:1.5rem 2rem;
       border-bottom:1px solid var(--edge)}
h1{margin:0;font-size:1.3rem;letter-spacing:.02em}
header span{color:var(--dim)}
main{max-width:78rem;margin:0 auto;padding:2rem;display:grid;gap:2rem}
section{background:var(--panel);border:1px solid var(--edge);border-radius:10px;
        padding:1.25rem 1.5rem}
h2{margin:0 0 .75rem;font-size:1rem;font-weight:600}
h2 small{color:var(--dim);font-weight:400;margin-left:.5rem}
textarea{width:100%;min-height:26rem;background:#0d1017;color:var(--ink);
         border:1px solid var(--edge);border-radius:8px;padding:1rem;
         font:13px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;resize:vertical}
button{background:var(--gold);color:#131313;border:0;border-radius:7px;
       padding:.55rem 1.1rem;font-weight:600;cursor:pointer}
button.ghost{background:transparent;color:var(--dim);border:1px solid var(--edge)}
.row{display:flex;gap:.75rem;align-items:center;margin-top:.75rem}
.msg{color:var(--dim)}
table{width:100%;border-collapse:collapse;font-size:13px}
th{text-align:left;color:var(--dim);font-weight:500;padding:.35rem .6rem;
   border-bottom:1px solid var(--edge)}
td{padding:.35rem .6rem;border-bottom:1px solid #1b2230;vertical-align:top}
td.n{text-align:right;font-variant-numeric:tabular-nums}
.tag{padding:.1rem .45rem;border-radius:5px;font-size:12px}
.run{background:#1b2740;color:#9db9ee}.ok{background:#17301f;color:var(--ok)}
.bad{background:#31191a;color:var(--bad)}
input[type=text]{background:#0d1017;color:var(--ink);border:1px solid var(--edge);
                 border-radius:7px;padding:.5rem .7rem;min-width:14rem}
a{color:var(--gold)}
</style></head><body>
<header><h1>ammit</h1><span>the scales, the record, and the eating</span>
<span style="margin-left:auto"><a href="{{charts}}" target="_blank">charts</a></span></header>
<main>
  <section>
    <h2>Limits <small>saved to the same file the server reads every tick</small></h2>
    <textarea id="limits" spellcheck="false"></textarea>
    <div class="row"><button onclick="save()">Save</button>
      <button class="ghost" onclick="load()">Reload</button>
      <span class="msg" id="msg"></span></div>
  </section>
  <section>
    <h2>Queue <small>started in order, as slots free</small></h2>
    <div class="row"><input type="text" id="qname" placeholder="ticket or job name">
      <button onclick="enqueue()">Queue it</button></div>
    <table id="queue"></table>
  </section>
  <section><h2>Runs</h2><table id="runs"></table></section>
  <section><h2>What ammit did <small>every limit crossed, and what followed</small></h2>
    <table id="judgements"></table></section>
</main>
<script>
const j = (p) => fetch(p).then(r => r.json());
const esc = (s) => String(s ?? "").replace(/[<>&]/g, c => ({"<":"&lt;",">":"&gt;","&":"&amp;"}[c]));
const ago = (t) => t ? new Date(t*1000).toLocaleString() : "";

async function load() {
  const text = await fetch("/limits.yml").then(r => r.text());
  document.getElementById("limits").value = text;
  document.getElementById("msg").textContent = "";
}
async function save() {
  const body = document.getElementById("limits").value;
  const res = await fetch("/limits.yml", {method: "PUT", body});
  document.getElementById("msg").textContent =
    res.ok ? "saved — in force on the next tick" : "could not save: " + await res.text();
}
async function enqueue() {
  const name = document.getElementById("qname").value.trim();
  if (!name) return;
  await fetch("/queue", {method: "POST", headers: {"Content-Type": "application/json"},
                         body: JSON.stringify({name})});
  document.getElementById("qname").value = "";
  refresh();
}
function table(el, cols, rows, cell) {
  el.innerHTML = "<tr>" + cols.map(c => "<th>" + c + "</th>").join("") + "</tr>" +
    (rows || []).map(r => "<tr>" + cell(r).map(c => "<td>" + c + "</td>").join("") + "</tr>").join("");
}
async function refresh() {
  const [runs, judgements, queue] = await Promise.all(
    [j("/runs"), j("/judgements"), j("/queue")]);
  table(document.getElementById("runs"),
    ["run", "started", "verdict", "minutes", "usd", "turns", "summary"], runs, r => [
      esc(r.name), ago(r.started),
      "<span class='tag " + (r.verdict ? (r.verdict === "PASS" ? "ok" : "bad") : "run") + "'>" +
        esc(r.verdict || "running") + "</span>",
      Math.round(((r.finished || Date.now()/1000) - r.started) / 60),
      (r.usd || 0).toFixed(2), r.turns || 0, esc(r.summary || "")]);
  table(document.getElementById("judgements"),
    ["when", "scope", "subject", "rule", "threshold", "observed", "action", "outcome"],
    judgements, x => [ago(x.at), esc(x.scope), esc(x.subject || ""), esc(x.rule),
      x.threshold ?? "", x.observed ?? "",
      "<span class='tag " + (x.action === "warn" ? "run" : "bad") + "'>" + esc(x.action) + "</span>",
      esc(x.outcome || "")]);
  table(document.getElementById("queue"), ["name", "requested", "state"], queue,
    q => [esc(q.name), ago(q.requested), esc(q.state)]);
}
load(); refresh(); setInterval(refresh, 10000);
</script></body></html>`

// serveUI wires the page and the two routes it needs. The config is served and
// saved as the file it is: what the page shows is what the server reads, and
// what git would show.
func serveUI(mux *http.ServeMux, confPath, chartsURL string) {
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, strings.ReplaceAll(page, "{{charts}}", chartsURL))
	})
	mux.HandleFunc("GET /limits.yml", func(w http.ResponseWriter, r *http.Request) {
		body, err := os.ReadFile(confPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(body)
	})
	mux.HandleFunc("PUT /limits.yml", func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0, 8192)
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil || len(body) > 1<<20 {
				break
			}
		}
		// Refuse a file the server would then fail to read: a limits file that
		// does not parse is a pipeline with no limits, and finding that out on
		// the next tick is too late.
		if len(loadConfigBytes(body)) == 0 {
			http.Error(w, "that does not parse as limits", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(confPath, body, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("ammit: limits changed through the page (%d bytes)", len(body))
		w.WriteHeader(http.StatusNoContent)
	})
}

// loadConfigBytes is loadConfig for something not yet on disk.
func loadConfigBytes(raw []byte) Config {
	conf := Config{}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			conf[section] = map[string]string{}
			continue
		}
		if section == "" {
			continue
		}
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		conf[section][strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return conf
}
