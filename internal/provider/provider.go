// Package provider defines the model backends shelp can call and the
// registry that discovers them.
//
// A provider is one concrete model route with a stable ID, e.g. "fm",
// "shortcuts:apple-pro", "claude:haiku", "lmstudio:qwen3-30b". Providers
// are grouped into families (one file each) that register themselves in
// init() and construct candidates from the config. Construction must be
// cheap (exec.LookPath at most); anything slower belongs in Detect, which
// only `shelp providers` and the benchmark call. The CLI never probes: it
// calls Generate and falls through the chain on error.
package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
)

// Mode is what the model is asked to do.
type Mode string

const (
	Suggest Mode = "suggest" // natural-language task -> one shell command
	Explain Mode = "explain" // shell command -> short plain-text explanation
)

// Request is one model call. System and Prompt are already fully rendered
// by internal/prompt; providers must not add their own instructions.
type Request struct {
	Mode   Mode
	System string
	Prompt string
	// OnChunk, if non-nil, receives output as it streams. Providers that
	// cannot stream never call it; Call then reports FirstChunk == Latency.
	OnChunk func(string)
}

// Kind groups providers for display and default ordering.
type Kind string

const (
	OnDevice    Kind = "on-device"    // Apple's on-device model (fm)
	AppleCloud  Kind = "apple-cloud"  // Apple Private Cloud Compute / ChatGPT via Shortcuts
	API         Kind = "api"          // hosted OpenAI-compatible endpoint with a key
	LocalServer Kind = "local-server" // LM Studio, Ollama, llama-server, fm serve
	AgentCLI    Kind = "agent-cli"    // an installed AI coding-agent CLI in headless mode (today: claude)
)

// Status is the result of probing a provider.
type Status struct {
	Available bool
	// Reason explains unavailability and how to fix it, e.g.
	// "fm license not accepted: run `sudo fm license`".
	Reason  string
	Version string // backend version string when cheaply known
}

// Provider is one callable model route.
type Provider interface {
	ID() string          // stable, lowercase, e.g. "shortcuts:apple-pro"
	Description() string // one line for `shelp providers`
	Kind() Kind
	// Detect probes availability. It may spawn processes or open sockets
	// and should finish within a couple of seconds.
	Detect(ctx context.Context) Status
	// Generate returns the model's raw text. Return an *UnavailableError
	// when the backend is missing or refused before generating, so callers
	// can fall through to the next provider. Return a *RateLimitedError
	// (via RateLimited) specifically when the backend says it's
	// rate-limited or out of quota, so Chain can remember to skip it for a
	// while instead of calling it again on the very next invocation.
	Generate(ctx context.Context, req Request) (string, error)
}

// Family constructs the candidate providers of one backend type.
type Family interface {
	Name() string
	Providers(cfg *config.Config) []Provider
}

// UnavailableError means a provider could not serve the request at all
// (not installed, not set up, offline, rate-limited).
type UnavailableError struct {
	ID     string
	Reason string
}

func (e *UnavailableError) Error() string { return e.ID + ": " + e.Reason }

// Unavailable is a convenience constructor for *UnavailableError.
func Unavailable(id, format string, args ...any) error {
	return &UnavailableError{ID: id, Reason: fmt.Sprintf(format, args...)}
}

// RateLimitedError means a provider is currently rate-limited or out of
// quota: a usage-limit message from Apple Shortcuts, an HTTP 429 from an
// OpenAI-compatible endpoint, or a quota error from an agent CLI. It
// satisfies errors.As(err, &*UnavailableError) via Unwrap, so every
// existing "is this provider unavailable" check (provider.Chain,
// internal/bench) treats a rate limit as unavailable without any changes
// on their part; provider.Chain additionally recognizes *RateLimitedError
// specifically (via errors.As) to put the provider into cooldown (see
// internal/cooldown) so a fallback chain -- or a benchmark run -- doesn't
// keep hammering (and, for Apple Shortcuts, keep triggering a macOS
// notification from) a backend that just said no.
type RateLimitedError struct {
	ID     string
	Reason string
	// RetryAfter is how long the backend says to wait before trying again,
	// when it told us (e.g. an HTTP Retry-After header). Zero means "not
	// specified": callers fall back to their own default
	// (cooldown.DefaultDuration, or shelp's `cooldown` config key).
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string { return e.ID + ": rate limited: " + e.Reason }

// Unwrap lets errors.As(err, &*UnavailableError) find a RateLimitedError
// too: a rate limit is a form of unavailability.
func (e *RateLimitedError) Unwrap() error { return &UnavailableError{ID: e.ID, Reason: e.Reason} }

// RateLimited is a convenience constructor for *RateLimitedError.
func RateLimited(id string, retryAfter time.Duration, format string, args ...any) error {
	return &RateLimitedError{ID: id, Reason: fmt.Sprintf(format, args...), RetryAfter: retryAfter}
}

// CooldownSkippedError means Chain declined to even call a provider
// because a previous RateLimitedError put it in cooldown (see
// internal/cooldown); Generate was never invoked. Pass force=true to
// Chain to bypass this for one call. It satisfies
// errors.As(err, &*UnavailableError) via Unwrap, like RateLimitedError.
type CooldownSkippedError struct {
	ID     string
	Until  time.Time
	Reason string
}

func (e *CooldownSkippedError) Error() string {
	return fmt.Sprintf("%s rate-limited until %s (%s); skipping", e.ID, e.Until.Local().Format("15:04"), e.Reason)
}

func (e *CooldownSkippedError) Unwrap() error {
	return &UnavailableError{ID: e.ID, Reason: fmt.Sprintf("in cooldown until %s (%s)", e.Until.Local().Format("15:04"), e.Reason)}
}

var (
	mu       sync.Mutex
	families []Family
)

// Register adds a family. Call it from init().
func Register(f Family) {
	mu.Lock()
	defer mu.Unlock()
	families = append(families, f)
}

// All returns every candidate provider, sorted by kind then ID.
func All(cfg *config.Config) []Provider {
	mu.Lock()
	fs := append([]Family(nil), families...)
	mu.Unlock()
	var out []Provider
	seen := map[string]bool{}
	for _, f := range fs {
		for _, p := range f.Providers(cfg) {
			if !seen[p.ID()] {
				seen[p.ID()] = true
				out = append(out, p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ki, kj := kindRank(out[i].Kind()), kindRank(out[j].Kind()); ki != kj {
			return ki < kj
		}
		return out[i].ID() < out[j].ID()
	})
	return out
}

func kindRank(k Kind) int {
	for i, kk := range []Kind{AppleCloud, OnDevice, API, LocalServer, AgentCLI} {
		if k == kk {
			return i
		}
	}
	return 99
}

// Get finds a provider by exact ID.
func Get(cfg *config.Config, id string) (Provider, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range All(cfg) {
		if p.ID() == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown provider %q (see `shelp providers`)", id)
}

// DefaultChain is the fallback order used when neither flags, env nor
// config name providers. It is tuned from benchmark results; see README.
var DefaultChain = []string{"shortcuts:apple-pro", "fm"}

// Result is a completed call with timing.
type Result struct {
	Text       string
	ProviderID string
	Latency    time.Duration // Generate wall time
	FirstChunk time.Duration // time to first streamed chunk (== Latency if not streamed)
}

// Call runs one provider and times it.
func Call(ctx context.Context, p Provider, req Request) (Result, error) {
	start := time.Now()
	var first time.Duration
	// Only wrap a real callback: a non-nil OnChunk tells providers to
	// stream, which can change their behavior and latency. The benchmark
	// passes a no-op callback when it wants time-to-first-chunk.
	if inner := req.OnChunk; inner != nil {
		req.OnChunk = func(s string) {
			if first == 0 {
				first = time.Since(start)
			}
			inner(s)
		}
	}
	text, err := p.Generate(ctx, req)
	lat := time.Since(start)
	if first == 0 {
		first = lat
	}
	return Result{Text: text, ProviderID: p.ID(), Latency: lat, FirstChunk: first}, err
}

// Chain tries each provider ID in order and returns the first success. A
// provider currently in cooldown (see internal/cooldown -- it hit a rate
// limit recently) is skipped without being called at all, unless force is
// true. onFallback, if non-nil, is told about each skip/failure before
// moving on.
//
// A *RateLimitedError from a live call additionally puts that provider
// into cooldown for next time: until = now + the error's RetryAfter when
// it specified one, else now + cfg's top-level `cooldown` key (default
// cooldown.DefaultDuration). A successful call never clears a cooldown --
// cooldowns just expire on their own.
func Chain(ctx context.Context, cfg *config.Config, ids []string, req Request, force bool, onFallback func(id string, err error)) (Result, error) {
	var errs []error
	cds := cooldown.Load()
	cdDur := cooldown.DefaultDuration
	if cfg != nil && cfg.Cooldown != "" {
		if d, perr := time.ParseDuration(cfg.Cooldown); perr == nil && d > 0 {
			cdDur = d
		}
	}
	for _, id := range ids {
		if !force {
			if e, ok := cooldown.Active(cds, id); ok {
				err := &CooldownSkippedError{ID: id, Until: e.Until, Reason: e.Reason}
				errs = append(errs, err)
				if onFallback != nil {
					onFallback(id, err)
				}
				continue
			}
		}
		p, err := Get(cfg, id)
		if err == nil {
			var res Result
			res, err = Call(ctx, p, req)
			if err == nil && strings.TrimSpace(res.Text) != "" {
				return res, nil
			}
			if err == nil {
				err = Unavailable(id, "empty response")
			}
			var rle *RateLimitedError
			if errors.As(err, &rle) {
				until := time.Now().Add(cdDur)
				if rle.RetryAfter > 0 {
					until = time.Now().Add(rle.RetryAfter)
				}
				_ = cooldown.Set(id, until, rle.Reason) // best-effort; a write failure never blocks the chain
			}
		}
		errs = append(errs, err)
		if onFallback != nil {
			onFallback(id, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 0 {
		return Result{}, errors.New("no providers configured")
	}
	return Result{}, errors.Join(errs...)
}

// SetupStep is an optional one-time installation a family offers through
// `shelp setup [name]` (e.g. importing Apple Shortcuts).
type SetupStep struct {
	Name string // e.g. "shortcuts"
	Help string // one line
	Run  func(ctx context.Context, cfg *config.Config, stdout, stderr io.Writer) error
}

var setupSteps []SetupStep

// RegisterSetup adds a setup step. Call it from init().
func RegisterSetup(s SetupStep) {
	mu.Lock()
	defer mu.Unlock()
	setupSteps = append(setupSteps, s)
}

// SetupSteps returns registered setup steps in registration order.
func SetupSteps() []SetupStep {
	mu.Lock()
	defer mu.Unlock()
	return append([]SetupStep(nil), setupSteps...)
}
