package dev.ammit;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.UUID;

/**
 * Report what a long-running pipeline is doing, so something outside it can judge.
 *
 * <p>Every send is asynchronous and its result is ignored: reporting must never be
 * the reason a run is slower or stops. A server that is down costs a dropped
 * future and nothing else.
 *
 * <pre>{@code
 * Ammit.Run run = Ammit.run("APF-1934");
 * Ammit.Phase phase = run.phase("implementing");
 * Ammit.Session s = run.session("implement", "req-3", "sonnet");
 * s.turn("");                       // heartbeat: still thinking
 * s.spend(0.42, 120_000, 3_400);
 * s.end(null);
 * phase.end(null);
 * run.finish("PASS", "31 scenarios, 0 failures");
 * }</pre>
 *
 * No dependencies beyond the JDK.
 */
public final class Ammit {

    private static final HttpClient CLIENT =
            HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(3)).build();
    private static String endpoint =
            System.getenv().getOrDefault("AMMIT_URL", "http://ammit:8099");
    private static final boolean ENABLED = !"1".equals(System.getenv("AMMIT_DISABLE"));

    private Ammit() {}

    public static void endpoint(String url) { endpoint = url; }

    /** One fact, with a timestamp. Never blocks, never throws. */
    public static void send(String kind, Map<String, Object> fields) {
        if (!ENABLED) return;
        Map<String, Object> payload = new LinkedHashMap<>();
        payload.put("kind", kind);
        payload.put("at", System.currentTimeMillis() / 1000.0);
        payload.putAll(fields);
        HttpRequest req = HttpRequest.newBuilder(URI.create(endpoint + "/events"))
                .timeout(Duration.ofSeconds(3))
                .header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(json(payload)))
                .build();
        CLIENT.sendAsync(req, HttpResponse.BodyHandlers.discarding())
              .exceptionally(err -> null);
    }

    /** Just enough JSON for flat maps; a serialiser is not worth a dependency here. */
    private static String json(Map<String, Object> map) {
        StringBuilder sb = new StringBuilder("{");
        boolean first = true;
        for (Map.Entry<String, Object> e : map.entrySet()) {
            if (!first) sb.append(',');
            first = false;
            sb.append('"').append(escape(e.getKey())).append("\":");
            Object v = e.getValue();
            if (v == null) sb.append("null");
            else if (v instanceof Number || v instanceof Boolean) sb.append(v);
            else if (v instanceof Map) sb.append(json(castMap(v)));
            else sb.append('"').append(escape(String.valueOf(v))).append('"');
        }
        return sb.append('}').toString();
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> castMap(Object v) { return (Map<String, Object>) v; }

    private static String escape(String s) {
        return s.replace("\\", "\\\\").replace("\"", "\\\"")
                .replace("\n", "\\n").replace("\r", "").replace("\t", "\\t");
    }

    private static String id() { return UUID.randomUUID().toString().replace("-", "").substring(0, 12); }

    public static Run run(String name) { return new Run(name, Map.of()); }

    public static final class Run {
        public final String id = id();
        public final String name;
        String currentPhase = "";
        private final long t0 = System.nanoTime();

        Run(String name, Map<String, Object> tags) {
            this.name = name;
            send("run_start", Map.of("run", id, "name", name, "tags", tags));
        }

        public Phase phase(String name) { return new Phase(this, name); }

        public Session session(String agent, String branch, String model) {
            return new Session(this, agent, branch, model);
        }

        public void note(String text) { send("note", Map.of("run", id, "text", text)); }

        public void finish(String verdict, String summary) {
            send("run_end", Map.of("run", id, "verdict", verdict, "summary", summary,
                    "seconds", (System.nanoTime() - t0) / 1e9));
        }
    }

    public static final class Phase {
        private final Run run;
        private final String name;
        private final long t0 = System.nanoTime();

        Phase(Run run, String name) {
            this.run = run;
            this.name = name;
            run.currentPhase = name;
            send("phase_start", Map.of("run", run.id, "phase", name));
        }

        public void end(Throwable error) {
            send("phase_end", Map.of("run", run.id, "phase", name,
                    "seconds", (System.nanoTime() - t0) / 1e9, "failed", error != null));
            run.currentPhase = "";
        }
    }

    /**
     * One conversation with a model, or one long tool call.
     *
     * <p>{@code turn} is the heartbeat the server watches: a session that stops
     * calling it is waiting on something that is not coming back.
     */
    public static final class Session {
        public final String id = id();
        private final Run run;
        private final String agent, branch, model;
        private int turns = 0;
        private double usd = 0;
        private final long t0 = System.nanoTime();

        Session(Run run, String agent, String branch, String model) {
            this.run = run; this.agent = agent; this.branch = branch; this.model = model;
            send("session_start", Map.of("run", run.id, "session", id, "agent", agent,
                    "branch", branch, "model", model, "phase", run.currentPhase));
        }

        public void turn(String note) {
            turns++;
            send("turn", Map.of("run", run.id, "session", id, "agent", agent,
                    "branch", branch, "phase", run.currentPhase, "n", turns, "note", note));
        }

        public void spend(double usdSpent, int tokensIn, int tokensOut) {
            usd += usdSpent;
            send("spend", Map.of("run", run.id, "session", id, "agent", agent,
                    "phase", run.currentPhase, "usd", usdSpent,
                    "tokens_in", tokensIn, "tokens_out", tokensOut));
        }

        public void log(String text) {
            send("log", Map.of("run", run.id, "session", id, "agent", agent,
                    "branch", branch, "phase", run.currentPhase, "text", text));
        }

        public void end(Throwable error) {
            send("session_end", Map.of("run", run.id, "session", id, "agent", agent,
                    "seconds", (System.nanoTime() - t0) / 1e9, "turns", turns,
                    "usd", usd, "failed", error != null,
                    "error", error == null ? "" : String.valueOf(error.getMessage())));
        }
    }
}
