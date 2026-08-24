package main

import (
	"fmt"
	"time"
)

// The footer every Chiron page wears: navy, three columns, monospace. Written
// once here and used by both pages, because the last time something belonging to
// this house existed in two copies they drifted four panels apart and nobody
// knew until an image was rebuilt from scratch.
func footerHTML() string {
	return fmt.Sprintf(`
<footer class=chiron-footer>
  <div class=col>
    <p>Chiron Systems. Agentic Solutions for Maritime industry, Testing tool for
       Product and Analytics teams, Job market and job</p>
    <a class=comms href="https://chiron.consulting#contact"><i>&gt;</i> init_comms()</a>
  </div>
  <div class="col mid"><span>&copy; %d Chiron Consulting</span></div>
  <div class="col right">
    <a href="https://github.com/ngavrish/chiron" target=_blank rel=noreferrer><i>//</i> github/chiron</a>
    <a href="https://www.linkedin.com/company/chironconsulting-tech" target=_blank rel=noreferrer><i>//</i> linkedin/chiron</a>
  </div>
</footer>`, time.Now().Year())
}

// The same styling, and the same reason for living in one place.
const footerCSS = `
.chiron-footer{background:#001F3F;color:#A0AEC0;border-top:1px solid rgba(255,255,255,.1);
  padding:64px 24px;margin-top:64px;
  font:13px/1.6 "JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  display:flex;flex-wrap:wrap;gap:32px;align-items:flex-start}
.chiron-footer .col{flex:1;display:flex;flex-direction:column;gap:12px;min-width:230px}
.chiron-footer .col.mid{align-items:center;opacity:.6}
.chiron-footer .col.right{align-items:flex-end;gap:12px}
.chiron-footer p{margin:0;max-width:38ch;opacity:.8;line-height:1.7}
.chiron-footer a{color:#A0AEC0;text-decoration:none;display:flex;align-items:center;gap:8px}
.chiron-footer a:hover{color:#F7FAFC}
.chiron-footer .comms{color:#CD7F32;margin-top:8px}
.chiron-footer .comms:hover{color:#F7FAFC}
.chiron-footer i{font-style:normal;color:#CD7F32;opacity:.5}
.chiron-footer a:hover i{opacity:1}
.chiron-footer .comms i{color:#A0AEC0;opacity:1}
.chiron-footer .comms:hover i{color:#CD7F32}
`
