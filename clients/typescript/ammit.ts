/**
 * Report what a long-running pipeline is doing, so something outside it can judge.
 *
 * No dependencies, and nothing here is awaited by the caller: reporting must
 * never be the reason a run is slower or stops. A server that is down costs a
 * rejected promise nobody reads.
 *
 *   const run = new Run("APF-1934", { mode: "test" });
 *   const phase = run.phase("implementing");
 *   const s = run.session("implement", "req-3", "sonnet");
 *   s.turn();
 *   s.spend({ usd: 0.42, tokensIn: 120_000, tokensOut: 3_400 });
 *   s.end();
 *   phase.end();
 *   run.finish("PASS", "31 scenarios, 0 failures");
 */

const ENDPOINT =
  (typeof process !== "undefined" && process.env?.AMMIT_URL) || "http://ammit:8099";
const ENABLED =
  !(typeof process !== "undefined" && process.env?.AMMIT_DISABLE === "1");

let complainedAt = 0;

export function send(kind: string, fields: Record<string, unknown> = {}): void {
  if (!ENABLED) return;
  const body = JSON.stringify({ kind, at: Date.now() / 1000, ...fields });
  void fetch(`${ENDPOINT}/events`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
  }).catch((err) => {
    const now = Date.now();
    if (now - complainedAt > 60_000) {
      complainedAt = now;
      console.warn(`ammit: not reporting (${err})`);
    }
  });
}

const id = () => Math.random().toString(16).slice(2, 14);

export class Session {
  readonly id = id();
  private turns = 0;
  private usd = 0;
  private readonly t0 = Date.now();

  constructor(
    private readonly run: Run,
    readonly agent: string,
    readonly branch = "",
    readonly model = "",
  ) {
    send("session_start", {
      run: run.id, session: this.id, agent, branch, model, phase: run.currentPhase,
    });
  }

  /** The heartbeat the server watches. A session that stops calling it is waiting
   *  on something that is not coming back. */
  turn(note = ""): void {
    this.turns += 1;
    send("turn", {
      run: this.run.id, session: this.id, agent: this.agent, branch: this.branch,
      phase: this.run.currentPhase, n: this.turns, note,
    });
  }

  spend(opts: { usd?: number; tokensIn?: number; tokensOut?: number } = {}): void {
    this.usd += opts.usd ?? 0;
    send("spend", {
      run: this.run.id, session: this.id, agent: this.agent,
      phase: this.run.currentPhase, usd: opts.usd ?? 0,
      tokens_in: opts.tokensIn ?? 0, tokens_out: opts.tokensOut ?? 0,
    });
  }

  log(text: string): void {
    send("log", {
      run: this.run.id, session: this.id, agent: this.agent, branch: this.branch,
      phase: this.run.currentPhase, text: text.slice(0, 8000),
    });
  }

  end(error?: unknown): void {
    send("session_end", {
      run: this.run.id, session: this.id, agent: this.agent,
      seconds: (Date.now() - this.t0) / 1000, turns: this.turns, usd: this.usd,
      failed: Boolean(error), error: error ? String(error).slice(0, 300) : "",
    });
  }
}

export class Phase {
  private readonly t0 = Date.now();
  constructor(private readonly run: Run, readonly name: string) {
    run.currentPhase = name;
    send("phase_start", { run: run.id, phase: name });
  }
  end(error?: unknown): void {
    send("phase_end", {
      run: this.run.id, phase: this.name,
      seconds: (Date.now() - this.t0) / 1000, failed: Boolean(error),
    });
    this.run.currentPhase = "";
  }
}

export class Run {
  readonly id: string;
  currentPhase = "";
  private readonly t0 = Date.now();

  constructor(readonly name: string, tags: Record<string, string> = {}, runId = "") {
    this.id = runId || id();
    send("run_start", { run: this.id, name, tags });
  }

  phase(name: string): Phase {
    return new Phase(this, name);
  }

  session(agent: string, branch = "", model = ""): Session {
    return new Session(this, agent, branch, model);
  }

  note(text: string): void {
    send("note", { run: this.id, text: text.slice(0, 2000) });
  }

  finish(verdict = "", summary = ""): void {
    send("run_end", {
      run: this.id, verdict, summary: summary.slice(0, 2000),
      seconds: (Date.now() - this.t0) / 1000,
    });
  }
}
