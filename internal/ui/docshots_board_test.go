package ui

import (
	"image"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
)

// The "The board" and "Cards" sections' shots.
func init() {
	scenes = append(scenes,
		scene{"board", func(s *shooter) {
			s.shot("board") // the home page's: no rings
			s.shot("board-overview",
				cut{"toolbar", dp(0, 0, 1600, 45), false},
				cut{"header", dp(2, 2, 264, 44), false},
				cut{"actions", around(union(s.at("tbArchive"), s.at("tbNew")), 6), false},
				cut{"column", boardColHead(s, 1), true},
				cut{"add", around(union(s.at("add1"), s.at("qa1")), 4), false},
			)
		}},
		scene{"board-nosidebar", func(s *shooter) {
			s.click("tbSide")
			s.move(boardAway) // off the button: no tooltip
			s.settle()
			s.shot("board-nosidebar", cut{"toggle", around(s.at("tbSide"), 6), true})
		}},
		scene{"board-drag", func(s *shooter) {
			from := boardCardRect(s, "Dark mode for the dashboard")
			to := boardColRect(s, 2)
			p := center(from)
			q := f32.Pt(center(to).X, float32(boardCardRect(s, "Fix the flaky checkout e2e test").Max.Y)+30)
			s.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p},
				pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p})
			for i := 1; i <= 8; i++ {
				at := p.Add(q.Sub(p).Mul(float32(i) / 8))
				s.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at})
			}
			s.shot("board-drag", cut{"columns", around(union(boardColRect(s, 1), boardColRect(s, 2)), 4), false})
			s.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: q})
		}},
		scene{"board-sidebar", func(s *shooter) {
			go func() { s.u.checkTickets(); s.u.checkAlerts(); s.u.checkCI() }()
			s.wait("tickets, alerts and CI", 10*time.Second, func() bool {
				return len(s.u.tickets) > 0 && len(s.u.alerts) > 0 && len(s.u.ci) > 0 && len(s.u.loading) == 0
			})
			s.move(boardAway)
			s.settle()
			s.shot("board-sidebar",
				cut{"top", dp(0, 45, 233, 950), false},
				cut{"all", boardSideSection(s, "side", "side"), false},
				cut{"agents", image.Rect(0, s.at("side").Max.Y+6, scaled(image.Pt(233, 0)).X, s.at("sidesess:"+boardCard(s, "SSO for the admin panel").ID).Max.Y+6), false},
				cut{"status", boardSideSection(s, "sidest:"+kanban.StatusWorking, "sidest:"+kanban.StatusFailed), false},
				cut{"orgs", boardSideSection(s, "sideorg:Acme", "sidetk:Initech"), false},
				cut{"reload", around(union(s.at("sideOrgReload"), s.at("sideOrgCfg")), 4), true},
			)
			boardSideEnd(s) // the rest of the sidebar
			s.shot("board-sidebar-end",
				cut{"bottom", dp(0, 45, 233, 950), false},
				cut{"labels", boardSideSection(s, "sidelab:api", "sidelab:web"), false},
				cut{"links", boardSideSection(s, "sideln:review", "sideln:tickets"), false},
				cut{"due", boardSideSection(s, "sidedue:over", "sidedue:week"), false},
				cut{"archive", boardSideSection(s, "side"+viewArchive, "side"+viewArchive), false},
			)
		}},
		scene{"board-search", func(s *shooter) {
			s.key("/", 0)
			s.typ("'acme")
			s.shot("board-search",
				cut{"box", boardSearchBox(s), true},
				cut{"count", boardColHead(s, 2), false},
			)
			s.u.search.SetText("")
			s.typ("#api | #tests")
			s.shot("board-search-labels", cut{"box", boardSearchBox(s), false})
		}},
		scene{"board-view", func(s *shooter) {
			s.click("sidest:" + kanban.StatusWaiting)
			s.move(boardAway)
			s.settle()
			s.shot("board-view",
				cut{"row", around(s.at("sidest:"+kanban.StatusWaiting), 2), true},
				cut{"header", dp(2, 2, 264, 44), false},
			)
		}},
		scene{"board-archive", func(s *shooter) {
			id := boardCard(s, "Fix the flaky checkout e2e test").ID // the card moves to the archive: keep its id, not a pointer
			s.clickAt(center(boardCardRect(s, "Fix the flaky checkout e2e test")), pointer.ButtonSecondary)
			s.click("cardMenuArchive")
			s.move(boardAway)
			s.settle()
			s.shot("board-archive-undo", cut{"toast", boardToast(s), true})
			boardSideEnd(s)
			s.click("side" + viewArchive)
			all, list := around(s.at("tbArchive"), 6), around(union(s.at("archChkGroupToday"), s.at("arch"+archivedID(s, "Rotate the API keys"))), 6)
			s.move(boardAway) // finding buttons hovers them
			s.settle()
			s.shot("board-archive", cut{"all", all, true}, cut{"list", list, false})
			s.click("archChkGroupYesterday")
			s.click("archChk" + id)
			s.move(boardAway)
			s.settle()
			s.shot("board-archive-bulk", cut{"bar", around(s.at("archBulkDelete"), 6), true})
			s.click("archBulkClear")
			s.click("arch" + archivedID(s, "Rotate the API keys")) // the demo's card with notes and a chat
			s.move(boardAway)
			s.settle()
			s.shot("board-archive-preview", cut{"actions", around(s.at("archPvRestore"), 6), true})
		}},
		scene{"board-palette", func(s *shooter) {
			s.key("K", key.ModShortcut)
			s.shot("board-palette", cut{"box", dp(444, 78, 1156, 656), false})
			s.typ("acme")
			s.shot("board-palette-search", cut{"box", dp(444, 78, 1156, 494), false})
			s.key(key.NameTab, 0)
			s.key(key.NameTab, 0)
			s.key(key.NameTab, 0)
			s.key(key.NameTab, 0) // Actions
			s.u.pal.q.SetText("")
			s.settle()
			s.shot("board-palette-actions", cut{"box", dp(444, 78, 1156, 580), false})
		}},

		// Cards
		scene{"cards-anatomy", func(s *shooter) {
			s.move(boardAway)
			s.shot("cards-anatomy",
				cut{"working", around(boardCardRect(s, "Migrate the billing exports to S3"), 6), false},
				cut{"todo", around(boardCardRect(s, "Add rate limiting to the public API"), 6), false},
				cut{"blocked", around(boardCardRect(s, "SSO for the admin panel"), 6), false},
			)
		}},
		scene{"cards-menu", func(s *shooter) {
			s.clickAt(center(boardCardRect(s, "Migrate the billing exports to S3")), pointer.ButtonSecondary)
			s.hover("cardMenuOpen session")
			s.shot("cards-menu", cut{"menu", around(union(boardMenuRect(s), boardCardRect(s, "Migrate the billing exports to S3")), 6), false})
			s.hover("cardMenuMove to")
			s.shot("cards-menu-move", cut{"menu", around(boardMenuRect(s), 4), false})
		}},
		scene{"cards-new", func(s *shooter) {
			s.key("N", key.ModShortcut)
			s.shot("cards-new",
				cut{"dialog", cardsDialog(s), false},
				cut{"box", cardsWhatBox(s), false},
				cut{"org", cardsRow(s, union(s.at("pillorg"), s.at("orgInitech")), 6), true},
				cut{"head", around(union(dp(460, 64, 1140, 80), s.at("shClose")), 6), false},
				cut{"foot", around(union(s.at("shOpt"), s.at("shSave")), 8), false},
			)
			s.typ("Renew the TLS certificate on the @Acme api servers before friday, ABC-123 !high #infra")
			s.shot("cards-new-tokens",
				cut{"dialog", cardsDialog(s), false},
				cut{"pills", around(union(s.at("pillorg"), s.at("pillmore")), 6), true},
			)
			s.click("orgAcme")
			s.click("pillmore")
			s.move(boardAway)
			s.settle()
			s.shot("cards-new-more",
				cut{"dialog", cardsDialog(s), false},
				cut{"place", around(union(s.at("orgChange"), s.at("dirapi")), 8), false},
				cut{"ticket", cardsRow(s, union(s.at("ticket0"), s.at("ticket1")).Union(s.at("ticket0").Sub(image.Pt(0, int(24*shotScale)))), 6), true},
			)
			s.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(1200, 1000), Scroll: f32.Pt(0, 2000)})
			s.move(boardAway)
			s.settle()
			s.shot("cards-new-more-end", cut{"dialog", cardsDialog(s), false})
		}},
		scene{"cards-optimize", func(s *shooter) {
			s.key("N", key.ModShortcut)
			s.typ("Login retry fails after the token expires, see ABC-123 @Acme")
			s.shot("cards-optimize-before", cut{"button", around(s.at("shOpt"), 6), true})
			s.key("J", key.ModShortcut)
			cardsUntil(s, 20*time.Second, func() bool {
				e := s.u.edit
				return e != nil && e.optStage == 2 && len(e.optLive.Prompt) > 260
			})
			s.shot("cards-optimize-live", cut{"dialog", cardsDialog(s), false})
			s.wait("the draft", 20*time.Second, func() bool { return s.u.edit != nil && s.u.edit.optimized })
			s.move(boardAway)
			s.settle()
			s.shot("cards-optimize",
				cut{"dialog", cardsDialog(s), false},
				cut{"banner", cardsRow(s, s.at("shAccept"), 10), false},
				cut{"mine", around(union(s.at("shMineTitle"), s.at("shMineNotes")), 6).Union(cardsRow(s, s.at("shMineTitle"), 6)), false},
				cut{"foot", around(union(s.at("shDiscard"), s.at("shSave")), 8), true},
			)
		}},
		scene{"cards-editing", func(s *shooter) {
			c := boardCard(s, "Add rate limiting to the public API")
			c.Notes = "Limit each API key to **100 requests a minute**; answer `429` with a `Retry-After` header past it."
			s.u.save()
			s.u.sel = c.ID
			s.key("E", 0)
			s.move(boardAway)
			s.settle()
			s.shot("cards-editing",
				cut{"drawer", dp(1080, 0, 1600, 950), false},
				cut{"head", around(union(s.at("shSess"), s.at("shClose")), 6), true},
				cut{"props", around(union(s.at("col0"), s.at("prio3")), 6).Union(cardsRow(s, s.at("col0"), 6)), false},
			)
		}},
		scene{"cards-links", func(s *shooter) {
			c := boardCard(s, "Add rate limiting to the public API")
			c.Links = []kanban.Link{
				{URL: "https://acme.atlassian.net/browse/ABC-131", Kind: links.KindJira, Ref: "ABC-131", State: "In Review", Tone: links.ToneWait},
				{URL: "https://github.com/acme-corp/api/pull/615", Kind: links.KindGitHubPR, Ref: "PR #615", State: "approved — merge · CI passed", Tone: links.ToneGood},
				{URL: "https://github.com/acme-corp/api/pull/609", Kind: links.KindGitHubPR, Ref: "PR #609", State: "changes requested", Tone: links.ToneBad},
				{URL: "https://github.com/acme-corp/api/issues/77", Kind: links.KindGitHubIssue, Ref: "#77", State: "closed", Tone: links.ToneDone},
				{URL: "https://gitlab.example.com/acme/gateway/-/merge_requests/12", Kind: links.KindGitLabMR, Ref: "MR !12", State: "no GitLab account", Tone: links.ToneWait},
			}
			c.Artifacts = []string{"https://claude.ai/public/artifacts/3f2a9c1e-6b0d-4c5e-9a1f-2d7b8e4c0a11"}
			s.u.save()
			s.move(boardAway)
			s.settle()
			s.shot("cards-links", cut{"card", around(boardCardRect(s, c.Title), 6), false})
			s.u.openPanel(c.ID)
			s.move(boardAway)
			s.settle()
			var rs []image.Rectangle
			for _, l := range c.Links {
				rs = append(rs, s.at("pnLink"+c.ID+l.URL))
			}
			rs = append(rs, s.at("pnArt"+c.ID+c.Artifacts[0]))
			s.move(boardAway)
			s.settle()
			ls := union(rs...)
			s.shot("cards-links-panel", cut{"links", image.Rect(ls.Min.X-int(16*shotScale), scaled(image.Pt(0, 46)).Y, scaled(shotSize).X, ls.Max.Y+int(12*shotScale)), false})
		}},
	)
}

// boardAway is where the pointer waits, over nothing that hovers: the
// window's bottom right corner.
var boardAway = f32.Pt(2395, 1420)

// boardOrigin is where the board's own coordinates (u.lay) start on screen.
func boardOrigin(s *shooter) image.Point {
	if s.u.side {
		return scaled(image.Pt(233, 45))
	}
	return scaled(image.Pt(0, 45))
}

// boardCard is the card titled title on the board.
// archivedID is the id of the archived card titled title.
func archivedID(s *shooter, title string) string {
	s.t.Helper()
	for _, c := range s.u.b.Archive {
		if c.Title == title {
			return c.ID
		}
	}
	s.t.Fatalf("no archived card %q", title)
	return ""
}

func boardCard(s *shooter, title string) *kanban.Card {
	s.t.Helper()
	for ci := range s.u.b.Columns {
		for j := range s.u.b.Columns[ci].Cards {
			if c := &s.u.b.Columns[ci].Cards[j]; c.Title == title {
				return c
			}
		}
	}
	s.t.Fatalf("no card %q", title)
	return nil
}

// boardCardRect is where card title is on screen, from the last frame.
func boardCardRect(s *shooter, title string) image.Rectangle {
	s.t.Helper()
	id := boardCard(s, title).ID
	s.frame()
	for _, h := range s.u.lay.cards {
		if h.id == id {
			return h.r.Add(boardOrigin(s))
		}
	}
	s.t.Fatalf("card %q is not on screen", title)
	return image.Rectangle{}
}

// boardColRect is column i on screen, header included.
func boardColRect(s *shooter, i int) image.Rectangle {
	s.frame()
	r := s.u.lay.cols[i]
	r.Min.Y -= int(42 * shotScale)
	return r.Add(boardOrigin(s))
}

// boardColHead is column i's header and its first cards.
func boardColHead(s *shooter, i int) image.Rectangle {
	r := boardColRect(s, i)
	r.Max.Y = r.Min.Y + int(42*shotScale)
	return around(r, 4)
}

// boardSideSection is the sidebar's rows from first to last (button ids),
// with the section's title above them.
func boardSideSection(s *shooter, first, last string) image.Rectangle {
	a, b := s.at(first), s.at(last)
	return image.Rect(0, a.Min.Y-int(22*shotScale), scaled(image.Pt(233, 0)).X, b.Max.Y+int(4*shotScale))
}

// boardSideEnd scrolls the sidebar to its end with the wheel.
func boardSideEnd(s *shooter) {
	s.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(170, 900), Scroll: f32.Pt(0, 3000)})
	s.move(boardAway)
	s.settle()
}

// boardSearchBox is the toolbar's search field.
func boardSearchBox(s *shooter) image.Rectangle {
	a, n := s.at("tbArchive"), s.at("tbNew")
	return around(image.Rect(a.Max.X+int(4*shotScale), n.Min.Y, n.Min.X-int(6*shotScale), n.Max.Y), 4)
}

// boardToast is the band at the window's foot where toasts show.
func boardToast(s *shooter) image.Rectangle {
	u := s.at("toastUndo")
	return image.Rect(int(460*shotScale), u.Min.Y-int(12*shotScale), int(1140*shotScale), u.Max.Y+int(12*shotScale))
}

// boardMenuRect is the open card menu (and its submenu) on screen.
func boardMenuRect(s *shooter) image.Rectangle {
	s.frame()
	return s.u.menu.r.Add(boardOrigin(s))
}

// cardsDialog is the new-card dialog: 680 dp wide from 64 dp down, to its
// footer.
func cardsDialog(s *shooter) image.Rectangle {
	f := s.at("shOpt") // Cancel is off while Improve runs
	return image.Rect(int(452*shotScale), int(56*shotScale), int(1148*shotScale), f.Max.Y+int(20*shotScale))
}

// cardsWhatBox is the new-card dialog's what-to-do box and its hint.
func cardsWhatBox(s *shooter) image.Rectangle {
	h, p := s.at("shClose"), s.at("pillorg")
	return image.Rect(int(468*shotScale), h.Max.Y+int(8*shotScale), int(1132*shotScale), p.Min.Y-int(4*shotScale))
}

// cardsRow widens r to the dialog's or drawer's row it sits in, pad dp
// around it.
func cardsRow(s *shooter, r image.Rectangle, pad int) image.Rectangle {
	x0, x1 := int(460*shotScale), int(1140*shotScale)
	if r.Min.X > int(1080*shotScale) { // the drawer, without the board beside it
		g := around(image.Rect(int(1080*shotScale), r.Min.Y, int(1600*shotScale), r.Max.Y), pad)
		g.Min.X = int(1082 * shotScale)
		return g
	}
	return around(image.Rect(x0, r.Min.Y, x1, r.Max.Y), pad)
}

// cardsUntil runs frames until ok, without settling after: for a moment
// that passes (Improve writing).
func cardsUntil(s *shooter, timeout time.Duration, ok func() bool) {
	s.t.Helper()
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		s.frame()
		if ok() {
			return
		}
	}
	s.t.Fatalf("waited %v", timeout)
}
