package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/style"
)

// providerRow is one provider as `shelp providers` reports it, both in the
// grouped view and in --json (field names are the JSON keys; the first six
// predate the redesign and stay as they were). Reason is short; Fix says
// how to make an unavailable provider work; Detail is the raw underlying
// error, shown by --all.
type providerRow struct {
	ID, Kind, Description, Status, Reason, Version string
	Available                                      bool
	Fix, Detail                                    string
	Label                                          string
}

// Row status values.
const (
	statusOK          = "ok"
	statusCooldown    = "cooldown"
	statusUnavailable = "unavailable"
)

// buildProviderRows turns probe results into rows. sts[i] is ps[i]'s
// Detect result (zero for a cooled-down provider, which is never probed).
func buildProviderRows(ps []provider.Provider, sts []provider.Status, cds map[string]cooldown.Entry) []providerRow {
	rows := make([]providerRow, len(ps))
	for i, p := range ps {
		r := providerRow{
			ID: p.ID(), Kind: string(p.Kind()), Description: p.Description(),
			Label: provider.LabelOf(p),
		}
		if e, ok := cooldown.Active(cds, p.ID()); ok {
			r.Status = statusCooldown
			r.Reason = fmt.Sprintf("until %s (%s)", e.Until.Local().Format("15:04"), e.Reason)
			rows[i] = r
			continue
		}
		st := sts[i]
		r.Version, r.Available = st.Version, st.Available
		r.Reason, r.Fix, r.Detail = st.Reason, st.Fix, st.Detail
		r.Status = statusUnavailable
		if st.Available {
			r.Status = statusOK
		}
		rows[i] = r
	}
	return rows
}

// chainSource says where chainFor("", cfg) got the chain: "$SHELP_PROVIDER",
// "config" or "default".
func chainSource(cfg *config.Config) string {
	switch {
	case os.Getenv("SHELP_PROVIDER") != "":
		return "$SHELP_PROVIDER"
	case len(cfg.Providers) > 0:
		return "config"
	}
	return "default"
}

// kindHeading names a group of providers.
func kindHeading(k provider.Kind, rows []providerRow) string {
	switch k {
	case provider.AppleCloud:
		return "Apple Shortcuts"
	case provider.OnDevice:
		return "On-device"
	case provider.API:
		return "API endpoints"
	case provider.LocalServer:
		return "Local servers"
	case provider.AgentCLI:
		for _, r := range rows {
			if !strings.HasPrefix(r.ID, "claude:") {
				return "AI CLIs"
			}
		}
		return "Claude Code"
	}
	return string(k)
}

func statusRank(s string) int {
	switch s {
	case statusOK:
		return 0
	case statusCooldown:
		return 1
	}
	return 2
}

// renderProviders draws the grouped view: the chain line, then one group
// per kind (see kindHeading), available providers first. all adds each
// unavailable row's raw Detail on a dim line below it. width > 0 truncates
// every line to width-1 columns. With a plain palette the output has no
// color codes and the same layout.
func renderProviders(p style.Palette, rows []providerRow, chain []string, source string, all bool, width int, offline bool) string {
	var out strings.Builder
	line := func(s string) {
		if width > 0 {
			s = style.Truncate(s, width-1)
		}
		out.WriteString(s)
		out.WriteByte('\n')
	}

	byID := map[string]string{}
	idw := 0
	for _, r := range rows {
		byID[r.ID] = r.Status
		if w := style.Width(r.ID); w > idw {
			idw = w
		}
	}

	if len(chain) > 0 {
		ids := make([]string, len(chain))
		for i, id := range chain {
			switch byID[id] {
			case statusOK:
				ids[i] = p.Bold(id)
			case statusCooldown:
				ids[i] = p.Warn(id)
			case statusUnavailable:
				ids[i] = p.Dim(id)
			default:
				ids[i] = p.Err(id)
			}
		}
		line(p.Dim("chain") + "  " + strings.Join(ids, p.Dim(" → ")) + "   " + p.Dim("("+source+")") + offlineNote(p, offline))
	}

	// Group by kind, kinds in display order.
	var kinds []provider.Kind
	groups := map[provider.Kind][]providerRow{}
	for _, r := range rows {
		k := provider.Kind(r.Kind)
		if _, ok := groups[k]; !ok {
			kinds = append(kinds, k)
		}
		groups[k] = append(groups[k], r)
	}
	sort.SliceStable(kinds, func(i, j int) bool { return provider.KindRank(kinds[i]) < provider.KindRank(kinds[j]) })

	for _, k := range kinds {
		g := groups[k]
		sort.SliceStable(g, func(i, j int) bool { return statusRank(g[i].Status) < statusRank(g[j].Status) })
		if out.Len() > 0 {
			line("")
		}
		line(p.Bold(p.Accent(kindHeading(k, g))))
		var fixes []string
		seen := map[string]bool{}
		addFix := func(f string) {
			if f != "" && !seen[f] {
				seen[f] = true
				fixes = append(fixes, f)
			}
		}
		for _, r := range g {
			id := style.PadRight(r.ID, idw)
			switch r.Status {
			case statusOK:
				text := r.Label
				if text == "" {
					text = r.Description
				}
				if r.Reason != "" {
					text += " · " + p.Warn(r.Reason)
					addFix(r.Fix)
				}
				line("  " + p.OK("✓") + " " + id + "  " + text)
			case statusCooldown:
				line("  " + p.Warn("◷") + " " + id + "  " + p.Warn("cooldown "+r.Reason))
			default:
				reason := r.Reason
				if reason == "" {
					reason = "unavailable"
				}
				line(p.Dim("  · " + id + "  " + reason))
				addFix(r.Fix)
				if all && r.Detail != "" {
					line(p.Dim("      " + r.Detail))
				}
			}
		}
		for _, f := range fixes {
			line("    " + p.Dim("→") + " " + f)
		}
	}
	return out.String()
}

// offlineNote flags the chain line when the machine has no network route.
func offlineNote(p style.Palette, offline bool) string {
	if !offline {
		return ""
	}
	return "  " + p.Warn("offline")
}
