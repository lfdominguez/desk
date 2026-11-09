package infra

import (
	"fmt"
	"image"
	"image/color"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
)

// sideRow is one visible line of the sidebar.
type sideRow struct {
	i                int    // index in the tree
	kind             string // section, org, group, host, empty
	label, meta, key string
	dot              color.NRGBA
	halo, sel, wait  bool
	closed           bool
	pin              bool        // a host row in the pinned section
	flat             bool        // a host shown under its folded org
	open             bool        // the host has a session
	color            color.NRGBA // org badge; group: worst state folded inside
	match            []int       // label runes the search matched
}

// hostKey names a host across restarts, for pins.
func hostKey(n guiNode) string { return n.Org + "/" + n.Group + "/" + n.Label }

func pinsPath() string { return filepath.Join(stateDir(), "pins") }

// loadPins reads the pinned hosts, one hostKey a line.
func loadPins() map[string]bool {
	pins := map[string]bool{}
	b, _ := os.ReadFile(pinsPath())
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			pins[l] = true
		}
	}
	return pins
}

func (u *ui) togglePin(k string) {
	if u.pins[k] {
		delete(u.pins, k)
	} else {
		u.pins[k] = true
	}
	u.treeOf = nil // Tree shows the pins
	os.MkdirAll(stateDir(), 0o700)
	os.WriteFile(pinsPath(), []byte(strings.Join(slices.Sorted(maps.Keys(u.pins)), "\n")+"\n"), 0o600)
}

// hostTab is the session already open on host n, or -1.
func (u *ui) hostTab(n guiNode) int {
	for i, t := range u.st.Tabs {
		if t.OrgOnly || t.Org != n.Org || t.Group != n.Group {
			continue
		}
		for _, p := range t.Panes {
			if p.Name == n.Label {
				return i
			}
		}
	}
	return -1
}

// openTab is the tab with a pane on host n: its own tab, else its org's
// Claude tab when the Claude opened one there; -1 none.
func (u *ui) openTab(n guiNode) int {
	if ti := u.hostTab(n); ti >= 0 {
		return ti
	}
	for i, t := range u.st.Tabs {
		if t.OrgOnly && t.Org == n.Org && slices.ContainsFunc(t.Panes, func(p guiPane) bool { return p.Name == n.Label && p.Dir == n.Dir }) {
			return i
		}
	}
	return -1
}

// hostRow is host i's row: its status dot and what the right side says.
func (u *ui) hostRow(i int, n guiNode) sideRow {
	r := sideRow{i: i, kind: "host", label: n.Label, meta: n.Addr, dot: cIdle}
	if ti := u.openTab(n); ti >= 0 {
		t := u.st.Tabs[ti]
		r.open = true
		r.sel = ti == u.st.Active && u.st.Group == ""
		up, count := false, 0
		for _, p := range t.Panes {
			if p.Name == n.Label {
				count++
				up = up || p.Conn == ""
			}
		}
		r.dot = cOrange // connecting or reconnecting
		if up {
			r.dot, r.halo = cGreen, true
		}
		if count > 1 {
			r.meta = fmt.Sprintf("%d panes", count)
		}
		switch t.State {
		case "approval":
			r.dot, r.halo, r.meta, r.wait = cOrange, true, "approve", true
		case "working":
			r.dot, r.halo = cPurple, true
		}
	}
	if n.Alert != "" && !r.wait {
		r.dot, r.halo, r.meta = cRed, false, n.Alert
	}
	return r
}

// worse ranks status dots for a folded group: alert, approval, open, idle.
func worse(a, b color.NRGBA) color.NRGBA {
	rank := func(c color.NRGBA) int { return map[color.NRGBA]int{cRed: 4, cOrange: 3, cPurple: 2, cGreen: 1}[c] }
	if rank(b) > rank(a) {
		return b
	}
	return a
}

func (u *ui) sideRows() []sideRow {
	tree, query := u.st.Tree, u.search.Text()
	q := strings.Fields(query)
	match := func(n guiNode) bool { // fzf syntax, as the TUI's sidebar
		_, ok := fzfScore(query, n.Label+" "+n.Addr+" "+n.Host+" "+n.Group+" "+n.Org+" "+n.Note)
		return ok
	}
	var rows []sideRow
	if len(q) == 0 {
		var pinned []sideRow
		for i, n := range tree {
			if n.Host != "" && n.Depth == 2 && (u.pins[hostKey(n)] || n.Pin) {
				r := u.hostRow(i, n)
				r.pin, r.key = true, hostKey(n)
				if r.dot == cIdle {
					r.meta = n.Group
				}
				pinned = append(pinned, r)
			}
		}
		if len(pinned) > 0 {
			rows = append(append(rows, sideRow{kind: "section", label: "PINNED", meta: fmt.Sprint(len(pinned))}), pinned...)
		}
	}
	var org, grp *sideRow // the headers being filled; dropped when nothing under them matches
	var hosts []sideRow
	orgAt := -1 // the org header's index in rows, once it's in
	flush := func() {
		if grp != nil && len(hosts) > 0 {
			if org != nil {
				rows = append(rows, *org)
				orgAt, org = len(rows)-1, nil
			}
			if rows[orgAt].closed { // a folded org keeps the worst dot inside and its open hosts
				rows[orgAt].dot = worse(rows[orgAt].dot, grp.color)
				for _, h := range hosts {
					if h.open {
						h.flat = true
						rows = append(rows, h)
					}
				}
				grp, hosts = nil, nil
				return
			}
			rows = append(rows, *grp)
			if !grp.closed {
				rows = append(rows, hosts...)
			}
		}
		grp, hosts = nil, nil
	}
	orgN, orgName := 0, ""
	for i, n := range tree {
		switch {
		case n.Depth == 0:
			flush()
			hs, open := 0, 0
			for j := i + 1; j < len(tree) && tree[j].Depth > 0; j++ {
				if tree[j].Host != "" {
					hs++
					if u.openTab(tree[j]) >= 0 {
						open++
					}
				}
			}
			meta := fmt.Sprint(hs)
			if open > 0 {
				meta = fmt.Sprintf("%d open · %d", open, hs)
			}
			orgName = n.Label
			org = &sideRow{i: i, kind: "org", label: n.Label, meta: meta, color: seriesColor(orgN), key: "orgopen:" + n.Label,
				closed: !u.closed["orgopen:"+n.Label] && len(q) == 0, dot: cIdle} // orgs start folded: u.closed holds the unfolded ones
			orgN++
		case n.Depth == 1:
			flush()
			key := orgName + "/" + n.Label // group nodes carry no org
			grp = &sideRow{i: i, kind: "group", label: n.Label, key: key, closed: u.closed[key] && len(q) == 0, color: cIdle}
		case n.Host != "":
			if len(q) > 0 && !match(n) {
				continue
			}
			r := u.hostRow(i, n)
			r.key, r.match = hostKey(n), matchRunes(query, n.Label)
			hosts = append(hosts, r)
			if grp != nil {
				grp.color = worse(grp.color, r.dot)
				grp.meta = fmt.Sprint(len(hosts))
			}
		}
	}
	flush()
	if len(rows) == 0 && len(q) > 0 {
		rows = append(rows, sideRow{kind: "empty", label: "No hosts match “" + query + "”"})
	}
	return rows
}

// sidePick is the keyboard row among the host rows (search ↑↓, enter).
func sidePick(rows []sideRow, sel int) int {
	n := 0
	for k, r := range rows {
		if r.kind == "host" {
			if n == sel {
				return k
			}
			n++
		}
	}
	return -1
}

func (u *ui) sidebar(gtx layout.Context) layout.Dimensions {
	w := gtx.Dp(240)
	gtx.Constraints = layout.Exact(image.Pt(w, gtx.Constraints.Max.Y))
	panel(gtx, image.Rectangle{Max: gtx.Constraints.Max}, 0, cPanel, color.NRGBA{})
	rows := u.sideRows()
	hosts := 0
	for _, r := range rows {
		if r.kind == "host" {
			hosts++
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.search, Name: key.NameUpArrow}, key.Filter{Focus: &u.search, Name: key.NameDownArrow},
			key.Filter{Focus: &u.search, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			switch e.Name {
			case key.NameUpArrow:
				u.sideSel = max(0, u.sideSel-1)
			case key.NameDownArrow:
				u.sideSel = min(hosts-1, u.sideSel+1)
			case key.NameEscape:
				u.search.SetText("")
				u.wantTerm = true
			}
			if k := sidePick(rows, u.sideSel); k >= 0 {
				u.treeList.ScrollTo(max(0, k-3))
			}
		}
	}
	for {
		ev, ok := u.search.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.ChangeEvent:
			u.sideSel = 0
			u.treeList.Position.First, u.treeList.Position.Offset = 0, 0
		case widget.SubmitEvent:
			if k := sidePick(rows, u.sideSel); k >= 0 {
				u.openHost(rows[k].i)
				u.search.SetText("")
			}
		}
	}
	rows = u.sideRows()
	u.sideSel = min(u.sideSel, max(0, hosts-1))
	cur := -1
	if gtx.Focused(&u.search) {
		cur = sidePick(rows, u.sideSel)
	}
	return layout.Inset{Top: 6, Left: 6, Right: 6, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(boxed(cPane, cLine, rCtl, layout.Inset{Top: 5, Bottom: 5, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(icon(14, cText3, icoSearch)),
					layout.Rigid(hgap(8)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return u.editor(gtx, &u.search, "Search hosts  ↑↓ ↵", 12, sansFont, cText)
					}))
			})),
			layout.Rigid(vgap(4)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.treeList.Layout(gtx, len(rows), func(gtx layout.Context, k int) layout.Dimensions { return u.sideRow(gtx, rows[k], k == cur) })
			}),
			layout.Rigid(vgap(4)),
			layout.Rigid(u.account),
		)
	})
}

func (u *ui) sideRow(gtx layout.Context, r sideRow, cur bool) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	switch r.kind {
	case "section", "empty":
		return layout.Inset{Top: 8, Bottom: 4, Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			if r.kind == "empty" {
				return u.label(gtx, r.label, 12, cText3, sansFont, 2)
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, u.lblFill(r.label, 11, cText3, fSemi(sansFont))),
				layout.Rigid(u.lbl(r.meta, 11, cText3, sansFont)))
		})
	case "org":
		return u.orgLine(gtx, r)
	case "group":
		d, ok := u.button(gtx, "grp"+r.key, color.NRGBA{}, cHover, rCtl, layout.Inset{Top: 4, Bottom: 4, Left: 6, Right: 6}, func(gtx layout.Context) layout.Dimensions {
			ico := icoChevron
			if r.closed {
				ico = icoChevronRight
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(icon(12, cText3, ico)),
				layout.Rigid(hgap(10)),
				layout.Flexed(1, u.lblFill(r.label, 12, cText2, fMedium(sansFont))),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !r.closed || r.color == cIdle { // what's live inside a folded group still shows
						return layout.Dimensions{}
					}
					return layout.Inset{Right: 8}.Layout(gtx, dot(r.color, false))
				}),
				layout.Rigid(u.lbl(r.meta, 11, cText3, sansFont)))
		})
		if ok {
			u.closed[r.key] = !u.closed[r.key]
			gtx.Execute(op.InvalidateCmd{})
		}
		return d
	}
	return u.hostLine(gtx, r, cur)
}

// orgLine is an org header: click folds it; on hover a ✦ button opens the
// org's Claude session (what enter on it does in the TUI).
func (u *ui) orgLine(gtx layout.Context, r sideRow) layout.Dimensions {
	c, chatC := u.click(fmt.Sprint("org", r.i)), u.click(fmt.Sprint("org", r.i, "chat"))
	for c.Clicked(gtx) {
		u.closed[r.key] = !u.closed[r.key]
		gtx.Execute(op.InvalidateCmd{}) // the rows were built before this click: draw again now, not on the next tick
	}
	for chatC.Clicked(gtx) {
		u.d.open(r.i)
	}
	hover := c.Hovered() || chatC.Hovered()
	d := c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		m := op.Record(gtx.Ops)
		d := layout.Inset{Top: 8, Bottom: 4, Left: 6, Right: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			ico := icoChevron
			if r.closed {
				ico = icoChevronRight
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(icon(12, cText3, ico)),
				layout.Rigid(hgap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { // initial badge: tells orgs apart at a glance
					sz := gtx.Dp(18)
					fillRRect(gtx.Ops, image.Rect(0, 0, sz, sz), gtx.Dp(rCtl), r.color)
					gtx.Constraints = layout.Exact(image.Pt(sz, sz))
					return layout.Center.Layout(gtx, u.lbl(strings.ToUpper(string([]rune(r.label)[:1])), 10, cWhite, fBold(sansFont)))
				}),
				layout.Rigid(hgap(8)),
				layout.Flexed(1, u.lblFill(r.label, 12, cText, fSemi(sansFont))),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !r.closed || r.dot == cIdle {
						return layout.Dimensions{}
					}
					return layout.Inset{Right: 8}.Layout(gtx, dot(r.dot, false))
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if hover {
						return layout.Dimensions{Size: image.Pt(gtx.Dp(20), gtx.Dp(14))} // the ✦ button goes here
					}
					return u.lbl(r.meta, 11, cText3, sansFont)(gtx)
				}))
		})
		call := m.Stop()
		size := image.Pt(gtx.Constraints.Max.X, d.Size.Y)
		if hover {
			fillRRect(gtx.Ops, image.Rect(0, gtx.Dp(4), size.X, size.Y), gtx.Dp(rCtl), cHover)
		}
		call.Add(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: size}
	})
	if hover {
		col := cPurple
		if chatC.Hovered() {
			col = cText
		}
		st := op.Offset(image.Pt(d.Size.X-gtx.Dp(28), gtx.Dp(4)+(d.Size.Y-gtx.Dp(4)-gtx.Dp(20))/2)).Push(gtx.Ops)
		chatC.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			pointer.CursorPointer.Add(gtx.Ops)
			return layout.UniformInset(3).Layout(gtx, icon(14, col, icoSpark))
		})
		st.Pop()
	}
	return d
}

// hostLine is a host row: status dot, name (search matches in bold),
// address or state on the right; on hover pin and new-pane buttons take the
// address's place. The open session's row has an accent bar.
func (u *ui) hostLine(gtx layout.Context, r sideRow, cur bool) layout.Dimensions {
	id := fmt.Sprint("host", r.i)
	if r.pin {
		id = "pinned" + id
	} else if r.flat {
		id = "flat" + id
	}
	c, pinC, addC := u.click(id), u.click(id+"pin"), u.click(id+"add")
	for c.Clicked(gtx) {
		u.openHost(r.i)
	}
	for pinC.Clicked(gtx) {
		u.togglePin(r.key)
		gtx.Execute(op.InvalidateCmd{})
	}
	for addC.Clicked(gtx) {
		if ti := u.hostTab(u.st.Tree[r.i]); ti >= 0 {
			u.d.activate(ti)
			u.d.press("ctrl+t")
		} else {
			u.d.open(r.i)
		}
		u.wantTerm = true
	}
	hover := c.Hovered() || pinC.Hovered() || addC.Hovered()
	indent := unit.Dp(26)
	if r.pin || r.flat {
		indent = 10
	}
	d := c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		m := op.Record(gtx.Ops)
		d := layout.Inset{Top: 5, Bottom: 5, Left: indent, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(dot(r.dot, r.halo)),
				layout.Rigid(hgap(8)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					d := u.matchLabel(gtx, r)
					d.Size.X = gtx.Constraints.Max.X // the address goes to the far end
					return d
				}),
				layout.Rigid(hgap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if hover {
						return layout.Dimensions{Size: image.Pt(gtx.Dp(44), 0)} // the buttons go here
					}
					col, f := cText3, monoFont
					if r.wait {
						col, f = cOrange, fSemi(sansFont)
					} else if r.dot == cRed {
						col, f = cRed, sansFont
					}
					gtx.Constraints.Max.X = gtx.Dp(110)
					return u.label(gtx, r.meta, 11, col, f, 1)
				}))
		})
		call := m.Stop()
		size := image.Pt(gtx.Constraints.Max.X, d.Size.Y)
		switch {
		case r.sel:
			fillRRect(gtx.Ops, image.Rectangle{Max: size}, gtx.Dp(rCtl), cAccSoft)
			fillRRect(gtx.Ops, image.Rect(0, gtx.Dp(4), gtx.Dp(2), size.Y-gtx.Dp(4)), gtx.Dp(1), cAcc)
		case cur || hover:
			fillRRect(gtx.Ops, image.Rectangle{Max: size}, gtx.Dp(rCtl), cHover)
		}
		if !r.pin && !r.flat { // indent guide under the group's chevron
			paint.FillShape(gtx.Ops, cLine, clip.Rect(image.Rect(gtx.Dp(12), 0, gtx.Dp(13), size.Y)).Op())
		}
		call.Add(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: size}
	})
	if hover {
		col := cText3
		if u.pins[r.key] {
			col = cOrange
		}
		ib := func(cl *widget.Clickable, c color.NRGBA, draw func(*clip.Path, float32)) layout.Widget {
			return func(gtx layout.Context) layout.Dimensions {
				return cl.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					if cl.Hovered() {
						c = cText
					}
					pointer.CursorPointer.Add(gtx.Ops)
					return layout.UniformInset(3).Layout(gtx, icon(14, c, draw))
				})
			}
		}
		bw := gtx.Dp(44)
		st := op.Offset(image.Pt(d.Size.X-gtx.Dp(6)-bw, (d.Size.Y-gtx.Dp(20))/2)).Push(gtx.Ops)
		layout.Flex{}.Layout(gtx, layout.Rigid(ib(pinC, col, icoStar)), layout.Rigid(hgap(4)), layout.Rigid(ib(addC, cText3, icoPlus)))
		st.Pop()
	}
	return d
}

// matchLabel is the host name with the search's matched letters in the accent.
func (u *ui) matchLabel(gtx layout.Context, r sideRow) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	if len(r.match) == 0 {
		return u.label(gtx, r.label, 12, cText, fMedium(sansFont), 1)
	}
	var kids []layout.FlexChild
	rs, at := []rune(r.label), 0
	for i := 0; i < len(rs); {
		j, hit := i, at < len(r.match) && r.match[at] == i
		for j < len(rs) && (at < len(r.match) && r.match[at] == j) == hit {
			if hit {
				at++
			}
			j++
		}
		col, f := cText, fMedium(sansFont)
		if hit {
			col, f = cAccText, fBold(sansFont)
		}
		kids = append(kids, layout.Rigid(u.lbl(string(rs[i:j]), 12, col, f)))
		i = j
	}
	return layout.Flex{Alignment: layout.Baseline}.Layout(gtx, kids...)
}

// openHost goes to the session on host i, opening one if there is none.
func (u *ui) openHost(i int) {
	if ti := u.openTab(u.st.Tree[i]); ti >= 0 {
		u.d.activate(ti)
	} else {
		u.d.open(i)
	}
	u.wantTerm = true
	u.keyTerm = paneNext // the new state comes frames later: the keyboard waits for its pane
}

// paneNext marks keyTerm as "whichever pane comes next".
var paneNext = new(Term)

func (u *ui) account(gtx layout.Context) layout.Dimensions {
	name, sub := "desk", fmt.Sprintf("%d sessions", len(u.st.Tabs))
	if t := u.tab(); t != nil {
		name, sub = t.Account, "Claude account"
	}
	return layout.UniformInset(4).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return boxed(cAcc, color.NRGBA{}, rCtl, layout.Inset{Top: 2, Bottom: 2, Left: 6, Right: 6}, u.lbl(strings.ToUpper(name[:1]), 11, cWhite, fBold(sansFont)))(gtx)
			}),
			layout.Rigid(hgap(8)),
			layout.Rigid(u.lbl(name, 12, cText, fSemi(sansFont))),
			layout.Rigid(hgap(6)),
			layout.Flexed(1, u.lblFill(sub, 11, cText3, sansFont)))
	})
}

var tabDot = map[string]color.NRGBA{"approval": cOrange, "working": cPurple, "your turn": cAcc}

// toolbar is a flat 34dp bar: tabs on the left, view toggles and the
// command palette on the right.
func (u *ui) toolbar(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(34)
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	paint.FillShape(gtx.Ops, cPanel, clip.Rect{Max: gtx.Constraints.Max}.Op())
	gtx.Constraints.Min.Y = 0
	return layout.Inset{Left: 4, Right: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		ib := func(id string, ico func(p *clip.Path, s float32), on bool, f func()) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				c, bg := cText2, color.NRGBA{}
				if on {
					c, bg = cAccText, cAccSoft
				}
				d, ok := u.button(gtx, id, bg, cHover, rCtl, layout.UniformInset(5), icon(15, c, ico))
				u.tip(gtx, id, d, tbTips[id])
				if ok {
					f()
				}
				return d
			})
		}
		kids := []layout.FlexChild{layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			var tabs []layout.FlexChild
			for n, i := range u.orgTabs() {
				t := u.st.Tabs[i]
				tabs = append(tabs, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.tabPill(gtx, i, n, t) }), layout.Rigid(hgap(2)))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, tabs...)
		})}
		kids = append(kids,
			ib("tbPane", icoSplit, false, func() { u.d.press("ctrl+t") }),
			ib("tbBroadcast", icoBroadcast, u.tab() != nil && u.tab().Broadcast, u.d.toggleBroadcast),
			ib("tbFiles", icoFolder, u.tabFiles() != nil, u.toggleFiles),
			ib("tbGroup", icoGrid, u.st.Group != "", func() {
				if u.st.Group != "" {
					u.d.group("")
				} else if t := u.tab(); t != nil && !t.OrgOnly {
					u.d.group(groupKey(t.Dir, t.Group))
				}
			}),
			ib("tbClaude", icoPanel, u.claude, func() { u.claude = !u.claude; u.saveLayout() }),
			layout.Rigid(hgap(6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := u.button(gtx, "tbPal", cPanelCard, cHover, rCtl, layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(icon(13, cText3, icoSearch)),
						layout.Rigid(hgap(6)),
						layout.Rigid(u.lbl("Commands", 12, cText2, sansFont)),
						layout.Rigid(hgap(10)),
						layout.Rigid(u.lbl(modText("K"), 11, cText3, sansFont)))
				})
				if ok {
					u.openPalette()
				}
				return d
			}))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	})
}

// orgTabs are the indexes of the tabs of the org on screen, in order.
func (u *ui) orgTabs() []int {
	var out []int
	for i, t := range u.st.Tabs {
		if t.Org == u.org {
			out = append(out, i)
		}
	}
	return out
}

// tabPill is tab i, the org's n-th (its shortcut number).
func (u *ui) tabPill(gtx layout.Context, i, n int, t guiTab) layout.Dimensions {
	name := t.Name + " · " + t.Group
	if t.OrgOnly {
		name = t.Org
		if t.Card != "" {
			name += " · " + t.Card // a card's own session: which ticket
		}
	}
	fill, fg := color.NRGBA{}, cText2
	if i == u.st.Active && u.st.Group == "" {
		fill, fg = cPanelCard, cText
	}
	d, ok := u.button(gtx, fmt.Sprint("tab", i), fill, cHover, rCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 4}, func(gtx layout.Context) layout.Dimensions {
		dc, has := tabDot[t.State]
		if !has {
			dc = cIdle
		}
		kids := []layout.FlexChild{
			layout.Rigid(dot(dc, false)), layout.Rigid(hgap(7)),
			layout.Rigid(u.lbl(name, 12, fg, fMedium(sansFont))),
		}
		if n < 9 {
			kids = append(kids, layout.Rigid(hgap(7)), layout.Rigid(u.lbl(modText(fmt.Sprint(n+1)), 11, cText3, sansFont)))
		}
		kids = append(kids, layout.Rigid(hgap(4)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := u.button(gtx, fmt.Sprint("tabx", i), color.NRGBA{}, cHover, 3, layout.UniformInset(3), icon(11, cText3, icoX))
			if ok {
				u.d.closeTab(i)
			}
			return d
		}))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	})
	if ok {
		u.d.activate(i)
		u.wantTerm = true
	}
	return d
}

var _ = unit.Dp(0)
