package main

import (
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/style"
	"github.com/ericandrechek/shelp/internal/term"
)

// The detail page: → or enter on any row of the list. It says what the
// provider is, where it runs, whether it works here and why not, its place
// in the chain, its route (for an OpenAI-compatible one) and how to make it
// work -- on the manager's own screen, so ← goes straight back. ↑↓ step to
// the previous and next provider without going back.

func (m *manager) updateDetail(k term.Key) {
	page := max(1, m.height-8)
	switch {
	case k.Code == term.KeyEsc && m.help: // Esc closes the key grid first
		m.help = false
	case k.Code == term.KeyLeft || k.Code == term.KeyEsc || k.Is('h') || k.Is('q') || k.Code == term.KeyBackspace:
		m.mode = modeList
	case k.Code == term.KeyUp || k.Is('k') || k.IsCtrl('p'):
		m.moveTo(m.idx - 1)
		m.dtop = 0
	case k.Code == term.KeyDown || k.Is('j') || k.IsCtrl('n') || k.Code == term.KeyTab:
		m.moveTo(m.idx + 1)
		m.dtop = 0
	case k.Code == term.KeyPgUp:
		m.dtop = max(0, m.dtop-page)
	case k.Code == term.KeyPgDn:
		m.dtop += page
	case k.Is(' '):
		id := m.cur
		m.toggle()
		m.cur = id // stay on this provider (or go back if it is gone)
		m.sync()
	case k.Code == term.KeyEnter:
		if r := m.rows[m.cur]; r != nil && canSetupShortcut(r) {
			m.effects = append(m.effects, mgrEffect{kind: "setup", ids: []string{r.ID}})
			return
		}
		m.say(false, "nothing to run for %s here; ← goes back to the list", m.cur)
	case k.Is('r'):
		if m.rows[m.cur] == nil {
			m.say(false, "nothing to check: shelp has no provider %s here", m.cur)
			return
		}
		m.recheck([]string{m.cur})
	case k.Is('x') || k.Code == term.KeyDelete:
		m.askRemove()
	case k.Is('s'):
		m.save()
	case k.Is('?'):
		m.help = !m.help
	}
}

// canSetupShortcut reports whether r is fixed by installing (or updating)
// shelp's Apple Shortcut, which the detail page offers to run.
func canSetupShortcut(r *mgrRow) bool {
	return r.probed && provider.Kind(r.Kind) == provider.AppleCloud && strings.Contains(r.Fix, "shelp setup shortcuts")
}

// setupDone records the Apple Shortcut setup's end (err nil: it finished)
// and returns the rows to check again: every Apple Shortcut one.
func (m *manager) setupDone(err error) []string {
	var again []string
	for _, id := range m.order {
		if r := m.rows[id]; r.Kind == string(provider.AppleCloud) {
			r.probed = false
			again = append(again, id)
		}
	}
	m.sync()
	if err != nil {
		m.say(true, "Apple Shortcut setup: %v", err)
	} else {
		m.say(false, "Apple Shortcut setup finished; checking again…")
	}
	return again
}

// ---- view ----

// detailView is the whole detail page; height > 0 crops its body, which
// pgup/pgdn scroll.
func (m *manager) detailView(width, height int) []string {
	head := m.detailHead()
	body := m.detailBody(width)
	foot := m.footLines(width)
	if avail := height - len(head) - len(foot); height > 0 && len(body) > avail && avail >= 3 {
		m.dtop = max(0, min(m.dtop, len(body)-avail))
		out := slices.Clone(body[m.dtop : m.dtop+avail])
		if m.dtop > 0 {
			out[0] = m.pal.Dim(fmt.Sprintf("   ↑ %d more (pgup)", m.dtop+1))
		}
		if rest := len(body) - (m.dtop + avail); rest > 0 {
			out[len(out)-1] = m.pal.Dim(fmt.Sprintf("   ↓ %d more (pgdn)", rest+1))
		}
		body = out
	} else {
		m.dtop = 0
	}
	return append(append(head, body...), foot...)
}

func (m *manager) detailHead() []string {
	p := m.pal
	items := m.items()
	title := p.Bold(p.Accent("shelp providers")) + p.Dim(" › ") + p.Bold(m.cur) +
		p.Dim(fmt.Sprintf("   %d of %d", m.idx+1, len(items)))
	if m.dirty() {
		title += "  " + p.Warn("● unsaved changes")
	}
	return []string{title, ""}
}

// detailRow is one "name  value" line of the page.
func detailRow(p style.Palette, name, value string) string {
	return "  " + p.Dim(style.PadRight(name, 9)) + " " + value
}

// detailRowIndent lines a wrapped detailRow value up under its first line.
var detailRowIndent = strings.Repeat(" ", 12)

// detailBody is the page below the title. Long lines wrap at width (a
// detail row's value under itself, other lines under their own indent).
func (m *manager) detailBody(width int) []string {
	p := m.pal
	r := m.rows[m.cur]
	var out []string
	add := func(ls ...string) {
		for _, l := range ls {
			plain := style.Strip(l)
			out = append(out, style.Wrap(l, width-1, plain[:len(plain)-len(strings.TrimLeft(plain, " "))])...)
		}
	}
	row := func(name, value string) {
		out = append(out, style.Wrap(detailRow(p, name, value), width-1, detailRowIndent)...)
	}
	if r == nil {
		add(p.Bold(m.cur), "")
		row("status", p.Err("? unknown here: not installed or not configured"))
		row("chain", m.chainPlace(m.cur))
		add("", p.Bold("To make it work"),
			"  shelp has no provider "+m.cur+" here: its program is not installed, or its route is not in your config. "+
				"Install or add it (a in the list adds a route), or take it out of the chain (space).")
		return out
	}
	add(p.Bold(rowLabel(r.providerRow)))
	// "cloud · " / "local · ": the sentence wraps under itself.
	out = append(out, style.Wrap(whereBadge(p, provider.Kind(r.Kind))+p.Dim(" · "+whereSentence(r)), width-1, "        ")...)
	add("")
	row("status", m.statusText(r))
	if r.Version != "" && r.Version != rowLabel(r.providerRow) { // what its check found: a version, a model
		row("detected", r.Version)
	}
	row("chain", m.chainPlace(r.ID))
	if r.Note != "" {
		row("note", r.Note)
	}
	if c := r.class(); c == clsSetup || c == clsUnavailable {
		add("", p.Bold("To make it work"))
		add(fixLines(p, r)...)
		if canSetupShortcut(r) {
			add("", "  "+p.Bold(p.Accent("enter"))+" to set up the Apple Shortcut "+
				p.Dim("(opens the Shortcuts app, where you click Add Shortcut, then comes back here)"))
		}
	} else if r.Detail != "" {
		row("detail", p.Dim(r.Detail))
	}
	if r.hasRoute {
		add("")
		for _, l := range m.routeLines(r) {
			if strings.HasPrefix(l, "  ") {
				out = append(out, style.Wrap(l, width-1, detailRowIndent)...)
			} else {
				add(l)
			}
		}
	}
	return out
}

// statusText is the status line: works, rate-limited, or why not.
func (m *manager) statusText(r *mgrRow) string {
	p := m.pal
	switch r.class() {
	case clsChecking:
		return p.Dim("… checking…")
	case clsOK:
		if r.Status == statusCooldown {
			return p.Warn("◷ rate-limited "+r.Reason) + p.Dim("; shelp skips it until then (`shelp providers --reset-cooldowns` clears that)")
		}
		s := p.OK("✓ works here")
		if r.Reason != "" {
			s += p.Dim(" · ") + p.Warn(r.Reason)
		}
		return s
	case clsSetup:
		return p.Warn("! " + r.Reason)
	}
	reason := r.Reason
	if reason == "" {
		reason = "unavailable"
	}
	return p.Dim("· " + reason)
}

// chainPlace says where id is in the working chain.
func (m *manager) chainPlace(id string) string {
	i := slices.Index(m.chain, id)
	if i < 0 {
		return m.pal.Dim("not in your chain (space adds it at the end)")
	}
	s := fmt.Sprintf("#%d of %d", i+1, len(m.chain))
	switch {
	case i == 0:
		s += ": tried first"
	case len(m.chain) > 1 && i == len(m.chain)-1:
		s += ": tried last"
	}
	if m.rows[id] == nil {
		s += m.pal.Dim(", but shelp does not know it here (space takes it out)")
	}
	return s
}

// whereSentence says, in one plain sentence, where r runs and what that
// means for the request.
func whereSentence(r *mgrRow) string {
	host := ""
	if r.hasRoute {
		host = hostOfURL(r.route.BaseURL)
	}
	switch provider.Kind(r.Kind) {
	case provider.OnDevice:
		return "runs on this Mac with Apple's on-device model: works offline, and nothing leaves the machine"
	case provider.LocalServer:
		if host == "" {
			return "an OpenAI-compatible server on this machine or your network: no internet needed"
		}
		if isLoopback(host) {
			return "an OpenAI-compatible server on this machine (" + host + "): works offline, and nothing leaves the machine"
		}
		return "an OpenAI-compatible server at " + host + ": no internet needed, but your request goes to that machine"
	case provider.AppleCloud:
		s := "runs through shelp's Apple Shortcut on Apple's Private Cloud Compute servers, not on this Mac: needs the internet"
		if strings.Contains(r.ID, "chatgpt") {
			s = "runs through shelp's Apple Shortcut, which hands your request to OpenAI's ChatGPT: needs the internet"
		}
		if strings.HasSuffix(r.ID, "-web") {
			s += ", and it may look things up online"
		}
		return s
	case provider.API:
		s := "a hosted API"
		if host != "" {
			s += " at " + host
		}
		s += ": your request goes over the internet"
		if r.hasRoute && r.route.KeyEnv != "" {
			s += ", with the key from $" + r.route.KeyEnv
		}
		return s
	case provider.AgentCLI:
		if fam, _, _ := strings.Cut(r.ID, ":"); fam == "claude" {
			return "the claude CLI, run headless with no tools: your request goes to Anthropic over the internet and counts against your Claude plan"
		}
		return "an AI CLI, run headless with no tools: your request goes over the internet"
	}
	if provider.Kind(r.Kind).NeedsNetwork() {
		return "needs the internet"
	}
	return "runs on this machine"
}

func isLoopback(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// routeLines describe an OpenAI-compatible provider's route: which entry
// it comes from, base URL, the key's variable (set or not, never its
// value), models and label.
func (m *manager) routeLines(r *mgrRow) []string {
	p := m.pal
	rt := r.route
	table := "[[openai.endpoint]]"
	if rt.Local {
		table = "[[openai.local]]"
	}
	var head string
	switch {
	case r.isNew:
		head = "a new " + table + " entry, not saved yet (s saves)"
	case rt.Configured && rt.Builtin:
		head = "your " + table + " entry, replacing the built-in " + rt.Name + " at " + hostOfURL(provider.BuiltinLocalURL(rt.Name))
	case rt.Configured:
		head = "your " + table + " entry in " + tildePath(m.path)
	case rt.Builtin:
		head = "built in: shelp looks for " + rt.Name + " here without any config"
	default:
		head = table
	}
	out := []string{p.Bold("Route") + p.Dim(" · "+head)}
	out = append(out, detailRow(p, "name", rt.Name))
	if rt.Preset != "" {
		out = append(out, detailRow(p, "preset", rt.Preset))
	}
	out = append(out, detailRow(p, "base URL", rt.BaseURL))
	if !rt.Local {
		key := p.Dim("none: requests are sent without one")
		if rt.KeyEnv != "" {
			key = "$" + rt.KeyEnv + " · "
			if v, ok := envLookup(rt.KeyEnv); ok && v != "" {
				key += p.OK("set in this shell")
			} else {
				key += p.Warn("not set in this shell")
			}
		}
		out = append(out, detailRow(p, "key", key))
	}
	if ms := m.routeModels(r); len(ms) > 0 {
		out = append(out, detailRow(p, "models", strings.Join(ms, ", ")))
	} else if rt.Local {
		out = append(out, detailRow(p, "models", p.Dim("the server's first loaded model (none seen yet)")))
	}
	label := p.Dim("none")
	if cr, ok := m.configuredRoute(rt); ok && cr.Label != "" {
		label = cr.Label
	}
	return append(out, detailRow(p, "label", label))
}

// configuredRoute is rt's entry in the working config, if it has one.
func (m *manager) configuredRoute(rt provider.Route) (provider.CustomRoute, bool) {
	for _, cr := range provider.ConfiguredRoutes(m.cfg) {
		if cr.Name == rt.Name && cr.Local == rt.Local {
			return cr, true
		}
	}
	return provider.CustomRoute{}, false
}

// routeModels lists the models of r's route, r's own marked "(this one)":
// a hosted endpoint's from its entry, a local server's as last seen.
func (m *manager) routeModels(r *mgrRow) []string {
	var ms []string
	if cr, ok := m.configuredRoute(r.route); ok && !cr.Local {
		ms = cr.Models
	} else {
		for _, id := range m.order {
			if o := m.rows[id]; o.hasRoute && o.route.Name == r.route.Name && o.route.Local == r.route.Local && o.route.Model != "" {
				ms = append(ms, o.route.Model)
			}
		}
	}
	out := make([]string, len(ms))
	for i, name := range ms {
		out[i] = name
		if strings.EqualFold(name, r.route.Model) {
			out[i] += m.pal.Dim(" (this one)")
		}
	}
	return out
}

// ---- keys ----

// detailHints are the footer's phrases on the detail page.
func (m *manager) detailHints() []keyHint {
	r := m.rows[m.cur]
	hs := []keyHint{{"← to go back", keepAlways}}
	if r != nil && canSetupShortcut(r) {
		hs = append(hs, keyHint{"enter to set up the Apple Shortcut", keepHigh})
	}
	if slices.Contains(m.chain, m.cur) {
		hs = append(hs, keyHint{"space to remove", keepMid})
	} else {
		hs = append(hs, keyHint{"space to add", keepMid})
	}
	if r != nil && r.hasRoute && (r.route.Configured || r.isNew) {
		hs = append(hs, keyHint{"x to remove", keepLow})
	}
	if r != nil {
		hs = append(hs, keyHint{"r to check again", keepLow})
	}
	if m.dirty() {
		hs = append(hs, keyHint{"s to save", keepMid})
	}
	return append(hs, keyHint{"? for shortcuts", keepAlways})
}

// detailKeyCells are the detail page's keys for the ? grid.
func detailKeyCells() []string {
	return []string{
		"← to go back", "↑↓ for other providers", "space to add or remove", "r to check again",
		"enter to run its setup", "x to remove a route", "s to save", "pgup/pgdn to scroll",
		"ctrl+c to quit now", "? to close",
	}
}

var detailAlternates = []string{"h, esc or q to go back", "k/j for other providers"}
