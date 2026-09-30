package bench

import (
	"context"
	"sync"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/provider"
)

// fakeProvider is a deterministic, in-process test double for
// provider.Provider: fixed output, fixed (simulated) latency, an
// unavailable mode that returns *provider.UnavailableError like a real
// backend that's missing or offline, and (via rateLimitAfter) a way to
// simulate a backend that rate-limits after a fixed number of calls, like
// Apple Intelligence Pro hitting its usage limit mid-benchmark.
type fakeProvider struct {
	id          string
	text        string
	latency     time.Duration
	unavailable bool
	// rateLimitAfter, when > 0, makes every call past the rateLimitAfter'th
	// return *provider.RateLimitedError instead of text/unavailable (i.e.
	// calls 1..rateLimitAfter succeed, call rateLimitAfter+1 onward fail).
	rateLimitAfter int

	mu    sync.Mutex
	calls int
}

func (f *fakeProvider) ID() string          { return f.id }
func (f *fakeProvider) Description() string { return "fake test provider " + f.id }
func (f *fakeProvider) Kind() provider.Kind { return provider.API }

func (f *fakeProvider) Detect(ctx context.Context) provider.Status {
	if f.unavailable {
		return provider.Status{Reason: "fake: offline"}
	}
	return provider.Status{Available: true, Version: "fake-1"}
}

func (f *fakeProvider) Generate(ctx context.Context, req provider.Request) (string, error) {
	if f.latency > 0 {
		select {
		case <-time.After(f.latency):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.rateLimitAfter > 0 {
		f.mu.Lock()
		f.calls++
		n := f.calls
		f.mu.Unlock()
		if n > f.rateLimitAfter {
			return "", provider.RateLimited(f.id, 0, "fake: usage limit reached after %d calls", f.rateLimitAfter)
		}
	}
	if f.unavailable {
		return "", provider.Unavailable(f.id, "fake backend offline")
	}
	if req.OnChunk != nil {
		req.OnChunk(f.text)
	}
	return f.text, nil
}

// fakeFamily registers a fixed, deterministic set of test providers so
// helpers that go through provider.All/provider.Get (like
// bench.ResolveProviders) can be exercised without any real backend.
type fakeFamily struct{}

func (fakeFamily) Name() string { return "faketest" }

func (fakeFamily) Providers(cfg *config.Config) []provider.Provider {
	return []provider.Provider{
		&fakeProvider{id: "faketest:ok", text: "echo hello"},
		&fakeProvider{id: "faketest:down", unavailable: true},
	}
}

func init() { provider.Register(fakeFamily{}) }
