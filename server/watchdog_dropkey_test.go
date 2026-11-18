package main

import "testing"

// Chain 617cad47 (APF-3253): cycle 2 made no fix, its drop was resumed, and
// the resume (9c3f0723) dropped at datatrace with the same gate output - but
// the run's summary carries the repair's own CAUSE prose, worded anew by each
// run, so the two drops keyed differently and cycle 3 was opened on the same
// evidence. The key keeps the CAUSE class and drops the words after it.
func TestDropKeyIgnoresTheRepairsCauseProse(t *testing.T) {
	gate := "last words of datatrace: RULES GREEN: datatracefix did not run, nothing to judge\n" +
		"DATA TRACE scope: 0 of 10 declared pipeline(s), the ones carrying a table this ticket's requirements name: none\n" +
		"DATA TRACE RED: no declared pipeline carries a table this ticket's requirements name - declare the ticket's path under `pipelines` in the product manifest\n[exit 1]\n" +
		"<!-- product-commit: ifp-service 950d6b58a65d490cf889cc6623bd931a015a07e2 -->\n"
	drop := func(run, said string) *dropFact {
		return &dropFact{run: run, phase: "datatrace", cause: "gate_red", evidence: []string{
			"run " + run + " (APF-3253, mode data-tester) ended BLOCKED: datatrace is red and Pipeline trace repair names the cause outside the tests: " + said,
			"dropped at phase datatrace", "gate datatrace red", gate, "repair datatracefix started 1 time(s)"}}
	}
	cycle2 := drop("350172c8-0d62-4275-a856-42f6a94bee83",
		"CAUSE: flow - the gate prints \"DATA TRACE RED: no declared pipeline carries a table this ticket's requirements name\"; the fix is a manifest `pipelines` entry, which no test change can supply.")
	cycle3 := drop("9c3f0723-9f6a-4e9e-9c1c-9b95600e865b",
		"CAUSE: flow - the gate's last output line is the scope red above; `data_trace_scope.json` shows `\"traced\": []` against 10 declared pipelines, and fixing it means editing the manifest, which a run cannot do.")
	if dropKey(cycle2) != dropKey(cycle3) {
		t.Fatalf("same drop, different keys:\n%s\n---\n%s", dropKey(cycle2), dropKey(cycle3))
	}
	// A different CAUSE class is a different drop.
	stand := drop("9c3f0723-9f6a-4e9e-9c1c-9b95600e865b", "CAUSE: stand - the warehouse refused the hop")
	if dropKey(stand) == dropKey(cycle3) {
		t.Fatalf("a stand cause keyed the same as a flow cause:\n%s", dropKey(stand))
	}
	// Different gate output is a different drop, whatever the prose.
	other := drop("9c3f0723-9f6a-4e9e-9c1c-9b95600e865b", "CAUSE: flow - x")
	other.evidence[3] = "last words of datatrace: DATA TRACE RED: hop unity returned no rows"
	if dropKey(other) == dropKey(cycle3) {
		t.Fatalf("different gate output keyed the same")
	}
}
