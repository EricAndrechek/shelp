package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
)

// chainFakeProvider is a minimal, in-process Provider test double for
// exercising Chain/RateLimited/cooldown behavior without any real backend.
// It's local to this file (rather than shared with internal/bench's own
// fakeProvider) since internal/bench already imports this package and a
// reverse import would cycle.
type chainFakeProvider struct {
	id   string
	text string
	err  error // returned by every call when set

	mu    sync.Mutex
	calls int
}

func (f *chainFakeProvider) ID() string          { return f.id }
func (f *chainFakeProvider) Description() string { return "chain fake test provider " + f.id }
func (f *chainFakeProvider) Kind() Kind          { return API }
func (f *chainFakeProvider) Detect(ctx context.Context) Status {
	return Status{Available: true}
}

func (f *chainFakeProvider) Generate(ctx context.Context, req Request) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return f.text, nil
}

func (f *chainFakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// chainFakeFamily serves whatever providers setChainFakeProviders last
// registered, so Get/All (and therefore Chain) can resolve fake IDs.
type chainFakeFamily struct{}

func (chainFakeFamily) Name() string { return "chainfaketest" }

var (
	chainFakeMu  sync.Mutex
	chainFakeSet []Provider
)

func (chainFakeFamily) Providers(cfg *config.Config) []Provider {
	chainFakeMu.Lock()
	defer chainFakeMu.Unlock()
	return append([]Provider(nil), chainFakeSet...)
}

func init() { Register(chainFakeFamily{}) }

// setChainFakeProviders installs ps as the fake family's provider set for
// the duration of t, restoring the previous set on cleanup.
func setChainFakeProviders(t *testing.T, ps ...Provider) {
	t.Helper()
	chainFakeMu.Lock()
	prev := chainFakeSet
	chainFakeSet = ps
	chainFakeMu.Unlock()
	t.Cleanup(func() {
		chainFakeMu.Lock()
		chainFakeSet = prev
		chainFakeMu.Unlock()
	})
}

func setCooldownCacheHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// --- RateLimitedError / CooldownSkippedError ---------------------------

func TestRateLimitedError_SatisfiesUnavailableViaAs(t *testing.T) {
	err := RateLimited("fake:x", 5*time.Second, "usage limit")
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("RateLimited() does not satisfy errors.As(&UnavailableError): %v", err)
	}
	if ue.ID != "fake:x" || ue.Reason != "usage limit" {
		t.Fatalf("unwrapped UnavailableError = %+v", ue)
	}
	var rle *RateLimitedError
	if !errors.As(err, &rle) {
		t.Fatal("RateLimited() does not satisfy errors.As(&RateLimitedError)")
	}
	if rle.RetryAfter != 5*time.Second {
		t.Fatalf("RetryAfter = %v, want 5s", rle.RetryAfter)
	}
}

func TestCooldownSkippedError_SatisfiesUnavailableViaAs(t *testing.T) {
	err := &CooldownSkippedError{ID: "fake:x", Until: time.Now().Add(time.Hour), Reason: "usage limit"}
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("CooldownSkippedError does not satisfy errors.As(&UnavailableError): %v", err)
	}
}

// --- Chain: cooldown skip / record / --force ----------------------------

func TestChain_RateLimitedProviderGetsCooledDownAndSkippedNextTime(t *testing.T) {
	setCooldownCacheHome(t)

	limited := &chainFakeProvider{id: "chainfaketest:limited", err: RateLimited("chainfaketest:limited", 0, "usage limit")}
	backup := &chainFakeProvider{id: "chainfaketest:backup", text: "ls -la"}
	setChainFakeProviders(t, limited, backup)

	cfg, err := config.Parse(`cooldown = "10m"`)
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}

	var fallbacks []string
	res, err := Chain(context.Background(), cfg, []string{"chainfaketest:limited", "chainfaketest:backup"}, Request{Prompt: "x"}, false,
		func(id string, _ error) { fallbacks = append(fallbacks, id) })
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if res.ProviderID != "chainfaketest:backup" {
		t.Fatalf("ProviderID = %q, want chainfaketest:backup", res.ProviderID)
	}
	if limited.callCount() != 1 {
		t.Fatalf("limited.calls after 1st Chain = %d, want 1", limited.callCount())
	}
	if len(fallbacks) != 1 || fallbacks[0] != "chainfaketest:limited" {
		t.Fatalf("fallbacks after 1st Chain = %v, want [chainfaketest:limited]", fallbacks)
	}

	// A second Chain call must skip chainfaketest:limited entirely: no
	// second Generate call, reported through onFallback as a
	// CooldownSkippedError.
	fallbacks = nil
	res2, err := Chain(context.Background(), cfg, []string{"chainfaketest:limited", "chainfaketest:backup"}, Request{Prompt: "x"}, false,
		func(id string, _ error) { fallbacks = append(fallbacks, id) })
	if err != nil {
		t.Fatalf("Chain (2nd): %v", err)
	}
	if res2.ProviderID != "chainfaketest:backup" {
		t.Fatalf("ProviderID (2nd) = %q, want chainfaketest:backup", res2.ProviderID)
	}
	if limited.callCount() != 1 {
		t.Fatalf("limited.calls after 2nd Chain = %d, want still 1 (should be skipped without a call)", limited.callCount())
	}
	if len(fallbacks) != 1 || fallbacks[0] != "chainfaketest:limited" {
		t.Fatalf("fallbacks after 2nd Chain = %v, want [chainfaketest:limited]", fallbacks)
	}
}

func TestChain_WithoutForceSkipsCooledDownSingleProvider(t *testing.T) {
	setCooldownCacheHome(t)
	if err := cooldown.Set("chainfaketest:limited", time.Now().Add(time.Hour), "usage limit"); err != nil {
		t.Fatalf("cooldown.Set: %v", err)
	}

	limited := &chainFakeProvider{id: "chainfaketest:limited", text: "ok"}
	setChainFakeProviders(t, limited)
	cfg, err := config.Parse("")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Chain(context.Background(), cfg, []string{"chainfaketest:limited"}, Request{Prompt: "x"}, false, nil)
	if err == nil {
		t.Fatal("expected an error: the only provider is cooled down and force is false")
	}
	if limited.callCount() != 0 {
		t.Fatalf("calls = %d, want 0 (should be skipped without --force even as the sole explicit provider)", limited.callCount())
	}
	var cse *CooldownSkippedError
	if !errors.As(err, &cse) {
		t.Fatalf("error = %v (%T), want it to unwrap/join to a *CooldownSkippedError", err, err)
	}
}

func TestChain_ForceBypassesCooldown(t *testing.T) {
	setCooldownCacheHome(t)
	if err := cooldown.Set("chainfaketest:limited", time.Now().Add(time.Hour), "usage limit"); err != nil {
		t.Fatalf("cooldown.Set: %v", err)
	}

	limited := &chainFakeProvider{id: "chainfaketest:limited", text: "ok"}
	setChainFakeProviders(t, limited)
	cfg, err := config.Parse("")
	if err != nil {
		t.Fatal(err)
	}

	res, err := Chain(context.Background(), cfg, []string{"chainfaketest:limited"}, Request{Prompt: "x"}, true, nil)
	if err != nil {
		t.Fatalf("Chain with force=true: %v", err)
	}
	if res.ProviderID != "chainfaketest:limited" {
		t.Fatalf("ProviderID = %q, want chainfaketest:limited", res.ProviderID)
	}
	if limited.callCount() != 1 {
		t.Fatalf("calls = %d, want 1 (force=true should bypass the cooldown and actually call it)", limited.callCount())
	}
}

func TestChain_UnexpiredCooldownUsesRetryAfterOverConfigDefault(t *testing.T) {
	setCooldownCacheHome(t)
	// A long configured default, but the error specifies a short
	// RetryAfter: the cooldown recorded should use RetryAfter, not the
	// configured default -- verified indirectly by checking it's already
	// expired almost immediately.
	limited := &chainFakeProvider{id: "chainfaketest:limited", err: RateLimited("chainfaketest:limited", 10*time.Millisecond, "usage limit")}
	setChainFakeProviders(t, limited)
	cfg, err := config.Parse(`cooldown = "1h"`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Chain(context.Background(), cfg, []string{"chainfaketest:limited"}, Request{Prompt: "x"}, false, nil)
	if err == nil {
		t.Fatal("expected an error: the only provider is rate-limited")
	}

	entries := cooldown.Load()
	e, ok := entries["chainfaketest:limited"]
	if !ok {
		t.Fatal("expected a cooldown entry to have been recorded")
	}
	if until := time.Until(e.Until); until > time.Second {
		t.Fatalf("cooldown until %v away, want ~10ms (RetryAfter, not the 1h config default)", until)
	}
}

func TestChain_NoProvidersConfigured(t *testing.T) {
	setCooldownCacheHome(t)
	cfg, _ := config.Parse("")
	_, err := Chain(context.Background(), cfg, nil, Request{}, false, nil)
	if err == nil {
		t.Fatal("expected an error for an empty id list")
	}
}
