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
:root{--navy:#001F3F;--bronze:#CD7F32;--ink:#E8EDF4;--dim:#8FA3BC;--line:#123A63}
*{box-sizing:border-box}
body{margin:0;background:var(--navy);color:var(--ink);
  font:14px/1.5 "JetBrains Mono",ui-monospace,Menlo,monospace}
header{padding:14px 20px;border-bottom:1px solid var(--line);display:flex;
  gap:16px;align-items:baseline;position:sticky;top:0;background:var(--navy);z-index:5}
h1{font:600 15px/1 "Plus Jakarta Sans",system-ui,sans-serif;margin:0;letter-spacing:.02em}
select,button{background:#00294F;color:var(--ink);border:1px solid var(--line);
  border-radius:2px;padding:5px 9px;font:inherit;font-size:12px}
main{padding:16px 20px 60px;display:flex;flex-direction:column;gap:22px}
.row{font:600 12px/1 "Plus Jakarta Sans",system-ui,sans-serif;letter-spacing:.12em;
  text-transform:uppercase;color:var(--bronze);border-bottom:1px solid var(--line);
  padding-bottom:7px;margin-top:14px}
.panel h2{font:600 13px/1.3 "Plus Jakarta Sans",system-ui,sans-serif;margin:0 0 3px}
.panel p{margin:0 0 9px;color:var(--dim);font-size:11.5px;max-width:95ch}
.plot{min-height:60px}
table{border-collapse:collapse;width:100%;font-size:12px;display:block;
  overflow-x:auto;max-height:340px}
th,td{border-bottom:1px solid var(--line);padding:4px 9px;text-align:left;
  white-space:nowrap;font-variant-numeric:tabular-nums}
th{color:var(--bronze);position:sticky;top:0;background:var(--navy)}
.err{color:#E06C5A;font-size:12px}
.empty{color:var(--dim);font-size:12px;padding:6px 0}
.u-legend{font-size:11px}
</style>
<header>
  <h1>ammit</h1>
  <select id=scope>
    <option value=window selected>a window</option>
    <option value=run>one run</option>
    <option value=lifetime>every run there has been</option>
  </select>
  <select id=run style=display:none></select>
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
const COLORS=["#CD7F32","#5794F2","#73BF69","#E0B400","#E06C5A","#B877D9",
  "#37A2A6","#F2CC0C","#8AB8FF","#FF9830"];
const main=document.getElementById("main");
let panels=[];

function hours(){return +document.getElementById("range").value}
let runs=[], scope="window";

// Picking a run narrows the window to that run's own span rather than rewriting
// forty-three queries to take a run id. The queue runs one at a time, so a run's
// span holds that run and nothing else — which is the assumption, said out loud.
function windowMs(){
  if(scope==="run"){
    const r=runs.find(x=>x.run===document.getElementById("run").value);
    if(r) return [r.started*1000-2000, (r.finished||Date.now()/1000)*1000+2000];
  }
  const to=Date.now();return[to-hours()*3600e3,to];
}

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
    axes:[{stroke:"#8FA3BC",grid:{stroke:"#123A63"},ticks:{stroke:"#123A63"}},
          {stroke:"#8FA3BC",grid:{stroke:"#123A63"},ticks:{stroke:"#123A63"}}],
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
  const q=scope==="lifetime"?"&scope=lifetime":"";
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

async function fillRuns(){
  runs=await (await fetch("/charts/runs")).json();
  const sel=document.getElementById("run");
  sel.innerHTML=runs.map(r=>{
    const when=new Date(r.started*1000).toLocaleString();
    const how=r.verdict||(r.finished?"":"running");
    return '<option value="'+r.run+'">'+(r.name||r.run)+" — "+when+
           (how?" — "+how:"")+(r.usd?" — $"+r.usd.toFixed(2):"")+'</option>';
  }).join("");
}

async function boot(){
  await fillRuns();
  panels=(await (await fetch("/charts/panels"+(scope==="lifetime"?"?scope=lifetime":""))).json()).panels;
  main.innerHTML=panels.map((p,i)=>p.kind==="row"
    ? '<div class=row>'+p.title+'</div>'
    : '<section class=panel><h2>'+p.title+'</h2>'+
      (p.about?'<p>'+p.about.replace(/[<&]/g,x=>x==="<"?"&lt;":"&amp;")+'</p>':'')+
      '<div class=plot id=plot-'+i+'></div></section>').join("");
  await load();
}
document.getElementById("refresh").onclick=load;
document.getElementById("range").onchange=load;
document.getElementById("scope").onchange=async e=>{
  scope=e.target.value;
  document.getElementById("run").style.display=scope==="run"?"":"none";
  document.getElementById("range").style.display=scope==="window"?"":"none";
  await boot();
};
document.getElementById("run").onchange=load;
addEventListener("resize",()=>{clearTimeout(window._t);window._t=setTimeout(load,250)});
boot();
</script>`
