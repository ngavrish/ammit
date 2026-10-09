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
	for key := range conf["loops"] {
		if !strings.HasPrefix(key, lapsPrefix) {
			continue
		}
		if v, ok := conf.num("loops", key); ok && v > 0 {
			caps[strings.TrimPrefix(key, lapsPrefix)] = v
		}
	}
	if _, ok := caps["branchheal"]; !ok {
		if v, ok := conf.num("loops", "heal_laps_per_branch"); ok && v > 0 {
			caps["branchheal"] = v
		}
	}
	return caps
}

// healCap is the heal loop's cap under whichever name the config uses.
func healCap(conf Config) (float64, string) {
	if v, ok := conf.num("loops", lapsPrefix+"branchheal"); ok {
		return v, "loops." + lapsPrefix + "branchheal"
	}
	v, _ := conf.num("loops", "heal_laps_per_branch")
	return v, "loops.heal_laps_per_branch"
}

// phaseStarts is branch -> how many times the phase started on it in this run.
func phaseStarts(run, phase string) map[string]int {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`
		SELECT coalesce(branch,''), count(*) FROM events
		WHERE run=? AND kind='phase_start' AND phase=?
		GROUP BY 1`, run, phase)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var branch string
		var n int
		if rows.Scan(&branch, &n) == nil {
			out[branch] = n
		}
	}
	return out
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
func lapsOver(run string, caps map[string]float64) []lapBreach {
	var out []lapBreach
	for phase, limit := range caps {
		for branch, n := range phaseStarts(run, phase) {
			if float64(n) > limit {
				out = append(out, lapBreach{phase, branch, n, limit})
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
	for _, b := range lapsOver(run, caps) {
		if lapsJudged(run, b.rule(), b.subject()) {
			continue
		}
		ctx["branch"], ctx["phase"] = b.Branch, b.subject()
		judge("branch", run, b.subject(), b.rule(), b.Cap, float64(b.Laps),
			action, act(action, conf, ctx))
		say := map[string]string{}
		for k, v := range ctx {
			say[k] = v
		}
		say["verdict"] = "REPAIR-LAPS"
		// Short: act keeps 200 bytes of what a command says, and the rule is
		// in the judgement line already.
		say["summary"] = fmt.Sprintf("%s: %s стартовал %d-й раз при лимите %.0f — ветка остановлена",
			b.Branch, b.Phase, b.Laps, b.Cap)
		// Said where a person looks: the notify command's own output is the
		// channel when it is an echo, and the judgement line above names the
		// rule either way.
		log.Printf("ammit: notify: %s", act("notify", conf, say))
	}
}
