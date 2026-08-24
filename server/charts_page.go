package main

import _ "embed"

// The page. uPlot is vendored rather than fetched: a chart that needs the
// internet is a chart that is blank on the machine that most needs it.

//go:embed uPlot.iife.min.js
var uplotJS string

//go:embed uPlot.min.css
var uplotCSS string

var chartsPage = `<!doctype html><meta charset="utf-8">
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
  --mono:"JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  --sans:"Plus Jakarta Sans",system-ui,-apple-system,sans-serif;
}
*{box-sizing:border-box}
body{margin:0;background:var(--ground);color:var(--ink);font:14px/1.55 var(--sans)}

/* One row that does not reflow when a control is hidden: the filters live in
   their own group and the group collapses whole. */
header{position:sticky;top:0;z-index:20;background:var(--ground);
  border-bottom:1px solid var(--hair);padding:11px 22px;
  display:flex;align-items:center;gap:18px;flex-wrap:wrap}
h1{margin:0;font:800 17px/1 var(--sans);letter-spacing:-.01em;color:var(--navy)}
h1 span{color:var(--bronze)}
#filters{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
/* Three places to be, all three visible. A dropdown hid two of them behind the
   third and made the current one a thing you had to open something to read. */
#tabs{display:flex;gap:2px;background:var(--hair-soft);border-radius:8px;padding:3px}
.tab{border:0;background:none;border-radius:6px;padding:6px 13px;color:var(--slate);
  font:600 12.5px/1 var(--sans);cursor:pointer}
.tab:hover{color:var(--navy)}
.tab.on{background:var(--ground);color:var(--navy);
  box-shadow:0 1px 3px rgba(15,21,32,.10)}
select,button,input{font:inherit;font-size:12.5px;color:var(--ink);
  background:var(--ground);border:1px solid var(--hair);border-radius:6px;
  padding:6px 10px}
select{cursor:pointer}
button{cursor:pointer;color:var(--slate)}
button:hover{border-color:var(--bronze);color:var(--bronze)}
input:focus,select:focus{outline:2px solid var(--bronze-wash);border-color:var(--bronze)}
#note{margin-left:auto;color:var(--mute);font-size:12px;font-variant-numeric:tabular-nums}

main{padding:20px 22px 72px;display:flex;flex-direction:column;gap:26px}
.row{font:700 11px/1 var(--sans);letter-spacing:.14em;text-transform:uppercase;
  color:var(--bronze);border-bottom:1px solid var(--hair);padding-bottom:8px;margin-top:12px}
.panel h2{font:700 14px/1.3 var(--sans);margin:0 0 3px;color:var(--navy)}
.panel p{margin:0 0 10px;color:var(--slate);font-size:12px;max-width:92ch}
.plot{min-height:60px}

table{border-collapse:collapse;width:100%;font:12px/1.5 var(--mono);display:block;
  overflow:auto;max-height:360px;border:1px solid var(--hair);border-radius:6px}
th,td{border-bottom:1px solid var(--hair-soft);padding:6px 11px;text-align:left;
  white-space:nowrap;font-variant-numeric:tabular-nums}
th{color:var(--slate);position:sticky;top:0;background:var(--raised);
  font-family:var(--sans);font-weight:700;font-size:11px;letter-spacing:.04em;
  text-transform:uppercase}
tbody tr:hover{background:var(--bronze-wash)}
.err{color:var(--bad);font-size:12px}
.empty{color:var(--mute);font-size:12px;padding:8px 0}
.u-legend{font-size:11px;color:var(--slate)}

.tiles{display:grid;gap:14px;grid-template-columns:repeat(auto-fill,minmax(258px,1fr))}
.tile{border:1px solid var(--hair);border-left:3px solid var(--mute);border-radius:8px;
  padding:13px 15px;cursor:pointer;background:var(--ground);transition:.12s}
.tile:hover{border-color:var(--hair);border-left-color:var(--bronze);
  box-shadow:0 2px 10px rgba(15,21,32,.07);transform:translateY(-1px)}
.tile.green{border-left-color:var(--good)}
.tile.red{border-left-color:var(--bad)}
.tile.going{border-left-color:var(--warm)}
.tile b{font:700 14px/1.3 var(--sans);display:block;color:var(--navy)}
.tile .verdict{font:700 10px/1.6 var(--sans);letter-spacing:.11em;
  text-transform:uppercase;color:var(--slate)}
.tile.green .verdict{color:var(--good)}
.tile.red .verdict{color:var(--bad)}
.tile.going .verdict{color:var(--warm)}
.tile dl{display:grid;grid-template-columns:auto 1fr;gap:3px 12px;margin:10px 0 0;
  font:11.5px/1.5 var(--mono);font-variant-numeric:tabular-nums}
.tile dt{color:var(--mute);font-family:var(--sans)}
.tile dd{margin:0;text-align:right;color:var(--ink)}
.back{color:var(--bronze);cursor:pointer;font-size:12.5px;font-weight:600;
  display:inline-block;margin-bottom:4px}
.back:hover{text-decoration:underline}
</style>
<header>
  <h1>ammit<span>.</span></h1>
  <nav id=tabs>
    <button class="tab on" data-scope=runs>Runs</button>
    <button class=tab data-scope=window>A window</button>
    <button class=tab data-scope=lifetime>All time</button>
  </nav>
  <div id=filters>
    <input id=q placeholder="ticket" size=10>
    <label class=when>started <input id=sf type=date> <input id=st type=date></label>
    <label class=when>finished <input id=ff type=date> <input id=ft type=date></label>
    <button id=clear>clear</button>
  </div>
  <select id=range>
    <option value=3>last 3 hours</option>
    <option value=12 selected>last 12 hours</option>
    <option value=48>last 2 days</option>
    <option value=168>last week</option>
  </select>
  <button id=refresh>refresh</button>
  <span id=note style="color:var(--dim);font-size:12px"></span>
</header>
<main id=main></main>
<script>` + uplotJS + `</script>
<script>
const COLORS=["#CD7F32","#0EA5E9","#22C55E","#FB923C","#EF4444","#8B5CF6",
  "#0891B2","#CA8A04","#2563EB","#DB2777"];
const main=document.getElementById("main");
let panels=[];

function hours(){return +document.getElementById("range").value}
let runs=[], scope="runs", chosen="";

// A verdict is a word this pipeline chose, and there are several for each of the
// two outcomes. Colour is about which of the two it was.
function shade(r){
  if(!r.finished) return "going";
  const v=(r.verdict||"").toUpperCase();
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
    if(r) return [r.started*1000-2000, (r.finished||Date.now()/1000)*1000+2000];
  }
  const to=Date.now();return[to-hours()*3600e3,to];
}
function runParam(){ return chosen?"&run="+encodeURIComponent(chosen):"" }

// The series come back as rows of (time, metric, value) — one long table, many
// lines. uPlot wants a column per line over one shared clock, so the rows are
// pivoted here and gaps are left as nulls rather than joined across.
function pivot(cols,rows){
  const ti=cols.indexOf("time"),mi=cols.indexOf("metric"),vi=cols.indexOf("value");
  if(ti<0||vi<0) return null;
  const times=[...new Set(rows.map(r=>+r[ti]))].sort((a,b)=>a-b);
  const at=new Map(times.map((t,i)=>[t,i]));
  const names=mi<0?["value"]:[...new Set(rows.map(r=>String(r[mi])))];
  const data=[times,...names.map(()=>new Array(times.length).fill(null))];
  for(const r of rows){
    const i=at.get(+r[ti]); const j=mi<0?0:names.indexOf(String(r[mi]));
    if(i!=null&&j>=0) data[j+1][i]=r[vi]==null?null:+r[vi];
  }
  return {data,names};
}

function drawSeries(box,payload){
  const s=payload.series[0]||{};
  if(s.error){box.innerHTML='<div class=err>'+s.error+'</div>';return}
  const p=pivot(s.columns||[],s.rows||[]);
  if(!p||!p.data[0].length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  const opts={
    width:box.clientWidth||900,height:(payload.panel.height||8)*26,
    scales:{x:{time:true}},
    axes:[{stroke:"#4A5568",grid:{stroke:"#F1F3F6"},ticks:{stroke:"#E5E7EB"}},
          {stroke:"#4A5568",grid:{stroke:"#F1F3F6"},ticks:{stroke:"#E5E7EB"}}],
    series:[{},...p.names.map((n,i)=>({
      label:n,stroke:COLORS[i%COLORS.length],width:1.4,
      // A limit is drawn as it behaves: flat until it is changed.
      ...(/limit|cap|ceiling/i.test(n)?{dash:[6,4],width:1}:{})
    }))],
  };
  box.innerHTML="";
  new uPlot(opts,p.data.map((c,i)=>i===0?c.map(t=>t/1000):c),box);
}

function drawTable(box,payload){
  const s=payload.series[0]||{};
  if(s.error){box.innerHTML='<div class=err>'+s.error+'</div>';return}
  if(!(s.rows||[]).length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  const th=(s.columns||[]).map(c=>"<th>"+c+"</th>").join("");
  const tr=s.rows.slice(0,300).map(r=>"<tr>"+r.map(c=>"<td>"+
    (c==null?"":String(c)).replace(/[<&]/g,x=>x==="<"?"&lt;":"&amp;")+"</td>").join("")+"</tr>").join("");
  box.innerHTML="<table><thead><tr>"+th+"</tr></thead><tbody>"+tr+"</tbody></table>";
}

async function load(){
  const [from,to]=windowMs();
  const q=(scope==="lifetime"?"&scope=lifetime":"")+runParam();
  const note=document.getElementById("note");
  let drawn=0;
  await Promise.all(panels.map(async (p,i)=>{
    if(p.kind==="row") return;
    const box=document.getElementById("plot-"+i);
    if(!box) return;
    try{
      const r=await fetch("/charts/data?panel="+i+"&from="+from+"&to="+to+q);
      const payload=await r.json();
      (p.kind==="table"?drawTable:drawSeries)(box,payload);
      drawn++;
    }catch(e){ box.innerHTML='<div class=err>'+e+'</div>' }
  }));
  note.textContent=drawn+" panels";
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
  main.querySelectorAll(".tile").forEach(t=>t.onclick=()=>{
    chosen=t.dataset.run; boot();
  });
}

async function boot(){
  // The filter group collapses whole, so the row cannot half-empty and reflow.
  const picking = scope==="runs" && !chosen;
  document.getElementById("filters").style.display=picking?"flex":"none";
  document.getElementById("range").style.display=scope==="window"?"":"none";
  if(picking){ await fillRuns(); drawTiles(); return; }
  if(scope==="runs") await fillRuns();
  panels=(await (await fetch("/charts/panels"+(scope==="lifetime"?"?scope=lifetime":""))).json()).panels;
  const r=runs.find(x=>x.run===chosen);
  const head=r?'<div class=back id=back>← every run</div><div class=row>'+
    (r.name||r.run)+" — "+when(r.started)+" — "+(r.verdict||"running")+
    " — "+(r.turns||0)+" turns — $"+(r.usd||0).toFixed(2)+'</div>':"";
  main.innerHTML=head+panels.map((p,i)=>p.kind==="row"
    ? '<div class=row>'+p.title+'</div>'
    : '<section class=panel><h2>'+p.title+'</h2>'+
      (p.about?'<p>'+p.about.replace(/[<&]/g,x=>x==="<"?"&lt;":"&amp;")+'</p>':'')+
      '<div class=plot id=plot-'+i+'></div></section>').join("");
  const back=document.getElementById("back");
  if(back) back.onclick=()=>{chosen="";boot()};
  await load();
}
document.getElementById("refresh").onclick=load;
document.getElementById("range").onchange=load;
document.querySelectorAll(".tab").forEach(t=>t.onclick=async ()=>{
  document.querySelectorAll(".tab").forEach(x=>x.classList.toggle("on",x===t));
  scope=t.dataset.scope; chosen=""; await boot();
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
boot();
</script>`
