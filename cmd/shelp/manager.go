package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/style"
	"github.com/ericandrechek/shelp/internal/term"
)

// The provider manager: `shelp providers -i` and `shelp config chain` on a
// terminal. This file is its terminal-free half -- state, Update(key) and
// View(width, height) -- so tests drive it with key sequences; the runner
// (manager_run.go) owns the terminal, probes providers in the background
// and carries out the effects Update asks for (probe, setup, save).
//
// Two pages: the list, and a provider's details (→ or enter on a row;
// manager_detail.go), which says where it runs, its status, its route and
// how to make it work. Both stay on the one screen; only running the Apple
// Shortcut's setup leaves it, and comes back to the details.
//
// Nothing is written until `s`: the chain, routes added with `a`, edited
// with `e` and removed with `x` are pending until then, and the provider
// list already shows them (it is built from the file's text with the
// pending route edits applied).

// mgrRow is one provider as the manager shows it.
type mgrRow struct {
	providerRow
	probed   bool // false: still checking
	route    provider.Route
	hasRoute bool
	isNew    bool // from a route added in this session, not saved yet
}

// Row classes, in the order the "other providers" list groups them.
const (
	clsOK = iota // available (or in a rate-limit cooldown)
	clsSetup
	clsUnavailable
	clsChecking
)

// classOr is r's class, clsUnavailable for nil (an ID shelp does not know).
func (r *mgrRow) classOr() int {
	if r == nil {
		return clsUnavailable
	}
	return r.class()
}

func (r *mgrRow) class() int {
	switch {
	case !r.probed:
		return clsChecking
	case r.Status == statusOK || r.Status == statusCooldown:
		return clsOK
	case r.Fix != "":
		return clsSetup
	}
	return clsUnavailable
}

type mgrMode int

const (
	modeList mgrMode = iota
	modeDetail
	modeConfirm
	modeForm
)

// mgrEffect is work Update hands the runner.
type mgrEffect struct {
	kind  string   // "probe" (re-list, then Detect ids), "setup" (the Apple Shortcut, for ids[0]), "save"
	ids   []string // probe/setup
	fresh bool     // probe: forget memoized checks first (r)
}

type manager struct {
	pal     style.Palette
	path    string // the config file, for the header
	managed string // why the file is not written directly ("" if it is)
	list    func(*config.Config) []provider.Provider

	src   string         // the file's text as loaded or last saved
	cfg   *config.Config // src with the pending route edits applied
	provs map[string]provider.Provider
	rows  map[string]*mgrRow
	order []string // provider IDs in list order

	chain    []string // the working chain
	saved    []string // the chain the file has (or the default)
	chainSet bool     // the file sets providers
	adds     []provider.CustomRoute
	removes  []string
	edits    []routeEdit

	cur  string // provider ID under the cursor
	idx  int    // its index in items()
	top  int    // first body line shown, when the body is cropped
	dtop int    // the same for the detail page
	help bool

	mode      mgrMode
	back      mgrMode // the page a question or the form goes back to
	question  string
	onYes     func()
	saveFirst bool // the question is "discard changes?": s saves, then quits
	form      *addForm

	msg     string
	msgWarn bool

	offline bool
	env     string // $SHELP_PROVIDER

	effects       []mgrEffect
	quitAfterSave bool
	quit          bool
	code          int // exit code once quit: 0, or 130 for Ctrl-C
	height        int // the last View height, for paging
	lastSaved     string
}

// newManager builds the manager for the config file text src. Its first
// effect probes every provider.
func newManager(pal style.Palette, path, src string, list func(*config.Config) []provider.Provider) (*manager, error) {
	cfg, err := config.Parse(src)
	if err != nil {
		return nil, err
	}
	m := &manager{pal: pal, path: path, src: src, cfg: cfg, list: list, rows: map[string]*mgrRow{}}
	m.chainSet = len(cfg.Providers) > 0
	for _, id := range cfg.Providers { // a hand-written chain may repeat one
		if !slices.Contains(m.chain, id) {
			m.chain = append(m.chain, id)
		}
	}
	if !m.chainSet {
		m.chain = slices.Clone(provider.DefaultChain)
	}
	m.saved = slices.Clone(m.chain)
	if ids := m.rebuild(); len(ids) > 0 {
		m.effects = append(m.effects, mgrEffect{kind: "probe", ids: ids})
	}
	m.moveTo(0)
	return m, nil
}

// takeEffects returns and clears the pending effects.
func (m *manager) takeEffects() []mgrEffect {
	e := m.effects
	m.effects = nil
	return e
}

// rebuild lists the providers of the working config. A row whose backend
// is unchanged (same Description) keeps its status; it returns the IDs
// that need a probe.
func (m *manager) rebuild() []string {
	added := map[string]bool{}
	for _, r := range m.adds {
		added[r.Name] = true
	}
	ps := m.list(m.cfg)
	rows := map[string]*mgrRow{}
	provs := map[string]provider.Provider{}
	var order, probe []string
	for _, p := range ps {
		id := p.ID()
		if rows[id] != nil {
			continue
		}
		r := &mgrRow{providerRow: providerRow{ID: id, Kind: string(p.Kind()), Description: p.Description(),
			Label: provider.LabelOf(p), Note: provider.NoteOf(p)}}
		r.route, r.hasRoute = provider.RouteOf(p)
		r.isNew = r.hasRoute && added[r.route.Name]
		if old := m.rows[id]; old != nil && old.Description == r.Description {
			r.probed = old.probed
			r.Status, r.Reason, r.Fix, r.Detail = old.Status, old.Reason, old.Fix, old.Detail
			r.Version, r.Available, r.Group = old.Version, old.Available, old.Group
		}
		if !r.probed {
			probe = append(probe, id)
		}
		rows[id], provs[id] = r, p
		order = append(order, id)
	}
	m.rows, m.provs, m.order = rows, provs, order
	return probe
}

// setProbe records a probe's result, unless the row changed meanwhile.
func (m *manager) setProbe(pr providerRow) {
	r := m.rows[pr.ID]
	if r == nil || r.Description != pr.Description {
		return
	}
	r.probed = true
	r.Status, r.Reason, r.Fix, r.Detail = pr.Status, pr.Reason, pr.Fix, pr.Detail
	r.Version, r.Available, r.Group = pr.Version, pr.Available, pr.Group
	m.sync()
}

// items are the selectable rows in display order: the chain as it will be
// tried, then every other provider grouped by class, then by kind (what
// runs on this machine first), then in list order.
func (m *manager) items() []string {
	out := slices.Clone(m.chain)
	in := map[string]bool{}
	for _, id := range m.chain {
		in[id] = true
	}
	var others []string
	for _, id := range m.order {
		if !in[id] {
			others = append(others, id)
		}
	}
	sort.SliceStable(others, func(i, j int) bool {
		a, b := m.rows[others[i]], m.rows[others[j]]
		if ca, cb := a.class(), b.class(); ca != cb {
			return ca < cb
		}
		return managerKindRank(provider.Kind(a.Kind)) < managerKindRank(provider.Kind(b.Kind))
	})
	return append(out, others...)
}

// sync puts the cursor back on m.cur after the list changed, or keeps its
// position when m.cur is gone (and leaves its detail page for the list).
func (m *manager) sync() {
	items := m.items()
	if i := slices.Index(items, m.cur); i >= 0 {
		m.idx = i
		return
	}
	m.moveTo(m.idx)
	if m.mode == modeDetail {
		m.mode = modeList
	}
	if (m.mode == modeConfirm || m.mode == modeForm) && m.back == modeDetail {
		m.back = modeList
	}
}

func (m *manager) moveTo(i int) {
	items := m.items()
	if len(items) == 0 {
		m.idx, m.cur = 0, ""
		return
	}
	m.idx = max(0, min(i, len(items)-1))
	m.cur = items[m.idx]
}

func (m *manager) dirty() bool { return !m.ops().empty() }

// ops is what `s` writes.
func (m *manager) ops() configOps {
	o := configOps{adds: slices.Clone(m.adds), removes: slices.Clone(m.removes), edits: slices.Clone(m.edits)}
	if !slices.Equal(m.chain, m.saved) {
		o.chain = append([]string{}, m.chain...)
	}
	return o
}

func (m *manager) say(warn bool, format string, args ...any) {
	m.msg, m.msgWarn = fmt.Sprintf(format, args...), warn
}

func (m *manager) ask(q string, yes func()) {
	m.back = m.page()
	m.mode, m.question, m.onYes, m.saveFirst = modeConfirm, q, yes, false
}

// page is the page on screen: the list or the details (under a question
// or the form, the one it goes back to).
func (m *manager) page() mgrMode {
	if m.mode == modeConfirm || m.mode == modeForm {
		return m.back
	}
	return m.mode
}

// openDetail shows the details of the provider under the cursor.
func (m *manager) openDetail() {
	if m.cur == "" {
		return
	}
	m.mode, m.dtop = modeDetail, 0
}

// save asks the runner to write the pending changes.
func (m *manager) save() {
	if !m.dirty() {
		m.say(false, "nothing to save")
		return
	}
	m.effects = append(m.effects, mgrEffect{kind: "save"})
}

// recheck probes ids again, forgetting memoized checks.
func (m *manager) recheck(ids []string) {
	for _, id := range ids {
		if r := m.rows[id]; r != nil {
			r.probed = false
		}
	}
	m.effects = append(m.effects, mgrEffect{kind: "probe", ids: slices.Clone(ids), fresh: true})
	m.sync()
}

// Update handles one key.
func (m *manager) Update(k term.Key) {
	m.msg, m.msgWarn = "", false
	if k.IsCtrl('c') { // always quits at once, unsaved changes and all
		m.quit, m.code = true, 130
		return
	}
	switch m.mode {
	case modeConfirm:
		m.updateConfirm(k)
		return
	case modeForm:
		m.updateForm(k)
		return
	case modeDetail:
		m.updateDetail(k)
		return
	}
	page := max(1, m.height-8)
	switch {
	case (k.Code == term.KeyUp && k.Shift) || k.Is('K') || k.Is('+'):
		m.moveEntry(-1)
	case (k.Code == term.KeyDown && k.Shift) || k.Is('J') || k.Is('-'):
		m.moveEntry(1)
	case k.Code == term.KeyUp || k.Is('k') || k.IsCtrl('p'):
		m.moveTo(m.idx - 1)
	case k.Code == term.KeyDown || k.Is('j') || k.IsCtrl('n') || k.Code == term.KeyTab:
		m.moveTo(m.idx + 1)
	case k.Code == term.KeyHome || k.Is('g'):
		m.moveTo(0)
	case k.Code == term.KeyEnd || k.Is('G'):
		m.moveTo(len(m.items()) - 1)
	case k.Code == term.KeyPgUp:
		m.moveTo(m.idx - page)
	case k.Code == term.KeyPgDn:
		m.moveTo(m.idx + page)
	case k.Is(' '):
		m.toggle()
	case k.Code == term.KeyRight || k.Code == term.KeyEnter || k.Is('l'):
		m.openDetail()
	case k.Is('a'):
		m.form = newAddForm(m.cfg, m.path)
		m.back, m.mode = modeList, modeForm
	case k.Is('e'):
		m.askEdit()
	case k.Is('x') || k.Code == term.KeyDelete:
		m.askRemove()
	case k.Is('s'):
		m.save()
	case k.Is('r'):
		m.recheck(m.order)
	case k.Is('?'):
		m.help = !m.help
	case k.Code == term.KeyEsc && m.help: // Esc closes the key grid first
		m.help = false
	case k.Code == term.KeyEsc || k.Is('q'):
		if !m.dirty() {
			m.quit = true
			return
		}
		m.ask("Discard your unsaved changes? y/n (s saves them first)", func() { m.quit = true })
		m.saveFirst = true
	}
}

func (m *manager) updateConfirm(k term.Key) {
	switch {
	case k.Is('y') || k.Is('Y'):
		m.mode = m.back
		m.onYes()
	case m.saveFirst && k.Is('s'):
		m.mode = m.back
		m.quitAfterSave = true
		m.effects = append(m.effects, mgrEffect{kind: "save"})
	case k.Is('n') || k.Is('N') || k.Code == term.KeyEsc || k.Is('q') || k.Code == term.KeyEnter:
		m.mode = m.back
	}
}

// toggle adds the highlighted provider to the end of the chain, or takes
// it out. An added one keeps the cursor (to move it up next); a removed
// one leaves the cursor where it was, on the next entry.
func (m *manager) toggle() {
	id := m.cur
	if id == "" {
		return
	}
	if i := slices.Index(m.chain, id); i >= 0 {
		m.chain = slices.Delete(m.chain, i, i+1)
		m.moveTo(m.idx)
		if len(m.chain) == 0 {
			m.say(true, "the chain is empty: shelp will use its default (%s) until you add one", strings.Join(provider.DefaultChain, " → "))
		} else {
			m.say(false, "took %s out of the chain", id)
		}
		return
	}
	m.chain = append(m.chain, id)
	m.sync()
	r := m.rows[id]
	switch {
	case r != nil && r.class() != clsOK && r.class() != clsChecking && m.mode == modeDetail:
		m.say(true, "added %s as #%d, but it does not work here yet: see below", id, len(m.chain))
	case r != nil && r.class() != clsOK && r.class() != clsChecking:
		m.say(true, "added %s, but it does not work here yet: → shows how to set it up", id)
	case m.mode == modeDetail:
		m.say(false, "added %s to the chain as #%d", id, len(m.chain))
	default:
		m.say(false, "added %s as #%d; shift+↑ moves it up", id, len(m.chain))
	}
}

// moveEntry moves the highlighted chain entry up (-1) or down (+1).
func (m *manager) moveEntry(d int) {
	i := slices.Index(m.chain, m.cur)
	if i < 0 {
		m.say(true, "only chain entries move: space puts this one in the chain first")
		return
	}
	j := i + d
	if j < 0 || j >= len(m.chain) {
		return
	}
	m.chain[i], m.chain[j] = m.chain[j], m.chain[i]
	m.sync()
}

// routeIDs are the IDs of the providers route name has right now.
func (m *manager) routeIDs(name string) []string {
	var out []string
	for _, id := range m.order {
		if r := m.rows[id]; r.hasRoute && r.route.Name == name {
			out = append(out, id)
		}
	}
	return out
}

// editableRoute is the row under the cursor when e and x can act on its
// route: one from the config file or added in this session. Otherwise it
// is nil, and why says so.
func (m *manager) editableRoute(verb string) (r *mgrRow, why string) {
	r = m.rows[m.cur]
	switch {
	case r == nil:
		return nil, fmt.Sprintf("shelp has no provider %s here; space takes it out of the chain", m.cur)
	case r.hasRoute && r.route.Builtin && !r.route.Configured:
		if verb == "edit" {
			return nil, fmt.Sprintf("%s is built in: to use another address, add a local server named %s (a in the list)", r.route.Name, r.route.Name)
		}
		return nil, fmt.Sprintf("%s is built in; to stop using it, leave it out of the chain (space)", r.route.Name)
	case !r.hasRoute || (!r.route.Configured && !r.isNew):
		return nil, fmt.Sprintf("only routes you added (with a, or in the config file) can be %s; space takes %s out of the chain", map[string]string{"edit": "edited", "remove": "removed"}[verb], r.ID)
	}
	return r, ""
}

// askEdit opens the form on the route under the cursor, filled in.
func (m *manager) askEdit() {
	r, why := m.editableRoute("edit")
	if r == nil {
		m.say(true, "%s", why)
		return
	}
	cur, ok := m.configuredRoute(r.route)
	if !ok {
		m.say(true, "could not find %s's entry; `shelp config edit` opens the file", r.route.Name)
		return
	}
	base, err := withoutRoute(m.ops().applyRoutes(m.src), cur)
	if err != nil {
		m.say(true, "%v", err)
		return
	}
	m.form = newEditForm(base, m.path, cur)
	m.back, m.mode = m.page(), modeForm
}

func (m *manager) askRemove() {
	r, why := m.editableRoute("remove")
	if r == nil {
		m.say(true, "%s", why)
		return
	}
	name := r.route.Name
	q := fmt.Sprintf("Remove %s (%s)? y/n", name, strings.Join(m.routeIDs(name), ", "))
	if r.route.Builtin {
		q = fmt.Sprintf("Remove your %s entry (back to the built-in %s)? y/n", name, hostOfURL(provider.BuiltinLocalURL(name)))
	}
	m.ask(q, func() { m.removeRoute(name) })
}

func hostOfURL(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	h, _, _ := strings.Cut(u, "/")
	return h
}

// removeRoute drops route name (pending until saved) and the chain
// entries that go with it.
func (m *manager) removeRoute(name string) {
	before := m.routeIDs(name)
	if i := slices.IndexFunc(m.adds, func(r provider.CustomRoute) bool { return r.Name == name }); i >= 0 {
		m.adds = slices.Delete(m.adds, i, i+1)
	} else if i := slices.IndexFunc(m.edits, func(e routeEdit) bool { return e.route.Name == name }); i >= 0 {
		m.removes = append(m.removes, m.edits[i].name) // the name the file has
		m.edits = slices.Delete(m.edits, i, i+1)
	} else {
		m.removes = append(m.removes, name)
	}
	if err := m.recompute(); err != nil {
		m.say(true, "%v", err)
		return
	}
	gone := map[string]bool{}
	for _, id := range before {
		if m.rows[id] == nil {
			gone[id] = true
		}
	}
	m.chain = slices.DeleteFunc(m.chain, func(id string) bool { return gone[id] })
	m.sync()
	m.say(false, "removed %s (not saved yet: s saves)", name)
}

// addRoute adds a route from the form (pending until saved), and its
// providers to the end of the chain when toChain.
func (m *manager) addRoute(r provider.CustomRoute, toChain bool) {
	m.adds = append(m.adds, r)
	if err := m.recompute(); err != nil {
		m.adds = m.adds[:len(m.adds)-1]
		m.say(true, "%v", err)
		return
	}
	ids := r.IDs()
	if toChain {
		for _, id := range ids {
			if !slices.Contains(m.chain, id) {
				m.chain = append(m.chain, id)
			}
		}
	}
	m.cur = ids[0]
	m.sync()
	m.say(false, "added %s: %s (not saved yet: s saves)", r.Name, strings.Join(ids, ", "))
}

// editRoute changes route old into r (pending until saved): a route added
// in this session is replaced, one from the file gets an in-place edit.
// Chain entries follow the IDs that are renamed and leave with those that
// go; with toChain, r's new IDs go at the end of the chain.
func (m *manager) editRoute(old, r provider.CustomRoute, toChain bool) {
	if sameRoute(old, r) {
		m.say(false, "nothing changed")
		return
	}
	adds, edits := slices.Clone(m.adds), slices.Clone(m.edits)
	match := func(c provider.CustomRoute) bool { return c.Name == old.Name && c.Local == old.Local }
	if i := slices.IndexFunc(m.adds, match); i >= 0 {
		m.adds[i] = r
	} else if i := slices.IndexFunc(m.edits, func(e routeEdit) bool { return match(e.route) }); i >= 0 {
		m.edits[i].route = r
		if f, ok := fileRoute(m.src, m.edits[i].name, r.Local); ok && sameRoute(f, r) {
			m.edits = slices.Delete(m.edits, i, i+1) // back to what the file has
		}
	} else {
		m.edits = append(m.edits, routeEdit{name: old.Name, local: old.Local, route: r})
	}
	if err := m.recompute(); err != nil {
		m.adds, m.edits = adds, edits
		m.say(true, "%v", err)
		return
	}
	chain, renamed, dropped := chainAfterEdit(m.chain, old, r)
	var added []string
	if toChain {
		for _, id := range r.IDs() {
			if !slices.Contains(chain, id) {
				chain = append(chain, id)
				added = append(added, id)
			}
		}
	}
	m.chain = chain
	if to, ok := routeIDChange(old, r, m.cur); ok {
		if to == "" && len(r.IDs()) > 0 {
			to = r.IDs()[0]
		}
		m.cur = to
	}
	m.sync()
	msg := "updated " + old.Name
	if r.Name != old.Name {
		msg += " (now " + r.Name + ")"
	}
	if len(renamed) > 0 {
		msg += "; in the chain: " + strings.Join(renamed, ", ")
	}
	if len(dropped) > 0 {
		msg += "; out of the chain: " + strings.Join(dropped, ", ")
	}
	if len(added) > 0 {
		msg += "; added to the chain: " + strings.Join(added, ", ")
	}
	m.say(len(dropped) > 0, "%s (not saved yet: s saves)", msg)
}

// fileRoute is the route named name in the config text src.
func fileRoute(src, name string, local bool) (provider.CustomRoute, bool) {
	for _, r := range provider.ConfiguredRoutes(mustParse(src)) {
		if r.Name == name && r.Local == local {
			return r, true
		}
	}
	return provider.CustomRoute{}, false
}

// recompute rebuilds the provider list from the file text with the
// pending route edits applied, and asks for probes of new rows.
func (m *manager) recompute() error {
	cfg, err := config.Parse(m.ops().applyRoutes(m.src))
	if err != nil {
		return err
	}
	m.cfg = cfg
	if ids := m.rebuild(); len(ids) > 0 {
		m.effects = append(m.effects, mgrEffect{kind: "probe", ids: ids})
	}
	return nil
}

// markSaved records a successful save: newSrc is the file's text now ("" if
// unknown, e.g. after a chezmoi apply).
func (m *manager) markSaved(newSrc string, summary string) {
	if newSrc != "" {
		m.src = newSrc
	} else {
		m.src = m.ops().applyRoutes(m.src)
	}
	m.adds, m.removes, m.edits = nil, nil, nil
	m.saved = slices.Clone(m.chain)
	m.chainSet = m.chainSet || len(m.chain) > 0
	if cfg, err := config.Parse(m.src); err == nil {
		m.cfg = cfg
	}
	m.rebuild()
	m.sync()
	m.lastSaved = summary
	m.say(false, "%s", summary)
	if m.quitAfterSave {
		m.quit = true
	}
}

// saveFailed reports a save that did not happen.
func (m *manager) saveFailed(why string) {
	m.quitAfterSave = false
	m.say(true, "%s", why)
}

// ---- view ----

type mgrWidths struct{ id, label int }

// widths sizes the ID and label columns to their longest entries, the ID
// column capped at two fifths of the screen (44 at most; 34 when the width
// is unknown), the label at 28.
func (m *manager) widths(width int) mgrWidths {
	idCap := 34
	if width > 0 {
		idCap = max(16, min(44, width*2/5))
	}
	w := mgrWidths{id: 8, label: 10}
	for _, id := range m.items() {
		w.id = max(w.id, style.Width(id))
		if r := m.rows[id]; r != nil {
			w.label = max(w.label, style.Width(rowLabel(r.providerRow)))
		}
	}
	w.id, w.label = min(w.id, idCap), min(w.label, 28)
	return w
}

// View draws the screen: header, chain, other providers, footer. height > 0
// crops the body to fit, keeping the cursor visible; width > 0 cuts every
// line to width-1 columns.
func (m *manager) View(width, height int) string {
	m.height = height
	var lines []string
	switch {
	case m.mode == modeForm:
		lines = m.form.view(m.pal, width)
	case m.page() == modeDetail:
		lines = m.detailView(width, height)
	default:
		head := m.headLines()
		body, at := m.bodyLines(width)
		foot := m.footLines(width)
		if avail := height - len(head) - len(foot); height > 0 && len(body) > avail && avail >= 3 {
			body = m.crop(body, at, avail)
		}
		lines = append(append(head, body...), foot...)
	}
	if width > 0 {
		for i, l := range lines {
			lines[i] = style.Truncate(l, width-1)
		}
	}
	return strings.Join(lines, "\n")
}

// crop shows avail lines of body around line at, with a "more" line at a
// cut end.
func (m *manager) crop(body []string, at, avail int) []string {
	if at < m.top+1 {
		m.top = max(0, at-1)
	}
	if at > m.top+avail-2 {
		m.top = at - avail + 2
	}
	m.top = max(0, min(m.top, len(body)-avail))
	out := slices.Clone(body[m.top : m.top+avail])
	if m.top > 0 {
		out[0] = m.pal.Dim(fmt.Sprintf("   ↑ %d more", m.top))
	}
	if rest := len(body) - (m.top + avail); rest > 0 {
		out[len(out)-1] = m.pal.Dim(fmt.Sprintf("   ↓ %d more", rest+1))
	}
	return out
}

func (m *manager) headLines() []string {
	p := m.pal
	title := p.Bold(p.Accent("shelp providers")) + "  " + p.Dim(tildePath(m.path))
	if m.managed != "" {
		title += p.Dim(" · " + m.managed)
	}
	if m.dirty() {
		title += "  " + p.Warn("● unsaved changes")
	}
	out := []string{title, p.Dim("local: runs on this machine, works offline · cloud: sends your request over the internet")}
	if m.offline {
		out = append(out, p.Warn("offline: no network route, so shelp skips cloud providers right now"))
	}
	if m.env != "" {
		out = append(out, p.Warn("$SHELP_PROVIDER is set ("+m.env+"): it overrides this chain until you unset it"))
	}
	return append(out, "")
}

// bodyLines are the chain and the other providers; at is the cursor's line.
func (m *manager) bodyLines(width int) (out []string, at int) {
	p := m.pal
	w := m.widths(width)
	items := m.items()
	heading := p.Bold("Your chain") + p.Dim(" · tried in this order; the first that answers wins")
	if !m.chainSet && slices.Equal(m.chain, provider.DefaultChain) {
		heading += p.Dim(" · shelp's default, not in your file")
	}
	out = append(out, heading)
	for i, id := range m.chain {
		if i == m.idx {
			at = len(out)
		}
		out = append(out, m.rowLine(id, i+1, i == m.idx, w))
	}
	if len(m.chain) == 0 {
		out = append(out, p.Dim("    (empty: shelp falls back to its default, "+strings.Join(provider.DefaultChain, " → ")+")"))
	}
	out = append(out, "", p.Bold("Other providers")+p.Dim(" · space adds one to the end of the chain"))
	titles := map[int]string{
		clsOK:          "available",
		clsSetup:       "needs setup · → shows how",
		clsUnavailable: "not available here · → shows how to start or install it",
		clsChecking:    "checking…",
	}
	last := -1
	for i := len(m.chain); i < len(items); i++ {
		id := items[i]
		if c := m.rows[id].class(); c != last {
			out = append(out, p.Dim("  "+titles[c]))
			last = c
		}
		if i == m.idx {
			at = len(out)
		}
		out = append(out, m.rowLine(id, 0, i == m.idx, w))
	}
	if len(items) == len(m.chain) {
		out = append(out, p.Dim("    (none: every provider is in the chain)"))
	}
	return out, at
}

func (m *manager) rowLine(id string, num int, sel bool, w mgrWidths) string {
	p := m.pal
	mark := "  "
	if sel {
		mark = p.Accent("❯") + " "
	}
	n := "   "
	if num > 0 {
		n = p.Bold(fmt.Sprintf("%2d", num)) + " "
	}
	idText := style.Truncate(id, w.id)
	if sel {
		idText = p.Bold(idText)
	}
	r := m.rows[id]
	if r == nil {
		return mark + n + p.Err("?") + " " + style.PadRight(idText, w.id) + "  " + style.PadRight("", w.label) + "  " +
			style.PadRight("", 5) + "  " + p.Err("unknown here: not installed or not configured")
	}
	label := style.Truncate(rowLabel(r.providerRow), w.label)
	if r.isNew {
		label = style.Truncate(label, max(1, w.label-6)) + p.Dim(" (new)")
	}
	var sym, tail string
	switch r.class() {
	case clsChecking:
		sym, tail = p.Dim("…"), p.Dim("checking…")
	case clsOK:
		if r.Status == statusCooldown {
			sym, tail = p.Warn("◷"), p.Warn("rate-limited "+r.Reason)
			break
		}
		sym, tail = p.OK("✓"), p.Dim(r.Note)
		if r.Reason != "" {
			if tail != "" {
				tail += p.Dim(" · ")
			}
			tail += p.Warn(r.Reason)
		}
	case clsSetup:
		sym, tail = p.Warn("!"), p.Warn(r.Reason)
	default:
		reason := r.Reason
		if reason == "" {
			reason = "unavailable"
		}
		sym, tail = p.Dim("·"), p.Dim(reason)
	}
	return mark + n + sym + " " + style.PadRight(idText, w.id) + "  " + style.PadRight(label, w.label) + "  " +
		style.PadRight(whereBadge(p, provider.Kind(r.Kind)), 5) + "  " + tail
}

// footLines are a blank line, the question or the last message if there
// is one, then the key hints (one dim line, or the ? grid). A question
// says its own keys, so it stands alone.
func (m *manager) footLines(width int) []string {
	p := m.pal
	out := []string{""}
	switch {
	case m.mode == modeConfirm:
		return append(out, p.Bold(p.Warn(m.question)))
	case m.msg != "" && m.msgWarn:
		out = append(out, p.Warn(m.msg))
	case m.msg != "":
		out = append(out, p.OK(m.msg))
	}
	return append(out, m.keyLines(width)...)
}
