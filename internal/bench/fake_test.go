package bench

import (
	"context"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/provider"
)

// fakeProvider is a deterministic, in-process test double for
// provider.Provider: fixed output, fixed (simulated) latency, and an
// unavailable mode that returns *provider.UnavailableError like a real
// backend that's missing or offline.
type fakeProvider struct {
	id          string
	text        string
	latency     time.Duration
	unavailable bool
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
