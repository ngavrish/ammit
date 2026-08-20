using System;
using System.Collections.Generic;
using System.Net.Http;
using System.Text;
using System.Text.Json;

namespace Ammit;

/// <summary>
/// Report what a long-running pipeline is doing, so something outside it can judge.
///
/// Every send is fire-and-forget: reporting must never be the reason a run is
/// slower or stops. Nothing outside the base class library.
///
/// <code>
/// var run = new Run("APF-1934");
/// using (run.Phase("implementing"))
/// using (var s = run.Session("implement", "req-3", "sonnet"))
/// {
///     s.Turn();                                  // heartbeat: still thinking
///     s.Spend(usd: 0.42, tokensIn: 120_000, tokensOut: 3_400);
/// }
/// run.Finish("PASS", "31 scenarios, 0 failures");
/// </code>
/// </summary>
public static class Client
{
    private static readonly HttpClient Http = new() { Timeout = TimeSpan.FromSeconds(3) };
    private static string _endpoint =
        Environment.GetEnvironmentVariable("AMMIT_URL") ?? "http://ammit:8099";
    private static readonly bool Enabled =
        Environment.GetEnvironmentVariable("AMMIT_DISABLE") != "1";

    public static void Endpoint(string url) => _endpoint = url.TrimEnd('/');

    public static void Send(string kind, Dictionary<string, object?> fields)
    {
        if (!Enabled) return;
        fields["kind"] = kind;
        fields["at"] = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() / 1000.0;
        var body = new StringContent(JsonSerializer.Serialize(fields), Encoding.UTF8,
                                     "application/json");
        // Deliberately not awaited, and failures are swallowed: a watchdog that
        // can stall the pipeline is not a watchdog.
        _ = Http.PostAsync($"{_endpoint}/events", body)
                .ContinueWith(t => { _ = t.Exception; });
    }

    internal static string NewId() => Guid.NewGuid().ToString("N")[..12];
}

public sealed class Run
{
    public string Id { get; } = Client.NewId();
    public string Name { get; }
    internal string CurrentPhase = "";
    private readonly DateTime _t0 = DateTime.UtcNow;

    public Run(string name, Dictionary<string, string>? tags = null)
    {
        Name = name;
        Client.Send("run_start", new() { ["run"] = Id, ["name"] = name, ["tags"] = tags });
    }

    public Phase Phase(string name) => new(this, name);

    public Session Session(string agent, string branch = "", string model = "") =>
        new(this, agent, branch, model);

    public void Note(string text) =>
        Client.Send("note", new() { ["run"] = Id, ["text"] = text });

    public void Finish(string verdict = "", string summary = "") =>
        Client.Send("run_end", new()
        {
            ["run"] = Id, ["verdict"] = verdict, ["summary"] = summary,
            ["seconds"] = (DateTime.UtcNow - _t0).TotalSeconds,
        });
}

public sealed class Phase : IDisposable
{
    private readonly Run _run;
    private readonly string _name;
    private readonly DateTime _t0 = DateTime.UtcNow;

    internal Phase(Run run, string name)
    {
        _run = run;
        _name = name;
        run.CurrentPhase = name;
        Client.Send("phase_start", new() { ["run"] = run.Id, ["phase"] = name });
    }

    public void Dispose()
    {
        Client.Send("phase_end", new()
        {
            ["run"] = _run.Id, ["phase"] = _name,
            ["seconds"] = (DateTime.UtcNow - _t0).TotalSeconds,
        });
        _run.CurrentPhase = "";
    }
}

/// <summary>
/// One conversation with a model, or one long tool call.
///
/// <see cref="Turn"/> is the heartbeat the server watches: a session that stops
/// calling it is waiting on something that is not coming back.
/// </summary>
public sealed class Session : IDisposable
{
    public string Id { get; } = Client.NewId();
    private readonly Run _run;
    private readonly string _agent, _branch;
    private int _turns;
    private double _usd;
    private readonly DateTime _t0 = DateTime.UtcNow;

    internal Session(Run run, string agent, string branch, string model)
    {
        _run = run; _agent = agent; _branch = branch;
        Client.Send("session_start", new()
        {
            ["run"] = run.Id, ["session"] = Id, ["agent"] = agent,
            ["branch"] = branch, ["model"] = model, ["phase"] = run.CurrentPhase,
        });
    }

    public void Turn(string note = "")
    {
        _turns++;
        Client.Send("turn", new()
        {
            ["run"] = _run.Id, ["session"] = Id, ["agent"] = _agent,
            ["branch"] = _branch, ["phase"] = _run.CurrentPhase,
            ["n"] = _turns, ["note"] = note,
        });
    }

    public void Spend(double usd = 0, int tokensIn = 0, int tokensOut = 0)
    {
        _usd += usd;
        Client.Send("spend", new()
        {
            ["run"] = _run.Id, ["session"] = Id, ["agent"] = _agent,
            ["phase"] = _run.CurrentPhase, ["usd"] = usd,
            ["tokens_in"] = tokensIn, ["tokens_out"] = tokensOut,
        });
    }

    public void Log(string text) => Client.Send("log", new()
    {
        ["run"] = _run.Id, ["session"] = Id, ["agent"] = _agent,
        ["branch"] = _branch, ["phase"] = _run.CurrentPhase,
        ["text"] = text.Length > 8000 ? text[..8000] : text,
    });

    public void Dispose() => Client.Send("session_end", new()
    {
        ["run"] = _run.Id, ["session"] = Id, ["agent"] = _agent,
        ["seconds"] = (DateTime.UtcNow - _t0).TotalSeconds,
        ["turns"] = _turns, ["usd"] = _usd,
    });
}
