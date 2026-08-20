# frozen_string_literal: true

# Report what a long-running pipeline is doing, so something outside it can judge.
#
# Every send happens on its own thread and its result is ignored: reporting must
# never be the reason a run is slower or stops. Standard library only.
#
#   run = Ammit::Run.new("APF-1934")
#   run.phase("implementing") do
#     run.session("implement", branch: "req-3", model: "sonnet") do |s|
#       s.turn                                   # heartbeat: still thinking
#       s.spend(usd: 0.42, tokens_in: 120_000, tokens_out: 3_400)
#     end
#   end
#   run.finish("PASS", "31 scenarios, 0 failures")

require "json"
require "net/http"
require "securerandom"
require "uri"

module Ammit
  ENDPOINT = ENV.fetch("AMMIT_URL", "http://ammit:8099")
  ENABLED = ENV["AMMIT_DISABLE"] != "1"

  module_function

  def send_event(kind, fields = {})
    return unless ENABLED

    payload = { kind: kind, at: Time.now.to_f }.merge(fields)
    Thread.new do
      uri = URI("#{ENDPOINT}/events")
      Net::HTTP.start(uri.host, uri.port, open_timeout: 3, read_timeout: 3) do |http|
        request = Net::HTTP::Post.new(uri, "Content-Type" => "application/json")
        request.body = JSON.generate(payload)
        http.request(request)
      end
    rescue StandardError
      # A watchdog that can stop the pipeline is not a watchdog.
      nil
    end
  end

  def id = SecureRandom.hex(6)

  # One conversation with a model, or one long tool call.
  #
  # +turn+ is the heartbeat the server watches: a session that stops calling it is
  # waiting on something that is not coming back.
  class Session
    attr_reader :id

    def initialize(run, agent, branch: "", model: "")
      @run = run
      @agent = agent
      @branch = branch
      @id = Ammit.id
      @turns = 0
      @usd = 0.0
      @t0 = Time.now
      Ammit.send_event("session_start", run: run.id, session: @id, agent: agent,
                                        branch: branch, model: model,
                                        phase: run.current_phase)
    end

    def turn(note = "")
      @turns += 1
      Ammit.send_event("turn", run: @run.id, session: @id, agent: @agent,
                               branch: @branch, phase: @run.current_phase,
                               n: @turns, note: note)
    end

    def spend(usd: 0.0, tokens_in: 0, tokens_out: 0)
      @usd += usd
      Ammit.send_event("spend", run: @run.id, session: @id, agent: @agent,
                                phase: @run.current_phase, usd: usd,
                                tokens_in: tokens_in, tokens_out: tokens_out)
    end

    def log(text)
      Ammit.send_event("log", run: @run.id, session: @id, agent: @agent,
                              branch: @branch, phase: @run.current_phase,
                              text: text[0, 8000])
    end

    def close(error = nil)
      Ammit.send_event("session_end", run: @run.id, session: @id, agent: @agent,
                                      seconds: (Time.now - @t0).round(1),
                                      turns: @turns, usd: @usd,
                                      failed: !error.nil?)
    end
  end

  # One unit of work the outside world cares about — a ticket, a job, a build.
  class Run
    attr_reader :id, :name
    attr_accessor :current_phase

    def initialize(name, tags: {})
      @id = Ammit.id
      @name = name
      @current_phase = ""
      @t0 = Time.now
      Ammit.send_event("run_start", run: @id, name: name, tags: tags)
    end

    def phase(name)
      @current_phase = name
      Ammit.send_event("phase_start", run: @id, phase: name)
      t0 = Time.now
      return self unless block_given?

      begin
        yield
      ensure
        Ammit.send_event("phase_end", run: @id, phase: name,
                                      seconds: (Time.now - t0).round(1))
        @current_phase = ""
      end
    end

    def session(agent, branch: "", model: "")
      s = Session.new(self, agent, branch: branch, model: model)
      return s unless block_given?

      begin
        yield s
      ensure
        s.close
      end
    end

    def note(text) = Ammit.send_event("note", run: @id, text: text[0, 2000])

    def finish(verdict = "", summary = "")
      Ammit.send_event("run_end", run: @id, verdict: verdict, summary: summary[0, 2000],
                                  seconds: (Time.now - @t0).round(1))
    end
  end
end
