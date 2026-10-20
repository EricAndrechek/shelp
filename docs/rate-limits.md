# Rate limits, cooldowns and offline

Moved out of CLAUDE.md verbatim (2026-10-20).

## Rate limits and cooldowns

A provider can be rate-limited or out of quota, not just missing/offline.
Real incident (2026-09-30): Apple Intelligence Pro (`shortcuts:apple-pro`)
hit "Error: You have reached the usage limit for this model. Please try
again later." after ~93 calls in ~2.5 minutes of shelp-bench benchmarking
(reset window unknown), and the benchmark kept dispatching ~240 more
calls -- each one a Shortcuts run that failed the same way. **Every
failed Shortcuts run posts a macOS notification**, so the user was
flooded. `shelp`'s own CLI has the identical exposure: each call tries
the rate-limited shortcut first (another notification) before falling
through to the next provider in the chain.

Classification per family: shortcuts.go treats stdout/stderr as a rate
limit in two shapes only, not a blanket substring match regardless of
exit code -- that would misfire on a successful answer that happens to
contain "try again later" (e.g. explaining a retry loop). (a) a non-zero
exit whose stdout/stderr contains "usage limit" or "try again later"
(case-insensitive); or (b) an exit-0 run whose trimmed stdout starts with
"Error:" and contains "usage limit" -- the one real observed exit-0 shape
("Error: You have reached the usage limit for this model. Please try
again later.": a rate-limited "Use Model" run typically still exits 0,
its output IS the error text). openai.go treats an HTTP 429 as one,
parsing `Retry-After` (seconds or an HTTP-date) when present; agentcli.go's
parse func (claude's, via `agentRateLimitReason`, shared with any future
agentcli spec) treats "rate limit", "quota", "usage limit", "429", or
"resource exhausted" in a failed call's stderr/stdout (case-insensitive,
non-zero exit) as one.
All three families construct
`provider.RateLimitedError` via `provider.RateLimited(id, retryAfter,
format, args...)`, which still satisfies `errors.As(err,
&*UnavailableError)` (via `Unwrap`), so every existing "is this provider
unavailable" check keeps working unchanged; `provider.Chain` and the
bench runner additionally recognize it specifically via `errors.As`.

Cooldown: a rate limit is persisted to `internal/cooldown`'s store,
`$XDG_CACHE_HOME/shelp/cooldown.json` (`~/.cache/shelp/` by default):
provider ID -> until (now + the error's `RetryAfter` when it specified
one, else the top-level `cooldown` config key, default 30m) + reason.
Reads/writes are failure-tolerant (a missing/corrupt file just means "no
cooldowns") and writes are atomic (a uniquely-named temp file + rename).
`provider.Chain` skips a cooled-down provider without calling it at all
(reported through the usual `-v` fallback callback, e.g. "shelp:
shortcuts:apple-pro rate-limited until 14:32 (usage limit); skipping"),
unless `--force` is passed -- which applies even when the cooled-down
provider is the only one explicitly named with `-p`. A successful call
never clears a cooldown; cooldowns just expire. `shelp providers` shows a
cooled-down provider as STATUS "cooldown" without probing it (same
reasoning as the CLI itself never calling Detect); `shelp providers
--reset-cooldowns` clears the whole store.

Benchmark circuit breaker: `internal/bench`'s runner trips a per-provider
breaker on that provider's first RateLimitedError and stops dispatching
its remaining trials for the rest of the run -- they are never written to
results.jsonl, so a later `--resume <dir>` fills them in once the
underlying issue has cleared. `run.json`'s `breaker_trips` records which
providers tripped, when, after how many calls, and how many trials were
skipped; each trial's `results.jsonl` line gets `rate_limited: true` when
it hit one (in addition to the existing `unavailable: true`). `shelp-bench
run --min-interval <duration>` paces consecutive calls to the SAME
provider at least that far apart, to help a future run stay under a rate
limit instead of tripping the breaker at all.

### Offline

`Kind.NeedsNetwork()` is true for AppleCloud (Shortcuts), API and AgentCLI
(claude); OnDevice (`fm`) and LocalServer are local. Before `provider.Chain`
calls its first network provider it runs `Offline()` once per process
(`internal/provider/offline.go`): a UDP `net.Dial` to documentation
addresses (192.0.2.1, 2001:db8::1) that only consults the routing table --
no packets -- and is offline only if neither IPv4 nor IPv6 has a route.
`offlineCheck` is a package var so tests fake it. Offline, network providers
are skipped without being called (`*OfflineSkippedError`, "shelp:
shortcuts:apple-pro skipped: offline (no network route)"), which also avoids
the Shortcuts failure notification; `--force` does NOT override it (it is for
cooldowns). If nothing local was tried the final error adds "offline, and no
local provider in the chain; add `fm` ...". An API provider failing with a
clear network error (`IsNetworkError`: DNS failure, unreachable, no route,
TLS handshake timeout; not connection refused) marks the rest of the chain
offline; Shortcuts/claude free-text errors are deliberately not classified.
Limit: Wi-Fi with no internet or a captive portal still has a route, so
provider timeouts still apply. `shelp providers` appends "offline" to the
chain line.

