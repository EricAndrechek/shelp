package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/style"
	"github.com/ericandrechek/shelp/internal/term"
)

// The manager's `a` form: one question at a time, adding a hosted
// OpenAI-compatible endpoint (preset or custom base URL, name, the NAME of
// the key's environment variable, models, label) or a local server (name,
// base URL, label). It never asks for a key: only where one lives.

type formStep int

const (
	stepKind formStep = iota
	stepPreset
	stepURL
	stepName
	stepKeyEnv
	stepModels
	stepLabel
	stepReview
	stepChain
)

type addForm struct {
	cfg    *config.Config // the manager's working config: names are checked against it
	path   string
	step   formStep
	local  bool
	sel    int    // the highlighted option on a list step
	preset string // "" is a custom base URL (or no choice yet)
	custom bool   // the custom base URL was chosen
	inputs map[formStep]*lineInput
	err    string

	route     provider.CustomRoute
	toChain   bool
	done      bool
	cancelled bool
}

func newAddForm(cfg *config.Config, path string) *addForm {
	f := &addForm{cfg: cfg, path: path, inputs: map[formStep]*lineInput{}}
	for _, s := range []formStep{stepURL, stepName, stepKeyEnv, stepModels, stepLabel} {
		f.inputs[s] = &lineInput{}
	}
	return f
}

func (m *manager) updateForm(k term.Key) {
	f := m.form
	f.update(k)
	switch {
	case f.cancelled:
		m.mode, m.form = modeList, nil
		m.say(false, "nothing added")
	case f.done:
		m.mode, m.form = modeList, nil
		m.addRoute(f.route, f.toChain)
	}
}

// steps are the questions this form asks, given the answers so far.
func (f *addForm) steps() []formStep {
	if f.local {
		return []formStep{stepKind, stepName, stepURL, stepLabel, stepReview, stepChain}
	}
	s := []formStep{stepKind, stepPreset}
	if f.preset == "" {
		s = append(s, stepURL)
	}
	return append(s, stepName, stepKeyEnv, stepModels, stepLabel, stepReview, stepChain)
}

func (f *addForm) val(s formStep) string { return f.inputs[s].value() }

// options for the list steps: a name and a dim description.
func (f *addForm) options() [][2]string {
	switch f.step {
	case stepKind:
		return [][2]string{
			{"hosted endpoint", "an OpenAI-compatible API you have a key for: OpenRouter, Groq, OpenAI, a company gateway… (cloud)"},
			{"local server", "an OpenAI-compatible server on this machine or your network: LM Studio, Ollama, llama-server, fm serve… (local)"},
		}
	case stepPreset:
		var out [][2]string
		for _, n := range provider.PresetNames() {
			b, _, _ := provider.PresetDefaults(n)
			out = append(out, [2]string{n, b})
		}
		return append(out, [2]string{"custom", "any other OpenAI-compatible base URL"})
	}
	return nil
}

func (f *addForm) update(k term.Key) {
	f.err = ""
	switch {
	case k.Code == term.KeyEsc:
		f.cancelled = true
		return
	case k.Code == term.KeyBackTab:
		f.back()
		return
	}
	switch f.step {
	case stepKind, stepPreset:
		opts := f.options()
		switch {
		case k.Code == term.KeyUp || k.Is('k'):
			f.sel = max(0, f.sel-1)
		case k.Code == term.KeyDown || k.Is('j') || k.Code == term.KeyTab:
			f.sel = min(len(opts)-1, f.sel+1)
		case k.Code == term.KeyRune && k.Rune >= '1' && k.Rune <= '9' && int(k.Rune-'1') < len(opts):
			f.sel = int(k.Rune - '1')
			f.next()
		case k.Code == term.KeyEnter:
			f.next()
		}
	case stepReview:
		if k.Code == term.KeyEnter || k.Is('y') {
			f.next()
		}
	case stepChain:
		switch {
		case k.Code == term.KeyEnter || k.Is('y') || k.Is('Y'):
			f.toChain, f.done = true, true
		case k.Is('n') || k.Is('N'):
			f.toChain, f.done = false, true
		}
	default:
		if k.Code == term.KeyEnter {
			f.next()
			return
		}
		f.inputs[f.step].update(k)
	}
}

// next checks the current answer and moves on.
func (f *addForm) next() {
	switch f.step {
	case stepKind:
		f.local = f.sel == 1
	case stepPreset:
		names := provider.PresetNames()
		f.preset, f.custom = "", f.sel >= len(names)
		if !f.custom {
			f.preset = names[f.sel]
		}
	case stepURL:
		if err := provider.ValidateBaseURL(f.val(stepURL)); err != nil {
			f.err = err.Error()
			return
		}
	case stepName:
		name := strings.ToLower(f.val(stepName))
		f.inputs[stepName].set(name)
		if err := provider.CheckRouteName(f.cfg, name, f.local); err != nil {
			f.err = err.Error()
			return
		}
	case stepKeyEnv:
		v := f.val(stepKeyEnv)
		if v == "" && f.preset != "" {
			_, v, _ = provider.PresetDefaults(f.preset)
			f.inputs[stepKeyEnv].set(v)
		}
		if v != "" {
			if err := provider.ValidateKeyEnv(v); err != nil {
				f.err = err.Error()
				return
			}
		}
	case stepModels:
		ms := splitModels(f.val(stepModels))
		if len(ms) == 0 {
			f.err = "at least one model ID is required"
			return
		}
		for _, m := range ms {
			if err := provider.ValidateModel(m); err != nil {
				f.err = err.Error()
				return
			}
		}
	case stepLabel:
		if err := provider.ValidateLabel(f.val(stepLabel)); err != nil {
			f.err = err.Error()
			return
		}
	case stepReview:
		if err := f.build().Validate(f.cfg); err != nil {
			f.err = err.Error()
			return
		}
		f.route = f.build()
	}
	steps := f.steps()
	if i := slices.Index(steps, f.step); i >= 0 && i+1 < len(steps) {
		f.enter(steps[i+1])
	}
}

func (f *addForm) back() {
	steps := f.steps()
	if i := slices.Index(steps, f.step); i > 0 {
		f.enter(steps[i-1])
	}
}

// enter shows step s, filling in a default its input was not edited away
// from: a preset's name and key variable, NAME_API_KEY for a custom URL.
func (f *addForm) enter(s formStep) {
	f.step = s
	switch s {
	case stepKind:
		f.sel = map[bool]int{false: 0, true: 1}[f.local]
	case stepPreset:
		f.sel = max(0, slices.Index(provider.PresetNames(), f.preset))
		if f.custom {
			f.sel = len(provider.PresetNames())
		}
	case stepName:
		if in := f.inputs[stepName]; !in.edited && f.preset != "" && !f.local {
			in.set("")
			for _, n := range []string{f.preset, f.preset + "-2", f.preset + "-3"} {
				if provider.CheckRouteName(f.cfg, n, false) == nil {
					in.set(n)
					break
				}
			}
		}
	case stepKeyEnv:
		if in := f.inputs[stepKeyEnv]; !in.edited {
			if _, k, ok := provider.PresetDefaults(f.preset); ok {
				in.set(k)
			} else {
				in.set(provider.DefaultKeyEnv(f.val(stepName)))
			}
		}
	}
}

func splitModels(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
}

func (f *addForm) build() provider.CustomRoute {
	r := provider.CustomRoute{Local: f.local, Name: strings.ToLower(f.val(stepName)), Label: f.val(stepLabel)}
	if f.local {
		r.BaseURL = f.val(stepURL)
		return r
	}
	r.Preset = f.preset
	if f.preset == "" {
		r.BaseURL = f.val(stepURL)
	}
	r.KeyEnv = f.val(stepKeyEnv)
	r.Models = splitModels(f.val(stepModels))
	return r
}

// ---- view ----

func (f *addForm) view(p style.Palette, width int) []string {
	steps := f.steps()
	n := slices.Index(steps, f.step) + 1
	out := []string{
		p.Bold(p.Accent("shelp providers")) + p.Dim(" › ") + p.Bold("add a route") + p.Dim(fmt.Sprintf("   step %d of %d", n, len(steps))),
		p.Dim("It goes in " + tildePath(f.path) + " when you save (s) in the list. shelp never asks for a key: only the name of the variable that holds it."),
		"",
	}
	for _, l := range f.summary(p) {
		out = append(out, "  "+l)
	}
	if len(out) > 3 {
		out = append(out, "")
	}
	q, hint := f.question()
	out = append(out, p.Bold(q))
	switch f.step {
	case stepKind, stepPreset:
		w := 0
		for _, o := range f.options() {
			w = max(w, style.Width(o[0]))
		}
		for i, o := range f.options() {
			mark := "    "
			name := style.PadRight(o[0], w)
			if i == f.sel {
				mark, name = "  "+p.Accent("❯")+" ", p.Bold(name)
			}
			out = append(out, mark+name+"  "+p.Dim(o[1]))
		}
	case stepReview:
		r := f.build()
		for _, l := range strings.Split(strings.TrimSuffix(r.Block(), "\n"), "\n") {
			out = append(out, "    "+p.Dim(l))
		}
		out = append(out, "", "  adds "+strings.Join(r.IDs(), ", ")+"  "+whereBadge(p, routeKind(r)))
	case stepChain:
	default:
		in := f.inputs[f.step]
		mask := f.step == stepKeyEnv && provider.LooksLikeSecret(in.value())
		out = append(out, "  "+p.Accent("›")+" "+in.render(p, f.placeholder(), mask))
	}
	out = append(out, "")
	switch {
	case f.err != "":
		out = append(out, p.Err(f.err))
	case f.step == stepKeyEnv:
		out = append(out, f.keyStatus(p)...)
	case hint != "":
		out = append(out, p.Dim(hint))
	}
	keys := []keyHint{{"enter for next", keepHigh}, {"shift+tab to go back", keepMid}, {"esc to cancel", keepAlways}}
	switch f.step {
	case stepKind, stepPreset:
		keys = append([]keyHint{{"↑↓ to choose", keepHigh}}, keys...)
	case stepChain:
		keys = []keyHint{{"y for yes", keepAlways}, {"n for no", keepAlways}, {"esc to cancel", keepAlways}}
	case stepReview:
		keys[0].text = "enter to add it"
	}
	return append(out, "", p.Dim(fitHints(keys, width)))
}

func routeKind(r provider.CustomRoute) provider.Kind {
	if r.Local {
		return provider.LocalServer
	}
	return provider.API
}

// summary lists the answers given so far.
func (f *addForm) summary(p style.Palette) []string {
	var out []string
	row := func(k, v string) { out = append(out, p.Dim(fmt.Sprintf("%-8s", k))+" "+v) }
	steps := f.steps()
	cur := slices.Index(steps, f.step)
	for _, s := range steps[:max(0, cur)] {
		switch s {
		case stepKind:
			row("kind", map[bool]string{false: "hosted endpoint", true: "local server"}[f.local])
		case stepPreset:
			if f.preset == "" {
				row("preset", "custom base URL")
			} else {
				b, _, _ := provider.PresetDefaults(f.preset)
				row("preset", f.preset+" "+p.Dim("("+b+")"))
			}
		case stepURL:
			row("url", f.val(stepURL))
		case stepName:
			row("name", f.val(stepName))
		case stepKeyEnv:
			k := f.val(stepKeyEnv)
			if k == "" {
				row("key", p.Dim("none"))
			} else {
				row("key", "$"+k)
			}
		case stepModels:
			row("models", strings.Join(splitModels(f.val(stepModels)), ", "))
		case stepLabel:
			if l := f.val(stepLabel); l != "" {
				row("label", l)
			}
		}
	}
	return out
}

func (f *addForm) question() (q, hint string) {
	switch f.step {
	case stepKind:
		return "What kind of route?", ""
	case stepPreset:
		return "Which service?", "a preset fills in the base URL and the usual key variable"
	case stepURL:
		if f.local {
			return "Base URL of the server", "the part before /chat/completions; shelp asks its /models for what it serves"
		}
		return "Base URL of the API", "the part before /chat/completions"
	case stepName:
		return "Name", "its provider IDs start with it: name:model (lowercase letters, digits, . _ -)"
	case stepKeyEnv:
		return "Environment variable that holds the API key (its name, not the key)", ""
	case stepModels:
		return "Model IDs, as the API names them (comma-separated)", "each one becomes a provider, " + f.val(stepName) + ":<model>"
	case stepLabel:
		return "Label (optional)", "how it reads in lists, e.g. \"Haiku via OpenRouter\"; enter skips it"
	case stepReview:
		return "Add this route?", "it goes in your config file when you save in the list (s)"
	case stepChain:
		return fmt.Sprintf("Put %s at the end of your chain? y/n", strings.Join(f.build().IDs(), ", ")), ""
	}
	return "", ""
}

func (f *addForm) placeholder() string {
	switch f.step {
	case stepURL:
		if f.local {
			return "http://127.0.0.1:11535/v1"
		}
		return "https://llm.example.com/v1"
	case stepName:
		if f.local {
			return "e.g. fm-serve"
		}
		return "e.g. work-llm"
	case stepKeyEnv:
		if f.preset == "" {
			return "empty: no key is sent"
		}
	case stepModels:
		switch f.preset {
		case "openrouter":
			return "e.g. anthropic/claude-haiku-4.5"
		case "openai":
			return "e.g. gpt-4o-mini"
		}
		return "the model ID from the service's docs"
	case stepLabel:
		return "optional"
	}
	return ""
}

// keyStatus says whether the key variable named so far is set in this
// shell (never its value), and where to set it if not.
func (f *addForm) keyStatus(p style.Palette) []string {
	v := f.val(stepKeyEnv)
	if v == "" {
		if _, k, ok := provider.PresetDefaults(f.preset); ok {
			return []string{p.Dim("enter uses " + f.preset + "'s usual $" + k)}
		}
		return []string{p.Dim("empty: requests are sent without a key")}
	}
	if err := provider.ValidateKeyEnv(v); err != nil {
		return []string{p.Err(err.Error())}
	}
	return keyEnvLines(p, v)
}

// ---- a one-line text input ----

type lineInput struct {
	r      []rune
	pos    int
	edited bool
}

func (l *lineInput) set(s string) { l.r, l.pos = []rune(s), len([]rune(s)) }

func (l *lineInput) value() string { return strings.TrimSpace(string(l.r)) }

// update edits the text for k; it reports whether k was an editing key.
func (l *lineInput) update(k term.Key) bool {
	switch {
	case k.Code == term.KeyRune && !k.Alt:
		l.r = slices.Insert(l.r, l.pos, k.Rune)
		l.pos++
	case k.Code == term.KeyBackspace:
		if l.pos > 0 {
			l.r = slices.Delete(l.r, l.pos-1, l.pos)
			l.pos--
		}
	case k.Code == term.KeyDelete || k.IsCtrl('d'):
		if l.pos < len(l.r) {
			l.r = slices.Delete(l.r, l.pos, l.pos+1)
		}
	case k.Code == term.KeyLeft || k.IsCtrl('b'):
		l.pos = max(0, l.pos-1)
		return true
	case k.Code == term.KeyRight || k.IsCtrl('f'):
		l.pos = min(len(l.r), l.pos+1)
		return true
	case k.Code == term.KeyHome || k.IsCtrl('a'):
		l.pos = 0
		return true
	case k.Code == term.KeyEnd || k.IsCtrl('e'):
		l.pos = len(l.r)
		return true
	case k.IsCtrl('u'):
		l.r, l.pos = slices.Clone(l.r[l.pos:]), 0
	case k.IsCtrl('k'):
		l.r = l.r[:l.pos]
	case k.IsCtrl('w'):
		i := l.pos
		for i > 0 && l.r[i-1] == ' ' {
			i--
		}
		for i > 0 && l.r[i-1] != ' ' {
			i--
		}
		l.r, l.pos = slices.Delete(l.r, i, l.pos), i
	default:
		return false
	}
	l.edited = true
	return true
}

// render draws the text with a cursor: reverse video on a color terminal,
// a thin bar otherwise. mask hides the text (a pasted key).
func (l *lineInput) render(p style.Palette, placeholder string, mask bool) string {
	text := l.r
	if mask {
		text = []rune(strings.Repeat("•", len(l.r)))
	}
	before, at, after := string(text[:l.pos]), " ", ""
	if l.pos < len(text) {
		at, after = string(text[l.pos]), string(text[l.pos+1:])
	}
	cursor := "▏"
	if p.On() {
		cursor = "\x1b[7m" + at + "\x1b[27m"
	} else if at != " " {
		after = at + after
	}
	if len(text) == 0 && placeholder != "" {
		return cursor + p.Dim(placeholder)
	}
	return before + cursor + after
}
