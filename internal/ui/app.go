// Package ui is the desk window: the sidebar of views, the Kanban board
// (or the archive), a card's chat panel, the card sheet and toasts. It owns
// the board on Gio's event goroutine and saves every change right away;
// Claude sessions and link checks report back through u.events.
package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/x/explorer"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
	"github.com/lfdominguez/desk/internal/vault"
)

type ui struct {
	*pane    // the window being laid out's own state: mainPane, or a detached session's (detach.go)
	mainPane pane
	detached map[string]*detached // cards whose session has its own window, by card
	mu       sync.Mutex           // held by each window's event loop while it handles an event: they share u
	closing  bool                 // desk is quitting: its detached windows close without reattaching
	w        *app.Window
	// reopen applies the window's options again before a lost window opens
	// anew (Gio drops them once used); drew says the window drew since.
	reopen func()
	drew   bool
	k      *kit.Kit
	b      *kanban.Board
	path   string // where b is saved

	root, home string   // the organizations' folder; the user's home (Claude profiles)
	orgList    []string // the organizations, read at start

	view       string // the sidebar's pick (see views.go); "" all cards
	side       bool   // the sidebar shows
	search     widget.Editor
	sel, hover string      // the keyboard's card; the one under the pointer
	lastOrg    string      // the organization of the last card saved: new cards start there
	scroll     []int       // each column's scroll, px
	maxScroll  []int       // and its limit, from the last frame
	lay        boardLayout // last frame's geometry, for the pointer and keys
	drag       dragState
	edit       *sheet // the card sheet, nil when closed

	panel      string // the card whose chat panel is open, "" none
	sideList   layout.List
	archList   layout.List
	arch       archState                     // the archive's grouping and filter chips
	shellOut   map[string]string             // by card: its "!" commands and what they printed, for Claude's next message
	peerOut    map[string]string             // by card: the infra Claude's events it couldn't take yet (cardTell)
	shellStop  map[string]context.CancelFunc // by card: its "!" command running, stopped by this
	chats      map[string][]kanban.ChatMsg   // conversations read so far, by card
	procs      map[string]session            // running sessions, by card
	pending    map[string]claude.Directives  // DESK- lines seen in a turn, applied when it ends
	events     chan func()                   // work from other goroutines, run on the next frame
	polling    bool                          // a link check is out
	locked     map[string]*vault.LockedError // vaults waiting for a password or code, by vault.Key
	unlock     *unlockDialog                 // asking for one, nil when closed
	cfg        *cfgDialog                    // an organization's settings, nil when closed
	tickets    map[string][]links.Ticket     // open tickets assigned to the user, by org (with a tracker)
	ticketErr  map[string]string             // what a tracker said instead, by org
	tkGen      int                           // bumps when tickets arrive, for sig
	tkList     layout.List
	alerts     map[string][]links.Alert // its Nagios's warnings and errors, by org (with one); tkGen bumps
	alertErr   map[string]string        // what a Nagios said instead, by org
	alList     layout.List
	ci         map[string][]ciItem // the CI of its cards' requests and commits, by org (with any); tkGen bumps
	cardSock   string              // where card sessions' desk tools call (cardtools.go); "" in tests
	ciList     layout.List
	infra      *infra.Workspace              // the servers; nil in tests
	tree       []infra.Org                   // its host tree, read once a frame
	infraTasks []infra.Task                  // and the server tasks cards handed it
	infraOn    bool                          // the workspace is what the user looks at
	pick       string                        // the org whose server picker is open, "" none
	pickQ      widget.Editor                 // its search
	pickList   widget.List                   // its rows, with a scrollbar
	pickSel    int                           // the row up/down moved to, enter opens
	pickLast   string                        // the search pickSel was moved under: a new one starts at the top
	pickFocus  bool                          // focus its search next frame
	srvClickOf string                        // the org whose Servers row was clicked last, and when: a second click soon opens the picker
	srvClickAt time.Time                     //
	terms      map[string]*termTab           // cards' Claude Code TUIs, by card
	login      *signIn                       // an account signing in again, in its dialog
	asks       map[string]*askState          // Claude's open questions and permission requests, by card
	allowed    map[string]map[string]bool    // per card, the tools allowed until desk quits (the ask's S)
	rsels      map[string]*richSel           // chat markdown blocks the mouse selects, by block
	chatSel    *richSel                      // the one with a selection
	selGl      []text.Glyph                  // scratch for shaping them
	termGen    atomic.Int64                  // bumps on terminal output, from its reader
	shownTerm  atomic.Value                  // the card whose terminal the view shows (string), for its reader
	limits     map[string]*planUsage         // plan limits, by Claude account
	limGen     int                           // bumps when limits arrive, for sig
	accts      map[string]string             // which Claude account and login, by account (accountName)
	snoozed    map[string]time.Time          // alerts hidden until then, by alertKey (saved next to the board)
	cardInfra  map[string]string             // cards whose Claude handed server work to an org's infra session: that org
	noSplit    map[string]bool               // cards whose session view shows the chat alone, not beside the infra session
	splitShown bool                          // a session view drew the infra session beside it this frame
	splitFor   string                        // the card and org the split last asked infra to open
	visKey     string                        // what vis was worked out for (board.go visible)
	vis        map[int][]int                 // each column's visible card indices
	cardHs     map[string]cardH              // each card's height as last laid out, to skip the ones scrolled out
	selSeen    map[string]time.Time          // when each selectable text (sels, rsels) was last drawn
	selSwept   time.Time                     // when sweepSels last looked
	countKey   string                        // what counts were worked out for (views.go count)
	counts     map[string]int                // board views' card counts
	snoozeAsk  map[string]string             // cards whose Claude suggests snoozing their alert: why
	snoozing   string                        // the alert whose snooze picker is open in Alerts
	snoozeEd   widget.Editor                 // the pickers' custom duration
	snoozeSel  int                           // the duration Snooze takes when nothing is typed
	tasks      map[string][]*task            // sessions' subagents and background shells, by card
	toolLines  map[string]string             // tool uses' lines, by id: a background shell's command
	toolBy     map[string]string             // the subagent (its tool use) that made a tool use, by id; none: the session
	toolCmds   map[string]string             // Bash tool uses' whole commands, by id
	explains   map[string]*explained         // Haiku's explanations of shell commands, by command
	exGen      int                           // bumps when one is asked or arrives, for sig
	diffs      map[string][]diffLine         // Edits' changes as lines, by message
	diffRowsOf map[string][]diffRow          // their rows, by message, view and width class
	cellsOf    map[cellKey][]cell            // those rows' sides as highlighted characters
	tableNat   map[string][]int              // markdown tables' natural column widths, by block
	diffOpen   map[string]bool               // long changes shown whole, by message
	diffMode   map[string]int                // the view picked for a change (diffUnified…), by message; none Auto
	diffGl     []text.Glyph                  // diffs' glyph buffer
	pushing    map[string]bool               // cards whose tickets are being moved to match them
	loading    map[string]int                // background fetches running, by org: its icon spins
	canvas     map[string]bool               // cards whose full window was switched to Canvas (true) or Conversation (false): canvasOn
	cv         map[string]*cvView            // each card's canvas pan, zoom and pointer
	handoffsOn bool                          // the canvas shows the handoffs beside it
	earlier    map[string]bool               // cards whose canvas shows its long-done agents again
	pushErr    map[string]string             // the last error moving a card's tickets: said once, not every check
	sels       map[string]*widget.Selectable // chat text the mouse selects, by message
	menu       *cardMenu                     // a card's right-click menu, nil when closed

	gen        int           // bumps on every board change (save), for sig
	saveAfter  time.Duration // how long save waits to write: 0 at once (tests)
	dirty      bool          // a change waits to be written (flush)
	scanWait   time.Duration // how long the folders page waits before scanning them, off the UI goroutine: 0 at once (tests)
	toast      string
	toastBad   bool
	toastUntil time.Time
	toastFrom  time.Time // when an undo toast came: its timer bar runs out to toastUntil
	undo       func()    // what the toast's Undo (Ctrl+Z) does; nil for a plain toast
	copyKey    string    // a card key the menu's Copy key put off to the next frame's clipboard write
	pal        *palette  // the Ctrl+K palette (palette.go), nil when closed
	recent     []string  // the palette's items opened last, newest first
	draft      string    // a new card's request left when its dialog closed
	focusLater any       // a field to focus on the next frame
	post       func()    // a click's action run once the frame is drawn (it changes the view)

	ws      map[string]*wsState // cards' workspaces (workspace.go), by card
	wsGen   int                 // bumps on any workspace change, for sig
	wsPaths map[string]string   // the main sessions' Edit and Write uses, by tool use: the file, checked by its open tab when done
	unsaved []wsUnsaved         // tabs with changes not saved the last quit kept, until their workspace opens (workspace_unsaved.go)
	wsEd    orgs.Editor         // the workspace's pane widths and wrap, as the settings keep them (workspace_panes.go)
	wsGrips [2]wsGrip           // the tree's and the chat's edges, being dragged

	draws    map[string]*drawState // open drawings (drawview.go), by file
	libs     map[string]*drawLib   // organizations' libraries (drawlib.go), read once
	drawList map[string][]drawItem // organizations' drawings, read once (drawings.go)
	thumbs   map[string]drawThumb  // their pictures, by file
	drawGen  int                   // bumps when a list changes, for sig
	drList   layout.List
}

func newUI(b *kanban.Board, path string) *ui {
	home, _ := os.UserHomeDir()
	u := &ui{w: new(app.Window), k: kit.New(), b: b, path: path, side: true, root: orgs.Root(), home: home,
		chats: map[string][]kanban.ChatMsg{}, procs: map[string]session{}, pending: map[string]claude.Directives{}, locked: map[string]*vault.LockedError{}, events: make(chan func(), 256),
		tickets: map[string][]links.Ticket{}, ticketErr: map[string]string{}, alerts: map[string][]links.Alert{}, alertErr: map[string]string{}, ci: map[string][]ciItem{}, terms: map[string]*termTab{}, asks: map[string]*askState{}, allowed: map[string]map[string]bool{}, rsels: map[string]*richSel{}, shellOut: map[string]string{}, peerOut: map[string]string{}, shellStop: map[string]context.CancelFunc{}, limits: map[string]*planUsage{}, tasks: map[string][]*task{}, toolLines: map[string]string{}, toolBy: map[string]string{}, toolCmds: map[string]string{}, explains: map[string]*explained{}, diffs: map[string][]diffLine{}, diffRowsOf: map[string][]diffRow{}, cellsOf: map[cellKey][]cell{}, tableNat: map[string][]int{}, selSeen: map[string]time.Time{}, diffOpen: map[string]bool{}, diffMode: map[string]int{}, pushing: map[string]bool{}, loading: map[string]int{}, accts: map[string]string{}, snoozeAsk: map[string]string{}, cardInfra: map[string]string{}, noSplit: map[string]bool{}, canvas: map[string]bool{}, cv: map[string]*cvView{}, earlier: map[string]bool{}, pushErr: map[string]string{}, sels: map[string]*widget.Selectable{}, ws: map[string]*wsState{}, wsPaths: map[string]string{}, draws: map[string]*drawState{}, libs: map[string]*drawLib{}, drawList: map[string][]drawItem{}, thumbs: map[string]drawThumb{}}
	title := "desk"
	if demo {
		title = "desk demo" // apart from a real desk's window (and its window rules)
	}
	opts := []app.Option{app.Title(title), app.Size(unit.Dp(1600), unit.Dp(950)), app.MinSize(unit.Dp(900), unit.Dp(500))}
	u.reopen = func() { u.w.Option(opts...) }
	u.reopen() // the window takes them when it opens
	u.orgList = orgs.List(u.root)
	u.unsaved, u.wsEd = loadUnsaved(path), orgs.EditorSettings()
	u.search.SingleLine, u.search.Submit = true, true
	u.pickQ.SingleLine, u.pickQ.Submit, u.pickList.Axis = true, true, layout.Vertical
	u.pane = &u.mainPane
	u.pane.init()
	u.detached = map[string]*detached{}
	u.snoozeEd.SingleLine, u.snoozeEd.Submit = true, true
	u.loadSnoozed()
	u.drList.Axis = layout.Vertical
	u.sideList.Axis, u.archList.Axis, u.tkList.Axis, u.alList.Axis, u.ciList.Axis = layout.Vertical, layout.Vertical, layout.Vertical, layout.Vertical, layout.Vertical
	for c := range b.Columns { // a session that was on a turn when desk last closed is no longer
		for i := range b.Columns[c].Cards {
			if p := &b.Columns[c].Cards[i]; p.Status == kanban.StatusWorking {
				p.Status, p.Note = kanban.StatusWaiting, "Interrupted: desk was closed"
			}
		}
	}
	u.reopenDetached() // the sessions detached when desk last closed, in their windows again
	return u
}

// Version is desk's release (2026.11.03, a second one that day 2026.11.03.1),
// shown in the settings; main sets it.
var Version = "dev"

// startInfra starts the infra workspace and the card tools' socket.
func (u *ui) startInfra() {
	u.infra = infra.New(u.root, u.w.Invalidate)
	u.infra.OnInvestigate(func(org, host, service, state, output string, since time.Time) {
		u.later(func() { u.investigate(org, host, service, state, output, "", since) })
	})
	if sock, err := infra.ServeCards(u.cardTool); err == nil {
		u.cardSock = sock
	} else {
		u.showToast("Card tools: "+err.Error(), true)
	}
	u.infra.SetCards(infra.Cards{Is: cardKey.MatchString,
		Tell: func(key, text string) error {
			_, err := onBoard(u, func() (string, error) { return "", u.cardTell(key, text) })
			return err
		},
		New: func(org, title, notes string) (string, error) {
			return onBoard(u, func() (string, error) { return u.cardNew(org, title, notes) })
		},
		Draw: func(who, name string, args []byte) (string, bool) { return u.drawTool(who, name, args) }})
}

// Run opens the window on board b and returns when it closes.
func Run(b *kanban.Board, path string) error {
	u := newUI(b, path)
	u.startInfra()
	files := explorer.NewExplorer(u.w)
	pickFile = func(exts ...string) (io.ReadCloser, error) {
		r, err := files.ChooseFile(exts...)
		if errors.Is(err, explorer.ErrUserDecline) {
			return nil, nil
		}
		return r, err
	}
	saveFile = func(name string) (io.WriteCloser, error) {
		w, err := files.CreateFile(name)
		if errors.Is(err, explorer.ErrUserDecline) {
			return nil, nil
		}
		return w, err
	}
	defer u.windowed()()
	defer func() {
		u.quitting()
		if u.infra != nil {
			u.infra.Close() // saves its session: tabs, panes, chats
		}
		for _, p := range u.procs {
			p.Close()
		}
		for _, st := range u.ws {
			if st.proc != nil {
				st.proc.Close()
			}
		}
		for _, tt := range u.terms {
			tt.t.Close()
		}
		for _, st := range u.draws {
			st.drawStop()
		}
	}()
	go u.pollLinks() // its first round brings the remote tickets in step with the board
	return u.loop(func() event.Event { ev := u.w.Event(); files.ListenEvents(ev); return ev })
}

// loop handles the window's events, next's one after another, until it closes.
// A window that closed on an error (the display dropped its connection: a
// monitor switched off and back) opens again, the board and sessions untouched;
// after reopenTries reopened windows in a row that never drew, it gives up.
// It returns holding u.mu: the detached windows' loops wait while desk quits.
func (u *ui) loop(next func() event.Event) error {
	var ops op.Ops
	failed := 0
	for {
		ev := next()
		u.mu.Lock() // a detached window's loop shares u; held on return, so desk's cleanup doesn't race it
		done, err := u.event(ev, &ops)
		if !done {
			u.mu.Unlock()
			continue
		}
		if u.drew {
			failed = 0
		}
		if err == nil {
			return nil
		}
		if failed >= reopenTries {
			u.crashNote("window lost for good: %v", err)
			return err
		}
		if failed == 0 { // the retries' own errors (no display yet) would fill the log
			u.crashNote("window lost, reopening: %v", err)
		}
		failed++
		u.drew = false
		u.mu.Unlock()
		u.windowLost()
	}
}

// crashNote adds a line to crash.log beside the board.
func (u *ui) crashNote(format string, args ...any) {
	if f, err := os.OpenFile(filepath.Join(filepath.Dir(u.path), "crash.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
		f.Close()
	}
}

// reopenWait is the pause before a lost window opens again (a display without
// outputs refuses a window: the wait lets the monitor come back); tests end it.
var reopenWait = func() <-chan time.Time { return time.After(time.Second) }

const reopenTries = 600 // ten minutes of reopenWait

// windowLost waits reopenWait running the work queued meanwhile (Claude's
// events wait on it), then asks for the window again.
func (u *ui) windowLost() {
	for wait := reopenWait(); ; {
		select {
		case f := <-u.events:
			u.mu.Lock()
			f()
			u.mu.Unlock()
			continue
		case <-wait:
		}
		break
	}
	if u.reopen != nil {
		u.reopen()
	}
}

// event handles one of the window's events; done when it closed.
func (u *ui) event(ev event.Event, ops *op.Ops) (done bool, err error) {
	switch e := ev.(type) {
	case app.DestroyEvent:
		return true, e.Err
	case app.ConfigEvent:
		u.focused = e.Config.Focused
	case app.FrameEvent:
		u.drew = true
		gtx := app.NewContext(ops, e)
		tg, ig := u.termGen.Load(), u.infraGen()
		u.frame(gtx)
		e.Frame(gtx.Ops)
		if u.termGen.Load() != tg || u.infraGen() != ig { // output that came after frame's own check: its Invalidate was lost too
			// Gio takes an Invalidate only once its event queue drains (mayInvalidate), which is after this returns.
			// ponytail: a short delay, not a hook into Gio's loop; one more Invalidate after it if 10 ms ever falls short.
			time.AfterFunc(10*time.Millisecond, u.w.Invalidate)
		}
	default: // Gio's wakeup, from later's Invalidate: a window that isn't shown gets no frames, and a Claude's tool waits on this work (onBoard)
		u.drain()
	}
	return false, nil
}

// later runs f on the UI goroutine soon (the next frame or wakeup); safe
// from anywhere.
func (u *ui) later(f func()) {
	u.events <- f
	u.w.Invalidate()
}

// drain runs the work later queued.
func (u *ui) drain() {
	ran := len(u.events) > 0
	for len(u.events) > 0 {
		(<-u.events)()
	}
	if ran {
		u.wakeDetached()
	}
}

// busy marks a background fetch for org running until done is called;
// safe from anywhere.
func (u *ui) busy(org string) (done func()) {
	u.later(func() { u.loading[org]++; u.tkGen++ })
	return func() {
		u.later(func() {
			if u.loading[org]--; u.loading[org] <= 0 {
				delete(u.loading, org)
			}
			u.tkGen++
		})
	}
}

// sig is the UI state a frame draws from, cheaply. Add new state here.
func (u *ui) sig() string {
	return fmt.Sprint(u.gen, u.view, u.side, u.sel, u.panel, u.edit != nil, u.edit != nil && u.edit.optStop != nil, u.search.Text(), u.blur, u.chatFocus, u.unlock != nil, len(u.locked), u.cfg.sig(), u.tkGen, len(u.terms), len(u.asks), u.limGen, u.sub, u.rail, u.selRange(), u.menu != nil, u.exGen, len(u.diffOpen), u.diffOpenCount(), fmt.Sprint(u.canvas), u.handoffsOn, len(u.earlier), u.pick, u.pickSel, fmt.Sprint(u.diffMode), len(u.shellStop), u.palSig(), u.wsGen, u.wsEd, u.wsGrips[0].dragging, u.wsGrips[1].dragging, orgs.Zoom(), u.drawGen, u.drawSig(), u.arch.ver)
}

// frame draws the window. Gio draws only when an event comes, and a click
// or an enter handled mid-layout changes state that parts above already
// drew from, so a frame that changed the state asks for one more.
func (u *ui) frame(gtx layout.Context) {
	tg, ig := u.termGen.Load(), u.infraGen()
	u.drain()
	u.reapTerms()
	u.loginFrame()
	u.infraFrame()
	id, _ := u.termCard()
	u.shownTerm.Store(id)
	u.sweepSels(gtx.Now)
	before := u.sig() // after the work above: this frame draws it, only what layout changes needs another
	defer func() {    // also when work, terminal output or a new infra snapshot arrived meanwhile: its Invalidate, sent mid-frame, is lost
		if u.sig() != before || len(u.events) > 0 || u.termGen.Load() != tg || u.infraGen() != ig {
			gtx.Execute(op.InvalidateCmd{})
		}
	}()
	u.keys(gtx)
	if u.blur {
		gtx.Execute(key.FocusCmd{})
		u.blur = false
	}
	if u.panel != "" && u.b.Card(u.panel) == nil {
		u.panel = ""
	}
	kit.Fill(gtx, kit.CLine)
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(u.toolbar),
		layout.Rigid(kit.VGap(1)),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			var kids []layout.FlexChild
			if u.side {
				kids = append(kids, layout.Rigid(u.sidebar), layout.Rigid(kit.HGap(1)))
			}
			main := u.board
			if u.view == viewArchive {
				main = u.archive
			} else if strings.HasPrefix(u.view, "tk:") {
				main = u.ticketsView
			} else if strings.HasPrefix(u.view, "al:") {
				main = u.alertsView
			} else if strings.HasPrefix(u.view, "ci:") {
				main = u.ciView
			} else if strings.HasPrefix(u.view, "srv:") {
				main = u.serversView
			} else if strings.HasPrefix(u.view, "term:") {
				main = u.termView
			} else if strings.HasPrefix(u.view, "sess:") {
				main = u.sessionView
			} else if strings.HasPrefix(u.view, "ws:") {
				main = u.wsView
			} else if strings.HasPrefix(u.view, "dr:") || strings.HasPrefix(u.view, "drc:") {
				main = u.drawingsView
			} else if strings.HasPrefix(u.view, "draw:") {
				main = u.drawView
			}
			kids = append(kids, layout.Flexed(1, main))
			if u.panel != "" && !strings.HasPrefix(u.view, "term:") && !strings.HasPrefix(u.view, "sess:") && !strings.HasPrefix(u.view, "ws:") && !strings.HasPrefix(u.view, "draw:") {
				kids = append(kids, layout.Rigid(kit.HGap(1)), layout.Rigid(u.chatPanel))
			}
			return layout.Flex{}.Layout(gtx, kids...)
		}))
	u.sheet(gtx)
	u.configDialog(gtx)
	u.unlockDialog(gtx)
	u.serverPicker(gtx)
	u.paletteDialog(gtx)
	u.toasts(gtx)
	if f := u.post; f != nil {
		u.post = nil
		f()
	}
}

// save writes the board; a failure shows rather than losing the change
// quietly. Every board change goes through it, so it also marks one (gen).
// With saveAfter (desk's window: a Claude stream changes the board several
// times a second) the write waits that long, so a burst is one write.
func (u *ui) save() {
	u.gen++
	if u.saveAfter == 0 {
		u.flush()
	} else if !u.dirty {
		u.dirty = true
		time.AfterFunc(u.saveAfter, func() { u.later(u.flush) })
	}
}

// windowed sets what only desk's window wants (tests keep the defaults):
// board writes and folder scans that wait, and a kill (SIGTERM, Ctrl+C)
// that still writes the board. stop lets go of the signals.
func (u *ui) windowed() (stop func()) {
	u.saveAfter, u.scanWait = 500*time.Millisecond, 300*time.Millisecond
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, os.Interrupt)
	go u.quitOn(quit, os.Exit)
	return func() { signal.Stop(quit) }
}

// flushPending writes a change still waiting (saveAfter), before quitting.
func (u *ui) flushPending() {
	if u.dirty {
		u.flush()
	}
}

// quitOn waits for a signal on c (desk killed rather than closed), has the
// UI goroutine write what's waiting (or gives up after a while: a hidden
// window may draw no frame), then exits.
func (u *ui) quitOn(c <-chan os.Signal, exit func(int)) {
	<-c
	done := make(chan bool)
	u.later(func() {
		u.quitting()
		close(done)
	})
	select {
	case <-done:
	case <-time.After(quitWait):
	}
	exit(1)
}

// quitWait is how long quitOn waits for the UI goroutine.
var quitWait = 2 * time.Second

// flush writes the board now, and pushes the tickets whose card moved.
func (u *ui) flush() {
	u.dirty = false
	u.syncTickets(false)
	if err := u.b.Save(u.path); err != nil {
		u.showToast("Not saved: "+err.Error(), true)
	}
}

func (u *ui) toolbar(gtx layout.Context) layout.Dimensions {
	for {
		ev, ok := u.search.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok { // enter: on to the first match
			u.blur, u.sel = true, ""
			u.nav(0, 0)
		}
	}
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, gtx.Dp(44)))
	kit.Fill(gtx, kit.CPanel)
	k := u.k
	ib := func(id string, ico kit.IconPath, on bool, tip string, f func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := k.IconBtn(gtx, id, ico, on)
			k.Tip(gtx, id, d, tip, id != "tbSide")
			if ok {
				f()
			}
			return d
		})
	}
	return layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0) // W keeps Min.Y: it would stretch the row's children
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				ib("tbSide", kit.IcoSide, u.side, "Show or hide the sidebar", func() { u.side = !u.side }),
				layout.Rigid(kit.HGap(10)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(k.Lbl(u.viewTitle(), 14, kit.CText, kit.Semi(kit.Sans))),
						layout.Rigid(k.Lbl(u.sentence(), 11, kit.CText3, kit.Sans)))
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					le, ok := u.lockedVault()
					if !ok || u.unlock != nil {
						return layout.Dimensions{}
					}
					d, clicked := k.Button(gtx, "tbUnlock", kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(kit.Icon(13, kit.COrange, kit.IcoWarn)),
							layout.Rigid(kit.HGap(6)),
							layout.Rigid(k.Lbl("Unlock "+le.Vault.Name, 12, kit.CText, kit.Medium(kit.Sans))))
					})
					if clicked {
						u.askUnlock(le)
					}
					return layout.Inset{Right: 8}.Layout(gtx, func(layout.Context) layout.Dimensions { return d })
				}),
				ib("tbArchive", kit.IcoArchive, false, "Archive every card in Done", func() {
					if n := u.b.ArchiveDone(); n > 0 {
						u.save()
						u.showToast(fmt.Sprintf("Archived %d done cards", n), false)
					} else {
						u.showToast("Nothing in Done to archive", false)
					}
				}),
				layout.Rigid(kit.HGap(6)),
				layout.Rigid(u.searchBox),
				layout.Rigid(kit.HGap(8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "tbNew", kit.CAcc, kit.CAccText, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(kit.Icon(13, kit.CWhite, kit.IcoPlus)),
							layout.Rigid(kit.HGap(6)),
							layout.Rigid(k.Lbl("New card", 12, kit.CWhite, kit.Semi(kit.Sans))),
							layout.Rigid(kit.HGap(8)),
							layout.Rigid(k.Lbl(kit.ModText("N"), 11, kit.Mix(kit.CWhite, kit.CAcc, .35), kit.Sans)))
					})
					if ok {
						u.newCard(u.selCol())
					}
					return d
				}))
		})
	})
}

func (u *ui) searchBox(gtx layout.Context) layout.Dimensions {
	k := u.k
	rim := kit.CLine
	if gtx.Focused(&u.search) {
		rim = kit.CAcc
	}
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(280), gtx.Dp(280)
	return kit.Boxed(kit.CPane, rim, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 8, Right: 4}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(kit.Icon(13, kit.CText3, kit.IcoSearch)),
			layout.Rigid(kit.HGap(8)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return k.Editor(gtx, &u.search, "Filter cards, #labels, PRs   /", 12, kit.Sans, kit.CText)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if u.search.Len() == 0 {
					return layout.Dimensions{Size: image.Pt(0, gtx.Dp(19))}
				}
				d, ok := k.Button(gtx, "tbClear", kit.CPane, kit.CHover, 3, layout.UniformInset(3), kit.Icon(13, kit.CText3, kit.IcoX))
				if ok {
					u.search.SetText("")
				}
				return d
			}))
	})(gtx)
}

func (u *ui) showToast(s string, bad bool) {
	u.toast, u.toastBad, u.undo = s, bad, nil
	u.toastUntil = time.Now().Add(map[bool]time.Duration{false: 2500 * time.Millisecond, true: 6 * time.Second}[bad])
	u.w.Invalidate()
}

func (u *ui) toasts(gtx layout.Context) {
	u.copyPending(gtx)
	if time.Until(u.toastUntil) <= 0 || u.toast == "" {
		u.undo = nil
		return
	}
	undo := u.undo
	if undo != nil {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)}) // the timer bar runs down
	} else {
		gtx.Execute(op.InvalidateCmd{At: u.toastUntil})
	}
	c := kit.CText
	if u.toastBad {
		c = kit.CRed
	}
	k := u.k
	m := op.Record(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}
	cgtx.Constraints.Max.X = min(cgtx.Constraints.Max.X, gtx.Dp(700))
	in := layout.Inset{Top: 7, Bottom: 7, Left: 12, Right: 12}
	if undo != nil {
		in = layout.Inset{Top: 6, Bottom: 6, Left: 14, Right: 8}
	}
	d := kit.Boxed(kit.CPanelCard, kit.CKeyRim, kit.RCard, in, func(gtx layout.Context) layout.Dimensions {
		if undo == nil {
			return k.Label(gtx, u.toast, 13, c, kit.Medium(kit.Sans), 2)
		}
		left := float32(time.Until(u.toastUntil)) / float32(u.toastUntil.Sub(u.toastFrom))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(kit.Icon(14, kit.CText2, kit.IcoArchive)), layout.Rigid(kit.HGap(10)),
			layout.Rigid(k.Lbl(u.toast, 13, c, kit.Medium(kit.Sans))), layout.Rigid(kit.HGap(12)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.RimButton(gtx, "toastUndo", kit.CFoot, kit.CHover, kit.CKeyRim, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 8}, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(k.Lbl("Undo", 13, kit.CAccText, kit.Semi(kit.Sans))), layout.Rigid(kit.HGap(8)),
						layout.Rigid(k.KeyCap(kit.ModText("Z"), false)))
				})
				if ok {
					u.runUndo()
				}
				return d
			}),
			layout.Rigid(kit.HGap(10)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { // the time left to undo
				w, h := gtx.Dp(42), gtx.Dp(3)
				kit.FillRRect(gtx.Ops, image.Rect(0, 0, w, h), h/2, kit.CKeyRim)
				kit.FillRRect(gtx.Ops, image.Rect(0, 0, int(float32(w)*max(left, 0)), h), h/2, kit.CText3)
				return layout.Dimensions{Size: image.Pt(w, h)}
			}),
			layout.Rigid(kit.HGap(4)))
	})(cgtx)
	call := m.Stop()
	st := op.Offset(image.Pt((gtx.Constraints.Max.X-d.Size.X)/2, gtx.Constraints.Max.Y-d.Size.Y-gtx.Dp(24))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
}
