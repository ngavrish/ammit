package main

import _ "embed"

// The page. uPlot is vendored rather than fetched: a chart that needs the
// internet is a chart that is blank on the machine that most needs it.

//go:embed uPlot.iife.min.js
var uplotJS string

//go:embed uPlot.min.css
var uplotCSS string

func chartsPageHTML(active string) string { return `<!doctype html><meta charset="utf-8">
<title>ammit — charts</title>
<style>` + uplotCSS + `
/* Light, on the palette the rest of Chiron uses: bronze against navy text on
   white, with the greys that sit between them. The charts were navy-on-navy and
   the header had grown one control at a time until the row wrapped into itself. */
:root{
  --bronze:#CD7F32; --bronze-wash:rgba(205,127,50,.10);
  --navy:#001F3F; --ink:#0F1520; --slate:#4A5568; --mute:#A0AEC0;
  --hair:#E5E7EB; --hair-soft:#F1F3F6; --ground:#FFFFFF; --raised:#FAFBFC;
  --sky:#0EA5E9; --good:#22C55E; --bad:#EF4444; --warm:#FB923C;
  --arrow:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 13 9'%3E%3Cpath d='M4.8.7 1 4.5l3.8 3.8M1 4.5h11' fill='none' stroke='%23000' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  --lens:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cg fill='none' stroke='%23000' stroke-width='1.9' stroke-linecap='round'%3E%3Ccircle cx='6.8' cy='6.8' r='4.7'/%3E%3Cpath d='M10.4 10.4 14 14'/%3E%3C/g%3E%3C/svg%3E");
  --navy-bar:#001F3F; --on-bar:#F7FAFC; --on-bar-dim:#A0AEC0;
  --bar-hair:rgba(205,127,50,.28); --bar-soft:rgba(247,250,252,.07);
  --mono:"JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  --sans:"Plus Jakarta Sans","Inter",ui-sans-serif,system-ui,-apple-system,sans-serif;
}
*{box-sizing:border-box}
body{margin:0;background:var(--ground);color:var(--ink);font:14px/1.55 var(--sans)}

` + headerCSS + `
#bar{position:sticky;top:81px;z-index:25;padding:9px 24px;
  background:var(--raised);border-bottom:1px solid var(--hair);
  display:flex;align-items:center;gap:12px;flex-wrap:wrap}
#tz{font:400 12px/1 var(--mono);color:var(--slate);background:var(--ground);
  border:1px solid var(--hair);border-radius:8px;padding:7px 9px;cursor:pointer}
#tz:hover{border-color:var(--bronze);color:var(--bronze)}
#bar>button{font:inherit;font-size:12.5px;color:var(--ink);
  background:var(--ground);border:1px solid var(--hair);border-radius:8px;
  padding:7px 11px;cursor:pointer}
#bar>button:hover{border-color:var(--bronze);color:var(--bronze)}

/* How long a window is, as the four lengths anybody picks. A dropdown hid three
   of them and made the fourth something you had to open a menu to read — the same
   thing the tabs above stopped doing. */
#range{display:flex;align-items:center;gap:0;border:1px solid var(--hair);
  border-radius:8px;overflow:hidden;background:var(--ground)}
.span{border:0;border-right:1px solid var(--hair);background:none;cursor:pointer;
  padding:7px 13px;color:var(--slate);
  font:400 12.5px/1 var(--mono);transition:color .12s,background .12s}
.span:last-child{border-right:0}
.span:hover{color:var(--bronze)}
.span.on{background:var(--bronze);color:#001F3F;font-weight:700}

/* And how often to ask again. Off by default: a page that reloads under you is
   a page you cannot read a chart on, and most of the time nothing is running. */
#every{display:flex;align-items:center;gap:0;border:1px solid var(--hair);
  border-radius:8px;overflow:hidden;background:var(--ground)}
.tick{border:0;border-right:1px solid var(--hair);background:none;cursor:pointer;
  padding:7px 11px;color:var(--mute);
  font:400 12px/1 var(--mono);transition:color .12s,background .12s}
.tick:last-child{border-right:0}
.tick:hover{color:var(--bronze)}
.tick.on{background:var(--hair-soft);color:var(--navy);font-weight:700}
.tick.on[data-s="0"]{color:var(--mute);font-weight:400}

/* One control rather than six sitting next to each other. */
#filters{display:flex;align-items:center;gap:0;flex-wrap:wrap;
  border:1px solid var(--hair);border-radius:8px;background:var(--ground);
  padding:2px;overflow:hidden}
#filters input,#filters button{border:0;background:none;border-radius:6px;
  padding:6px 10px;font:inherit;font-size:12.5px;color:var(--ink)}
#filters input::placeholder{color:var(--mute)}
#filters input:focus{outline:0;background:var(--bronze-wash)}

/* The ticket is what anybody types first and it was the smallest thing on the
   strip. A lens in front of it, room for a whole ticket key, and the type that
   ticket keys are written in. */
.find{display:flex;align-items:center;padding-left:10px;border-radius:7px}
#seek{border:1px solid var(--hair);border-radius:8px;background:var(--ground)}
#seek input{border:0;background:none;padding:8px 12px;font:inherit;font-size:14.5px;
  color:var(--ink);width:52ch;outline:0}
#seek input::placeholder{color:var(--mute)}
#seek:focus-within{background:var(--bronze-wash)}
.how{color:var(--mute);font-size:11.5px;margin:6px 0 -6px}
.panel.away{display:none}
.bars{display:grid;grid-template-columns:auto 1fr auto;gap:6px 14px;align-items:center;
  font:12px/1.4 var(--mono);font-variant-numeric:tabular-nums;max-width:900px}
.bars .n{color:var(--ink);white-space:nowrap}
.bars .t{position:relative;height:18px;background:var(--hair-soft);border-radius:3px}
.bars .b{position:absolute;left:0;top:0;bottom:0;border-radius:3px}
.bars .lim{position:absolute;top:-3px;bottom:-3px;width:0;border-left:2px dashed var(--bad)}
.bars .v{color:var(--slate);white-space:nowrap;text-align:right}
.bars .v em{font-style:normal;color:var(--ink);font-weight:700;margin-left:10px;display:inline-block;min-width:4ch}
.bars .t.hdr{background:none;height:16px}
.bars .zero{position:absolute;left:0;top:0;font:11px var(--sans);color:var(--mute)}
.bars .limlab{position:absolute;top:0;transform:translateX(-100%);padding-right:6px;white-space:nowrap;
  font:600 11px var(--sans);color:var(--bad)}
.bars .cap{grid-column:1/-1;color:var(--mute);font:11px var(--sans)}
.bars .cap i{display:inline-block;width:10px;height:10px;border-radius:2px;margin:0 5px 0 10px;vertical-align:-1px}
.bars .cap i:first-child{margin-left:0}
.tl{font:11.5px/1.4 var(--mono);font-variant-numeric:tabular-nums}
.tl .r{display:grid;grid-template-columns:minmax(90px,auto) 1fr;gap:12px;align-items:center;margin:3px 0}
.tl .n{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;color:var(--ink)}
.tl .unfold{font:inherit;color:var(--mute);background:none;border:1px dashed var(--line);padding:2px 10px;cursor:pointer}
.tl .t{position:relative;height:22px;background:var(--hair-soft);border-radius:3px}
.tl .s i{font-style:normal;font-weight:400;opacity:.85;margin-left:6px}
.tl .s{position:absolute;top:2px;bottom:2px;border-radius:2px;overflow:hidden;white-space:nowrap;
  font:600 10px/18px var(--sans);color:#fff;padding:0 5px;box-sizing:border-box;min-width:2px}
.tl .ax{display:grid;grid-template-columns:minmax(90px,auto) 1fr;gap:12px;margin-top:6px}
.tl .ax div{position:relative;height:16px;color:var(--slate);font-size:11px}
.tl .ax div span{position:absolute;transform:translateX(-50%)}
.tl .keys{margin-top:16px;color:var(--slate);font:11px var(--sans)}
.tl .n b{font:600 12px var(--sans);color:var(--ink)}
.tl .ax .xn{text-align:center;font:600 12px var(--sans);color:var(--ink);height:auto;margin-top:8px}
.candles + .keys{margin-top:12px;color:var(--mute);font:11px var(--sans)}
.tl .keys i{display:inline-block;width:10px;height:10px;border-radius:2px;margin:0 5px 0 12px;vertical-align:-1px}
.candles{display:block;max-width:100%}
.candles g:hover rect{fill-opacity:.9}
.plot{position:relative}
.tip{position:absolute;display:none;pointer-events:none;z-index:5;background:var(--navy);color:#F7FAFC;
  font:12px/1.4 var(--mono);padding:6px 9px;border-radius:6px;white-space:nowrap;box-shadow:0 2px 10px rgba(15,21,32,.2)}
.vbars{cursor:crosshair;user-select:none}
.vbars g:hover rect{filter:brightness(.85)}
.vbars + .keys{margin-top:14px;color:var(--slate);font:11px var(--sans)}
.vbars + .keys i{display:inline-block;width:10px;height:10px;border-radius:2px;margin:0 5px 0 12px;vertical-align:-1px}
.vbars + .keys i:first-child{margin-left:0}
.vbars + .keys span{color:var(--mute);margin-left:12px}
.stats{display:grid;grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:12px 18px}
.stat{border:1px solid var(--hair);border-radius:8px;padding:10px 12px;background:var(--ground)}
.stat b{display:block;font:700 22px/1.2 var(--sans);color:var(--navy);letter-spacing:-.01em;white-space:nowrap}
.stat span{font:12px/1.4 var(--sans);color:var(--slate)}
.pie{display:flex;align-items:center;gap:28px;flex-wrap:wrap}
.pie canvas{flex:none}
.slices{width:auto;min-width:280px;font-variant-numeric:tabular-nums}
.slices td{padding:4px 10px 4px 0;border-bottom:1px solid var(--hair-soft);white-space:nowrap}
.slices td:nth-child(n+2){text-align:right;color:var(--slate)}
.slices i{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:8px;vertical-align:-1px}
.slices tr{cursor:pointer}
.slices tr.hot td{background:var(--bronze-wash)}
.slices tr.off td{color:var(--mute);text-decoration:line-through}
.slices tr.off i{opacity:.3}
.find::before{content:"";width:13px;height:13px;flex:none;opacity:.5;
  background:var(--slate);
  -webkit-mask:var(--lens) center/contain no-repeat;
  mask:var(--lens) center/contain no-repeat}
.find:focus-within{background:var(--bronze-wash);
  box-shadow:0 0 0 1px rgba(205,127,50,.35)}
.find:focus-within::before{background:var(--bronze);opacity:1}
#q{font:600 13.5px/1 var(--mono);letter-spacing:.03em;padding:9px 14px 9px 8px;
  text-transform:uppercase;min-width:230px}
#q::placeholder{font-weight:400;letter-spacing:.02em;text-transform:none}
#q:focus{background:none}
#filters input[type=date]{font:11.5px/1 var(--mono);color:var(--slate);
  padding:6px;min-width:116px}
.sep{width:1px;align-self:stretch;background:var(--hair);margin:3px 2px}
.when{display:flex;align-items:center;gap:2px}
.when i{font:600 9.5px/1 var(--sans);font-style:normal;letter-spacing:.1em;
  text-transform:uppercase;color:var(--mute);padding:0 6px 0 9px}
#clear{color:var(--mute);cursor:pointer}
#clear:hover{color:var(--bad)}

#note{margin-left:auto;color:var(--slate);font:600 11px/1 var(--sans);
  letter-spacing:.06em;text-transform:uppercase;font-variant-numeric:tabular-nums}

main{padding:20px 22px 72px;display:flex;flex-direction:column;gap:26px}
.row{font:700 11px/1 var(--sans);letter-spacing:.14em;text-transform:uppercase;
  color:var(--bronze);border-bottom:1px solid var(--hair);padding-bottom:8px;margin-top:12px}
.panel{border-left:3px solid var(--g,transparent);padding-left:14px;margin-left:-17px}
.row.grp{border-left:3px solid var(--g);padding-left:14px;margin-left:-17px}
.row.grp small{margin-left:10px;font:400 11px/1 var(--mono);color:var(--mute);letter-spacing:0}
.blurb{border-left:3px solid var(--g);padding-left:14px;margin:-14px 0 -6px -17px;color:var(--slate);
  font-size:12.5px;line-height:1.5;max-width:100ch}
.panel h2{font:700 14px/1.3 var(--sans);margin:0 0 3px;color:var(--navy)}
.panel p{margin:0 0 10px;color:var(--slate);font-size:12px;max-width:92ch}
/* Height for a plot, not for a message. A panel with nothing in this window
   holds one line of text, and reserving two thirds of the screen under it turns
   an empty window into a page of scrolling. */
.plot{min-height:0}
/* Room for a chart, not for a row of bars or a pie: those are as tall as they are. */
.plot.drawn:has(.uplot){min-height:360px}

table{border-collapse:collapse;width:100%;font:12px/1.5 var(--mono);display:block;
  overflow:auto;max-height:360px;border:1px solid var(--hair);border-radius:6px}
th,td{border-bottom:1px solid var(--hair-soft);padding:6px 11px;text-align:left;
  white-space:nowrap;font-variant-numeric:tabular-nums}
th{color:var(--slate);position:sticky;top:0;background:var(--raised);
  font-family:var(--sans);font-weight:700;font-size:11px;letter-spacing:.04em;
  text-transform:uppercase}
tbody tr:hover{background:var(--bronze-wash)}
tbody tr:nth-child(even){background:var(--raised)}
td.num{text-align:right}
td.t{color:var(--slate)}
td small{color:var(--mute);font-size:10.5px;margin-left:6px}
td .verdict{font-style:normal;font-weight:700}
.grid{display:grid;grid-template-columns:repeat(6,minmax(0,1fr));gap:26px 22px;align-items:start;
  grid-auto-flow:dense}   /* a narrow table climbs into the gap a wide one left */
.grid .panel{grid-column:span 6;min-width:0}
.grid table{max-height:420px}
.err{color:var(--bad);font-size:12px}
.empty{color:var(--mute);font-size:12px;padding:8px 0}
.u-legend{font-size:11px;color:var(--slate);margin-top:12px}

.tiles{display:grid;gap:14px;grid-template-columns:repeat(auto-fill,minmax(258px,1fr))}
.tile{border:1px solid var(--hair);border-left:3px solid var(--mute);border-radius:8px;
  padding:13px 15px;cursor:pointer;background:var(--ground);transition:.12s}
.tile:hover{border-color:var(--hair);border-left-color:var(--bronze);
  box-shadow:0 2px 10px rgba(15,21,32,.07);transform:translateY(-1px)}
.tile.green{border-left-color:var(--good)}
.tile.red{border-left-color:var(--bad)}
.tile.going{border-left-color:var(--warm)}
.tile.abandoned{border-left-color:var(--mute)}
.tile b{font:700 14px/1.3 var(--sans);display:block;color:var(--navy)}
.tile .verdict{font:700 10px/1.6 var(--sans);letter-spacing:.11em;
  text-transform:uppercase;color:var(--slate)}
.tile.green .verdict{color:var(--good)}
.tile.red .verdict{color:var(--bad)}
.tile.going .verdict{color:var(--warm)}
.tile.abandoned .verdict{color:var(--mute)}
.tile dl{display:grid;grid-template-columns:auto 1fr;gap:3px 12px;margin:10px 0 0;
  font:11.5px/1.5 var(--mono);font-variant-numeric:tabular-nums}
.tile dt{color:var(--mute);font-family:var(--sans)}
.tile dd{margin:0;text-align:right;color:var(--ink)}
.back{color:var(--bronze);cursor:pointer;font-size:12.5px;font-weight:600;
  display:inline-block;margin-bottom:4px}
.back:hover{text-decoration:underline}

/* The panel's own controls: quiet words, not buttons. They sit in the title
   line because that is what they act on. */
.pact{font:600 10px/1 var(--sans);letter-spacing:.08em;text-transform:uppercase;
  color:var(--mute);cursor:pointer;margin-left:12px;vertical-align:middle}
.pact:hover{color:var(--bronze)}
.by{margin-left:14px;font:600 10.5px/1 var(--sans);letter-spacing:.08em;text-transform:uppercase;color:var(--mute);vertical-align:middle}
.by button{font:600 10.5px/1 var(--sans);letter-spacing:.08em;text-transform:uppercase;border:1px solid var(--hair);
  background:none;color:var(--slate);border-radius:5px;padding:3px 7px;margin-left:4px;cursor:pointer}
.by button.on{background:var(--bronze);border-color:var(--bronze);color:#fff}
.pact.danger:hover{color:var(--bad)}
.chips{display:flex;flex-wrap:wrap;gap:8px}
.chip{border:1px dashed var(--hair);border-radius:8px;padding:6px 11px;
  font:12px/1 var(--sans);color:var(--slate);cursor:pointer}
.chip:hover{border-color:var(--bronze);color:var(--bronze)}

dialog#dlg{border:1px solid var(--hair);border-radius:10px;padding:18px 20px;
  width:min(760px,92vw);box-shadow:0 8px 40px rgba(15,21,32,.18)}
#dlg::backdrop{background:rgba(15,21,32,.35)}
#dlg h3{margin:0 0 4px;font:700 14px var(--sans);color:var(--navy)}
#dlg .hint{color:var(--slate);font-size:12px;margin:0 0 8px}
#dlg label{display:block;font:600 10.5px/1.8 var(--sans);color:var(--slate);
  text-transform:uppercase;letter-spacing:.06em;margin-top:10px}
#dlg input,#dlg select,#dlg textarea{width:100%;font:12.5px/1.5 var(--mono);
  border:1px solid var(--hair);border-radius:6px;padding:7px 9px;color:var(--ink);
  background:var(--ground)}
#dlg textarea{min-height:150px;resize:vertical}
#dlg .actions{display:flex;gap:10px;align-items:center;justify-content:flex-end;margin-top:14px}
#dlg .actions button{font:600 12px var(--sans);border:1px solid var(--hair);
  border-radius:8px;background:var(--ground);padding:8px 14px;cursor:pointer}
#dlg .actions button.go{background:var(--bronze);border-color:var(--bronze);color:#fff}
#dlgerr{margin-right:auto}
` + footerCSS + `
</style>
` + headerHTML(active) + `

<div id=bar>
  <div id=filters>
    <span class=find>
      <input id=q placeholder="APF-1934" size=22 autocomplete=off spellcheck=false>
    </span>
    <span class=sep></span>
    <label class=when><i>started</i><input id=sf type=date><input id=st type=date></label>
    <span class=sep></span>
    <label class=when><i>finished</i><input id=ff type=date><input id=ft type=date></label>
    <button id=clear title="clear every filter">clear</button>
  </div>
  <nav id=range>
    <button class=span data-h=3>3 hours</button>
    <button class="span on" data-h=12>12 hours</button>
    <button class=span data-h=48>2 days</button>
    <button class=span data-h=168>a week</button>
  </nav>
  <nav id=every title="how often to run every query again">
    <button class="tick on" data-s=0>manual</button>
    <button class=tick data-s=5>5s</button>
    <button class=tick data-s=300>5m</button>
    <button class=tick data-s=1800>30m</button>
    <button class=tick data-s=3600>1h</button>
  </nav>
  <button id=refresh title="run every query again now">refresh</button>
  <span class=find id=seek title="which charts to show: a word from the title, what it is about, its unit, or a line's name">
    <input id=find placeholder="find a chart: cost, memory, audit…" autocomplete=off spellcheck=false>
  </span>
  <button id=addchart title="a chart of your own, kept in charts-local.json">add chart</button>
  <select id=tz title="which clock the times are read in"></select>
  <span id=note></span>
</div>

<main id=main></main>

<dialog id=dlg>
  <h3>A chart of your own</h3>
  <p class=hint>The query returns <b>time, metric, value</b> — unix seconds, a line
  name, a number. On a run page it is narrowed to that run by itself. A second
  query (a limit line, say) goes after a line holding only <b>;;</b></p>
  <label>title</label><input id=ctitle autocomplete=off>
  <label>about</label><input id=cabout autocomplete=off>
  <div style="display:flex;gap:12px">
    <span style="flex:1"><label>kind</label>
      <select id=ckind><option>series</option><option>scatter</option><option>columns</option><option>stacked</option><option>bars</option><option>pie</option><option>stats</option><option>candles</option><option>timeline</option><option>table</option></select></span>
    <span style="flex:1"><label>unit</label><input id=cunit placeholder="ms"></span>
  </div>
  <label>sql</label><textarea id=csql spellcheck=false
    placeholder="SELECT at AS time, 'probe' AS metric, json_extract(payload,'$.latency_ms') AS value FROM events WHERE kind='netprobe' AND at*1000 BETWEEN $__from AND $__to ORDER BY 1"></textarea>
  <div class=actions>
    <span class=err id=dlgerr></span>
    <button id=dlgcancel>cancel</button>
    <button class=go id=dlgsave>save</button>
  </div>
</dialog>
` + footerHTML() + `
<script>` + uplotJS + `</script>
<script>
// What a limit is called when a person reads it, keyed by what the config
// calls it. Served by the same map the Limits page prints.
const LIMIT_TITLES=` + limitTitlesJS() + `;
function limitTitle(name){ return LIMIT_TITLES[name] || name }
// A sentence with a machine name in it, said in words: "against limits.usd_per_run"
// becomes "against the cost cap per run".
function humanize(text){
  return String(text).replace(/\b(limits|timeouts)\.[a-z_]+/g, m=>LIMIT_TITLES[m] ? "the "+LIMIT_TITLES[m].charAt(0).toLowerCase()+LIMIT_TITLES[m].slice(1) : m);
}
// Twenty, told apart: seventeen containers on a palette of ten put the two
// that mattered in the same orange.
const COLORS=["#CD7F32","#0EA5E9","#22C55E","#EF4444","#8B5CF6","#0891B2","#CA8A04","#2563EB","#DB2777","#059669",
  "#F97316","#6366F1","#14B8A6","#A16207","#7C3AED","#DC2626","#0284C7","#65A30D","#BE185D","#4B5563"];
const main=document.getElementById("main");
let panels=[], spec={};
let local=null;

// What was added and what was put away, as the server keeps it. Saved whole:
// the file is small and a whole write cannot half-apply.
async function fetchLocal(){
  local=await (await fetch("/charts/local")).json();
}
async function saveLocal(){
  const r=await fetch("/charts/local",{method:"POST",
    headers:{"Content-Type":"application/json"},body:JSON.stringify(local)});
  if(!r.ok){
    document.getElementById("note").textContent=await r.text();
    await fetchLocal();
    return;
  }
  boot();
}

// Which clock the axis is read in. uPlot draws in whatever the browser thinks
// local is, and a browser in a container thinks UTC — so a run at seven in the
// evening was labelled five, with nothing on the page saying which.
const HERE=Intl.DateTimeFormat().resolvedOptions().timeZone||"UTC";
const ZONES=[...new Set([HERE,"UTC","Europe/Berlin","Europe/Paris","Europe/London",
  "Europe/Moscow","America/New_York","America/Los_Angeles","Asia/Tokyo"])];
let zone = localStorage.getItem("ammit.tz") || HERE;

function fillZones(){
  const sel=document.getElementById("tz");
  sel.innerHTML=ZONES.map(z=>'<option value="'+z+'"'+(z===zone?" selected":"")+'>'+
    z.replace(/_/g," ")+(z===HERE?" (this machine)":"")+'</option>').join("");
  sel.onchange=()=>{ zone=sel.value; localStorage.setItem("ammit.tz",zone); load(); };
}

// uPlot hands the axis a moment and asks for a label; this reads it in the
// chosen zone rather than the browser's.
function tzFmt(){
  const day=new Intl.DateTimeFormat("en-GB",{timeZone:zone,day:"2-digit",month:"short"});
  const min=new Intl.DateTimeFormat("en-GB",{timeZone:zone,hour:"2-digit",minute:"2-digit",hour12:false});
  const sec=new Intl.DateTimeFormat("en-GB",{timeZone:zone,hour:"2-digit",minute:"2-digit",second:"2-digit",hour12:false});
  return (u,splits)=>{
    // Ticks closer than a minute need the seconds, or the axis reads 21:25
    // 21:25 21:26 21:26 and looks broken.
    const fine=splits.length>1 && splits[1]-splits[0]<60;
    return splits.map((t,i)=>{
      const d=new Date(t*1000);
      const hm=fine?sec.format(d):min.format(d);
      return (i===0||hm==="00:00") ? hm+"\n"+day.format(d) : hm;
    });
  };
}

function hours(){
  const on=document.querySelector(".span.on");
  return on ? +on.dataset.h : 12;
}

// Open on a window that holds something.
//
// Twelve hours was the default and the last run was the day before yesterday, so
// the page opened empty and three of the four lengths were indistinguishable from
// each other — every one of them showed nothing, so switching between them did
// nothing. The smallest window that reaches the newest run is the one worth
// opening on.
let pickedWindow=false;
async function windowForNewest(){
  if(pickedWindow) return; pickedWindow=true;
  try{
    const rows=await (await fetch("/charts/runs")).json();
    if(!rows.length) return;
    const ageH=(Date.now()/1000 - rows[0].started)/3600;
    const fits=[...document.querySelectorAll(".span")]
      .map(b=>+b.dataset.h).sort((a,b)=>a-b).find(h=>h>ageH);
    const want=fits||168;
    document.querySelectorAll(".span").forEach(x=>
      x.classList.toggle("on", +x.dataset.h===want));
  }catch(e){/* leave the default */}
}
let runs=[], scope="runs", chosen="";
// The pages, as the server registers them; the ones about every run at once
// have no window and no run.
const PAGES=` + pagesJSON() + `;
const ALLTIME=PAGES.filter(p=>p.allTime).map(p=>p.key);

// A verdict is a word this pipeline chose, and there are several for each of the
// two outcomes. Colour is about which of the two it was. ABANDONED is neither:
// it is what the sweeper calls a run nothing reported on, not a verdict about
// the work, so it is grey rather than red.
function shade(r){
  if(!r.finished) return "going";
  const v=(r.verdict||"").toUpperCase();
  if(v==="ABANDONED") return "abandoned";
  if(/PASS|GREEN|DONE|APPROVE|SHIPPED/.test(v)) return "green";
  if(!v) return "";
  return "red";
}
function ago(sec){
  if(!sec) return "—";
  const m=Math.round(sec/60);
  if(m<60) return m+" min";
  const h=Math.floor(m/60); return h+"h "+(m%60)+"m";
}
function when(t){return new Date(t*1000).toLocaleString()}

// Picking a run sends the run, and the server narrows every table to it. The
// window still travels because some queries draw against it, and it is opened
// wide enough to hold the whole run.
function windowMs(){
  if(chosen){
    const r=runs.find(x=>x.run===chosen);
    // Whole milliseconds. started is a float of seconds, and the server reads
    // the window as an integer: a fraction sent it back to its default.
    if(r) return [Math.floor(r.started*1000-2000), Math.ceil((r.finished||Date.now()/1000)*1000+2000)];
  }
  const to=Date.now();return[to-hours()*3600e3,to];
}
function runParam(){ return chosen?"&run="+encodeURIComponent(chosen):"" }

// The series come back as rows of (time, metric, value) — one long table, many
// lines. uPlot wants a column per line over one shared clock, so the rows are
// pivoted here and gaps are left as nulls rather than joined across.
// Seconds or milliseconds, whichever the query happened to return.
//
// Most of them select e.at, which is unix seconds; the window travels in
// milliseconds because that is what Grafana's macros were written in. Dividing
// everything by a thousand sent the seconds to 1970, and while the scale
// followed the data that was invisible — the axis was wrong and the line was
// there. Pinned to the window, the line left the chart entirely.
const secs = t => t > 1e12 ? t/1000 : t;

// opts.agg "sum": rows sharing a moment and a name are added rather than the
// last one winning - the same memory reading under one phase from three
// containers is their sum. opts.acc "cumsum": each line becomes its own
// running total, which is what a chart of spend or tokens "as they accrued"
// is. Both let one row-per-event query serve every colouring the heading
// offers, where there used to be a window-function query per colouring.
function pivot(cols,rows,opts){
  opts=opts||{};
  const ti=cols.indexOf("time"),mi=cols.indexOf("metric"),vi=cols.indexOf("value");
  if(ti<0||vi<0) return null;
  const times=[...new Set(rows.map(r=>secs(+r[ti])))].sort((a,b)=>a-b);
  const at=new Map(times.map((t,i)=>[t,i]));
  const names=mi<0?["value"]:[...new Set(rows.map(r=>String(r[mi])))];
  const data=[times,...names.map(()=>new Array(times.length).fill(null))];
  for(const r of rows){
    const i=at.get(secs(+r[ti])); const j=mi<0?0:names.indexOf(String(r[mi]));
    if(i==null||j<0) continue;
    const v=r[vi]==null?null:+r[vi];
    if(opts.agg==="sum"&&v!=null&&data[j+1][i]!=null) data[j+1][i]+=v; else data[j+1][i]=v;
  }
  if(opts.acc==="cumsum") names.forEach((n,j)=>{
    if(LIMIT.test(n)) return;
    let run=null; const col=data[j+1];
    for(let k=0;k<col.length;k++){ if(col[k]!=null){ run=(run||0)+col[k]; } if(run!=null) col[k]=run; }
  });
  return {data,names};
}

// What a number on the value axis is. The panel says it in Grafana's words —
// these charts were Grafana once — and anything it says that is not one of
// those is taken as the unit's own name and written as it is: turns, tokens.
function trim(v){ return String(+(+v).toFixed(2)) }
function short(v){
  if(v==null) return "";
  const a=Math.abs(v);
  return a>=1e9 ? trim(v/1e9)+"G" : a>=1e6 ? trim(v/1e6)+"M" : a>=1e3 ? trim(v/1e3)+"k" : trim(v);
}
// A span of time, in words. Never a letter: "27h 47m" is a code, and the
// axis is read by people who did not write it. Every word comes from
// TIME_WORDS, which the server hands over and a test guards.
const TIME_WORDS=` + timeWordsJS() + `;
function count(n,one,many){ return trim(n)+" "+(n===1?one:many) }
function dur(v){
  if(v==null) return "";
  const W=TIME_WORDS, a=Math.abs(v), neg=v<0?"-":"";
  if(a<60) return neg+count(a<10?+a.toFixed(1):Math.round(a),W.second,W.seconds);
  if(a<3600){ const m=Math.floor(a/60), sec=Math.round(a%60); return neg+count(m,W.minute,W.minutes)+(sec?" "+count(sec,W.second,W.seconds):"") }
  if(a<172800){ const h=Math.floor(a/3600), m=Math.round((a%3600)/60); return neg+count(h,W.hour,W.hours)+(m?" "+count(m,W.minute,W.minutes):"") }
  const d=Math.floor(a/86400), h=Math.round((a%86400)/3600);
  return neg+count(d,W.day,W.days)+(h?" "+count(h,W.hour,W.hours):"");
}
const mb=v=>v==null?"":Math.abs(v)>=1024?trim(v/1024)+" GB":short(v)+" MB";
// Under a second it is milliseconds, in words like the rest; from a second up
// it is the clock.
const ms=v=>v==null?"":Math.abs(v)<1000?count(Math.round(v),TIME_WORDS.millisecond,TIME_WORDS.milliseconds):dur(v/1000);
const UNITS={
  currencyUSD:{name:"USD", tick:v=>"$"+short(v), val:v=>"$"+(+v).toFixed(Math.abs(v)<10?3:2)},
  s:{name:"seconds", tick:dur, val:dur},
  ms:{name:"milliseconds", tick:v=>ms(v), val:v=>ms(v)},
  m:{name:"minutes", tick:v=>dur(v*60), val:v=>dur(v*60)},
  mbytes:{name:"MB", tick:mb, val:mb},
  bytes:{name:"bytes", tick:v=>short(v)+"B", val:v=>short(v)+"B"},
  percent:{name:"%", tick:v=>trim(v)+"%", val:v=>Math.round(v)+"%"},
  short:{name:"", tick:short, val:trim},
};
// Steps a clock is read in. Left to itself an axis of seconds steps by tens
// of thousands - 5h 33m, 11h 7m - because ten is what it knows.
const TIME_INCRS=[1,2,5,10,15,30,60,120,300,600,900,1800,3600,7200,10800,14400,21600,43200,86400,172800,604800];
// Counts step by whole numbers: an axis of turns does not pass 0.2 of one.
const COUNT_INCRS=[1,2,5,10,20,50,100,200,500,1000,2000,5000,10000,20000,50000,100000,200000,500000,1e6,2e6,5e6,1e7];
function incrsOf(U){
  if(U===UNITS.s) return TIME_INCRS;
  if(U===UNITS.ms) return [1,2,5,10,20,50,100,200,500].concat(TIME_INCRS.map(x=>x*1000));
  if(U===UNITS.m) return TIME_INCRS.map(x=>x/60).filter(x=>x>=1);
  if(U===UNITS.currencyUSD||U===UNITS.percent||U===UNITS.mbytes||U===UNITS.bytes||U===UNITS.short) return null;
  return COUNT_INCRS;   // turns, tokens, runs, findings, processes, requests/min - anything named
}
// A grid step that ends up with about five lines: on a clock one of the steps
// above, otherwise 1, 2 or 5 times a power of ten.
function niceStep(top,U){
  const inc=incrsOf(U);
  if(inc){ return inc.find(x=>top/x<=6)||inc[inc.length-1]; }
  const raw=top/5, m=Math.pow(10,Math.floor(Math.log10(raw))), f=raw/m;
  return (f<=1?1:f<=2?2:f<=5?5:10)*m;
}
function unitOf(u){
  u=(u||"").trim();
  if(!u) return UNITS.short;
  if(UNITS[u]) return UNITS[u];
  if(/^(min|mins|minutes)$/.test(u)) return UNITS.m;
  if(/^(sec|secs|seconds)$/.test(u)) return UNITS.s;
  return {name:u, tick:short, val:v=>trim(v)+" "+u};
}

// The value axis is as wide as its widest label and no wider. It was seventy-
// four pixels whatever it held, which is a lot of nothing beside "5".
function axisWidth(u,values,i,cycle){
  const ax=u.axes[i];
  if(cycle>1) return ax._size;
  let w=ax.ticks.size+ax.gap+8;
  const longest=(values||[]).reduce((a,v)=>String(v).length>a.length?String(v):a,"");
  if(longest){ u.ctx.font=ax.font[0]; w+=u.ctx.measureText(longest).width/devicePixelRatio; }
  return Math.ceil(w);
}

// What each chart is zoomed to, kept beside the chart rather than in it: the
// chart is thrown away and drawn again on every tick, and a zoom that lasted
// five seconds would not be a zoom. Forgotten when the view changes - another
// run, another span - because a time range from one is nonsense on the next.
const zooms=new WeakMap();
function zoomOf(box){
  const key=scope+"|"+chosen+"|"+hours();
  let z=zooms.get(box);
  if(!z||z.key!==key){ z={key,x:null,y:null}; zooms.set(box,z); }
  return z;
}

// The unit and the clock, big enough to read from where the chart is read.
// The size of a tick value, a step below the panel title: it names the axis,
// it does not compete with it.
const LABEL_FONT='600 12px "Plus Jakarta Sans","Inter",system-ui,sans-serif';

// Every query a panel has, as one table: the second one is usually the limit.
// Only the first was read for a long time, so a chart titled "against
// timeouts.request" never drew the timeout.
function rowsOf(payload,by){
  let cols=null, rows=[];
  for(const s of payload.series||[]){
    if(!s||s.error||!(s.rows||[]).length) continue;
    const c=s.columns||[]; const ti=c.indexOf("time"),vi=c.indexOf("value"),li=c.indexOf("label");
    // One set of points, coloured by whichever column the heading's switch
    // says - agent or phase - when the query returns those instead of metric.
    let mi=c.indexOf("metric"); if(by&&c.indexOf(by)>=0) mi=c.indexOf(by);
    if(ti<0||vi<0) continue;
    if(!cols) cols=["time","metric","value","label"];
    for(const r of s.rows) rows.push([r[ti], mi<0?"value":r[mi], r[vi], li<0?null:r[li]]);
  }
  // Content starts where the numbers do. The rule for every chart on this
  // page, applied once, here, on the way in: rows before the first non-zero
  // value and after the last are not drawn, so a run of zeros at the edge
  // cannot leave a chart mostly blank. Zeros in the middle stay - they are
  // the shape - and a limit line is not content.
  let lo=Infinity, hi=-Infinity;
  for(const r of rows){ if(LIMIT.test(String(r[1]))) continue; const v=+r[2]; if(r[2]==null||isNaN(v)||v===0) continue; const t=secs(+r[0]); if(t<lo) lo=t; if(t>hi) hi=t; }
  if(lo<=hi) rows=rows.filter(r=>{ if(LIMIT.test(String(r[1]))) return true; const t=secs(+r[0]); return t>=lo&&t<=hi; });
  return {cols:cols||[],rows};
}
const LIMIT=/^(limits|timeouts|loops)\.|limit|cap|ceiling/i;
// A verdict wears its own colour, whatever position it holds in the legend:
// green is green, a refusal is red, an abandoned run is grey, a run with no
// verdict yet is the warm colour a running tile has. Everything else takes
// the next colour of the palette.
function colourFor(name,i){
  const v=String(name).toUpperCase();
  if(/ABANDON/.test(v)) return "#A0AEC0";
  if(/PASS|GREEN|DONE|APPROVE|SHIPPED/.test(v)) return "#22C55E";
  if(/RED|FAIL|BLOCK|ERROR|REFUSE/.test(v)) return "#EF4444";
  if(/STOP|CUT|KILL|TIMEOUT/.test(v)) return "#FB923C";   // ammit's hand, not the work's verdict
  if(/NONE|RUNNING/.test(v)) return "#0EA5E9";
  if(/UNKNOWN|\?/.test(v)) return "#4A5568";
  return COLORS[i%COLORS.length];
}
const FONT='"Plus Jakarta Sans","Inter",system-ui,sans-serif';

function drawSeries(box,payload,kind){
  const bad=(payload.series||[]).find(s=>s&&s.error);
  if(bad){box.classList.remove("drawn");box.innerHTML='<div class=err>'+bad.error+'</div>';return}
  box._payload=payload;
  const P=payload.panel||{};
  const all=rowsOf(payload,box.dataset.by||(P.by||[])[0]);
  const p=pivot(all.cols,all.rows,P);
  box.classList.toggle("drawn", !!(p&&p.data[0].length));
  if(!p||!p.data[0].length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  box.dataset.metrics=p.names.join(" ");
  const U=unitOf(payload.panel&&payload.panel.unit);
  const Z=zoomOf(box);
  const from=payload.from/1000, to=payload.to/1000;
  // Where the data is, not counting the limit: a limit query carries one point
  // at the start of the window so the dashed line has somewhere to begin, and
  // that point stretched a forty-minute run across twelve hours of nothing.
  // And only the data inside the window: a query that forgets to filter by
  // time brings every point there has ever been, and the first of them is
  // not where this chart starts.
  let dlo=Infinity, dhi=-Infinity;
  p.names.forEach((n,i)=>{ if(LIMIT.test(n)) return;
    p.data[i+1].forEach((v,k)=>{ if(v!=null){ const t=p.data[0][k]; if(t<from||t>to) return; if(t<dlo) dlo=t; if(t>dhi) dhi=t; } }); });
  if(dlo===Infinity){ dlo=p.data[0][0]; dhi=p.data[0][p.data[0].length-1]; }
  const legendFmt=new Intl.DateTimeFormat("en-GB",{timeZone:zone,day:"2-digit",month:"short",
    hour:"2-digit",minute:"2-digit",second:"2-digit",hour12:false});
  // The series that matters most comes first - on the chart and in the
  // legend: sorted by peak, biggest first, the limit last. Seventeen
  // containers in the order the database happened to return them put the
  // one burning ten cores eleventh.
  {
    const peak=p.data.slice(1).map(col=>Math.max(...col.filter(v=>v!=null).map(Math.abs),-Infinity));
    // A stack is drawn tallest-first and its legend reads in draw order, so
    // for a stack the names go smallest-first and come out biggest-first.
    const asc=kind==="columns"||kind==="stacked";
    const order=p.names.map((_,i)=>i).sort((a,b)=>(LIMIT.test(p.names[a])?1:0)-(LIMIT.test(p.names[b])?1:0)||(asc?peak[a]-peak[b]:peak[b]-peak[a]));
    p.names=order.map(i=>p.names[i]); p.data=[p.data[0]].concat(order.map(i=>p.data[i+1]));
  }
  const isLim=p.names.map(n=>LIMIT.test(n));
  // A line is a shape, and one or two points have none: a lone dot in the
  // middle of an axis, or a slope between two runs a day apart that says
  // nothing about the day between. One number per run is a column per run.
  if(kind==="series"||!kind){
    const sparse=p.names.every((n,i)=>LIMIT.test(n)||p.data[i+1].filter(v=>v!=null).length<=2);
    if(sparse) kind="columns";
  }
  // What is drawn versus what is said. Stacked kinds draw running sums so the
  // top of one series is the floor of the next, and the legend still answers
  // with the series' own number.
  const own=p.data.slice(1);
  let cols=own, order=p.names.map((_,i)=>i);
  if(kind==="columns"||kind==="stacked"){
    const n=p.data[0].length; let floor=new Array(n).fill(0);
    cols=p.names.map((_,i)=>{
      if(isLim[i]) return own[i];
      const col=new Array(n); let last=0;
      for(let k=0;k<n;k++){
        let v=own[i][k];
        if(v==null) v = kind==="stacked" ? last : 0;   // a running total keeps its last value; a count is zero
        last=v; col[k]=floor[k]+v; floor[k]=col[k];
      }
      return col;
    });
    // The tallest stack first, so each one shows over the one beneath it.
    order=p.names.map((_,i)=>i).filter(i=>!isLim[i]).reverse().concat(p.names.map((_,i)=>i).filter(i=>isLim[i]));
  }
  // The data columns in the same order as the series that draw them. They
  // were not, once: the tallest stack drew under the wrong name and the
  // whole chart came out the colour of the last series.
  const data=[p.data[0]].concat(order.map(i=>cols[i]));
  const series=order.map(i=>{
    const n=p.names[i], color=colourFor(n,i);
    const s={label:isLim[i]?limitTitle(n):n,stroke:color,width:1.4,
      value:(u,v,si,k)=>{const o=own[i][k]; return o==null?"—":U.val(o)}};
    if(isLim[i]) return {...s,scale:"lim",dash:[6,4],width:1};
    if(kind==="scatter") return {...s,paths:()=>null,points:{show:true,size:6,fill:color,stroke:color}};
    if(kind==="columns") return {...s,paths:uPlot.paths.bars({size:[0.7,80],align:0}),fill:color,width:0,points:{show:false}};
    if(kind==="stacked") return {...s,fill:color+"55",width:1,points:{show:false}};
    return s;
  });
  const opts={
    width:box.clientWidth||900,
    // Two thirds of the window, whatever the panel asked for. A time series
    // squeezed into two hundred pixels is a line that goes up: the shape is the
    // whole point of drawing it, and the shape needs room.
    height:Math.max(360, Math.round(innerHeight*0.66)),
    scales:{
      // As wide as what came back, inside the window that was asked for. The
      // window alone drew a forty-minute run as a hair at the right edge of
      // twelve hours; the data alone let a run that merely overlaps a
      // three-hour window draw all twenty-four of its own.
      x:{time:true,range:(u,min,max)=>{
        if(Z.x) return Z.x;
        const lo=Math.max(dlo,from), hi=Math.min(dhi,to);
        if(!(hi>lo)) return [lo-600, lo+600];
        const pad=(hi-lo)*0.015; return [lo-pad, hi+pad];
      }},
      // Fitted to what is in view, unless somebody has zoomed it, in which case
      // it stays where they put it while the time axis moves under it.
      y:{auto:()=>!Z.y, range:(u,min,max)=>Z.y||uPlot.rangeNum(min,max,0.1,true)},
      // The limit rides the value axis without stretching it: a sixty-dollar
      // ceiling over an eight-dollar run would flatten the run to the floor.
      // Off the top when it is far away, and always in the legend.
      lim:{from:"y",range:(u,min,max)=>[min,max]},
    },
    // Drag along the time axis to zoom time, along the value axis to zoom the
    // values, and diagonally for both; double-click to have it all back. uni
    // is how far a drag has to go in one direction before it counts as only
    // that direction.
    cursor:{drag:{x:true,y:true,uni:30,dist:6,setScale:false},
            bind:{dblclick:(u,targ,handler)=>e=>{ Z.x=Z.y=null; u.setData(u.data); }}},
    hooks:{setSelect:[u=>{
      const sel=u.select;
      if(!(sel.width>0||sel.height>0)) return;
      const W=u.over.clientWidth, H=u.over.clientHeight;
      const alongX=sel.width>0 && sel.width<W-1;
      const alongY=sel.height>0 && sel.height<H-1;
      if(alongX) Z.x=[u.posToVal(sel.left,"x"), u.posToVal(sel.left+sel.width,"x")];
      if(alongY) Z.y=[u.posToVal(sel.top+sel.height,"y"), u.posToVal(sel.top,"y")];
      u.setSelect({left:0,top:0,width:0,height:0},false);
      if(alongX) u.setScale("x",{min:Z.x[0],max:Z.x[1]});
      if(alongY) u.setScale("y",{min:Z.y[0],max:Z.y[1]});
    }]},
    axes:[{stroke:"#4A5568",grid:{stroke:"#F1F3F6"},ticks:{stroke:"#E5E7EB"},size:44,
           values:tzFmt(),
           // Which clock the times are read in, said on the axis itself rather
           // than only in a dropdown at the top of the page.
           label:"time ("+zone.replace(/_/g," ")+")",labelSize:26,labelFont:LABEL_FONT,labelGap:10},
          {stroke:"#4A5568",grid:{stroke:"#F1F3F6"},ticks:{stroke:"#E5E7EB"},
           size:axisWidth,
           ...(incrsOf(U)?{incrs:incrsOf(U)}:{}),
           values:(u,vs)=>vs.map(v=>v==null?"":U.tick(v)),
           label:U.name||"value",labelSize:22,labelFont:LABEL_FONT,labelGap:4}],
    // The legend's moment in the same clock as the axis. Left alone uPlot
    // writes it in the browser's zone, beside an axis that says another.
    series:[{value:(u,t)=>t==null?"—":legendFmt.format(new Date(t*1000))},...series],
  };
  box.innerHTML="";
  const u=new uPlot(opts,data,box);
  if(Z.x) u.setScale("x",{min:Z.x[0],max:Z.x[1]});
  if(Z.y) u.setScale("y",{min:Z.y[0],max:Z.y[1]});
}

// The end of every line, as a bar: how many turns each phase took, how long
// each run went. The limit, if the panel has one, is a dashed mark on the
// same scale rather than a number in a corner.
function finals(payload,by){
  const P=payload.panel||{};
  const all=rowsOf(payload,by||(P.by||[])[0]), p=pivot(all.cols,all.rows,P);
  if(!p) return {parts:[],limit:null};
  let limit=null; const parts=[];
  p.names.forEach((n,i)=>{
    const col=p.data[i+1]; let v=null;
    for(let k=col.length-1;k>=0;k--) if(col[k]!=null){v=col[k];break}
    if(LIMIT.test(n)){ if(v!=null) limit={name:n,value:v}; return; }
    if(v!=null) parts.push({name:n,value:v});
  });
  parts.sort((a,b)=>b.value-a.value);
  return {parts,limit};
}
function drawBars(box,payload){
  const bad=(payload.series||[]).find(s=>s&&s.error);
  if(bad){box.classList.remove("drawn");box.innerHTML='<div class=err>'+bad.error+'</div>';return}
  // One column per row, left to right in the order they started. "Per run"
  // means the axis is runs, not time: a row is a run (its start is the row's
  // time, its name the label), and a chart with several numbers per run gets
  // several columns per run, coloured by which. A row per category - turns
  // by phase - is the same thing with the category for a label.
  //
  // The limit is a dashed line across, and every column knows its share of
  // it. Drag across the columns to keep only those runs; double-click for
  // all of them. The name and date filters at the top narrow it too.
  box._payload=payload;
  const P=payload.panel||{};
  const all=rowsOf(payload,box.dataset.by||(P.by||[])[0]); let limit=null; let rows=[];
  for(const r of all.rows){
    const v=r[2]==null?null:+r[2]; if(v==null||isNaN(v)) continue;
    const name=String(r[1]);
    if(LIMIT.test(name)){ limit={name,value:v}; continue; }
    rows.push({t:secs(+r[0]),name,value:v,label:r[3]==null?null:String(r[3])});
  }
  // agg "sum" on a bar chart: one bar per name, the rows added up - a count
  // per phase from one row per turn.
  if(P.agg==="sum"){
    const by=new Map();
    for(const x of rows){ const g=by.get(x.name); if(g){ g.value+=x.value; g.t=Math.max(g.t,x.t); } else by.set(x.name,{t:x.t,name:x.name,value:x.value,label:null}); }
    rows=[...by.values()].sort((a,b)=>b.value-a.value);
  }
  box.classList.toggle("drawn", rows.length>0);
  if(!rows.length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  const U=unitOf(payload.panel&&payload.panel.unit);
  const Z=zoomOf(box);
  const metrics=[...new Set(rows.map(x=>x.name))];
  const groups=new Map();
  rows.sort((a,b)=>a.t-b.t).forEach(x=>{ const k=x.label!=null?x.label+" "+x.t:String(x.t); if(!groups.has(k)) groups.set(k,{t:x.t,label:x.label,bars:[]}); groups.get(k).bars.push(x); });
  const when=new Intl.DateTimeFormat("en-GB",{timeZone:zone,day:"2-digit",month:"short",hour:"2-digit",minute:"2-digit",hour12:false});
  const short=new Intl.DateTimeFormat("en-GB",{timeZone:zone,day:"2-digit",month:"short"});
  let gs=[...groups.values()];
  const oneEach=gs.every(g=>g.bars.length===1), distinct=metrics.length===rows.length;
  const byName=oneEach&&distinct;
  gs.forEach(g=>{ g.title = g.label!=null ? g.label : (byName ? g.bars[0].name : when.format(new Date(g.t*1000))); });
  const seen={}; gs.forEach(g=>{seen[g.title]=(seen[g.title]||0)+1});
  gs.forEach(g=>{ if(seen[g.title]>1) g.title+=" "+when.format(new Date(g.t*1000)); });
  box.dataset.metrics=metrics.concat(gs.map(g=>g.title)).join(" ");
  // The filters at the top, when the page shows them, and the drag.
  const f=document.getElementById("filters").style.display!=="none" ? filterValues() : null;
  const total=gs.length;
  if(f){
    if(f.name) gs=gs.filter(g=>g.title.toLowerCase().includes(f.name));
    if(f.sf) gs=gs.filter(g=>g.t>=f.sf);
    if(f.st) gs=gs.filter(g=>g.t<=f.st+86399);
  }
  if(Z.x) gs=gs.filter(g=>g.t>=Z.x[0]&&g.t<=Z.x[1]);
  if(!gs.length){box.innerHTML='<div class=empty>no run matches that</div>';return}
  const esc=t=>String(t).replace(/[<&"]/g,x=>x==="<"?"&lt;":x==="&"?"&amp;":"&quot;");
  const colour=n=>COLORS[metrics.indexOf(n)%COLORS.length];
  const W=box.clientWidth||900, H=Math.max(320,Math.round(innerHeight*0.45));
  const L=78, R=16, T=18, B=(byName?34:62)+30, pw=W-L-R, ph=H-T-B;
  const top=Math.max(...gs.flatMap(g=>g.bars.map(x=>x.value)), limit?limit.value:0)||1;
  const nice=niceStep(top,U);
  const ymax=Math.ceil(top/nice)*nice||1;
  const y=v=>T+ph-(v/ymax)*ph;
  const slot=pw/gs.length, per=metrics.length, bw=Math.max(2,Math.min(36,(slot*0.72)/per));
  let g='';
  for(let v=0;v<=ymax+1e-9;v+=nice) g+='<line x1="'+L+'" x2="'+(W-R)+'" y1="'+y(v)+'" y2="'+y(v)+'" stroke="'+(v?"#E5E7EB":"#A0AEC0")+'"/>'+
    '<text x="'+(L-8)+'" y="'+(y(v)+4)+'" text-anchor="end" font-size="11" fill="#4A5568">'+esc(U.tick(v))+'</text>';
  const every=Math.ceil(gs.length/Math.max(4,Math.floor(pw/(byName?60:92))));
  const showVal=gs.length*per<=24;
  const share=v=>limit?Math.round(100*v/limit.value)+"%":"";
  const cols=gs.map((gr,i)=>{
    const x0=L+slot*i+(slot-bw*per)/2;
    const bars=gr.bars.map((x,j)=>{
      const bx=x0+bw*j, hot=limit&&x.value>=limit.value;
      const tip=gr.title+(per>1?" - "+x.name:"")+": "+U.val(x.value)+(limit?" ("+share(x.value)+" of the "+limitTitle(limit.name).toLowerCase()+")":"");
      return '<g data-tip="'+esc(tip)+'"><rect x="'+bx.toFixed(1)+'" y="'+y(x.value).toFixed(1)+'" width="'+bw.toFixed(1)+'" height="'+Math.max(0.5,y(0)-y(x.value)).toFixed(1)+
        '" fill="'+(hot?"#EF4444":colour(x.name))+'" rx="1.5"/>'+
        (showVal?'<text x="'+(bx+bw/2).toFixed(1)+'" y="'+(y(x.value)-4).toFixed(1)+'" text-anchor="middle" font-size="10.5" fill="#0F1520">'+esc(U.val(x.value))+(limit?'<tspan fill="#4A5568"> '+share(x.value)+'</tspan>':'')+'</text>':'')+'</g>';
    }).join("");
    const lab=i%every===0 ? (byName
      ? '<text x="'+(L+slot*(i+0.5)).toFixed(1)+'" y="'+(H-B+16)+'" text-anchor="middle" font-size="11" fill="#4A5568">'+esc(gr.title)+'</text>'
      : '<text transform="translate('+(L+slot*(i+0.5)).toFixed(1)+','+(H-B+12)+') rotate(35)" font-size="10.5" fill="#4A5568">'+esc(gr.label!=null&&seen[gr.label]===1?gr.title:(gr.label||"")+" "+short.format(new Date(gr.t*1000)))+'</text>') : '';
    return bars+lab;
  }).join("");
  const lim=limit?'<line x1="'+L+'" x2="'+(W-R)+'" y1="'+y(limit.value).toFixed(1)+'" y2="'+y(limit.value).toFixed(1)+'" stroke="#EF4444" stroke-width="1.5" stroke-dasharray="6 4"/>'+
    '<text x="'+(W-R)+'" y="'+(y(limit.value)-5).toFixed(1)+'" text-anchor="end" font-size="11" font-weight="600" fill="#EF4444">'+esc(limitTitle(limit.name)+" = "+U.val(limit.value))+'</text>':'';
  const keys=per>1?metrics.map(m=>'<i style="background:'+colour(m)+'"></i>'+esc(m)).join(""):'';
  const note=(gs.length<total?gs.length+" of "+total+" - ":"")+(limit?"hover a column for its share of the limit - ":"")+"drag across columns to keep a range, double-click for all";
  const xName=byName ? (payload.panel&&/by (phase|agent)/i.test(payload.panel.title) ? payload.panel.title.match(/by (phase|agent)/i)[1].toLowerCase()+"s" : "name") : "runs, in the order they started ("+zone.replace(/_/g," ")+")";
  box.innerHTML='<svg class="candles vbars" width="'+W+'" height="'+H+'" viewBox="0 0 '+W+' '+H+'" font-family="'+FONT.replace(/"/g,"")+'">'+g+cols+lim+axisNames(W,H,L,B,esc(U.name||"value"),esc(xName))+
    '<rect class=sel x="0" y="'+T+'" width="0" height="'+ph+'" fill="#CD7F32" fill-opacity=".15" style="display:none"/></svg>'+
    '<div class=keys>'+keys+'<span>'+esc(note)+'</span></div>';
  hoverTips(box);
  const svg=box.querySelector("svg"), sel=svg.querySelector(".sel");
  let x0=null;
  const px=e=>{const rc=svg.getBoundingClientRect(); return (e.clientX-rc.left)*W/rc.width};
  svg.onmousedown=e=>{x0=px(e); sel.setAttribute("x",x0); sel.setAttribute("width",0); sel.style.display=""; e.preventDefault();};
  svg.onmousemove=e=>{if(x0==null) return; const x=px(e); sel.setAttribute("x",Math.min(x0,x)); sel.setAttribute("width",Math.abs(x-x0));};
  svg.onmouseup=e=>{if(x0==null) return; const x1=px(e); const lo=Math.min(x0,x1), hi=Math.max(x0,x1); x0=null; sel.style.display="none";
    if(hi-lo<6) return;
    const i0=Math.max(0,Math.floor((lo-L)/slot)), i1=Math.min(gs.length-1,Math.floor((hi-L)/slot));
    if(i1<i0) return;
    Z.x=[gs[i0].t, gs[i1].t]; drawBars(box,payload);};
  svg.onmouseleave=()=>{x0=null; sel.style.display="none";};
  svg.ondblclick=()=>{Z.x=null; drawBars(box,payload);};
}

// The two axis names of an SVG chart: the value axis up the side, the other
// along the bottom. Every chart says what its axes are; none is left to be
// guessed from the title.
function axisNames(W,H,L,B,yName,xName){
  return '<text transform="translate(13,'+((H-B)/2+8).toFixed(0)+') rotate(-90)" text-anchor="middle" font-size="12" font-weight="600" fill="#0F1520">'+yName+'</text>'+
    '<text x="'+(L+(W-L)/2).toFixed(0)+'" y="'+(H-4)+'" text-anchor="middle" font-size="12" font-weight="600" fill="#0F1520">'+xName+'</text>';
}

// The number under the pointer, at once. A browser's own tooltip on an SVG
// title arrives a second late and small; this one follows the mouse.
function hoverTips(box){
  const svg=box.querySelector("svg"); if(!svg) return;
  let tip=box.querySelector(".tip");
  if(!tip){ tip=document.createElement("div"); tip.className="tip"; box.appendChild(tip); }
  svg.addEventListener("mousemove",e=>{
    const g=e.target.closest("g[data-tip]");
    if(!g){ tip.style.display="none"; return; }
    tip.textContent=g.dataset.tip; tip.style.display="block";
    const rc=box.getBoundingClientRect();
    let x=e.clientX-rc.left+14, y=e.clientY-rc.top-30;
    if(x+tip.offsetWidth>rc.width-8) x=e.clientX-rc.left-tip.offsetWidth-14;
    tip.style.left=x+"px"; tip.style.top=Math.max(0,y)+"px";
  });
  svg.addEventListener("mouseleave",()=>{tip.style.display="none"});
}

// What the boxes at the top say, as values: a name to look for, and the days.
function filterValues(){
  const day=id=>{const v=document.getElementById(id).value; return v?Math.floor(new Date(v).getTime()/1000):0};
  return {name:document.getElementById("q").value.trim().toLowerCase(), sf:day("sf"), st:day("st"), ff:day("ff"), ft:day("ft")};
}

// A spread, not a point: every run of the day as one candle. The query gives
// one row per run - its day for time, its number for value - and the page
// works out the rest: wick from the cheapest to the dearest, body across the
// middle half (25th to 75th percentile), a line at the average. A day with
// one run is a line with no body.
function drawCandles(box,payload){
  const bad=(payload.series||[]).find(s=>s&&s.error);
  if(bad){box.classList.remove("drawn");box.innerHTML='<div class=err>'+bad.error+'</div>';return}
  box._payload=payload;
  const P=payload.panel||{};
  const col=box.dataset.by||(P.by||[])[0];
  // One candle per day when the rows come by the day; one per name - agent,
  // phase, gate - when the heading's switch says so. The spread is the
  // same idea either way: what is usual, what is extreme, how many.
  const byName=!!(col&&col!=="time");
  const all=rowsOf(payload,byName?col:null);
  // Over time the candles stand on buckets - five minutes, an hour, a day -
  // chosen so the window holds a few dozen of them. Five thousand requests as
  // five thousand points is a smear; as forty candles it is a shape.
  let lo=Infinity, hi=-Infinity;
  for(const r of all.rows){ if(LIMIT.test(String(r[1]))) continue; const t=secs(+r[0]); if(t<lo) lo=t; if(t>hi) hi=t; }
  const bucket=byName?0:(TIME_INCRS.find(x=>(hi-lo)/x<=40)||604800);
  const by=new Map(); let limit=null;
  for(const r of all.rows){ const v=r[2]==null?null:+r[2]; if(v==null||isNaN(v)) continue;
    if(LIMIT.test(String(r[1]))){ limit={name:String(r[1]),value:v}; continue; }
    const k=byName?String(r[1]):Math.floor(secs(+r[0])/bucket)*bucket; if(!by.has(k)) by.set(k,[]); by.get(k).push(v); }
  const q=(a,f)=>{const i=(a.length-1)*f, lo=Math.floor(i), hi=Math.ceil(i); return a[lo]+(a[hi]-a[lo])*(i-lo)};
  let stat=[...by.keys()].map(k=>{const a=by.get(k).slice().sort((x,y)=>x-y);
    return {k,t:byName?0:k,n:a.length,min:a[0],max:a[a.length-1],q1:q(a,.25),q3:q(a,.75),avg:a.reduce((x,y)=>x+y,0)/a.length}});
  stat.sort(byName?((a,b)=>b.avg-a.avg):((a,b)=>a.t-b.t));
  const days=stat;
  box.classList.toggle("drawn", days.length>0);
  if(!days.length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  const U=unitOf(P.unit);
  box.dataset.metrics=byName?stat.map(s=>s.k).join(" "):days.length+" days";
  const W=box.clientWidth||900, H=Math.max(300,Math.round(innerHeight*0.45));
  const L=78, R=12, T=14, B=70, pw=W-L-R, ph=H-T-B;
  const top=Math.max(...stat.map(s=>s.max), limit?limit.value:0)||1;
  // A spread that runs from a second to ten minutes is a flat line with a
  // whisker on a linear axis: the body is a hundredth of the height. When the
  // widest value is twenty times the typical one, the axis goes logarithmic,
  // and the typical second and the worst ten minutes are both readable.
  const typical=(()=>{const all=[].concat(...[...by.values()]).filter(v=>v>0).sort((a,b)=>a-b); return all.length?all[Math.floor(all.length/2)]:1})();
  const log=top/Math.max(typical,1e-9)>20 && U!==UNITS.percent;
  const positive=[].concat(...[...by.values()]).filter(v=>v>0);
  const ymin=log?Math.max(Math.min(...positive)*0.8,1e-3):0;
  const nice=log?0:niceStep(top,U);
  const ymax=log?top*1.15:Math.ceil(top/nice)*nice;
  const y=log?(v=>T+ph-(Math.log(Math.max(v,ymin)/ymin)/Math.log(ymax/ymin))*ph):(v=>T+ph-(v/ymax)*ph);
  // Grid lines on a log axis: every clock step in range for a clock, every
  // power of ten times one, two and five otherwise.
  const gridAt=log?((incrsOf(U)||[1,2,5,10,20,50,100,200,500,1e3,2e3,5e3,1e4,2e4,5e4,1e5,2e5,5e5,1e6]).filter(v=>v>=ymin&&v<=ymax)):null;
  // The body takes most of its slot: a dozen days across a wide page are a
  // dozen wide candles, not a dozen matchsticks in a field.
  const slot=pw/stat.length, bw=Math.max(6,Math.min(72,slot*0.6));
  const day=new Intl.DateTimeFormat("en-GB",{timeZone:zone,day:"2-digit",month:"short"});
  const esc=t=>String(t).replace(/[<&"]/g,x=>x==="<"?"&lt;":x==="&"?"&amp;":"&quot;");
  let g='';
  const ticks=log?gridAt:(()=>{const o=[]; for(let v=0;v<=ymax+1e-9;v+=nice) o.push(v); return o})();
  for(const v of ticks) g+='<line x1="'+L+'" x2="'+(W-R)+'" y1="'+y(v)+'" y2="'+y(v)+'" stroke="'+(v?"#E5E7EB":"#A0AEC0")+'"/>'+
    '<text x="'+(L-8)+'" y="'+(y(v)+4)+'" text-anchor="end" font-size="11" fill="#4A5568">'+esc(U.tick(v))+'</text>';
  const every=Math.ceil(stat.length/12);
  // A bucket shorter than a day is named by its clock, a day by its date.
  const clock=new Intl.DateTimeFormat("en-GB",{timeZone:zone,hour:"2-digit",minute:"2-digit",hour12:false});
  const nameOf=s=>byName?s.k:(bucket&&bucket<86400?clock:day).format(new Date(s.t*1000));
  const noun=byName||(bucket&&bucket<86400)?(P.noun||"point"):"run";
  const c=stat.map((s,i)=>{const x=L+slot*(i+0.5);
    const tip=nameOf(s)+': '+s.n+' '+noun+(s.n===1?'':'s')+', min '+U.val(s.min)+', average '+U.val(s.avg)+', max '+U.val(s.max);
    return '<g data-tip="'+esc(tip)+'">'+
      '<line x1="'+x+'" x2="'+x+'" y1="'+y(s.max)+'" y2="'+y(s.min)+'" stroke="#4A5568" stroke-width="1.5"/>'+
      (s.n>1?'<rect x="'+(x-bw/2)+'" y="'+y(s.q3)+'" width="'+bw+'" height="'+Math.max(1,y(s.q1)-y(s.q3))+'" fill="#CD7F32" fill-opacity=".55" stroke="#CD7F32" rx="2"/>':'')+
      '<line x1="'+(x-bw/2-3)+'" x2="'+(x+bw/2+3)+'" y1="'+y(s.avg)+'" y2="'+y(s.avg)+'" stroke="#001F3F" stroke-width="2"/>'+
      (i%every===0?'<text x="'+x+'" y="'+(H-B+16)+'" text-anchor="middle" font-size="11" fill="#4A5568">'+esc(nameOf(s))+'</text>':'')+
      '</g>'});
  const lim=limit?'<line x1="'+L+'" x2="'+(W-R)+'" y1="'+y(limit.value).toFixed(1)+'" y2="'+y(limit.value).toFixed(1)+'" stroke="#EF4444" stroke-width="1.5" stroke-dasharray="6 4"/>'+
    '<text x="'+(W-R)+'" y="'+(y(limit.value)-5).toFixed(1)+'" text-anchor="end" font-size="11" font-weight="600" fill="#EF4444">'+esc(limitTitle(limit.name)+" = "+U.val(limit.value))+'</text>':'';
  box.innerHTML='<svg class=candles width="'+W+'" height="'+H+'" viewBox="0 0 '+W+' '+H+'" font-family="'+FONT.replace(/"/g,"")+'">'+g+c.join("")+lim+
    axisNames(W,H,L,B,esc((U.name||"value")+(log?", logarithmic":"")),byName?esc(col+"s, the usual first"):(bucket&&bucket<86400?"time, in buckets of "+dur(bucket)+" ("+zone.replace(/_/g," ")+")":"day ("+zone.replace(/_/g," ")+")"))+'</svg>'+
    '<div class=keys><span>wick: the least to the most - body: the middle half - line: the average'+(limit?' - dashed: '+esc(limitTitle(limit.name)):'')+(log?' - logarithmic axis: the typical and the worst are both readable':'')+'</span></div>';
  hoverTips(box);
}

// Numbers, not a chart: the query returns metric, value and the unit each
// value is read in, and each becomes a tile - the number large, the name
// under it. For the handful of figures a page is opened to find out.
function drawStats(box,payload){
  const s=payload.series[0]||{};
  if(s.error){box.classList.remove("drawn");box.innerHTML='<div class=err>'+s.error+'</div>';return}
  const c=s.columns||[]; const mi=c.indexOf("metric"), vi=c.indexOf("value"), ui=c.indexOf("unit");
  const rows=(s.rows||[]).filter(r=>r[vi]!=null);
  box.classList.toggle("drawn", rows.length>0);
  if(!rows.length){box.innerHTML='<div class=empty>nothing yet</div>';return}
  const when=new Intl.DateTimeFormat("en-GB",{timeZone:zone,day:"2-digit",month:"short",year:"numeric"});
  const esc=t=>String(t).replace(/[<&]/g,x=>x==="<"?"&lt;":"&amp;");
  // Whole numbers with their thousands, a total is read exactly; the unit's
  // word is under the tile, not repeated in it.
  const show=(v,u)=>{
    if(u==="moment") return when.format(new Date(secs(+v)*1000));
    const U=unitOf(u); const n=+v;
    if(U===UNITS.currencyUSD) return "$"+n.toLocaleString("en-GB",{minimumFractionDigits:2,maximumFractionDigits:2});
    // A tile in minutes stays in minutes - "270 minutes", not "4 hours 30
    // minutes" - because that is the unit the person asked to read it in.
    if(U===UNITS.m) return Math.round(n).toLocaleString("en-GB")+" "+TIME_WORDS.minutes;
    if(U===UNITS.s||U===UNITS.ms) return U.val(n);
    // Billions of tokens read as billions: 4.79G, not 4,794,086,795.
    if(u==="tokens") return short(n)+" tokens";
    return Math.round(n).toLocaleString("en-GB");
  };
  box.dataset.metrics=rows.map(r=>r[mi]).join(" ");
  box.innerHTML='<div class=stats>'+rows.map(r=>'<div class=stat><b>'+esc(show(r[vi],ui<0?"":r[ui]))+'</b><span>'+esc(r[mi])+'</span></div>').join("")+'</div>';
  const panel=box.closest(".panel"); if(panel&&box.closest(".grid")) panel.style.gridColumn="span 4";
}

// Spans on a clock: when each phase or session began and ended, one row per
// name. The query returns time (the start), metric (the row), value (the end)
// and, if it wants a different word on the bar than on the row, label.
function drawTimeline(box,payload){
  const bad=(payload.series||[]).find(s=>s&&s.error);
  if(bad){box.classList.remove("drawn");box.innerHTML='<div class=err>'+bad.error+'</div>';return}
  const all=rowsOf(payload);
  const from=payload.from/1000, to=payload.to/1000;
  const spans=all.rows.map(r=>({start:secs(+r[0]),row:String(r[1]),end:secs(+r[2]),label:r[3]==null?String(r[1]):String(r[3])}))
    .filter(x=>x.end>x.start&&x.end>=from&&x.start<=to);
  box.classList.toggle("drawn", spans.length>0);
  if(!spans.length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  const every=[...new Set(spans.map(x=>x.row))];
  // Past twelve rows the chart is a page of its own; the rest fold behind a
  // line that says how many, and one click opens them.
  const FOLD=12, folded=every.length>FOLD&&!box._unfolded;
  const rows=folded?every.slice(0,FOLD):every;
  const labels=[...new Set(spans.map(x=>x.label))];
  box.dataset.metrics=rows.concat(labels).join(" ");
  const lo=Math.max(from, Math.min(...spans.map(x=>x.start)));
  const hi=Math.min(to, Math.max(...spans.map(x=>x.end)));
  const W=hi-lo||1, pct=t=>(100*(Math.min(Math.max(t,lo),hi)-lo)/W).toFixed(3)+"%";
  const hm=new Intl.DateTimeFormat("en-GB",{timeZone:zone,hour:"2-digit",minute:"2-digit",hour12:false});
  const full=new Intl.DateTimeFormat("en-GB",{timeZone:zone,day:"2-digit",month:"short",hour:"2-digit",minute:"2-digit",second:"2-digit",hour12:false});
  const esc=t=>String(t).replace(/[<&"]/g,x=>x==="<"?"&lt;":x==="&"?"&amp;":"&quot;");
  const colour=l=>COLORS[labels.indexOf(l)%COLORS.length];
  const html=rows.map(r=>'<div class=r><span class=n title="'+esc(r)+'">'+esc(r)+'</span><span class=t>'+
    spans.filter(x=>x.row===r).map(x=>{
      const w=100*(Math.min(x.end,hi)-Math.max(x.start,lo))/W;
      return '<span class=s style="left:'+pct(x.start)+';width:'+w.toFixed(3)+'%;background:'+colour(x.label)+
        '" title="'+esc(x.label)+': '+full.format(new Date(x.start*1000))+' → '+full.format(new Date(x.end*1000))+' ('+dur(x.end-x.start)+')">'+
        (w>4?esc(x.label)+(w>14?' <i>'+dur(x.end-x.start)+'</i>':''):'')+'</span>';
    }).join("")+'</span></div>').join("");
  // Five or six marks along the bottom, at whole minutes or hours.
  const steps=[60,120,300,600,900,1800,3600,7200,10800,21600,43200,86400];
  const step=steps.find(st=>W/st<=7)||86400;
  let ticks=""; for(let t=Math.ceil(lo/step)*step;t<=hi;t+=step) ticks+='<span style="left:'+pct(t)+'">'+hm.format(new Date(t*1000))+'</span>';
  const keys=labels.length>1||labels[0]!==rows[0] ? '<div class=keys>'+labels.map(l=>'<i style="background:'+colour(l)+'"></i>'+esc(l)).join("")+
    ' <span style="margin-left:14px">'+zone.replace(/_/g," ")+'</span></div>' : '<div class=keys>'+zone.replace(/_/g," ")+'</div>';
  const rowName=payload.panel&&/session/i.test(payload.panel.title)?"agent":/branch/i.test((payload.panel||{}).title)?"run":"phase";
  const fold=folded?'<div class=r><span class=n></span><span><button class=unfold>'+(every.length-FOLD)+' more rows</button></span></div>':'';
  box.innerHTML='<div class=tl><div class=r><span class=n><b>'+rowName+'</b></span><span></span></div>'+html+fold+
    '<div class=ax><span></span><div>'+ticks+'</div></div><div class=ax><span></span><div class=xn>time of day ('+zone.replace(/_/g," ")+')</div></div>'+keys+'</div>';
  const b=box.querySelector(".unfold"); if(b) b.onclick=()=>{box._unfolded=true; drawTimeline(box,payload)};
}

// A share, not a history. The query is the same one the line chart reads —
// each line a running total — and a pie is what those totals are at the end
// of the window, one slice per line. So a chart that was a line can become a
// pie by saying so, and nobody rewrites the SQL.
function drawPie(box,payload){
  const bad=(payload.series||[]).find(s=>s&&s.error);
  if(bad){box.classList.remove("drawn");box.innerHTML='<div class=err>'+bad.error+'</div>';return}
  box._payload=payload;
  const parts=finals(payload,box.dataset.by).parts.filter(x=>x.value>0);
  box.classList.toggle("drawn", parts.length>0);
  if(!parts.length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  box.dataset.metrics=parts.map(x=>x.name).join(" ");
  const U=unitOf(payload.panel&&payload.panel.unit);
  const size=Math.max(260, Math.min(420, Math.round(innerHeight*0.42)));
  const dpr=devicePixelRatio||1, c=document.createElement("canvas");
  c.width=size*dpr; c.height=size*dpr; c.style.width=c.style.height=size+"px";
  const ctx=c.getContext("2d");
  const cx=size/2, cy=size/2, r=size/2-10, hole=r*0.55;
  const esc=t=>String(t).replace(/[<&]/g,x=>x==="<"?"&lt;":"&amp;");
  // A slice under the pointer steps out and says what it is; a click on a
  // slice or on its row takes it out of the whole, so the rest can be read as
  // shares of what is left. Click again and it is back.
  const off=new Set(); let hot=-1; let arcs=[];
  function draw(){
    const live=parts.filter((_,i)=>!off.has(i));
    const total=live.reduce((a,x)=>a+x.value,0)||1;
    ctx.setTransform(dpr,0,0,dpr,0,0); ctx.clearRect(0,0,size,size);
    arcs=[]; let a=-Math.PI/2;
    parts.forEach((x,i)=>{
      if(off.has(i)) return;
      const b=a+2*Math.PI*x.value/total, mid=(a+b)/2, out=i===hot?6:0;
      const ox=out*Math.cos(mid), oy=out*Math.sin(mid);
      ctx.beginPath(); ctx.moveTo(cx+ox+hole*Math.cos(a),cy+oy+hole*Math.sin(a));
      ctx.arc(cx+ox,cy+oy,r,a,b); ctx.arc(cx+ox,cy+oy,hole,b,a,true); ctx.closePath();
      ctx.fillStyle=COLORS[i%COLORS.length]; ctx.globalAlpha=hot>=0&&i!==hot?0.55:1; ctx.fill(); ctx.globalAlpha=1;
      ctx.strokeStyle="#fff"; ctx.lineWidth=2; ctx.stroke();
      arcs.push({i,a,b}); a=b;
    });
    ctx.textAlign="center"; ctx.textBaseline="middle";
    if(hot>=0&&!off.has(hot)){
      const x=parts[hot];
      ctx.fillStyle=COLORS[hot%COLORS.length]; ctx.font='700 13px '+FONT; ctx.fillText(x.name,cx,cy-20);
      ctx.fillStyle="#0F1520"; ctx.font='700 18px '+FONT; ctx.fillText(U.val(x.value),cx,cy+2);
      ctx.fillStyle="#4A5568"; ctx.font='500 11px '+FONT; ctx.fillText((100*x.value/total).toFixed(1)+"%",cx,cy+22);
    }else{
      ctx.fillStyle="#0F1520"; ctx.font='700 18px '+FONT; ctx.fillText(U.val(total),cx,cy-8);
      ctx.fillStyle="#4A5568"; ctx.font='500 11px '+FONT; ctx.fillText(off.size?"of what is on":"in all",cx,cy+12);
    }
    box.querySelectorAll(".slices tr").forEach((tr,i)=>{
      tr.classList.toggle("off",off.has(i)); tr.classList.toggle("hot",i===hot);
      const share=off.has(i)?"—":(100*parts[i].value/total).toFixed(1)+"%";
      tr.lastElementChild.textContent=share;
    });
  }
  function at(e){
    const rc=c.getBoundingClientRect(), x=e.clientX-rc.left-cx, y=e.clientY-rc.top-cy;
    const d=Math.hypot(x,y); if(d<hole-4||d>r+8) return -1;
    let ang=Math.atan2(y,x); if(ang<-Math.PI/2) ang+=2*Math.PI;
    const hit=arcs.find(s=>ang>=s.a&&ang<s.b); return hit?hit.i:-1;
  }
  c.onmousemove=e=>{const i=at(e); if(i!==hot){hot=i; c.style.cursor=i>=0?"pointer":""; draw();}};
  c.onmouseleave=()=>{hot=-1; draw();};
  c.onclick=e=>{const i=at(e); if(i<0) return; off.has(i)?off.delete(i):off.add(i); if(off.size===parts.length) off.clear(); draw();};
  box.innerHTML='<div class=pie></div>';
  const wrap=box.firstChild; wrap.appendChild(c);
  wrap.insertAdjacentHTML("beforeend",'<table class=slices><tbody>'+parts.map((x,i)=>
    '<tr data-i='+i+'><td><i style="background:'+COLORS[i%COLORS.length]+'"></i>'+esc(x.name)+'</td>'+
    '<td>'+U.val(x.value)+'</td><td></td></tr>').join("")+
    '</tbody></table>');
  wrap.querySelectorAll(".slices tr").forEach(tr=>{
    const i=+tr.dataset.i;
    tr.onmouseenter=()=>{hot=i; draw();}; tr.onmouseleave=()=>{hot=-1; draw();};
    tr.onclick=()=>{off.has(i)?off.delete(i):off.add(i); if(off.size===parts.length) off.clear(); draw();};
  });
  draw();
}

// A table that reads: numbers on the right, moments in the chosen clock,
// money with its sign, a verdict in its colour, a limit by its name. And a
// width that is its own: the panel spans as much of the six-column grid as
// the table needs, measured, so three narrow tables sit in one row.
function drawTable(box,payload){
  const s=payload.series[0]||{};
  if(s.error){box.innerHTML='<div class=err>'+s.error+'</div>';return}
  if(!(s.rows||[]).length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  const cols=s.columns||[];
  const when=new Intl.DateTimeFormat("en-GB",{timeZone:zone,day:"2-digit",month:"short",hour:"2-digit",minute:"2-digit",hour12:false});
  const esc=t=>String(t).replace(/[<&]/g,x=>x==="<"?"&lt;":"&amp;");
  const isMoment=c=>/^(at|started|finished|changed|time|when)$/i.test(c);
  const isMoney=c=>/^(usd|cost|bill)$|_usd$|^usd_/i.test(c);
  const isVerdict=c=>/verdict|ended|outcome|action/i.test(c);
  const cell=(c,v)=>{
    if(v==null||v==="") return '<td class=num>—</td>';
    if(isMoment(c)&&!isNaN(+v)&&+v>1e9) return '<td class=t>'+when.format(new Date(secs(+v)*1000))+'</td>';
    if(isMoney(c)&&!isNaN(+v)) return '<td class=num>$'+(+v).toFixed(Math.abs(+v)<10?3:2)+'</td>';
    if(c==="name"&&LIMIT_TITLES[v]) return '<td><b>'+esc(LIMIT_TITLES[v])+'</b> <small>'+esc(v)+'</small></td>';
    if(isVerdict(c)&&/^[A-Za-z_ ]+$/.test(String(v))) return '<td><i class=verdict style="color:'+colourFor(v,9)+'">'+esc(v)+'</i></td>';
    if(typeof v==="number"||(/^-?\d+(\.\d+)?$/.test(String(v)))) return '<td class=num>'+esc(v)+'</td>';
    return '<td>'+esc(v)+'</td>';
  };
  const th=cols.map(c=>'<th>'+esc(c)+'</th>').join("");
  const tr=s.rows.slice(0,300).map(r=>'<tr>'+r.map((v,i)=>cell(cols[i],v)).join("")+'</tr>').join("");
  box.innerHTML='<table style="width:max-content"><thead><tr>'+th+'</tr></thead><tbody>'+tr+'</tbody></table>';
  const table=box.firstChild, grid=box.closest(".grid");
  if(grid){
    const unit=grid.getBoundingClientRect().width/6;
    const span=Math.min(6,Math.max(2,Math.ceil((table.offsetWidth+8)/unit)));
    box.closest(".panel").style.gridColumn="span "+span;
  }
  table.style.width="100%";
}

async function load(){
  const [from,to]=windowMs();
  const q=(ALLTIME.includes(scope)?"&scope="+scope:"")+runParam();
  const note=document.getElementById("note");
  let drawn=0;
  await Promise.all(panels.map(async (p,i)=>{
    if(p.kind==="row") return;
    const box=document.getElementById("plot-"+i);
    if(!box) return;
    try{
      const r=await fetch("/charts/data?panel="+i+"&from="+from+"&to="+to+q);
      const payload=await r.json();
        box._kind=p.kind;
      box._draw=(box,payload,kind)=>{
        const by=box.dataset.by||((payload.panel||{}).by||[])[0];
        const k=((payload.panel||{}).kinds||{})[by]||kind||box._kind;
        ({table:drawTable,pie:drawPie,bars:drawBars,timeline:drawTimeline,candles:drawCandles,stats:drawStats}[k]||drawSeries)(box,payload,k);
      };
      box._draw(box,payload,p.kind);
      drawn++;
    }catch(e){ box.innerHTML='<div class=err>'+e+'</div>' }
  }));
  const withData=[...document.querySelectorAll(".plot")]
    .filter(b=>b.classList.contains("drawn")||b.querySelector("table")).length;
  note.textContent = withData===drawn ? drawn+" panels"
    : withData+" of "+drawn+" panels have data";
  sink();
  sift();
}

function filters(){
  const day=id=>{const v=document.getElementById(id).value;
    return v?Math.floor(new Date(v).getTime()/1000):""};
  const p=new URLSearchParams();
  const n=document.getElementById("q").value.trim();
  if(n) p.set("name",n);
  const sf=day("sf"), st=day("st"), ff=day("ff"), ft=day("ft");
  if(sf) p.set("started_from",sf);
  if(st) p.set("started_to",st+86399);
  if(ff) p.set("finished_from",ff);
  if(ft) p.set("finished_to",ft+86399);
  return p.toString();
}

async function fillRuns(){
  runs=await (await fetch("/charts/runs?"+filters())).json();
}

function drawTiles(){
  document.getElementById("note").textContent=runs.length+" runs";
  if(!runs.length){
    main.innerHTML='<div class=empty>no run matches that</div>';return;
  }
  main.innerHTML='<div class=tiles>'+runs.map(r=>
    '<div class="tile '+shade(r)+'" data-run="'+r.run+'">'+
      '<b>'+(r.name||r.run)+'</b>'+
      '<span class=verdict>'+(r.finished?(r.verdict||"no verdict"):"running")+'</span>'+
      '<dl>'+
        '<dt>started</dt><dd>'+when(r.started)+'</dd>'+
        '<dt>took</dt><dd>'+(r.finished?ago(r.finished-r.started):ago(Date.now()/1000-r.started))+'</dd>'+
        '<dt>turns</dt><dd>'+(r.turns||0)+'</dd>'+
        '<dt>cost</dt><dd>$'+(r.usd||0).toFixed(2)+'</dd>'+
      '</dl></div>').join("")+'</div>';
  main.querySelectorAll(".tile").forEach(t=>t.onclick=()=>go("/ammit/runs/"+t.dataset.run));
}

// Panels with nothing in them go to the bottom, together, under a line. Left
// where they are, they put a gap between every pair of charts and reading the
// page became scrolling past what is not there.
// One box for finding a chart, whatever you know about it: a word of the title,
// of what it is about, the unit it is drawn in, or the name of a line on it. Words that mean the same thing here are the same word —
// "cost" finds the USD charts and "money" finds them too. Every word typed has
// to land somewhere; where does not matter.
const KIN=[
  ["cost","usd","money","spend","spent","price","dollar","bill","стоимость","деньги","цена","бюджет","доллар"],
  ["time","seconds","second","duration","long","idle","silence","latency","minute","timeout","took","время","длительность","секунд","минут","таймаут","простой"],
  ["memory","mb","mbytes","ram","память"],
  ["tokens","token","cache","context","токен","токены","кэш","контекст"],
  ["turns","turn","ход","ходы","шаг"],
  ["cpu","percent","processor","процессор","процент"],
  ["requests","request","call","calls","запрос","запросы","вызов"],
  ["runs","run","прогон","прогоны"],
  ["phase","phases","фаза","фазы"],
  ["agent","agents","агент","агенты"],
  ["container","containers","контейнер","docker"],
  ["limit","limits","cap","ceiling","against","лимит","предел"],
  ["gate","gates","findings","finding","ворота","замечания"],
  ["processes","process","pids","процесс","процессы"],
  ["died","error","errors","failed","ошибка","ошибки","упал"],
];
function kin(word){
  const out=new Set([word]);
  for(const row of KIN) if(row.some(w=>w===word||w.startsWith(word)&&word.length>2)) row.forEach(w=>out.add(w));
  return [...out];
}
function haystack(sec,i){
  const p=panels[i]||{}, box=sec.querySelector(".plot");
  const U=unitOf(p.unit);
  // Not the query: every one of them says "AS time" and "AS metric", so a
  // word from the SQL is a word that finds everything.
  return [p.title,p.label,humanize(p.title),p.about,p.unit,U.name,box&&box.dataset.metrics]
    .filter(Boolean).join(" \n ").toLowerCase();
}
function sift(){
  const q=document.getElementById("find").value.trim().toLowerCase();
  const words=q.split(/\s+/).filter(Boolean);
  const secs=[...main.querySelectorAll(".panel")];
  let shown=0;
  secs.forEach(sec=>{
    const i=+sec.querySelector(".pact").dataset.i;
    const hay=haystack(sec,i);
    const hit=words.every(w=>kin(w).some(k=>hay.includes(k)));
    sec.classList.toggle("away",!hit);
    if(hit) shown++;
  });
  const note=document.getElementById("note");
  if(words.length) note.textContent=shown+" of "+secs.length+" charts match";
  else if(/charts match$/.test(note.textContent)) note.textContent=secs.length+" panels";
}
document.getElementById("find").oninput=()=>{clearTimeout(window._s);window._s=setTimeout(sift,120)};

function sink(){
  const empties=[...document.querySelectorAll(".panel")]
    .filter(p=>{const b=p.querySelector(".plot");
                return b && !b.classList.contains("drawn") && !b.querySelector("table")});
  document.getElementById("nothing")?.remove();
  if(!empties.length) return;
  const rule=document.createElement("div");
  rule.id="nothing"; rule.className="row";
  rule.textContent="Nothing in this window ("+empties.length+")";
  main.appendChild(rule);
  empties.forEach(p=>main.appendChild(p));
}

async function boot(){
  if(!document.getElementById("tz").options.length) fillZones();
  // The filter group collapses whole, so the row cannot half-empty and reflow.
  const picking = scope==="runs" && !chosen;
  document.getElementById("filters").style.display=(picking||ALLTIME.includes(scope))?"flex":"none";
  document.getElementById("seek").style.display=picking?"none":"flex";
  document.getElementById("range").style.display=scope==="window"?"flex":"none";
  if(scope==="window") await windowForNewest();
  if(picking){ await fillRuns(); drawTiles(); return; }
  if(scope==="runs") await fillRuns();
  spec=await (await fetch("/charts/panels"+(ALLTIME.includes(scope)?"?scope="+scope:""))).json(); panels=spec.panels;
  const r=runs.find(x=>x.run===chosen);
  const head=r?'<div class=back id=back>← every run</div><div class=row>'+
    (r.name||r.run)+" — "+when(r.started)+" — "+(r.verdict||"running")+
    " — "+(r.turns||0)+" turns — $"+(r.usd||0).toFixed(2)+'</div>':"";
  await fetchLocal();
  const section=(i,colour)=>{const p=panels[i];
    return '<section class=panel style="--g:'+colour+'" title="'+p.title.replace(/"/g,"&quot;")+'"><h2>'+(p.label||humanize(p.title))+
      ((p.by||[]).length>1?'<span class=by>by '+p.by.map((b,k)=>'<button data-i='+i+' data-by="'+b+'" class="'+(k?'':'on')+'">'+b+'</button>').join("")+'</span>':'')+
      '<span class=pact data-i='+i+' data-act=hide>hide</span>'+
      (p.custom?'<span class="pact danger" data-i='+i+' data-act=del>delete</span>':'')+
      '</h2>'+
      (p.about?'<p>'+p.about.replace(/[<&]/g,x=>x==="<"?"&lt;":"&amp;")+'</p>':'')+
      '<div class=plot id=plot-'+i+'></div></section>'};
  // What the page is opened for goes first, above every group.
  const tops=panels.map((p,i)=>p.top&&!p.hidden&&p.kind!=="row"?i:-1).filter(i=>i>=0);
  main.innerHTML=head+'<div class=how>drag along the time axis or the value axis to zoom that axis, '+
    'diagonally for both; double-click a chart to have it all back</div>'+
    (tops.length?'<div class=grid>'+tops.map(i=>section(i,"var(--bronze)")).join("")+'</div>':'')+
    grouped().map(g=>'<div class="row grp" style="--g:'+g.colour+'">'+g.title+
      '<small>'+g.items.length+'</small></div>'+
      (g.blurb?'<p class=blurb style="--g:'+g.colour+'">'+g.blurb+'</p>':'')+
      // Tables share the width: a three-column table does not need a screen.
      (g.key==="table"?'<div class=grid>':'')+g.items.map(i=>section(i,g.colour)).join("")+(g.key==="table"?'</div>':'')).join("");
  const away=panels.filter(p=>p.kind!=="row"&&p.hidden);
  if(away.length)
    main.innerHTML+='<div class=row>Hidden ('+away.length+')</div><div class=chips>'+
      away.map(p=>'<span class=chip data-t="'+p.title.replace(/"/g,"&quot;")+'">'+
        p.title+'</span>').join("")+'</div>';
  main.querySelectorAll(".pact").forEach(b=>b.onclick=()=>{
    const p=panels[+b.dataset.i];
    if(b.dataset.act==="del"){
      local.panels=local.panels.filter(x=>x.title!==p.title);
      local.hidden=local.hidden.filter(t=>t!==p.title);
    }else if(!local.hidden.includes(p.title)) local.hidden.push(p.title);
    saveLocal();
  });
  main.querySelectorAll(".by button").forEach(b=>b.onclick=()=>{
    const box=document.getElementById("plot-"+b.dataset.i);
    b.parentElement.querySelectorAll("button").forEach(x=>x.classList.toggle("on",x===b));
    box.dataset.by=b.dataset.by;
    if(box._payload&&box._draw) box._draw(box,box._payload,box._kind);
  });
  main.querySelectorAll(".chip").forEach(c=>c.onclick=()=>{
    local.hidden=local.hidden.filter(t=>t!==c.dataset.t);
    saveLocal();
  });
  const back=document.getElementById("back");
  if(back) back.onclick=()=>go("/ammit/runs");
  await load();
}
document.getElementById("refresh").onclick=load;

const dlg=document.getElementById("dlg");
document.getElementById("addchart").onclick=async()=>{
  if(!local) await fetchLocal();
  dlg.showModal();
};
document.getElementById("dlgcancel").onclick=e=>{e.preventDefault();dlg.close()};
document.getElementById("dlgsave").onclick=async e=>{
  e.preventDefault();
  const v=id=>document.getElementById(id).value.trim();
  const err=document.getElementById("dlgerr");
  const queries=v("csql").split(/\n\s*;;\s*\n/).map(x=>x.trim()).filter(Boolean);
  const p={kind:v("ckind"),title:v("ctitle"),about:v("cabout"),unit:v("cunit"),
           height:8,queries:queries};
  if(!p.title){err.textContent="it needs a title";return}
  if(!queries.length){err.textContent="it needs a query";return}
  local.panels=local.panels.filter(x=>x.title!==p.title).concat([p]);
  const r=await fetch("/charts/local",{method:"POST",
    headers:{"Content-Type":"application/json"},body:JSON.stringify(local)});
  if(!r.ok){err.textContent=await r.text();await fetchLocal();return}
  err.textContent="";dlg.close();boot();
};

// Asking again on a timer. One timer, replaced rather than stacked: setting an
// interval without clearing the last one is how a page ends up making four
// requests a second and nobody can say why.
let ticking=0;
document.querySelectorAll(".tick").forEach(b=>b.onclick=()=>{
  document.querySelectorAll(".tick").forEach(x=>x.classList.toggle("on",x===b));
  clearInterval(ticking);
  const s=+b.dataset.s;
  if(s>0) ticking=setInterval(load, s*1000);
});
document.querySelectorAll(".span").forEach(b=>b.onclick=()=>{
  document.querySelectorAll(".span").forEach(x=>x.classList.toggle("on",x===b));
  load();
});
// The tabs are links: they work with the middle button, they can be copied, and
// a keyboard reaches them. The handler only spares the page a reload.
document.querySelectorAll(".tab[data-scope]").forEach(t=>t.onclick=e=>{
  e.preventDefault(); go(t.getAttribute("href"));
});
["q","sf","st","ff","ft"].forEach(id=>{
  const el=document.getElementById(id);
  el.oninput=()=>{clearTimeout(window._f);window._f=setTimeout(boot,300)};
});
document.getElementById("clear").onclick=()=>{
  ["q","sf","st","ff","ft"].forEach(id=>document.getElementById(id).value="");
  boot();
};
addEventListener("resize",()=>{clearTimeout(window._t);window._t=setTimeout(load,250)});
// The address says where you are: /charts#window, /charts#lifetime, and a run by
// its id. A view you cannot send anybody is a view you have to describe instead.
// The address is the state. Nothing else holds it: a click writes the hash, the
// hash draws the page. Keeping a copy beside it is what made the page fight
// itself — one wrote, the other read what was there a moment ago, and every
// switch landed on the view you had just left.
function go(path){
  if(location.pathname === path){ render(); return; }
  history.pushState(null,"",path);
  render();
}

// Grouped by what the value axis measures rather than by what the query is
// about. Time is always along the bottom, so what makes two charts kin is the
// unit up the side: every dollar chart together, every seconds chart together.
// Spans on a clock and tables have no value axis and come last.
// Where the money figure comes from, said once at the top of the money charts
// rather than guessed at under each of them.
const HOW_MONEY="Cost as the Claude SDK reports it per session (total_cost_usd), at public API "+
  "prices for input, cache and output tokens, summed per run. On the subscription nobody is "+
  "billed this; it is what the same tokens would cost.";
const GROUPS=[["currencyUSD","Money (USD)",HOW_MONEY],["s","Time"],["tokens","Tokens"],
  ["turns","Turns"],["mbytes","Memory (MB)"],["percent","Share (%)"],["requests/min","Requests per minute"],
  ["processes","Processes"],["findings","Findings"],["runs","Runs"],
  ["other","Other"],["timeline","On the clock"],["table","Tables"]];
function groupKey(p){
  if(p.kind==="table"||p.kind==="timeline") return p.kind;
  const u=(p.unit||"").trim(), U=unitOf(u);
  if(U===UNITS.m||U===UNITS.ms||U===UNITS.s) return "s";
  return GROUPS.some(g=>g[0]===u) ? u : "other";
}
function grouped(){
  // A spec that sections by its rows keeps its order: each row opens a
  // chapter, and what follows it belongs to it, empty or not.
  if(spec.sections==="rows"){
    const out=[]; let cur=null;
    panels.forEach((p,i)=>{
      if(p.kind==="row"){ cur={key:"row"+i,title:p.title,blurb:p.about||"",items:[],colour:COLORS[out.length%COLORS.length]}; out.push(cur); return; }
      if(p.hidden||p.top) return;
      if(!cur){ cur={key:"row",title:"Charts",blurb:"",items:[],colour:COLORS[0]}; out.push(cur); }
      cur.items.push(i);
    });
    return out.filter(g=>g.items.length);
  }
  const by=new Map();
  panels.forEach((p,i)=>{
    if(p.kind==="row"||p.hidden||p.top) return;
    if(p.scope==="window"&&chosen) return;   // a chart of many runs has no place on the page of one
    const k=groupKey(p);
    if(!by.has(k)) by.set(k,[]);
    by.get(k).push(i);
  });
  return GROUPS.filter(g=>by.has(g[0])).map((g,n)=>({key:g[0],title:g[1],blurb:g[2]||"",items:by.get(g[0]),colour:COLORS[n%COLORS.length]}));
}

async function render(){
  const parts=location.pathname.split("/").filter(Boolean);   // ammit, view, id?
  const view=parts[1]||"runs";
  chosen = view==="runs" && parts[2] ? parts[2] : "";
  scope  = PAGES.some(p=>p.key===view) ? view : "runs";
  document.querySelectorAll(".tab").forEach(x=>
    x.classList.toggle("on", !chosen && x.dataset.scope===scope));
  await boot();
}

addEventListener("popstate", render);
render();
</script>` }
