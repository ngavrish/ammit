package main

// Who: the member behind a hand.
//
// A decision a person makes in the SaaS control plane's chat - a start, a
// stop, an approval - comes into the cell through its gateway with the
// member's identity: X-Revisore-Member (email), X-Revisore-Member-Name
// (path-escaped), X-Revisore-Via ("chat:telegram", "chat:whatsapp", ...).
// Those headers count here only beside the cell's own token (CELL_TOKEN) in
// X-Revisore-Cell-Token, and never from agent-runner: a name anybody can type
// is not an approver. Without them a hand is recorded exactly as before, with
// no member. agentic-v-model internal/member is the same rule on the gateway
// and the orchestrator, which carry the headers on to /act, /queue and /hand.
//
// The member rides on the judgement row of the hand (member, member_name,
// via), the row a stop or a person's command already writes; a start a member
// queues and an approval the orchestrator decided write one too.

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

type handWho struct {
	email, name, via string
}

var (
	handEmailRE = regexp.MustCompile(`^[^\s@<>"]{1,64}@[^\s@<>"]{1,190}$`)
	handViaRE   = regexp.MustCompile(`^(web|chat:[a-z0-9_-]{1,32})$`)
)

// memberOf is the member a request names, when it carries the cell's token
// and does not come from a run's agent; nil otherwise.
func memberOf(r *http.Request) *handWho {
	token := strings.TrimSpace(os.Getenv("CELL_TOKEN"))
	cellToken := r.Header.Get("X-Revisore-Cell-Token")
	if token == "" || cellToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(cellToken)) != 1 {
		return nil
	}
	if runnerCaller(r) != "" {
		return nil
	}
	who := &handWho{email: strings.TrimSpace(r.Header.Get("X-Revisore-Member")),
		via: strings.TrimSpace(r.Header.Get("X-Revisore-Via"))}
	if who.via == "" {
		who.via = "web"
	}
	if !handEmailRE.MatchString(who.email) || !handViaRE.MatchString(who.via) {
		return nil
	}
	memberName := r.Header.Get("X-Revisore-Member-Name")
	if unescaped, err := url.PathUnescape(memberName); err == nil {
		memberName = unescaped
	}
	var b strings.Builder
	for _, char := range strings.TrimSpace(memberName) {
		if char >= 0x20 && char != 0x7f && b.Len() < 400 {
			b.WriteRune(char)
		}
	}
	who.name = b.String()
	return who
}

func (w *handWho) String() string {
	if w == nil {
		return ""
	}
	if w.name != "" {
		return w.name + " <" + w.email + "> via " + w.via
	}
	return w.email + " via " + w.via
}

// handDecisions are the approvals another service decides and a member's
// hand is recorded for here (POST /hand).
var handDecisions = map[string]bool{"approve_gate": true, "approve_report": true, "approve_bug": true}

// postHand records a member's approval the orchestrator decided: one
// judgement row with scope "hand", like a stop through /act.
func postHand(w http.ResponseWriter, r *http.Request) {
	who := memberOf(r)
	if who == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "a hand names its member " +
			"with the cell's token; this request does not"})
		return
	}
	var in struct {
		Decision string `json:"decision"`
		Run      string `json:"run"`
		Name     string `json:"name"`
		Outcome  string `json:"outcome"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !handDecisions[in.Decision] ||
		!validName.MatchString(in.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `need {"decision":` +
			`"approve_gate|approve_report|approve_bug","name":"<ticket>","run","outcome"}`})
		return
	}
	if len(in.Outcome) > 500 {
		in.Outcome = in.Outcome[:500]
	}
	judgeBy(who, "hand", in.Run, in.Name, "hand."+in.Decision, 0, 0, in.Decision, in.Outcome)
	writeJSON(w, http.StatusCreated, map[string]string{"recorded": in.Decision, "by": who.String()})
}
