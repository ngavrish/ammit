<?php

declare(strict_types=1);

namespace Ammit;

/**
 * Report what a long-running pipeline is doing, so something outside it can judge.
 *
 * Sends are short-timeout and their failures are ignored: reporting must never be
 * the reason a run is slower or stops. No dependencies beyond ext-curl.
 *
 *   $run = new Run('APF-1934');
 *   $phase = $run->phase('implementing');
 *   $s = $run->session('implement', 'req-3', 'sonnet');
 *   $s->turn();                                  // heartbeat: still thinking
 *   $s->spend(0.42, 120000, 3400);
 *   $s->end();
 *   $phase->end();
 *   $run->finish('PASS', '31 scenarios, 0 failures');
 */
final class Client
{
    private static string $endpoint;

    public static function endpoint(?string $url = null): string
    {
        if ($url !== null) {
            self::$endpoint = rtrim($url, '/');
        }
        return self::$endpoint ??= getenv('AMMIT_URL') ?: 'http://ammit:8099';
    }

    /** @param array<string, mixed> $fields */
    public static function send(string $kind, array $fields): void
    {
        if (getenv('AMMIT_DISABLE') === '1') {
            return;
        }
        $payload = json_encode(['kind' => $kind, 'at' => microtime(true)] + $fields);
        $ch = curl_init(self::endpoint() . '/events');
        curl_setopt_array($ch, [
            CURLOPT_POST => true,
            CURLOPT_POSTFIELDS => $payload,
            CURLOPT_HTTPHEADER => ['Content-Type: application/json'],
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_TIMEOUT => 3,
            CURLOPT_CONNECTTIMEOUT => 2,
        ]);
        curl_exec($ch);   // failures are deliberately unchecked
        curl_close($ch);
    }

    public static function id(): string
    {
        return bin2hex(random_bytes(6));
    }
}

final class Run
{
    public readonly string $id;
    public string $currentPhase = '';
    private readonly float $t0;

    /** @param array<string, string> $tags */
    public function __construct(public readonly string $name, array $tags = [])
    {
        $this->id = Client::id();
        $this->t0 = microtime(true);
        Client::send('run_start', ['run' => $this->id, 'name' => $name, 'tags' => $tags]);
    }

    public function phase(string $name): Phase
    {
        return new Phase($this, $name);
    }

    public function session(string $agent, string $branch = '', string $model = ''): Session
    {
        return new Session($this, $agent, $branch, $model);
    }

    public function note(string $text): void
    {
        Client::send('note', ['run' => $this->id, 'text' => substr($text, 0, 2000)]);
    }

    public function finish(string $verdict = '', string $summary = ''): void
    {
        Client::send('run_end', [
            'run' => $this->id, 'verdict' => $verdict,
            'summary' => substr($summary, 0, 2000),
            'seconds' => round(microtime(true) - $this->t0, 1),
        ]);
    }
}

final class Phase
{
    private readonly float $t0;

    public function __construct(private readonly Run $run, private readonly string $name)
    {
        $this->t0 = microtime(true);
        $run->currentPhase = $name;
        Client::send('phase_start', ['run' => $run->id, 'phase' => $name]);
    }

    public function end(): void
    {
        Client::send('phase_end', [
            'run' => $this->run->id, 'phase' => $this->name,
            'seconds' => round(microtime(true) - $this->t0, 1),
        ]);
        $this->run->currentPhase = '';
    }
}

/**
 * One conversation with a model, or one long tool call.
 *
 * turn() is the heartbeat the server watches: a session that stops calling it is
 * waiting on something that is not coming back.
 */
final class Session
{
    public readonly string $id;
    private int $turns = 0;
    private float $usd = 0.0;
    private readonly float $t0;

    public function __construct(
        private readonly Run $run,
        private readonly string $agent,
        private readonly string $branch = '',
        string $model = '',
    ) {
        $this->id = Client::id();
        $this->t0 = microtime(true);
        Client::send('session_start', [
            'run' => $run->id, 'session' => $this->id, 'agent' => $agent,
            'branch' => $branch, 'model' => $model, 'phase' => $run->currentPhase,
        ]);
    }

    public function turn(string $note = ''): void
    {
        $this->turns++;
        Client::send('turn', [
            'run' => $this->run->id, 'session' => $this->id, 'agent' => $this->agent,
            'branch' => $this->branch, 'phase' => $this->run->currentPhase,
            'n' => $this->turns, 'note' => $note,
        ]);
    }

    public function spend(float $usd = 0.0, int $tokensIn = 0, int $tokensOut = 0): void
    {
        $this->usd += $usd;
        Client::send('spend', [
            'run' => $this->run->id, 'session' => $this->id, 'agent' => $this->agent,
            'phase' => $this->run->currentPhase, 'usd' => $usd,
            'tokens_in' => $tokensIn, 'tokens_out' => $tokensOut,
        ]);
    }

    public function log(string $text): void
    {
        Client::send('log', [
            'run' => $this->run->id, 'session' => $this->id, 'agent' => $this->agent,
            'branch' => $this->branch, 'phase' => $this->run->currentPhase,
            'text' => substr($text, 0, 8000),
        ]);
    }

    public function end(?\Throwable $error = null): void
    {
        Client::send('session_end', [
            'run' => $this->run->id, 'session' => $this->id, 'agent' => $this->agent,
            'seconds' => round(microtime(true) - $this->t0, 1),
            'turns' => $this->turns, 'usd' => $this->usd,
            'failed' => $error !== null,
        ]);
    }
}
