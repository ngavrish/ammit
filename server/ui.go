package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

// The page where the limits are set.
//
// They lived in a file, which is honest and unusable at the moment it matters:
// a run is going, it is costing more than it should, and the person who can
// change that is editing a compose file over somebody's shoulder. So the limits
// are fields here, next to what they are doing to the run in front of you — and
// underneath they are still the same file, so the config stays in git and a
// change made under pressure is a diff somebody can read later.
//
// Fields rather than a blob of yaml because a limit is changed in a hurry, and
// "which of these numbers is the session one" is not a question to answer in a
// hurry. The whole file is still one click away for what fields cannot express:
// adding a command, renaming a section.
//
// One page, no framework, no build step.
const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>ammit</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Ctext y='13' font-size='13'%3E%E2%9A%96%3C/text%3E%3C/svg%3E">
<style>
/* The house style: deep navy, bronze, and mono for anything that is data.
   Fonts come from the network when there is one and fall back to what the
   machine already has when there is not — a page about a pipeline that is down
   should not itself depend on being online. */
@import url('https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;700&family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap');
:root{
  --navy:#001F3F; --deep:#001428; --deeper:#000d1a; --panel:#001a33;
  --bronze:#CD7F32; --bronze-dim:rgba(205,127,50,.28); --bronze-wash:rgba(205,127,50,.08);
  --ink:#F7FAFC; --mute:#A0AEC0; --dim:#4A5568;
  --ok:#4FA97C; --bad:#E06C5A;
  --mono:'JetBrains Mono',ui-monospace,SFMono-Regular,Menlo,monospace;
  --sans:'Plus Jakarta Sans','Inter',ui-sans-serif,system-ui,-apple-system,sans-serif;
}
*{box-sizing:border-box}
body{margin:0;background:var(--navy);color:var(--ink);font:15px/1.65 var(--sans)}
header{position:relative;display:flex;align-items:baseline;gap:1rem;padding:1.5rem 2.5rem;
       border-bottom:1px solid var(--bronze-dim);background:var(--deep)}
/* Centered independent of how wide the title or the tabs are, positioned
   rather than a flex child that would shove its neighbours. Full strength,
   not dimmed — the reference never fades this mark, and doing it here read
   as washed out next to the real thing. */
.brand{position:absolute;left:50%;top:50%;transform:translate(-50%,-50%);
       display:flex;align-items:center;gap:.5rem;pointer-events:none}
.brand svg{display:block}
/* Block children, not <b><br><small> — a bare <br> takes the browser's
   default line-height, which the reference component never touches (it
   stacks two block elements in a flex column instead), and that inherited
   leading was inflating this line's box past its own font-size. */
.brand-text{display:flex;flex-direction:column}
.brand b{display:block;font:800 20px/1 var(--sans);letter-spacing:-.01em;color:var(--ink)}
.brand small{display:block;font:600 7px/1 var(--mono);letter-spacing:.32em;color:var(--mute);
             text-transform:uppercase;margin-top:3px}
h1{margin:0;font:700 1.15rem/1 var(--mono);letter-spacing:.18em;text-transform:uppercase;
   color:var(--bronze)}
header .sub{color:var(--dim);font-size:13px;letter-spacing:.04em}
main{max-width:82rem;margin:0 auto;padding:2.5rem;display:grid;gap:1.5rem}
section{background:var(--deep);border:1px solid var(--bronze-dim);border-radius:2px;
        padding:1.5rem 1.75rem;box-shadow:0 0 15px rgba(205,127,50,.05)}
h2{margin:0 0 1.25rem;font:600 .8rem/1 var(--mono);letter-spacing:.16em;
   text-transform:uppercase;color:var(--bronze);display:flex;align-items:baseline;gap:.75rem}
h2 small{color:var(--dim);font:400 12px/1.4 var(--sans);letter-spacing:.02em;
         text-transform:none}
.hint{color:var(--dim);font-size:12px;line-height:1.4}
fieldset{border:0;border-top:1px solid rgba(205,127,50,.14);margin:0 0 1.5rem;padding:1.1rem 0 0}
legend{color:var(--bronze);font:500 11px/1 var(--mono);letter-spacing:.22em;
       text-transform:uppercase;padding-right:.75rem}
.fields{display:grid;grid-template-columns:repeat(auto-fill,minmax(19rem,1fr));
        gap:1rem 1.75rem}
label{display:grid;gap:.3rem}
label .k{font:500 12px/1.4 var(--mono);color:var(--mute);letter-spacing:.04em}
.field{display:flex;align-items:center;gap:.6rem}
input,select,textarea{background:var(--deeper);color:var(--ink);
      border:1px solid rgba(205,127,50,.18);border-radius:2px;padding:.5rem .7rem;
      font:400 13px/1.5 var(--mono);width:100%}
input:focus,select:focus,textarea:focus{outline:0;border-color:var(--bronze);
      box-shadow:0 0 0 1px var(--bronze-dim)}
input.changed,select.changed,textarea.changed{border-color:var(--bronze);
      background:var(--bronze-wash)}
.as{color:var(--dim);font:400 12px/1 var(--mono);white-space:nowrap;min-width:5.5rem}
textarea{min-height:24rem;line-height:1.7;resize:vertical}
button{background:transparent;color:var(--bronze);border:1px solid rgba(205,127,50,.4);
       border-radius:2px;padding:.55rem 1.4rem;cursor:pointer;
       font:700 12px/1 var(--mono);letter-spacing:.12em;text-transform:uppercase;
       transition:background .15s,color .15s}
button:hover{background:var(--bronze);color:var(--navy)}
button.ghost{color:var(--mute);border-color:rgba(160,174,192,.22)}
button.ghost:hover{background:rgba(160,174,192,.12);color:var(--ink)}
button:disabled{opacity:.4;cursor:default}
.row{display:flex;gap:.9rem;align-items:center;flex-wrap:wrap}
.bar{margin-top:1.25rem;padding-top:1.25rem;border-top:1px solid rgba(205,127,50,.14)}
.msg{color:var(--mute);font-size:12px;font-family:var(--mono)}
.msg.bad{color:var(--bad)} .msg.ok{color:var(--ok)}
table{width:100%;border-collapse:collapse;font:400 13px/1.5 var(--mono)}
th{text-align:left;color:var(--bronze);font-weight:500;font-size:11px;
   letter-spacing:.14em;text-transform:uppercase;padding:.5rem .75rem;
   border-bottom:1px solid var(--bronze-dim)}
td{padding:.45rem .75rem;border-bottom:1px solid rgba(160,174,192,.08);
   vertical-align:top;color:var(--mute)}
tr:hover td{background:rgba(205,127,50,.04);color:var(--ink)}
td.n{text-align:right;font-variant-numeric:tabular-nums}
.tag{padding:.15rem .55rem;border-radius:2px;font-size:11px;letter-spacing:.08em;
     text-transform:uppercase;border:1px solid}
.live{color:var(--bronze);border-color:var(--bronze-dim);background:var(--bronze-wash)}
.ok{color:var(--ok);border-color:rgba(79,169,124,.35);background:rgba(79,169,124,.08)}
.bad{color:var(--bad);border-color:rgba(224,108,90,.35);background:rgba(224,108,90,.08)}
a{color:var(--bronze);text-decoration:none;border-bottom:1px solid var(--bronze-dim);
  font:500 12px/1 var(--mono);letter-spacing:.12em;text-transform:uppercase}
a:hover{border-bottom-color:var(--bronze)}
[hidden]{display:none!important}
/* One page, two tabs. The charts are not a different product and should not
   look like one: same bar, same bronze, no second logo to explain. */
nav.tabs{margin-left:auto;display:flex;gap:.25rem;align-self:center}
nav.tabs button{background:transparent;border:1px solid transparent;border-radius:2px;
  padding:.45rem .9rem;color:var(--mute);cursor:pointer;
  font:500 12px/1 var(--mono);letter-spacing:.12em;text-transform:uppercase}
nav.tabs button:hover{background:var(--bronze-wash);color:var(--ink)}
nav.tabs button[aria-selected="true"]{background:var(--bronze-wash);
  border-color:var(--bronze-dim);color:var(--bronze)}
#charts{max-width:none;padding:0;display:block;height:calc(100vh - 5.4rem)}
#charts iframe{width:100%;height:100%;border:0;display:block;background:var(--navy)}
</style></head><body>
<header><h1>ammit</h1><span class="sub">the scales, the record, and the eating</span>
<!-- Same mark, same bow-and-arrow path, as dokimos.chiron.systems — the light
     variant, since this header is dark. Static: the animation there is for a
     page somebody arrives at once, not a bar redrawn every refresh. -->
<span class="brand" aria-hidden="true">
  <svg width="36" height="32" viewBox="0 0 180 160">
    <defs>
      <linearGradient id="brandArrow" x1="0%" y1="0%" x2="100%" y2="0%">
        <stop offset="0%" stop-color="#CD7F32" stop-opacity="0"/>
        <stop offset="40%" stop-color="#CD7F32" stop-opacity=".4"/>
        <stop offset="85%" stop-color="#CD7F32" stop-opacity=".9"/>
        <stop offset="100%" stop-color="#CD7F32"/>
      </linearGradient>
      <mask id="brandCut">
        <rect width="180" height="160" fill="#fff"/>
        <line x1="5" y1="80" x2="180" y2="80" stroke="#000" stroke-width="15" stroke-linecap="round"/>
      </mask>
    </defs>
    <g transform="rotate(-30 90 80)">
      <path d="M 58 18 C 115 20, 150 45, 150 80 C 150 115, 115 140, 58 142
               C 105 135, 128 110, 128 80 C 128 50, 105 25, 58 18 Z"
            fill="#F7FAFC" mask="url(#brandCut)"/>
      <path d="M 10 80 L 165 77.5 L 175 80 L 165 82.5 Z" fill="url(#brandArrow)"/>
    </g>
  </svg>
  <span class="brand-text"><b>CHIRON</b><small>SYSTEMS</small></span>
</span>
<nav class="tabs" role="tablist">
  <button id="tab-scales" role="tab" aria-selected="true" onclick="tab('scales')">scales</button>
  <button id="tab-charts" role="tab" aria-selected="false" onclick="tab('charts')">charts</button>
</nav></header>
<main id="scales">
  <section>
    <h2>Limits <small id="where"></small></h2>
    <div id="form"></div>
    <div id="raw" hidden>
      <textarea id="rawtext" spellcheck="false"></textarea>
    </div>
    <div class="row bar">
      <button id="save" onclick="save()">Save</button>
      <button class="ghost" onclick="load()">Discard changes</button>
      <button class="ghost" id="toggle" onclick="toggleRaw()">Edit the file</button>
      <span class="msg" id="msg"></span>
    </div>
  </section>
  <section>
    <h2>Queue <small>started in order, as slots free</small></h2>
    <div class="row"><input type="text" id="qname" placeholder="ticket or job name"
      style="max-width:18rem" onkeydown="if(event.key==='Enter')enqueue()">
      <button onclick="enqueue()">Queue it</button></div>
    <table id="queue"></table>
  </section>
  <section><h2>Runs</h2><table id="runs"></table></section>
  <section><h2>What ammit did <small>every limit crossed, and what followed</small></h2>
    <table id="judgements"></table></section>
</main>
<main id="charts" hidden>
  <!-- Grafana's own nav stays: the side menu carries Explore, Alerting and the
       rest, and hiding it to keep the tab bar clean cost more than it saved. -->
  <iframe id="charts-frame" title="charts" loading="lazy" src="about:blank"></iframe>
</main>
<script>
// The charts load the first time somebody asks for them: a tab nobody opened
// should not be polling Grafana every thirty seconds behind the page.
const CHARTS = "{{charts}}/d/ammit/ammit?theme=dark&refresh=30s";
let earliest = 0;
function chartsURL() {
  // A little before the first run, a little after now: the whole record and no
  // more. Falls back to six hours when there is nothing to measure against.
  const from = earliest ? Math.round((earliest - 120) * 1000) : "now-6h";
  return CHARTS + "&from=" + from + "&to=now";
}
function tab(which) {
  for (const name of ["scales", "charts"]) {
    document.getElementById(name).hidden = name !== which;
    document.getElementById("tab-" + name).setAttribute("aria-selected", name === which);
  }
  const f = document.getElementById("charts-frame");
  if (which === "charts" && f.getAttribute("src") === "about:blank") f.src = chartsURL();
  history.replaceState(null, "", which === "charts" ? "#charts" : "#");
}
if (location.hash === "#charts") addEventListener("DOMContentLoaded", () => tab("charts"));

const j = (p) => fetch(p).then(r => r.json());
const esc = (s) => String(s ?? "").replace(/[<>&"]/g, c =>
  ({"<":"&lt;",">":"&gt;","&":"&amp;",'"':"&quot;"}[c]));
const when = (t) => t ? new Date(t*1000).toLocaleString() : "";

// What each setting is for, in the words somebody changing it under pressure
// would use. Anything not named here still gets a field — this service does not
// decide which of somebody's limits are the interesting ones.
const HINT = {
  "queue.parallel": "runs allowed to be going at once",
  "timeouts.run": "the whole run, start to verdict",
  "timeouts.phase": "one phase",
  "timeouts.session": "one agent session",
  "timeouts.turn": "silence inside one turn — the hang nothing inside the run catches",
  "limits.usd_per_run": "spend on one run",
  "limits.turns_per_run": "turns one run may take",
  "retention.days": "finished runs older than this move to the archive",
  "retention.dir": "where archives are written",
};
const SECONDS = /^timeouts\./;
const ORDER = ["queue", "timeouts", "limits", "retention", "actions", "commands", "context"];
const NOTE = {
  timeouts: "Seconds. Zero means no limit — and a zero somebody typed is a decision, not a gap.",
  actions: "What happens when a limit is crossed. Each names a command below.",
  commands: "{run} {name} {phase} {agent} {session} and anything under context are substituted.",
  context: "Substituted into the commands above.",
};

let conf = {}, dirty = {};

function human(seconds) {
  const n = Number(seconds);
  if (!isFinite(n) || n <= 0) return n === 0 ? "no limit" : "";
  if (n < 90) return n + " s";
  if (n < 5400) return (n/60).toFixed(n % 60 ? 1 : 0) + " min";
  return (n/3600).toFixed(n % 3600 ? 1 : 0) + " h";
}

function field(section, key, value) {
  const name = section + "." + key;
  const secs = SECONDS.test(name);
  const num = /^-?\d+(\.\d+)?$/.test(value);
  const control = section === "actions"
    ? "<select data-name='" + name + "' onchange='touch(this)'>" +
        Object.keys(conf.commands || {}).concat(
          Object.keys(conf.commands || {}).includes(value) ? [] : [value])
        .map(c => "<option" + (c === value ? " selected" : "") + ">" + esc(c) + "</option>")
        .join("") + "</select>"
    : "<input data-name='" + name + "' value='" + esc(value) + "'" +
      (num ? " type='number' step='any' inputmode='decimal'" : " type='text'") +
      " oninput='touch(this)'>";
  return "<label><span class='k'>" + esc(key) + "</span>" +
    "<span class='field'>" + control +
    (secs ? "<span class='as' data-as='" + name + "'>" + human(value) + "</span>" : "") +
    "</span>" +
    (HINT[name] ? "<span class='hint'>" + esc(HINT[name]) + "</span>" : "") + "</label>";
}

function draw() {
  const sections = Object.keys(conf).sort(
    (a, b) => (ORDER.indexOf(a) + 1 || 99) - (ORDER.indexOf(b) + 1 || 99));
  document.getElementById("form").innerHTML = sections.map(s =>
    "<fieldset><legend>" + esc(s) + "</legend>" +
    (NOTE[s] ? "<div class='hint' style='margin:-.4rem 0 .8rem'>" + esc(NOTE[s]) + "</div>" : "") +
    "<div class='fields'>" +
      Object.keys(conf[s]).sort().map(k => field(s, k, conf[s][k])).join("") +
    "</div></fieldset>").join("");
}

function touch(el) {
  const name = el.dataset.name;
  const was = conf[name.split(".")[0]][name.split(".").slice(1).join(".")];
  if (String(el.value) === String(was)) delete dirty[name]; else dirty[name] = el.value;
  el.classList.toggle("changed", name in dirty);
  const as = document.querySelector("[data-as='" + name + "']");
  if (as) as.textContent = human(el.value);
  say(Object.keys(dirty).length ? Object.keys(dirty).length + " unsaved" : "");
}

function say(text, kind) {
  const el = document.getElementById("msg");
  el.textContent = text; el.className = "msg" + (kind ? " " + kind : "");
}

async function load() {
  conf = await j("/limits");
  dirty = {};
  draw();
  document.getElementById("rawtext").value = await fetch("/limits.yml").then(r => r.text());
  document.getElementById("where").textContent = "in force on the next tick, no restart";
  say("");
}

async function save() {
  const raw = !document.getElementById("raw").hidden;
  const res = raw
    ? await fetch("/limits.yml", {method: "PUT",
        body: document.getElementById("rawtext").value})
    : await fetch("/limits.yml", {method: "PATCH",
        headers: {"Content-Type": "application/json"}, body: JSON.stringify(dirty)});
  if (!res.ok) { say(await res.text() || "not saved", "bad"); return; }
  await load();
  say("saved — in force on the next tick", "ok");
}

function toggleRaw() {
  const raw = document.getElementById("raw"), form = document.getElementById("form");
  const showRaw = raw.hidden;
  raw.hidden = !showRaw; form.hidden = showRaw;
  document.getElementById("toggle").textContent = showRaw ? "Back to fields" : "Edit the file";
  say(showRaw ? "the file itself — comments, sections, everything" : "");
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
  // The charts open on the runs there are, not on a fixed twelve hours. A run
  // three hours old inside a twelve hour window is three quarters of empty
  // chart with the line squeezed against the right edge.
  const started = runs.map(r => r.started).filter(Boolean);
  if (started.length) earliest = Math.min(...started);
  table(document.getElementById("runs"),
    ["run", "started", "verdict", "minutes", "usd", "turns", "summary"], runs, r => [
      esc(r.name), when(r.started),
      "<span class='tag " + (r.verdict ? (r.verdict === "PASS" ? "ok" : "bad") : "live") + "'>" +
        esc(r.verdict || "running") + "</span>",
      Math.round(((r.finished || Date.now()/1000) - r.started) / 60),
      (r.usd || 0).toFixed(2), r.turns || 0, esc(r.summary || "")]);
  table(document.getElementById("judgements"),
    ["when", "scope", "subject", "rule", "threshold", "observed", "action", "outcome"],
    judgements, x => [when(x.at), esc(x.scope), esc(x.subject || ""), esc(x.rule),
      x.threshold ?? "", x.observed ?? "",
      "<span class='tag " + (x.action === "warn" ? "live" : "bad") + "'>" + esc(x.action) + "</span>",
      esc(x.outcome || "")]);
  table(document.getElementById("queue"), ["name", "requested", "state"], queue,
    q => [esc(q.name), when(q.requested), esc(q.state)]);
}

load(); refresh(); setInterval(refresh, 10000);
</script></body></html>`

// serveUI wires the page and the routes it edits the config through.
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

	// The whole file, for what fields cannot say: a new command, a renamed
	// section, a comment explaining why a number is what it is.
	mux.HandleFunc("PUT /limits.yml", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Refuse a file the server would then fail to read: a limits file that
		// does not parse is a pipeline with no limits, and finding that out on
		// the next tick is too late.
		if len(parseConfig(string(body))) == 0 {
			http.Error(w, "that does not parse as limits", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(confPath, body, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("ammit: limits replaced through the page (%d bytes)", len(body))
		w.WriteHeader(http.StatusNoContent)
	})

	// One field at a time: {"timeouts.run": "7200"}. Values are edited in place,
	// so the comments and the order somebody wrote survive being changed by
	// somebody else in a hurry.
	mux.HandleFunc("PATCH /limits.yml", func(w http.ResponseWriter, r *http.Request) {
		var values map[string]string
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&values); err != nil {
			http.Error(w, "not json", http.StatusBadRequest)
			return
		}
		if len(values) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		before, err := os.ReadFile(confPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		after, err := setValues(string(before), values)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(parseConfig(after)) == 0 {
			http.Error(w, "that does not parse as limits", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(confPath, []byte(after), 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, name := range sortedKeys(values) {
			log.Printf("ammit: %s set to %s through the page", name, values[name])
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// setValues rewrites named settings in the file and leaves everything else
// exactly as it was — indentation, blank lines, and above all the comments,
// which are usually the only record of why a limit is the number it is. A
// setting the file does not have yet is added to its section, or the section is
// added at the end.
func setValues(text string, values map[string]string) (string, error) {
	for name, value := range values {
		if !strings.Contains(name, ".") {
			return "", errNamed(name)
		}
		if strings.ContainsAny(value, "\n\r") {
			return "", errNamed(name)
		}
	}
	lines := strings.Split(text, "\n")
	section := ""
	done := map[string]bool{}

	for i, line := range lines {
		body, comment := cutComment(line)
		if strings.TrimSpace(body) == "" {
			continue
		}
		if !strings.HasPrefix(body, " ") && !strings.HasPrefix(body, "\t") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), ":"))
			continue
		}
		key, _, found := strings.Cut(strings.TrimSpace(body), ":")
		if !found {
			continue
		}
		name := section + "." + strings.TrimSpace(key)
		value, wanted := values[name]
		if !wanted {
			continue
		}
		indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
		// The gap before a trailing comment is kept, so a column of aligned
		// comments stays a column.
		gap := ""
		if comment != "" {
			gap = strings.Repeat(" ", max(1, len(body)-len(strings.TrimRight(body, " "))))
		}
		lines[i] = indent + strings.TrimSpace(key) + ": " + quoted(value) + gap + comment
		done[name] = true
	}

	for _, name := range sortedKeys(values) {
		if done[name] {
			continue
		}
		want, key, _ := strings.Cut(name, ".")
		lines = insertInto(lines, want, "  "+key+": "+quoted(values[name]))
	}
	return strings.Join(lines, "\n"), nil
}

// insertInto puts a line at the end of a section, creating the section if the
// file has never had one.
func insertInto(lines []string, want, line string) []string {
	section, last := "", -1
	for i, l := range lines {
		body, _ := cutComment(l)
		if strings.TrimSpace(body) == "" {
			continue
		}
		if !strings.HasPrefix(body, " ") && !strings.HasPrefix(body, "\t") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), ":"))
			continue
		}
		if section == want {
			last = i
		}
	}
	if last < 0 {
		return append(lines, "", want+":", line)
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:last+1]...)
	out = append(out, line)
	return append(out, lines[last+1:]...)
}

// quoted keeps a value that would confuse the reader — a comment marker, a
// leading or trailing space — as a quoted string.
func quoted(value string) string {
	if value == "" {
		return `""`
	}
	if body, comment := cutComment(value); comment != "" || body == "" ||
		strings.TrimSpace(value) != value {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return value
}

type errNamed string

func (e errNamed) Error() string { return "cannot set " + string(e) }

// parseConfig is loadConfig for text that is not on disk yet.
func parseConfig(raw string) Config {
	conf := Config{}
	section := ""
	for _, line := range strings.Split(raw, "\n") {
		line, _ = cutComment(line)
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
		conf[section][strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
	}
	return conf
}
