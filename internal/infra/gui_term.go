package infra

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/image/math/fixed"

	"github.com/lfdominguez/desk/internal/keys"
	deskorgs "github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// termView is the window's state for one terminal: scroll, size, selection.
type termView struct {
	scroll     int
	cols, rows int
	sel        [2]image.Point // cell (col,row) in the view; equal = no selection
	selecting  bool
	clicks     clicks
	runs       [][]Run // last drawn, for copying
	rowY       []int   // last drawn top of each row: block cards space rows unevenly
	// the screen as last read (screen): at Version gridVer and scroll
	// gridScroll, the gridGen-th read
	gridVer    uint64
	gridScroll int
	gridGen    int
	sb, cx, cy int
	meta       []Row
	cursor     bool
	sudoAt     uint64 // Version+1 sudoUser/sudoOK are for (sudo)
	sudoUser   string
	sudoOK     bool
	cellKey    cellKey // what cellCall was recorded from, with cellRowY
	cellRowY   []int
	cellOps    op.Ops
	cellCall   op.CallOp
	recorded   int // times cells were recorded

	glyphs    []text.Glyph
	finding   bool          // the find bar is open
	find      widget.Editor // its query
	findQ     string        // the query hits are for
	hits      []int         // absolute lines matching, oldest first
	hit       int           // the match shown
	focusFind bool          // give the find bar the keyboard on the next frame
	blockSel  int           // block picked from the keyboard (⌃⇧↑↓), 0: none
}

// selectBlock moves the focused pane's keyboard block selection up or down,
// as ctrl+↑↓ does in the TUI; past the newest block it ends.
func (u *ui) selectBlock(up bool) {
	t := u.tab()
	if t == nil {
		return
	}
	for _, p := range t.Panes {
		if !p.Focused || len(p.Blocks) == 0 {
			continue
		}
		v, n := u.view(p.Term), 0
		for k := range p.Blocks {
			n = max(n, k)
		}
		switch {
		case v.blockSel == 0 && up:
			v.blockSel = n
		case up:
			v.blockSel = max(1, v.blockSel-1)
		case v.blockSel != 0:
			v.blockSel++
		}
		b, ok := p.Blocks[v.blockSel]
		if !ok || v.blockSel > n {
			v.blockSel, v.scroll = 0, 0
			return
		}
		sb := p.Term.Scrollback()
		v.scroll = max(0, min(sb, sb-(b.Start-2)))
		u.wantTerm = true
	}
}

// blockKey handles keys while a block is picked: space pins it for Claude,
// y copies its output, ↑↓ move, esc/enter leave. It reports whether it took
// the key; any other key leaves block mode and goes to the shell.
func (u *ui) blockKey(p guiPane, v *termView, name key.Name, text string) bool {
	if v.blockSel == 0 {
		return false
	}
	switch {
	case text == " ":
		u.d.pin(v.blockSel)
	case text == "y" || text == "Y":
		if b, ok := p.Blocks[v.blockSel]; ok {
			u.setClipboard(b.Output)
			u.showToast(fmt.Sprintf("Copied the output of #%d", v.blockSel), false)
		}
	case name == key.NameUpArrow || name == key.NameDownArrow:
		u.selectBlock(name == key.NameUpArrow)
	case name == key.NameEscape || name == key.NameReturn || name == key.NameEnter:
		v.blockSel, v.scroll = 0, 0
	default:
		v.blockSel = 0
		return false
	}
	return true
}

// openFind opens the focused pane's find bar.
func (u *ui) openFind() {
	t := u.tab()
	if t == nil {
		return
	}
	for _, p := range t.Panes {
		if p.Focused {
			v := u.view(p.Term)
			v.find.SingleLine, v.find.Submit = true, true
			v.finding, v.focusFind = true, true
			v.find.SetCaret(0, len([]rune(v.find.Text())))
		}
	}
}

// findBar is the pane's scrollback search (⌃⇧H): typing jumps to the newest
// match, enter to the next older one (wrapping), esc closes.
func (u *ui) findBar(gtx layout.Context, p guiPane, v *termView, area image.Rectangle) {
	if v.focusFind {
		v.focusFind = false
		gtx.Execute(key.FocusCmd{Tag: &v.find})
	}
	for {
		ev, ok := v.find.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok && len(v.hits) > 0 {
			v.hit = (v.hit - 1 + len(v.hits)) % len(v.hits)
			v.scroll = p.Term.ScrollFor(v.hits[v.hit])
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &v.find, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			v.finding, v.hits, v.findQ, v.scroll = false, nil, "", 0
			u.wantTerm = true
			return
		}
	}
	if q := v.find.Text(); q != v.findQ {
		v.findQ, v.hits = q, p.Term.Find(q)
		v.hit = len(v.hits) - 1
		if v.hit >= 0 {
			v.scroll = p.Term.ScrollFor(v.hits[v.hit])
		}
	}
	count, cc := "no matches", cText3
	if len(v.hits) > 0 {
		count, cc = fmt.Sprintf("%d of %d", len(v.hits)-v.hit, len(v.hits)), cText2
	} else if v.findQ == "" {
		count = "type to search"
	}
	w := gtx.Dp(320)
	st := op.Offset(image.Pt(area.Max.X-w-gtx.Dp(12), area.Min.Y+gtx.Dp(8))).Push(gtx.Ops)
	bgtx := gtx
	bgtx.Constraints = layout.Exact(image.Pt(w, gtx.Dp(30)))
	boxed(cPanel, cAcc, rCtl, layout.Inset{Top: 5, Bottom: 5, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(icon(13, cText3, icoSearch)),
			layout.Rigid(hgap(6)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.editor(gtx, &v.find, "Find in scrollback", 12, monoFont, cText)
			}),
			layout.Rigid(hgap(6)),
			layout.Rigid(u.lbl(count+" · ↵ older · esc", 11, cc, sansFont)))
	})(bgtx)
	st.Pop()
}

var ansi16 = [16]color.NRGBA{
	rgb(0x3a3a3c), rgb(0xff6961), rgb(0x30d158), rgb(0xffd60a), rgb(0x409cff), rgb(0xda8fff), rgb(0x70d7ff), rgb(0xd1d1d6),
	rgb(0x636366), rgb(0xff8a84), rgb(0x63e07f), rgb(0xffe45c), rgb(0x6bb5ff), rgb(0xe5a8ff), rgb(0x8fe3ff), rgb(0xf5f5f7),
}

func cellColor(c color.Color, def color.NRGBA) color.NRGBA {
	switch c := c.(type) {
	case nil:
		return def
	case ansi.BasicColor:
		return ansi16[c&15]
	case ansi.IndexedColor:
		if c < 16 {
			return ansi16[c]
		}
	}
	r, g, b, a := c.RGBA()
	return color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

// terminals stacks the active tab's panes, or shows what to do without any.
func (u *ui) terminals(gtx layout.Context) layout.Dimensions {
	t := u.tab()
	var fv *filesView
	if t != nil {
		fv = u.files[t.Key]
	}
	if t == nil || len(t.Panes) == 0 && fv == nil {
		if u.keyTerm != paneNext { // no pane to follow: a later one doesn't take the composer's keys
			u.keyTerm = nil
		}
		title, sub := "No session", "Pick a server in the sidebar, or press "+keys.Short("infra.palette")+"."
		if t != nil {
			title, sub = t.Org+" · Claude", "No server open yet. Ask Claude to connect to one, or pick a host in the sidebar."
		}
		sz := gtx.Constraints.Max
		panel(gtx, image.Rectangle{Max: sz}, 0, cPane, color.NRGBA{})
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(u.lbl(title, 17, cText, fSemi(sansFont))),
				layout.Rigid(vgap(8)),
				layout.Rigid(u.lbl(sub, 13, cText3, sansFont)))
		})
	}
	var kids []layout.FlexChild
	for i := range t.Panes {
		if i > 0 {
			kids = append(kids, layout.Rigid(vgap(1)))
		}
		kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.pane(gtx, t, i) }))
	}
	if fv != nil {
		if len(kids) > 0 {
			kids = append(kids, layout.Rigid(vgap(1)))
		}
		kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.filesPanels(gtx, fv) }))
	}
	// forget views of terminals that are gone
	live, liveTabs := map[*Term]bool{}, map[*Tab]bool{}
	for _, tb := range u.st.Tabs {
		liveTabs[tb.Key] = true
		for _, p := range tb.Panes {
			live[p.Term] = true
		}
	}
	for k := range u.files { // and directory splits of closed tabs
		if !liveTabs[k] {
			delete(u.files, k)
		}
	}
	for term := range u.terms {
		if !live[term] {
			delete(u.terms, term)
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func (u *ui) view(t *Term) *termView {
	v, ok := u.terms[t]
	if !ok {
		v = &termView{}
		u.terms[t] = v
	}
	return v
}

// typingTo is where keys typed into p go: p, or every pane of t while
// broadcasting.
func typingTo(t *guiTab, p guiPane) []*Term {
	if !t.Broadcast {
		return []*Term{p.Term}
	}
	out := []*Term{p.Term}
	for _, o := range t.Panes {
		if o.Term != p.Term {
			out = append(out, o.Term)
		}
	}
	return out
}

// pane draws one terminal: header, grid with block cards, overlay.
func (u *ui) pane(gtx layout.Context, t *guiTab, i int) layout.Dimensions {
	p := t.Panes[i]
	sz := gtx.Constraints.Max
	rim := color.NRGBA{}
	if p.Focused && len(t.Panes) > 1 {
		rim = cAccSoft
	}
	if t.Broadcast {
		rim = cOrange
	}
	panel(gtx, image.Rectangle{Max: sz}, 0, cPane, rim)
	head := gtx.Dp(28)
	// header
	hgtx := gtx
	hgtx.Constraints = layout.Exact(image.Pt(sz.X, head))
	layout.Inset{Left: 10, Right: 4}.Layout(hgtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(dot(map[bool]color.NRGBA{true: cGreen, false: cOrange}[p.Conn == ""], false)),
			layout.Rigid(hgap(8)),
			layout.Rigid(u.lbl(p.Name, 12, cText, fSemi(sansFont))),
			layout.Rigid(hgap(8)),
			layout.Rigid(u.lbl(p.Addr, 12, cText3, sansFont)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !p.Claude {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: 10}.Layout(gtx, u.pill("Claude", cPurple, cPurpleSoft))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !t.Broadcast {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: 10}.Layout(gtx, u.pill("typing into every pane", cOrange, cOrangeSoft))
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { // how its connection went, until the next
				ss := p.Term.Steps()
				if p.Conn != "" || stepsTotal(ss) == "" {
					return layout.Dimensions{}
				}
				id := fmt.Sprintf("steps%p", p.Term)
				s := "connected in " + fmtDur(ss[len(ss)-1].End.Sub(ss[0].Start))
				d, _ := u.button(gtx, id, color.NRGBA{}, cHover, rCtl, layout.Inset{Top: 2, Bottom: 2, Left: 7, Right: 7}, u.lbl(s, 11, cText3, sansFont))
				u.tipW(gtx, id, d, 460, 600, u.connSteps(ss, gtx.Now))
				return layout.Inset{Right: 6}.Layout(gtx, func(layout.Context) layout.Dimensions { return d })
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !p.Focused {
					return layout.Dimensions{}
				}
				s, fg, bg := "□ screen off", cText2, cPanelCard
				if t.PinScreen {
					s, fg, bg = "□ screen", cAccText, cAccSoft
				} else if t.Live {
					s = "□ screen auto"
				}
				d, ok := u.button(gtx, fmt.Sprintf("scr%p", p.Term), bg, cHover, rCtl, layout.Inset{Top: 2, Bottom: 2, Left: 7, Right: 7}, u.lbl(s, 11, fg, fSemi(sansFont)))
				if ok {
					u.d.press("ctrl+s")
				}
				return d
			}),
			layout.Rigid(hgap(10)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if n := u.view(p.Term).blockSel; n != 0 {
					return u.lbl(fmt.Sprintf("block #%d · space pins · y copies · ↑↓ · esc", n), 11, cOrange, sansFont)(gtx)
				}
				return u.lbl("click #n to pin · ⇧ copies its output · "+keys.Short("infra.blockup")+" picks", 11, cText3, sansFont)(gtx)
			}),
			layout.Rigid(hgap(6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := u.button(gtx, fmt.Sprintf("close%p", p.Term), color.NRGBA{}, cHover, rCtl, layout.UniformInset(4), icon(13, cText2, icoX))
				if ok {
					u.d.closePane(i)
				}
				return d
			}),
		)
	})
	paint.FillShape(gtx.Ops, cLine, clip.Rect(image.Rect(0, head, sz.X, head+gtx.Dp(1))).Op())

	area := image.Rect(0, head+gtx.Dp(1), sz.X, sz.Y)
	u.grid(gtx, t, i, p, area)
	if p.Focused && p.Conn != "" {
		u.connOverlay(gtx, area, p)
	}
	if v := u.view(p.Term); v.finding {
		u.findBar(gtx, p, v, area)
	} else if user, ok := v.sudo(p.Term); ok && p.Focused && p.Conn == "" {
		u.sudoBar(gtx, p, user, area)
	}
	return layout.Dimensions{Size: sz}
}

// sudoBar offers the server password while sudo (su, doas) waits for one:
// read from the Term at draw time, since the prompt comes with PTY output,
// not with a model update.
func (u *ui) sudoBar(gtx layout.Context, p guiPane, user string, area image.Rectangle) {
	who := "a"
	if user != "" {
		who = user + "'s"
	}
	st := op.Offset(image.Pt(area.Max.X-gtx.Dp(420)-gtx.Dp(12), area.Min.Y+gtx.Dp(8))).Push(gtx.Ops)
	bgtx := gtx
	bgtx.Constraints = layout.Exact(image.Pt(gtx.Dp(420), gtx.Dp(34)))
	boxed(cPanel, cOrange, rCtl, layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 4}, func(gtx layout.Context) layout.Dimensions {
		if !p.Password {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, u.lblFill("Asks for "+who+" password · no #@ password for this server", 12, cOrange, fSemi(sansFont))))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, u.lblFill("Asks for "+who+" password", 12, cOrange, fSemi(sansFont))),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := u.button(gtx, "sudoPw", cAcc, cHover, rCtl, layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10},
					u.lbl("Type server password  "+keys.Short("infra.password"), 12, cWhite, fSemi(sansFont)))
				if ok {
					u.d.press("alt+p")
					u.wantTerm = true
				}
				return d
			}))
	})(bgtx)
	st.Pop()
}

// grid draws the cells, block cards, gutter and cursor in area, and handles
// the terminal's keyboard and mouse.
func (u *ui) grid(gtx layout.Context, t *guiTab, i int, p guiPane, area image.Rectangle) {
	gtx = kit.Zoomed(gtx, deskorgs.Zoom()) // desk's text size: the cells follow it, and the pane's size in them
	v := u.view(p.Term)
	size := fixed.I(gtx.Sp(13))
	cw, chh, asc := cellMetrics(u.sh, monoFont, size, &v.glyphs)
	rowH := int(float32(chh) * 1.3)
	small := fixed.I(gtx.Sp(11))
	maxN := 9 // gutter: as wide as the widest block number, not a fixed margin
	for n := range p.Blocks {
		maxN = max(maxN, n)
	}
	numW := textWidth(u.sh, monoFont, small, fmt.Sprintf("#%d", maxN), &v.glyphs)
	gutter := gtx.Dp(4) + numW + gtx.Dp(18)
	inner := image.Rect(area.Min.X+gutter, area.Min.Y+gtx.Dp(6), area.Max.X-gtx.Dp(12), area.Max.Y-gtx.Dp(4))
	// A frame with no real room (the window's first ones, before its size
	// comes) must not resize: a restored pane's history may be written at
	// that moment, hard-wrapped at that width for good.
	if c, r := inner.Dx()/cw, inner.Dy()/rowH; (c != v.cols || r != v.rows) && c >= 20 && r >= 3 {
		v.cols, v.rows = c, r
		p.Term.Resize(c, r)
	}

	// focus: the focused pane of the active tab owns the keyboard unless an
	// editor or a sheet has it
	if p.Focused {
		switch {
		case gtx.Focused(v):
			u.keyTerm = p.Term
		case u.keyTerm != nil && u.keyTerm != p.Term:
			u.wantTerm = true
		case u.keyTerm == p.Term: // the keyboard left for an editor
			u.keyTerm = nil
		}
	}
	if p.Focused && u.wantTerm && u.st.Approval == nil && u.st.Ask == "" && !u.pal {
		gtx.Execute(key.FocusCmd{Tag: v})
		u.wantTerm = false
	}
	u.termEvents(gtx, t, i, p, v, inner, cw, rowH)

	sb, runs, meta, cx, cy, cursor := v.screen(p.Term)
	gap := rowH / 2
	last := cy                    // the last row worth showing: the cursor or the last text
	end := make([]int, len(runs)) // where each row's text ends, in px
	for k, row := range runs {
		for _, r := range row {
			if s := strings.TrimRight(r.Text, " "); s != "" || r.Style.Bg != nil {
				last = max(last, k)
				if s != "" {
					end[k] = inner.Min.X + (r.X+len([]rune(s)))*cw
				}
			}
		}
	}
	group := func(k int) int {
		if r := meta[k]; r.N == 0 && r.Running && k <= max(cy, last) {
			return -1
		} else {
			return r.N
		}
	}

	// blocks and their badges (Claude, pinned, exit, time): drawn at the right
	// end of the first row with room for them, the command line first, else
	// on a slim line of their own at the bottom of the card
	type badge struct {
		s string
		c color.NRGBA
	}
	type block struct {
		k, j, g int
		pinned  bool
		picked  bool    // selected from the keyboard
		badges  []badge // right to left
		left    int     // x where the badges start
		row     int     // where they go; -1: their own line
	}
	var blocks []block
	extra := map[int]int{} // extra height after a row, for a badge line
	for k := 0; k < len(meta); {
		g := group(k)
		j := k
		for j < len(meta) && group(j) == g {
			j++
		}
		if g != 0 {
			bl := block{k: k, j: j, g: g, pinned: g > 0 && p.Focused && t.Pinned[g], picked: g > 0 && g == v.blockSel, row: -1}
			if g < 0 {
				bl.badges = []badge{{"running", cPurple}}
			} else if b, ok := p.Blocks[g]; ok {
				bl.badges = append(bl.badges, badge{dur(b.Dur), cText3})
				if b.Exit != 0 {
					bl.badges = append(bl.badges, badge{fmt.Sprintf("✗ %d", b.Exit), cRed})
				} else {
					bl.badges = append(bl.badges, badge{"✓ 0", cGreen})
				}
				if bl.pinned {
					bl.badges = append(bl.badges, badge{"pinned", cAccText})
				}
				if b.ByClaude {
					bl.badges = append(bl.badges, badge{"Claude", cPurple})
				}
			}
			w := 0
			for _, b := range bl.badges {
				w += textWidth(u.sh, monoFont, small, b.s, &v.glyphs) + gtx.Dp(10)
			}
			bl.left = inner.Max.X - gtx.Dp(4) - w
			rows := []int{k}
			for r := j - 1; r > k; r-- {
				rows = append(rows, r)
			}
			for _, r := range rows {
				if end[r] < bl.left-gtx.Dp(8) {
					bl.row = r
					break
				}
			}
			if bl.row < 0 && len(bl.badges) > 0 {
				extra[j-1] = rowH * 3 / 4
			}
			blocks = append(blocks, bl)
		}
		k = j
	}

	// rows get y positions with a gap around each block so the cards
	// breathe; the view is anchored at the bottom
	ys := make([]int, len(runs))
	y := 0
	for k := range runs {
		if k > 0 && group(k) != group(k-1) {
			y += gap
		}
		ys[k] = y
		y += rowH + extra[k]
	}
	shift := 0
	if last < len(ys) {
		shift = max(0, ys[last]+rowH+extra[last]-inner.Dy())
	}
	at := func(k int) int { return inner.Min.Y + ys[k] - shift }
	v.rowY = v.rowY[:0]
	for k := range runs {
		v.rowY = append(v.rowY, at(k))
	}

	cl := clip.Rect(area).Push(gtx.Ops)
	for _, bl := range blocks {
		k, j, g := bl.k, bl.j, bl.g
		top, bot := at(k)-gap/2+gtx.Dp(2), at(j-1)+rowH+extra[j-1]+gap/2-gtx.Dp(2)
		r := image.Rect(inner.Min.X-gtx.Dp(12), top, inner.Max.X+gtx.Dp(8), bot)
		if bl.picked {
			fillRRect(gtx.Ops, r, gtx.Dp(rCard+1), cOrange)
			r = r.Inset(gtx.Dp(1.5))
		}
		if bl.pinned {
			fillRRect(gtx.Ops, r, gtx.Dp(rCard+1), cAcc)
			r = r.Inset(gtx.Dp(1.5))
		}
		fillRRect(gtx.Ops, r, gtx.Dp(rCard), cCard)
		badgeY := at(j-1) + rowH + (extra[j-1]-chh)/2 + asc - gtx.Dp(2)
		if bl.row >= 0 {
			badgeY = at(bl.row) + (rowH-chh)/2 + asc
		}
		x := inner.Max.X - gtx.Dp(4)
		for _, b := range bl.badges {
			x -= textWidth(u.sh, monoFont, small, b.s, &v.glyphs)
			drawText(gtx.Ops, u.sh, monoFont, small, b.s, x, badgeY, b.c, &v.glyphs)
			x -= gtx.Dp(10)
		}
		if g > 0 { // gutter number: click to pin for Claude
			c := u.click(fmt.Sprintf("pin%p-%d", p.Term, g))
			for {
				click, ok := c.Update(gtx)
				if !ok {
					break
				}
				if b, has := p.Blocks[g]; has && click.Modifiers.Contain(key.ModShift) { // shift-click copies the output
					u.setClipboard(b.Output)
					u.showToast(fmt.Sprintf("Copied the output of #%d", g), false)
					continue
				}
				if !p.Focused {
					u.d.focusPane(i)
				}
				u.d.pin(g)
			}
			st := op.Offset(image.Pt(area.Min.X+gtx.Dp(4), at(k))).Push(gtx.Ops)
			bgtx := gtx
			bgtx.Constraints = layout.Exact(image.Pt(numW, rowH))
			c.Layout(bgtx, func(gtx layout.Context) layout.Dimensions {
				col := cText3
				if bl.pinned {
					col = cAccText
				} else if c.Hovered() {
					col = cText
				}
				s := fmt.Sprintf("#%d", g)
				w := textWidth(u.sh, monoFont, small, s, &v.glyphs)
				drawText(gtx.Ops, u.sh, monoFont, small, s, gtx.Constraints.Min.X-w, (rowH-chh)/2+asc, col, &v.glyphs)
				pointer.CursorPointer.Add(gtx.Ops)
				return layout.Dimensions{Size: gtx.Constraints.Min}
			})
			st.Pop()
		}
	}

	// the find bar's current match
	if v.finding && v.hit >= 0 && v.hit < len(v.hits) {
		if k := v.hits[v.hit] - max(0, sb-v.scroll); k >= 0 && k < len(runs) {
			paint.FillShape(gtx.Ops, withAlpha(cOrange, 0x40), clip.Rect(image.Rect(inner.Min.X-gtx.Dp(4), at(k), inner.Max.X, at(k)+rowH)).Op())
		}
	}

	// selection, cells, cursor
	lo, hi := order(v.sel[0], v.sel[1])
	for k := range runs {
		py := at(k)
		if lo != hi && k >= lo.Y && k <= hi.Y { // clipped to area
			x0, x1 := 0, v.cols
			if k == lo.Y {
				x0 = lo.X
			}
			if k == hi.Y {
				x1 = hi.X
			}
			paint.FillShape(gtx.Ops, cAccSoft, clip.Rect(image.Rect(inner.Min.X+x0*cw, py, inner.Min.X+x1*cw, py+rowH)).Op())
		}
	}
	key := cellKey{v.gridGen, guiTheme, inner.Min.X, area.Min.Y, area.Max.Y, cw, rowH, chh, asc, size}
	if key != v.cellKey || !slices.Equal(v.rowY, v.cellRowY) {
		v.cellKey, v.cellRowY = key, append(v.cellRowY[:0], v.rowY...)
		v.cellOps.Reset()
		rec := op.Record(&v.cellOps)
		u.cells(&v.cellOps, v, runs, meta, inner.Min.X, area, at, cw, rowH, chh, asc, size)
		v.cellCall = rec.Stop()
		v.recorded++
	}
	v.cellCall.Add(gtx.Ops)
	if cursor && cy < len(runs) {
		x, top := inner.Min.X+cx*cw, at(cy)+(rowH-chh)/2
		c := cAcc
		if !gtx.Focused(v) {
			c.A = 0x60
		}
		paint.FillShape(gtx.Ops, c, clip.Rect(image.Rect(x, top, x+cw, top+chh)).Op())
	}
	cl.Pop()
}

// cellKey is what a pane's recorded cells (termView.cellCall) were drawn
// from, besides the rows' y (cellRowY): the screen read (gridGen), the theme,
// where and how big.
type cellKey struct {
	gridGen                            int
	theme                              string
	x, top, bottom, cw, rowH, chh, asc int
	size                               fixed.Int26_6
}

// screen is the Term's Scrollback and Grid at the view's scroll, read again
// only when its Version or the scroll moved: frames come far more often than
// output, and Grid styles every cell. Mid synchronized update the last screen
// stays, so a half-drawn one never flickers through.
func (v *termView) screen(t *Term) (sb int, runs [][]Run, meta []Row, cx, cy int, cursor bool) {
	if ver := t.Version(); v.runs == nil || v.scroll != v.gridScroll || ver != v.gridVer && !t.Synchronized() {
		v.gridVer, v.gridScroll, v.gridGen = ver, v.scroll, v.gridGen+1
		v.sb = t.Scrollback()
		v.runs, v.meta, v.cx, v.cy, v.cursor = t.Grid(v.scroll)
	}
	return v.sb, v.runs, v.meta, v.cx, v.cy, v.cursor
}

// sudo is the Term's SudoPrompt, asked again only after new output.
func (v *termView) sudo(t *Term) (string, bool) {
	if at := t.Version() + 1; at != v.sudoAt {
		v.sudoAt = at
		v.sudoUser, v.sudoOK = t.SudoPrompt()
	}
	return v.sudoUser, v.sudoOK
}

// cells draws the runs' backgrounds and text into ops (a recording the
// frames replay until the screen or the layout changes); trailing blanks
// aren't shaped.
func (u *ui) cells(ops *op.Ops, v *termView, runs [][]Run, meta []Row, left int, area image.Rectangle, at func(int) int, cw, rowH, chh, asc int, size fixed.Int26_6) {
	for k, row := range runs {
		py := at(k)
		if py+rowH < area.Min.Y || py > area.Max.Y {
			continue
		}
		for _, r := range row {
			fg, bg := cellColor(r.Style.Fg, cText), cellColor(r.Style.Bg, color.NRGBA{})
			if r.Style.Attrs&uv.AttrReverse != 0 {
				fg, bg = cellColor(r.Style.Bg, cPane), cellColor(r.Style.Fg, cText)
			}
			if r.Style.Attrs&uv.AttrFaint != 0 {
				fg.A = 0x90
			}
			f := monoFont
			if r.Style.Attrs&uv.AttrBold != 0 {
				f = fBold(f)
			}
			x := left + r.X*cw
			if bg.A != 0 {
				paint.FillShape(ops, bg, clip.Rect(image.Rect(x, py, x+r.W*cw, py+rowH)).Op())
			}
			text := strings.TrimRight(r.Text, " ")
			if strings.TrimSpace(text) == "" {
				continue
			}
			base := py + (rowH-chh)/2 + asc
			if m := meta[k]; m.First && r.X < m.PX && r.Style.Fg == nil { // the prompt: quiet, its sign in the accent
				rs := []rune(text)
				n := min(len(rs), m.PX-r.X)
				prompt := strings.TrimRight(string(rs[:n]), " ")
				if pr := []rune(prompt); len(pr) > 0 && strings.ContainsRune("$#%>❯", pr[len(pr)-1]) {
					drawRun(ops, u.sh, f, size, string(pr[:len(pr)-1]), x, base, cw, cText3, &v.glyphs)
					drawRun(ops, u.sh, f, size, string(pr[len(pr)-1:]), x+(len(pr)-1)*cw, base, cw, cAcc, &v.glyphs)
				} else {
					drawRun(ops, u.sh, f, size, prompt, x, base, cw, cText3, &v.glyphs)
				}
				drawRun(ops, u.sh, f, size, string(rs[n:]), x+n*cw, base, cw, fg, &v.glyphs)
				continue
			}
			drawRun(ops, u.sh, f, size, text, x, base, cw, fg, &v.glyphs)
		}
	}
}

func order(a, b image.Point) (image.Point, image.Point) {
	if b.Y < a.Y || b.Y == a.Y && b.X < a.X {
		return b, a
	}
	return a, b
}

func dur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
}

// termEvents feeds keys, mouse, wheel and paste to the terminal.
func (u *ui) termEvents(gtx layout.Context, t *guiTab, i int, p guiPane, v *termView, inner image.Rectangle, cw, rowH int) {
	mods := key.ModCtrl | key.ModShift | key.ModAlt | key.ModSuper | key.ModCommand
	cell := func(pos image.Point) image.Point {
		row := (pos.Y - inner.Min.Y) / rowH
		if len(v.rowY) > 0 {
			row = 0
			for k, y := range v.rowY {
				if pos.Y >= y {
					row = k
				}
			}
		}
		return image.Pt(max(0, min(v.cols, (pos.X-inner.Min.X+cw/2)/cw)), max(0, min(v.rows-1, row)))
	}
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: v},
			key.Filter{Focus: v, Name: "", Optional: mods},
			key.Filter{Focus: v, Name: key.NameTab, Optional: mods},
			pointer.Filter{Target: v, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6}},
			transfer.TargetFilter{Target: v, Type: "application/text"},
		)
		if !ok {
			break
		}
		switch ev := ev.(type) {
		case key.EditEvent:
			if p.Conn != "" { // the connection box is up: its keys (R, C, A…), not the dead shell's
				for _, r := range strings.ToLower(ev.Text) {
					u.d.press(string(r))
				}
				continue
			}
			if u.blockKey(p, v, "", ev.Text) {
				continue
			}
			v.scroll, v.sel = 0, [2]image.Point{}
			for _, term := range typingTo(t, p) {
				for _, r := range ev.Text {
					term.SendKey(uv.KeyPressEvent{Code: r, Text: string(r)})
				}
			}
		case key.Event:
			if u.shortcut(ev) {
				continue
			}
			if p.Conn != "" {
				if ev.State == key.Press && ev.Modifiers == 0 {
					switch ev.Name {
					case key.NameReturn, key.NameEnter:
						u.d.press("enter")
					case key.NameEscape:
						u.d.press("esc")
					}
				}
				continue
			}
			if ev.State == key.Press && ev.Modifiers == 0 && u.blockKey(p, v, ev.Name, "") {
				continue
			}
			if k, ok := termKey(ev); ok {
				v.scroll, v.sel = 0, [2]image.Point{}
				for _, term := range typingTo(t, p) {
					term.SendKey(k)
				}
			}
		case pointer.Event:
			switch ev.Kind {
			case pointer.Press:
				gtx.Execute(key.FocusCmd{Tag: v})
				if !p.Focused {
					u.d.focusPane(i)
				}
				u.clearSelections()
				c := cell(ev.Position.Round())
				v.sel, v.selecting = [2]image.Point{c, c}, true
				switch v.clicks.press(ev.Position.Round()) {
				case 2:
					lo, hi := wordAt(v.line(c.Y), (ev.Position.Round().X-inner.Min.X)/cw) // the cell under the pointer, not the nearest edge
					v.sel = [2]image.Point{{lo, c.Y}, {hi, c.Y}}
				case 3:
					v.sel = [2]image.Point{{0, c.Y}, {len(v.line(c.Y)), c.Y}}
				}
			case pointer.Drag:
				if v.selecting && v.clicks.n == 1 {
					v.sel[1] = cell(ev.Position.Round())
				}
			case pointer.Release:
				if v.selecting {
					u.copyOnSelect(v.selected())
				}
				v.selecting = false
			case pointer.Scroll:
				lines := int(ev.Scroll.Y / float32(rowH/3+1))
				if lines == 0 {
					lines = map[bool]int{true: 1, false: -1}[ev.Scroll.Y > 0]
				}
				c := cell(ev.Position.Round())
				btn := uv.MouseWheelDown
				if lines < 0 {
					btn = uv.MouseWheelUp
				}
				if p.Term.WheelToApp(uv.MouseWheelEvent{X: c.X, Y: c.Y, Button: btn}) {
					continue
				}
				v.scroll = max(0, min(p.Term.Scrollback(), v.scroll-lines))
			}
		case transfer.DataEvent:
			if r := ev.Open(); r != nil {
				b, _ := io.ReadAll(r)
				r.Close()
				for _, term := range typingTo(t, p) {
					term.Paste(string(b))
				}
			}
		}
	}
	if gtx.Focused(v) {
		if u.copySelection {
			u.copySelection = false
			if s := v.selected(); s != "" {
				gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
				u.showToast("Copied", false)
			}
		}
		if u.pasteWanted {
			u.pasteWanted = false
			gtx.Execute(clipboard.ReadCmd{Tag: v})
		}
	}
	st := clip.Rect(inner.Inset(-gtx.Dp(8))).Push(gtx.Ops)
	event.Op(gtx.Ops, v)
	pointer.CursorText.Add(gtx.Ops)
	st.Pop()
}

// line is row y as last drawn, trailing spaces trimmed.
func (v *termView) line(y int) []rune {
	if y < 0 || y >= len(v.runs) {
		return nil
	}
	var line []rune
	for _, r := range v.runs[y] {
		for len(line) < r.X {
			line = append(line, ' ')
		}
		line = append(line, []rune(r.Text)...)
	}
	return []rune(strings.TrimRight(string(line), " "))
}

// selected is the text under the selection, trailing spaces trimmed.
func (v *termView) selected() string {
	lo, hi := order(v.sel[0], v.sel[1])
	if lo == hi {
		return ""
	}
	var lines []string
	for y := lo.Y; y <= hi.Y && y < len(v.runs); y++ {
		line := v.line(y)
		x0, x1 := 0, len(line)
		if y == lo.Y {
			x0 = min(lo.X, len(line))
		}
		if y == hi.Y {
			x1 = min(hi.X, len(line))
		}
		lines = append(lines, strings.TrimRight(string(line[x0:max(x0, x1)]), " "))
	}
	return strings.Join(lines, "\n")
}

// connOverlay asks what to do with a connection that isn't up: a card with
// ssh's error once it failed, a compact one while it connects, is slow or
// redials.
func (u *ui) connOverlay(gtx layout.Context, area image.Rectangle, p guiPane) {
	paint.FillShape(gtx.Ops, color.NRGBA{11, 12, 15, 0xc7}, clip.Rect(area).Op())
	press := func(k string) func() { return func() { u.d.press(k) } }
	text := func(s string, c color.NRGBA, f font.Font) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, s, 12, c, f, 0) })
	}
	title, sub, guide := connText(p, gtx.Now)
	var kids []layout.FlexChild
	ss := p.Term.Steps() // live: they come with the dial, not with a snapshot
	steps := func() {
		if len(ss) > 0 {
			kids = append(kids, layout.Rigid(vgap(10)), layout.Rigid(u.connSteps(ss, gtx.Now)))
		}
	}
	if p.Conn == "failed" {
		head := []layout.FlexChild{layout.Rigid(u.lbl(title, 15, cText, fBold(sansFont)))}
		if sub != "" {
			head = append(head, layout.Rigid(vgap(3)), text(sub, cText3, sansFont))
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(iconTile(32, 16, rSheet, mix(cRed, cPane, 0.8), cRedText, icoNoLink)), layout.Rigid(hgap(12)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, head...)
				}))
		}))
		steps()
		if c := p.KeyChange; c != nil { // the key sent and the one known, to compare
			lines := []string{fmt.Sprintf("sent now  %-8s %s", c.Type, c.Remote)}
			for _, o := range c.Old {
				lines = append(lines, fmt.Sprintf("known     %-8s %s", cmpOr(o.Type, "?"), cmpOr(o.Print, "(unreadable)")),
					fmt.Sprintf("          in %s:%d", tildeHome(o.File), o.Line))
			}
			kids = append(kids, layout.Rigid(vgap(12)), layout.Rigid(boxed(cPane, cLine, rCard, layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return u.label(gtx, strings.Join(lines, "\n"), 12, cText, monoFont, 6)
			})))
		} else if p.Err != "" {
			kids = append(kids, layout.Rigid(vgap(12)), layout.Rigid(boxed(cPane, cLine, rCard, layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return u.label(gtx, p.Err, 12, cRedText, monoFont, 4)
			})))
		}
		kids = append(kids, layout.Rigid(vgap(10)), text(guide, cText2, sansFont), layout.Rigid(vgap(14)))
		row := []layout.FlexChild{u.kb("connC", kit.Ghost, "Close pane", "C", press("c")), spacer()}
		if p.CanFix {
			row = append(row, u.kb("connF", kit.Secondary, "Fix algorithms", "F", press("f")), layout.Rigid(hgap(8)))
		}
		if p.KeyChange != nil && len(p.KeyChange.Old) > 0 {
			row = append(row, u.kb("connA", kit.Danger, "Accept new key", "A", press("a")), layout.Rigid(hgap(8)))
		}
		row = append(row, u.kb("connR", kit.Primary, "Reconnect", "R", press("r")))
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
		}))
	} else {
		tick := time.Second // the seconds count up
		if p.Conn == "connecting" {
			tick = 100 * time.Millisecond // and the running step's time
		}
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(tick)})
		row := []layout.FlexChild{u.kb("connC", kit.Ghost, "Close", "C", press("c")), spacer()}
		switch p.Conn {
		case "timeout":
			row = append(row, u.kb("connW", kit.Primary, "Keep waiting", "W", press("w")))
		case "reconnecting":
			row = append(row, u.kb("connR", kit.Secondary, "Retry now", "R", press("r")))
		}
		kids = append(kids, layout.Rigid(u.lbl(title, 14, cText, fBold(sansFont))), layout.Rigid(vgap(6)))
		if p.Conn != "timeout" && p.Conn != "reconnecting" { // connecting: how far into the wait
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				w, h := gtx.Constraints.Max.X, gtx.Dp(3)
				f := min(1, gtx.Now.Sub(p.Since).Seconds()/connectWait.Seconds())
				fillRRect(gtx.Ops, image.Rect(0, 0, w, h), gtx.Dp(2), cHover)
				fillRRect(gtx.Ops, image.Rect(0, 0, int(float64(w)*f), h), gtx.Dp(2), cAcc)
				return layout.Dimensions{Size: image.Pt(w, h)}
			}), layout.Rigid(vgap(6)))
		}
		kids = append(kids, text(sub, cText3, sansFont))
		steps()
		kids = append(kids, layout.Rigid(vgap(10)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
		}))
	}
	width, in := unit.Dp(420), layout.Inset{Top: 18, Bottom: 16, Left: 18, Right: 18}
	if p.Conn != "failed" {
		width, in = 360, layout.Inset{Top: 14, Bottom: 14, Left: 16, Right: 16}
	}
	if len(ss) > 0 {
		width = 460 // a step's name and its time on one line
	}
	if p.KeyChange != nil {
		width = 560 // a fingerprint per line
	}
	st := op.Offset(area.Min).Push(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints = layout.Exact(area.Size())
	layout.Center.Layout(cgtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(24), gtx.Dp(width))
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
		return boxed(cPanel, cRim, rSheet, in, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		})(gtx)
	})
	st.Pop()
}

// connText is the connection box's title, the line under it and, once ssh
// failed, what to do about it.
func connText(p guiPane, now time.Time) (title, sub, guide string) {
	secs := func(d time.Duration) string { return fmt.Sprintf("%ds", max(0, int(d.Seconds()))) }
	switch p.Conn {
	case "failed":
		title, guide = "Couldn't connect to "+p.Name, "The connection ended before the shell started. If the server is up, check the VPN; the scrollback stays as it was."
		if p.Tries > 0 {
			sub = fmt.Sprintf("Tried %d times", p.Tries)
		}
		if p.CanFix {
			guide = "The server offers only algorithms modern SSH turns off. Fix adds them to its ssh_config and reconnects."
		}
		if p.KeyChange != nil {
			title = p.Name + "'s host key changed"
			guide = "It sent another key than the one you have for it. If it was reinstalled or its keys rotated, accept the new one: " +
				"desk drops the old line from known_hosts, saves the key it sent and connects. Not expecting a change? Don't: someone may be in between."
		}
	case "timeout":
		title, sub = p.Name+" isn't answering yet", secs(now.Sub(p.Since))+" and counting. A VPN may be down."
	case "connecting": // its steps show under it, so no Keep waiting
		title, sub = "Connecting to "+p.Name+"…", secs(now.Sub(p.Since))
		if now.Sub(p.Since) > connectWait {
			sub += " and counting. A VPN may be down."
		}
	case "reconnecting":
		title, sub = "Reconnecting to "+p.Name+"…", "The connection dropped "+secs(now.Sub(p.Since))+" ago"
		if p.Tries > 0 {
			sub += fmt.Sprintf(" · tried %d times, next in %s", p.Tries, secs(p.Retry.Sub(now)))
		}
	default:
		title, sub = "Connecting to "+p.Name+"…", "Waiting for the server · "+secs(now.Sub(p.Since))
	}
	return title, sub, guide
}

// connSteps lists a dial's steps with their times (the running one's
// counting), and the total once it connected.
func (u *ui) connSteps(ss []connStep, now time.Time) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		var rows []layout.FlexChild
		for _, s := range ss {
			mark, mc, nc := "✓", cGreen, cText2
			switch {
			case s.Err != "":
				mark, mc, nc = "✕", cRedText, cRedText
			case s.End.IsZero():
				mark, mc, nc = "•", cAcc, cText
			}
			if s.User {
				nc = cText3 // the user's time, not the network's
			}
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.Y = 0
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Dp(16)
						return u.label(gtx, mark, 12, mc, sansFont, 1)
					}),
					layout.Flexed(1, u.lblFill(s.Name, 12, nc, sansFont)),
					layout.Rigid(hgap(10)),
					layout.Rigid(u.lbl(fmtDur(s.dur(now)), 12, cText3, monoFont)),
				)
			}), layout.Rigid(vgap(3)))
		}
		if t := stepsTotal(ss); t != "" {
			rows = append(rows, layout.Rigid(vgap(3)), layout.Rigid(u.lbl(t, 12, cText, fSemi(sansFont))))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	}
}

// lastLine is s's last line with text: what ssh said as it ended.
// ponytail: whatever printed last, ssh's error in practice; match ssh's
// message shapes if a remote banner ever ends up there.
func lastLine(s string) string {
	ls := strings.Split(strings.TrimRight(s, " \n"), "\n")
	return strings.TrimSpace(ls[len(ls)-1])
}

// termKey turns a Gio key press into the terminal key it means. Printable
// text comes as EditEvent instead, except with Ctrl/Alt held.
func termKey(e key.Event) (uv.KeyPressEvent, bool) {
	if e.State != key.Press {
		return uv.KeyPressEvent{}, false
	}
	var k uv.KeyPressEvent
	if e.Modifiers.Contain(key.ModCtrl) {
		k.Mod |= uv.ModCtrl
	}
	if e.Modifiers.Contain(key.ModAlt) {
		k.Mod |= uv.ModAlt
	}
	if e.Modifiers.Contain(key.ModShift) {
		k.Mod |= uv.ModShift
	}
	if e.Name == key.NameSpace && k.Mod&(uv.ModCtrl|uv.ModAlt) == 0 {
		return uv.KeyPressEvent{}, false // a plain space also arrives as EditEvent text
	}
	if c, ok := specialKeys[e.Name]; ok {
		k.Code = c
		if c != uv.KeyTab {
			k.Mod &^= uv.ModShift
		}
		return k, true
	}
	if k.Mod&(uv.ModCtrl|uv.ModAlt) == 0 {
		return uv.KeyPressEvent{}, false
	}
	r := []rune(strings.ToLower(string(e.Name)))
	if len(r) != 1 {
		return uv.KeyPressEvent{}, false
	}
	k.Code, k.Mod = r[0], k.Mod&^uv.ModShift
	if k.Mod&uv.ModCtrl == 0 {
		k.Text = string(r[0])
	}
	return k, true
}

var specialKeys = map[key.Name]rune{
	key.NameReturn: uv.KeyEnter, key.NameEnter: uv.KeyEnter, key.NameEscape: uv.KeyEscape, key.NameTab: uv.KeyTab,
	key.NameDeleteBackward: uv.KeyBackspace, key.NameDeleteForward: uv.KeyDelete, key.NameUpArrow: uv.KeyUp,
	key.NameDownArrow: uv.KeyDown, key.NameLeftArrow: uv.KeyLeft, key.NameRightArrow: uv.KeyRight,
	key.NameHome: uv.KeyHome, key.NameEnd: uv.KeyEnd, key.NamePageUp: uv.KeyPgUp, key.NamePageDown: uv.KeyPgDown,
	key.NameSpace: uv.KeySpace, key.NameF1: uv.KeyF1, key.NameF2: uv.KeyF2, key.NameF3: uv.KeyF3, key.NameF4: uv.KeyF4,
	key.NameF5: uv.KeyF5, key.NameF6: uv.KeyF6, key.NameF7: uv.KeyF7, key.NameF8: uv.KeyF8, key.NameF9: uv.KeyF9,
	key.NameF10: uv.KeyF10, key.NameF11: uv.KeyF11, key.NameF12: uv.KeyF12,
}

// Text drawing for the grid: runs snapped to cells, in chunks under Gio's
// 4096px rasterization limit.

func cellMetrics(sh *text.Shaper, f font.Font, size fixed.Int26_6, gl *[]text.Glyph) (w, h, ascent int) {
	sh.LayoutString(text.Parameters{Font: f, PxPerEm: size, MaxWidth: 1 << 24}, "M")
	g, _ := sh.NextGlyph()
	for _, ok := sh.NextGlyph(); ok; _, ok = sh.NextGlyph() {
	}
	return g.Advance.Round(), (g.Ascent + g.Descent).Ceil(), g.Ascent.Ceil()
}

func shape(sh *text.Shaper, f font.Font, size fixed.Int26_6, s string, gl *[]text.Glyph) {
	sh.LayoutString(text.Parameters{Font: f, PxPerEm: size, MaxWidth: 1 << 24, MaxLines: 1}, s)
	*gl = (*gl)[:0]
	for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
		*gl = append(*gl, g)
	}
}

func drawRun(ops *op.Ops, sh *text.Shaper, f font.Font, size fixed.Int26_6, s string, x, baseline, cw int, c color.NRGBA, gl *[]text.Glyph) {
	if r := []rune(s); len(r) > 128 {
		drawRun(ops, sh, f, size, string(r[:128]), x, baseline, cw, c, gl)
		drawRun(ops, sh, f, size, string(r[128:]), x+128*cw, baseline, cw, c, gl)
		return
	}
	shape(sh, f, size, s, gl)
	for i := range *gl {
		(*gl)[i].X = fixed.I(i * cw)
	}
	paintGlyphs(ops, sh, x, baseline, c, *gl)
}

func drawText(ops *op.Ops, sh *text.Shaper, f font.Font, size fixed.Int26_6, s string, x, baseline int, c color.NRGBA, gl *[]text.Glyph) {
	shape(sh, f, size, s, gl)
	paintGlyphs(ops, sh, x, baseline, c, *gl)
}

func textWidth(sh *text.Shaper, f font.Font, size fixed.Int26_6, s string, gl *[]text.Glyph) int {
	shape(sh, f, size, s, gl)
	w := fixed.I(0)
	for _, g := range *gl {
		w += g.Advance
	}
	return w.Ceil()
}

func paintGlyphs(ops *op.Ops, sh *text.Shaper, x, baseline int, c color.NRGBA, gl []text.Glyph) {
	if len(gl) == 0 {
		return
	}
	t := op.Offset(image.Pt(x, baseline)).Push(ops)
	o := clip.Outline{Path: sh.Shape(gl)}.Op().Push(ops)
	paint.ColorOp{Color: c}.Add(ops)
	paint.PaintOp{}.Add(ops)
	o.Pop()
	if call := sh.Bitmaps(gl); call != (op.CallOp{}) {
		call.Add(ops)
	}
	t.Pop()
}

var _ = unit.Dp(0)
