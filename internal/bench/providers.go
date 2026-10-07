package bench

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/provider"
)

// DetectTimeout bounds how long a single provider's Detect may take when
// probing for `--providers available`.
const DetectTimeout = 10 * time.Second

// Skipped records why a candidate provider was left out of a run.
type Skipped struct {
	ID     string
	Reason string
}

// ResolveProviders turns the --providers flag value into a concrete
// provider list. "" and "available" both mean: probe every candidate
// provider's Detect in parallel (bounded by DetectTimeout) and keep the
// ones that pass; skipped returns the rest with their reasons, sorted by
// ID, for the caller to print. A comma-separated list of IDs instead
// resolves each exactly via provider.Get, without probing.
func ResolveProviders(ctx context.Context, cfg *config.Config, selector string) (ps []provider.Provider, skipped []Skipped, err error) {
	if selector == "" || selector == "available" {
		return detectAvailable(ctx, cfg)
	}
	var out []provider.Provider
	for _, id := range SplitList(selector) {
		p, e := provider.Get(cfg, id)
		if e != nil {
			return nil, nil, e
		}
		out = append(out, p)
	}
	return out, nil, nil
}

// ProviderManifestOf runs Detect once (bounded by DetectTimeout) and returns
// the manifest record for it. Used for run.json regardless of how the
// provider was selected.
func ProviderManifestOf(ctx context.Context, p provider.Provider) ProviderManifest {
	c, cancel := context.WithTimeout(ctx, DetectTimeout)
	defer cancel()
	st := p.Detect(c)
	return ProviderManifest{
		ID: p.ID(), Kind: string(p.Kind()), Description: p.Description(),
		Available: st.Available, Reason: joinReasonFix(st), Version: st.Version,
	}
}

func detectAvailable(ctx context.Context, cfg *config.Config) ([]provider.Provider, []Skipped, error) {
	all := provider.All(cfg)
	sts := make([]provider.Status, len(all))
	var wg sync.WaitGroup
	for i, p := range all {
		wg.Add(1)
		go func(i int, p provider.Provider) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, DetectTimeout)
			defer cancel()
			sts[i] = p.Detect(c)
		}(i, p)
	}
	wg.Wait()
	var ps []provider.Provider
	var skipped []Skipped
	for i, p := range all {
		if sts[i].Available {
			ps = append(ps, p)
		} else {
			skipped = append(skipped, Skipped{ID: p.ID(), Reason: joinReasonFix(sts[i])})
		}
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].ID < skipped[j].ID })
	return ps, skipped, nil
}

// joinReasonFix renders a Status's short reason plus its fix, e.g.
// "not installed: run `shelp setup shortcuts`", for skipped-provider
// messages.
func joinReasonFix(st provider.Status) string {
	switch {
	case st.Reason != "" && st.Fix != "":
		return st.Reason + ": " + st.Fix
	case st.Reason == "":
		return st.Fix
	}
	return st.Reason
}
