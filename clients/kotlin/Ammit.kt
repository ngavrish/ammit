@file:Suppress("unused")

package dev.ammit

import java.net.URI
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import java.time.Duration
import java.util.UUID

/**
 * Report what a long-running pipeline is doing, so something outside it can judge.
 *
 * Every send is asynchronous and unchecked: reporting must never be why a run is
 * slower or stops. No dependencies beyond the JDK.
 *
 * ```
 * val run = Ammit.run("APF-1934")
 * run.phase("implementing").use { _ ->
 *     run.session("implement", branch = "req-3", model = "sonnet").use { s ->
 *         s.turn()                       // heartbeat: still thinking
 *         s.spend(usd = 0.42, tokensIn = 120_000, tokensOut = 3_400)
 *     }
 * }
 * run.finish("PASS", "31 scenarios, 0 failures")
 * ```
 */
object Ammit {
    private val client: HttpClient =
        HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(3)).build()
    private var endpoint: String = System.getenv("AMMIT_URL") ?: "http://ammit:8099"
    private val enabled: Boolean = System.getenv("AMMIT_DISABLE") != "1"

    fun endpoint(url: String) { endpoint = url }

    fun send(kind: String, fields: Map<String, Any?>) {
        if (!enabled) return
        val payload = linkedMapOf<String, Any?>(
            "kind" to kind, "at" to System.currentTimeMillis() / 1000.0,
        ).apply { putAll(fields) }
        val request = HttpRequest.newBuilder(URI.create("$endpoint/events"))
            .timeout(Duration.ofSeconds(3))
            .header("Content-Type", "application/json")
            .POST(HttpRequest.BodyPublishers.ofString(json(payload)))
            .build()
        client.sendAsync(request, HttpResponse.BodyHandlers.discarding())
            .exceptionally { null }
    }

    private fun json(map: Map<String, Any?>): String =
        map.entries.joinToString(",", "{", "}") { (key, value) ->
            "\"${escape(key)}\":" + when (value) {
                null -> "null"
                is Number, is Boolean -> value.toString()
                is Map<*, *> -> @Suppress("UNCHECKED_CAST") json(value as Map<String, Any?>)
                else -> "\"${escape(value.toString())}\""
            }
        }

    private fun escape(s: String) = s.replace("\\", "\\\\").replace("\"", "\\\"")
        .replace("\n", "\\n").replace("\r", "").replace("\t", "\\t")

    private fun id() = UUID.randomUUID().toString().replace("-", "").take(12)

    fun run(name: String, tags: Map<String, String> = emptyMap()) = Run(name, tags)

    class Run(val name: String, tags: Map<String, String>) {
        val id = id()
        internal var currentPhase = ""
        private val t0 = System.nanoTime()

        init { send("run_start", mapOf("run" to id, "name" to name, "tags" to tags)) }

        fun phase(name: String) = Phase(this, name)
        fun session(agent: String, branch: String = "", model: String = "") =
            Session(this, agent, branch, model)

        fun note(text: String) = send("note", mapOf("run" to id, "text" to text))

        fun finish(verdict: String = "", summary: String = "") = send(
            "run_end", mapOf("run" to id, "verdict" to verdict, "summary" to summary,
                "seconds" to (System.nanoTime() - t0) / 1e9))
    }

    class Phase(private val run: Run, private val name: String) : AutoCloseable {
        private val t0 = System.nanoTime()

        init {
            run.currentPhase = name
            send("phase_start", mapOf("run" to run.id, "phase" to name))
        }

        override fun close() {
            send("phase_end", mapOf("run" to run.id, "phase" to name,
                "seconds" to (System.nanoTime() - t0) / 1e9))
            run.currentPhase = ""
        }
    }

    /**
     * One conversation with a model, or one long tool call.
     *
     * [turn] is the heartbeat the server watches: a session that stops calling it
     * is waiting on something that is not coming back.
     */
    class Session(
        private val run: Run, private val agent: String,
        private val branch: String = "", private val model: String = "",
    ) : AutoCloseable {
        val id = id()
        private var turns = 0
        private var usd = 0.0
        private val t0 = System.nanoTime()

        init {
            send("session_start", mapOf("run" to run.id, "session" to id,
                "agent" to agent, "branch" to branch, "model" to model,
                "phase" to run.currentPhase))
        }

        fun turn(note: String = "") {
            turns++
            send("turn", mapOf("run" to run.id, "session" to id, "agent" to agent,
                "branch" to branch, "phase" to run.currentPhase, "n" to turns,
                "note" to note))
        }

        fun spend(usd: Double = 0.0, tokensIn: Int = 0, tokensOut: Int = 0) {
            this.usd += usd
            send("spend", mapOf("run" to run.id, "session" to id, "agent" to agent,
                "phase" to run.currentPhase, "usd" to usd,
                "tokens_in" to tokensIn, "tokens_out" to tokensOut))
        }

        fun log(text: String) = send("log", mapOf("run" to run.id, "session" to id,
            "agent" to agent, "branch" to branch, "phase" to run.currentPhase,
            "text" to text.take(8000)))

        override fun close() {
            send("session_end", mapOf("run" to run.id, "session" to id,
                "agent" to agent, "seconds" to (System.nanoTime() - t0) / 1e9,
                "turns" to turns, "usd" to usd))
        }
    }
}
