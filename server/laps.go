package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
)

// Repair laps, counted here and not taken on the runner's word.
//
// A repair phase loops back to the gate it answers: implrulefix to implrules,
// gatefix to implgate, branchheal to branchrun. The runner holds a cap on each
// and gives the branch up at it - and on run f9104c8a (APF-3296) it did not:
// claim-5 and claim-9 each started implrulefix four times against a cap of
// two, and nothing outside the runner had a number to hold it to. Only the
// heal loop was a limit here, and that one was written down after the fact
// with the action "none".
//
// So every repair phase gets its number in the config - loops.laps_<phase>,
// how many times that phase may start on one branch - and this service counts
// the starts itself from the phase_start events. The (cap+1)th start is the
// lap nobody budgeted: the branch is stopped (actions.on_repair_laps,
// stop_branch unless written otherwise) and a person is told why.

const lapsPrefix = "laps_"

// lapCaps is phase -> how many times it may start on one branch, from
// loops.laps_<phase>. The heal cap's old name is read when the new one is
// absent, so a config written before the rename still holds the heal loop.
func lapCaps(conf Config) map[string]float64 {
	caps := map[string]float64{}
	for loop := range conf["loops"] {
		// laps_watchdog is the watchdog's cycles per chain (watchdog.go),
		// not a repair phase: no phase of that name starts on a branch.
		if !strings.HasPrefix(loop, lapsPrefix) || loop == lapsPrefix+"watchdog" {
			continue
		}
		if lapCap, ok := conf.num("loops", loop); ok && lapCap > 0 {
			caps[strings.TrimPrefix(loop, lapsPrefix)] = lapCap
		}
	}
	if _, ok := caps["branchheal"]; !ok {
		if lapCap, ok := conf.num("loops", "heal_laps_per_branch"); ok && lapCap > 0 {
			caps["branchheal"] = lapCap
		}
	}
	return caps
}

// healCap is the heal loop's cap under whichever name the config uses.
func healCap(conf Config) (float64, string) {
	if lapCap, ok := conf.num("loops", lapsPrefix+"branchheal"); ok {
		return lapCap, "loops." + lapsPrefix + "branchheal"
	}
	lapCap, _ := conf.num("loops", "heal_laps_per_branch")
	return lapCap, "loops.heal_laps_per_branch"
}

// phaseStarts is branch -> how many times the phase started on it in the
// branch's current attempt: since its latest start of attemptPhase, or in the
// whole run when it has none. A claim that does not converge gets one fresh
// attempt - a new implementer in an emptied directory - and its laps were
// counted on top of the first attempt's: on APF-3296 (run fa72bb3b) claim-12's
// fresh attempt was stopped by loops.laps_implrulefix as it entered its first
// repair, and every phase after it ran in 0.05 s with nothing done.
func phaseStarts(run, phase, attemptPhase string) map[string]int {
	mu.Lock()
	defer mu.Unlock()
	startRows, err := db.Query(`
		SELECT coalesce(e.branch,''), count(*) FROM events e
		WHERE e.run=? AND e.kind='phase_start' AND e.phase=?
		  AND e.at > coalesce((SELECT max(a.at) FROM events a
		      WHERE a.run=e.run AND a.kind='phase_start' AND a.phase=?
		        AND coalesce(a.branch,'')=coalesce(e.branch,'')), 0)
		GROUP BY 1`, run, phase, attemptPhase)
	if err != nil {
		return nil
	}
	defer startRows.Close()
	starts := map[string]int{}
	for startRows.Next() {
		var branch string
		var n int
		if startRows.Scan(&branch, &n) == nil {
			starts[branch] = n
		}
	}
	_ = startRows.Err() // a cut-off read is the shorter map it always was
	return starts
}

// lapBreach is one branch whose repair phase started more often than its cap.
type lapBreach struct {
	Phase, Branch string
	Laps          int
	Cap           float64
}

func (b lapBreach) subject() string { return phaseKey(b.Phase, b.Branch) }
func (b lapBreach) rule() string    { return "loops." + lapsPrefix + b.Phase }

// lapsOver is every branch of the run past a repair cap, in a stable order.
func lapsOver(run string, caps map[string]float64, attemptPhase string) []lapBreach {
	var out []lapBreach
	for phase, limit := range caps {
		for branch, laps := range phaseStarts(run, phase, attemptPhase) {
			if float64(laps) > limit {
				out = append(out, lapBreach{phase, branch, laps, limit})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].subject() < out[j].subject() })
	return out
}

// lapsJudged says whether this branch was already stopped for this loop. Read
// from the judgements table, not from memory: a restart of this service must
// not stop and announce the same branch again.
func lapsJudged(run, rule, subject string) bool {
	mu.Lock()
	defer mu.Unlock()
	var n int
	db.QueryRow(`SELECT count(*) FROM judgements WHERE run=? AND rule=? AND subject=?`,
		run, rule, subject).Scan(&n)
	return n > 0
}

// judgeRepairLaps stops every branch of a live run that started a repair phase
// past its cap, and says so.
func judgeRepairLaps(run string, conf Config, ctx map[string]string) {
	caps := lapCaps(conf)
	if len(caps) == 0 {
		return
	}
	action := conf.str("actions", "on_repair_laps", "stop_branch")
	// The phase a branch's attempt begins with: its laps are counted from its
	// latest start (phaseStarts).
	attempt := conf.str("loops", "attempt_phase", "implementing")
	for _, over := range lapsOver(run, caps, attempt) {
		if lapsJudged(run, over.rule(), over.subject()) {
			continue
		}
		ctx["branch"], ctx["phase"] = over.Branch, over.subject()
		judge("branch", run, over.subject(), over.rule(), over.Cap, float64(over.Laps),
			action, act(action, conf, ctx))
		say := map[string]string{}
		for field, fieldValue := range ctx {
			say[field] = fieldValue
		}
		say["verdict"] = "REPAIR-LAPS"
		// Short: act keeps 200 bytes of what a command says, and the rule is
		// in the judgement line already.
		say["summary"] = fmt.Sprintf("%s: %s started for the %d time against a limit of %.0f — branch stopped",
			over.Branch, over.Phase, over.Laps, over.Cap)
		// Said where a person looks: the notify command's own output is the
		// channel when it is an echo, and the judgement line above names the
		// rule either way.
		log.Printf("ammit: notify: %s", act("notify", conf, say))
	}
}
