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
func pageHTML() string { return `<!doctype html>
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
  /* Light, on the same palette the rest of Chiron uses. The names keep their
     jobs — navy is the ground the eye rests against, ink is what is written on
     it — so every rule below goes on meaning what it meant. */
  --navy:#FFFFFF; --deep:#FAFBFC; --deeper:#F1F3F6; --panel:#FFFFFF;
  --arrow:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 13 9'%3E%3Cpath d='M4.8.7 1 4.5l3.8 3.8M1 4.5h11' fill='none' stroke='%23000' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  --bronze:#CD7F32; --bronze-dim:#E5E7EB; --bronze-wash:rgba(205,127,50,.09);
  --ink:#0F1520; --mute:#4A5568; --dim:#A0AEC0;
  --ok:#22C55E; --bad:#EF4444;
  --mono:'JetBrains Mono',ui-monospace,SFMono-Regular,Menlo,monospace;
  --sans:'Plus Jakarta Sans','Inter',ui-sans-serif,system-ui,-apple-system,sans-serif;
}
*{box-sizing:border-box}
body{margin:0;background:var(--navy);color:var(--ink);font:15px/1.65 var(--sans)}
/* The bar stays navy on a light page: it is the house style, the same one
   dokimos.chiron.systems wears, and the mark is drawn for that ground. */
/* Back to the project this belongs to. A grey word with an arrow read as a
   footnote; it is the only way out of here, and it is one of two products
   rather than a link in prose — so it looks like something to press. */
/* The menu from dokimos.chiron.systems, measured rather than guessed. Same as
   the charts page: what differs between the two is the logo, the back button and
   which buttons there are. */
.home{justify-self:start;display:inline-flex;align-items:center;gap:9px;height:36px;
  padding:0 20px 0 16px;text-decoration:none;border:1px solid rgba(205,127,50,.35);
  color:#A0AEC0;font:400 14px/20px var(--mono);transition:color .15s,border-color .15s}
.home::before{content:"";width:13px;height:9px;flex:none;background:#CD7F32;
  -webkit-mask:var(--arrow) center/contain no-repeat;
  mask:var(--arrow) center/contain no-repeat;transition:transform .15s}
.home:hover{color:#F7FAFC;border-color:#CD7F32}
.home:hover::before{transform:translateX(-3px)}

.brand{justify-self:center;display:flex;align-items:center;gap:10px}
.brand-text b{font:700 15px/1 var(--sans);letter-spacing:.13em;color:#F7FAFC}
.brand-text small{font:400 9px/1.6 var(--mono);letter-spacing:.13em;color:#CD7F32}

nav.tabs{justify-self:end;margin-left:auto;align-self:center;display:flex;
  align-items:center;gap:8px;background:none;border:0;border-radius:0;padding:0}
.tabs button,.tabs .tab-link{background:none;border:0;border-radius:0;height:36px;
  padding:0 24px;color:#A0AEC0;cursor:pointer;text-decoration:none;
  display:inline-flex;align-items:center;
  font:400 14px/20px var(--mono);letter-spacing:0;text-transform:none;
  transition:color .15s,background .15s}
.tabs button:hover,.tabs .tab-link:hover{color:#F7FAFC;background:none}
.tabs button[aria-selected="true"]{background:#CD7F32;color:#001F3F;font-weight:700}
#charts{max-width:none;padding:0;display:block;height:calc(100vh - 5.4rem)}
</style></head><body>
<header>
<!-- Same mark, same bow-and-arrow path, as dokimos.chiron.systems — the light
     variant, since this header is dark. Static: the animation there is for a
     page somebody arrives at once, not a bar redrawn every refresh. -->
<a class="home" href="https://dokimos.chiron.systems">back</a>
<span class="brand" aria-hidden="true">
  <svg width="30" height="27" viewBox="0 0 180 160">
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
  <span class="brand-text"><b>AMMIT</b><small>Chiron.consulting</small></span>
</span>
<nav class="tabs" role="tablist">
  <button id="tab-scales" role="tab" aria-selected="true">Limits</button>
  <a class="tab-link" href="/charts">Runs</a>
  <a class="tab-link" href="/charts#window">A window</a>
  <a class="tab-link" href="/charts#lifetime">All time</a>
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
<script>
// The charts load the first time somebody asks for them: a tab nobody opened
// should not be polling Grafana every thirty seconds behind the page.
const CHARTS = "{{charts}}/d/ammit/ammit?theme=dark&refresh=30s";
let earliest = 0;
// The charts are a page of their own now, not a panel that hides.
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
</script>` + footerHTML() + `</body></html>` }

// serveUI wires the page and the routes it edits the config through.
func serveUI(mux *http.ServeMux, confPath, chartsURL string) {
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, strings.ReplaceAll(pageHTML(), "{{charts}}", chartsURL))
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
