package main

import (
	"fmt"
	"time"
)

// The footer every Chiron page wears, written once. Quieter than it was: on a
// dark page a navy block at the bottom is just more page, so it is a hairline
// and three columns of small monospace under the content.
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

const footerCSS = `
.chiron-footer{margin-top:auto;padding:36px 24px 40px;border-top:1px solid var(--hair);
  color:var(--dim);font:12px/1.6 var(--mono);
  display:flex;flex-wrap:wrap;gap:28px;align-items:flex-start}
.chiron-footer .col{flex:1;display:flex;flex-direction:column;gap:10px;min-width:220px}
.chiron-footer .col.mid{align-items:center;opacity:.7}
.chiron-footer .col.right{align-items:flex-end;gap:10px}
.chiron-footer p{margin:0;max-width:40ch;line-height:1.7}
.chiron-footer a{color:var(--mute);text-decoration:none;display:flex;align-items:center;gap:8px;
  border:0;font:inherit;letter-spacing:0;text-transform:none;transition:color var(--quick)}
.chiron-footer a:hover{color:var(--ink)}
.chiron-footer .comms{color:var(--bronze);margin-top:4px}
.chiron-footer .comms:hover{color:var(--bronze-hi)}
.chiron-footer i{font-style:normal;color:var(--bronze);opacity:.5;transition:opacity var(--quick)}
.chiron-footer a:hover i{opacity:1}
`
