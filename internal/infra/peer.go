package infra

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Peers are other Claudes on this machine, typically one developing a service
// of the org. They need no MCP: `desk peer` (any shell) talks to the running desk
// over a unix socket and describes itself, so a Claude learns it on the spot.
// They send context and tasks to the org's Claude, which can ask them back.

const peerHelp = `desk peer: talk to the infrastructure Claude of a running desk. The user watches
everything in desk; every server change still waits for the user's approval.

  desk peer                                list orgs and sessions (this text too)
  desk peer tell ORG [--from NAME]         give context (markdown on stdin or --text):
                                          what the service is, its DB schema, configs
  desk peer suggest ORG --cmd CMD --why WHY  propose a command for it to consider
  desk peer do ORG [--from NAME]           hand over a task (stdin or --text), e.g. a deploy:
                                          steps, versions, healthchecks, how to roll back
  desk peer wait [--from NAME] [--timeout S]  block until a question or task result arrives
                                          (run it in the background and run it again after each event)
  desk peer answer ID [--from NAME]        answer question ID (stdin or --text)

NAME defaults to the current directory's name. ORG is an org from the list (case-insensitive);
its Claude tab opens if needed. Context is information for it, not orders.`

type peerReq struct {
	Op, Org, From, Text, Cmd, Why string
	ID, Timeout                   int
}

type peerResp struct {
	Text, Err string
}

type peerMsg struct {
	req   peerReq
	reply chan peerResp
}

type peerEvent struct {
	Kind string // question, result, turn
	ID   int
	Org  string
	Text string
}

type peerTask struct {
	id         int
	from, text string
	tab        *Tab
}

type peerQuestion struct {
	ch  chan string
	tab *Tab
	to  string
}

// hub is shared by the socket goroutines, tool calls and the UI loop.
var hub = struct {
	sync.Mutex
	seq       int
	inbox     map[string][]peerEvent // by peer name
	wake      chan struct{}          // closed and replaced on every event
	tasks     map[int]*peerTask
	taskGen   int // bumps whenever tasks changes (Workspace.Tasks caches by it)
	running   map[*Tab]*peerTask
	questions map[int]*peerQuestion
	last      map[*Tab]string // the peer that last talked to a tab
}{inbox: map[string][]peerEvent{}, wake: make(chan struct{}), tasks: map[int]*peerTask{},
	running: map[*Tab]*peerTask{}, questions: map[int]*peerQuestion{}, last: map[*Tab]string{}}

// Cards connects the infra Claudes to desk's board: a card's Claude needn't
// run desk peer wait, desk brings its events into the card's session, and
// an infra Claude can write to a card or make one.
type Cards struct {
	Is   func(name string) bool                         // a card's key; any goroutine
	Tell func(key, text string) error                   // into the card's session
	New  func(org, title, notes string) (string, error) // a card in To do: its key
	// Draw answers a drawing tool for who ("infra:<org>"): text, or ImageResult's
	Draw func(who, name string, args []byte) (string, bool)
}

var cards Cards

// SetCards has the infra Claudes reach desk's cards through c. Any goroutine.
func (w *Workspace) SetCards(c Cards) {
	hub.Lock()
	cards = c
	hub.Unlock()
}

// post queues ev for peer to, or hands it to its card (hub locked).
func post(to string, ev peerEvent) {
	if cards.Tell != nil && cards.Is(to) {
		tell, text := cards.Tell, formatEvent(to, ev)
		go func() { // not under the hub's lock: the board takes its own turn
			if err := tell(to, text); err != nil && ev.Kind == "question" {
				hub.Lock()
				if q := hub.questions[ev.ID]; q != nil {
					delete(hub.questions, ev.ID)
					q.ch <- "(desk couldn't reach " + to + ": " + err.Error() + ")"
				}
				hub.Unlock()
			}
		}()
		return
	}
	hub.inbox[to] = append(hub.inbox[to], ev)
	close(hub.wake)
	hub.wake = make(chan struct{})
}

func nextID() int { hub.seq++; return hub.seq }

// waitEvents returns name's queued events, waiting up to timeout (0: forever).
func waitEvents(name string, timeout time.Duration) []peerEvent {
	var deadline <-chan time.Time
	if timeout > 0 {
		deadline = time.After(timeout)
	}
	for {
		hub.Lock()
		if evs := hub.inbox[name]; len(evs) > 0 {
			delete(hub.inbox, name)
			hub.Unlock()
			return evs
		}
		wake := hub.wake
		hub.Unlock()
		select {
		case <-wake:
		case <-deadline:
			return nil
		}
	}
}

func formatEvents(name string, evs []peerEvent) string {
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(formatEvent(name, e) + "\n\n")
	}
	return b.String() + "Run desk peer wait again for more."
}

func formatEvent(name string, e peerEvent) string {
	switch e.Kind {
	case "question":
		return fmt.Sprintf("question %d from %s's Claude:\n%s\n→ reply: desk peer answer %d --from %s <<'EOF' … EOF", e.ID, e.Org, e.Text, e.ID, name)
	case "result":
		return fmt.Sprintf("result of task %d (%s):\n%s", e.ID, e.Org, e.Text)
	case "turn":
		return fmt.Sprintf("task %d (%s) is not reported done yet; %s's Claude stopped and said:\n%s", e.ID, e.Org, e.Org, e.Text)
	}
	return fmt.Sprintf("message from %s's Claude:\n%s\n→ reply, if it needs one: desk peer tell %s --from %s <<'EOF' … EOF", e.Org, e.Text, e.Org, name)
}

// tellCard is the message_card tool: it writes to a card's Claude, which
// becomes this tab's peer (ask_peer's default).
func (t *Tab) tellCard(key, text string) toolResult {
	hub.Lock()
	c := cards
	hub.Unlock()
	if c.Tell == nil {
		return toolResult{Text: "no desk board here", IsError: true}
	}
	key = strings.ToUpper(strings.TrimSpace(key))
	if !c.Is(key) {
		return toolResult{Text: key + " is not a card key (like DESK-12)", IsError: true}
	}
	if err := c.Tell(key, formatEvent(key, peerEvent{Kind: "message", Org: t.org, Text: text})); err != nil {
		return toolResult{Text: err.Error(), IsError: true}
	}
	hub.Lock()
	hub.last[t] = key
	hub.Unlock()
	return toolResult{Text: "sent to " + key + "'s Claude; it replies with context or a task"}
}

// newCard is the create_card tool.
func (t *Tab) newCard(title, notes string) toolResult {
	hub.Lock()
	c := cards
	hub.Unlock()
	if c.New == nil {
		return toolResult{Text: "no desk board here", IsError: true}
	}
	key, err := c.New(t.org, title, notes)
	if err != nil {
		return toolResult{Text: err.Error(), IsError: true}
	}
	return toolResult{Text: key + " is on the board in To do, for the user to start"}
}

func peerDir() string { return filepath.Join(runtimeDir(), "peer") }

// servePeers listens for `desk peer` on <runtime>/desk/peer/<pid>.sock (user only).
func servePeers() (string, error) {
	dir := peerDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	os.Chmod(dir, 0o700)
	sock := filepath.Join(dir, fmt.Sprintf("%d.sock", os.Getpid()))
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var r peerReq
				if json.NewDecoder(conn).Decode(&r) != nil {
					return
				}
				var resp peerResp
				if r.Op == "wait" {
					evs := waitEvents(r.From, time.Duration(r.Timeout)*time.Second)
					resp.Text = formatEvents(r.From, evs)
					if evs == nil {
						resp = peerResp{Err: fmt.Sprintf("nothing in %ds", r.Timeout)}
					}
				} else {
					reply := make(chan peerResp, 1)
					dispatch(peerMsg{r, reply})
					resp = <-reply
				}
				json.NewEncoder(conn).Encode(resp)
			}()
		}
	}()
	return sock, nil
}

// peer runs a request in the UI loop.
func (m *model) peer(r peerReq) peerResp {
	switch r.Op {
	case "list":
		return peerResp{Text: m.peerList()}
	case "answer":
		hub.Lock()
		q := hub.questions[r.ID]
		delete(hub.questions, r.ID)
		hub.Unlock()
		if q == nil {
			return peerResp{Err: fmt.Sprintf("no open question %d (answered already or it timed out)", r.ID)}
		}
		q.ch <- r.Text
		q.tab.chat = append(q.tab.chat, &chatEntry{who: "peer", text: fmt.Sprintf("%s · answer %d\n%s", r.From, r.ID, r.Text)})
		return peerResp{Text: "answer delivered"}
	}
	t, err := m.peerTab(r.Org, r.From)
	if err != nil {
		return peerResp{Err: err.Error()}
	}
	hub.Lock()
	hub.last[t] = r.From
	hub.Unlock()
	switch r.Op {
	case "suggest":
		r.Text = strings.TrimSpace(fmt.Sprintf("Suggested command: %s\nWhy: %s\n%s", r.Cmd, r.Why, r.Text))
		fallthrough
	case "tell":
		t.peerCtx = append(t.peerCtx, fmt.Sprintf("<peer from=%q>\n%s\n</peer>", r.From, r.Text))
		t.chat = append(t.chat, &chatEntry{who: "peer", text: r.From + " · " + r.Op + "\n" + r.Text})
		return peerResp{Text: t.org + "'s Claude gets it with its next question or task"}
	case "do":
		hub.Lock()
		k := &peerTask{id: nextID(), from: r.From, text: r.Text, tab: t}
		hub.tasks[k.id] = k
		hub.taskGen++
		hub.Unlock()
		if t.busy {
			t.queue = append(t.queue, k)
			return peerResp{Text: fmt.Sprintf("task %d queued: %s's Claude is busy. Wait for its result with desk peer wait.", k.id, t.org)}
		}
		if err := m.startTask(t, k); err != nil {
			return peerResp{Err: err.Error()}
		}
		return peerResp{Text: fmt.Sprintf("task %d started in %s; the user watches it. Run desk peer wait (in the background) for its questions and result.", k.id, t.org)}
	}
	return peerResp{Err: "unknown op " + r.Op}
}

func (m *model) peerList() string {
	var b strings.Builder
	fmt.Fprintf(&b, "desk %d\n", os.Getpid())
	for _, n := range m.tree {
		if n.depth != 0 {
			continue
		}
		fmt.Fprintf(&b, "org %s\n", n.org)
		open := false
		for i, t := range m.tabs {
			if t.org != n.org {
				continue
			}
			open = open || t.orgOnly
			state := "idle"
			if t.busy {
				state = "working"
			}
			t.mu.Lock()
			panes := "no panes"
			if len(t.panes) > 0 {
				panes = strings.ReplaceAll(t.describePanes(), "\n", "; ")
			}
			t.mu.Unlock()
			fmt.Fprintf(&b, "  tab %d %q · %s · %s\n", i+1, t.name, state, panes)
			for j := len(t.chat) - 1; j >= 0; j-- {
				if t.chat[j].who == "you" {
					fmt.Fprintf(&b, "    user's last question: %q\n", t.chat[j].text)
					break
				}
			}
		}
		if !open {
			b.WriteString("  its Claude tab is not open (tell or do opens it)\n")
		}
	}
	return b.String() + "\n" + peerHelp
}

// peerTab is the org's Claude-only tab, opened in the background if needed:
// card's own when card is one of desk's cards (each ticket's server work
// in a session of its own), else the org's.
func (m *model) peerTab(org, card string) (*Tab, error) {
	hub.Lock()
	is := cards.Is
	hub.Unlock()
	if is == nil || !is(card) {
		card = ""
	}
	for _, t := range m.tabs {
		if t.orgOnly && t.card == card && strings.EqualFold(t.org, org) {
			return t, nil
		}
	}
	var orgs []string
	for _, n := range m.tree {
		if n.depth != 0 {
			continue
		}
		if strings.EqualFold(n.org, org) {
			had, active := len(m.tabs) > 0, m.active
			m.openOrg(n, card)
			t := m.tab()
			if had { // don't pull the user away from what they're doing
				m.active = active
				m.flash = "a peer opened " + n.org + "'s Claude (tab " + strconv.Itoa(len(m.tabs)) + ")"
			}
			return t, nil
		}
		orgs = append(orgs, n.org)
	}
	return nil, fmt.Errorf("no org %q; orgs: %s", org, strings.Join(orgs, ", "))
}

func (m *model) startTask(t *Tab, k *peerTask) error {
	hub.Lock()
	hub.running[t] = k
	hub.Unlock()
	q := fmt.Sprintf("<task id=%d from=%q>\n%s\n</task>", k.id, k.from, k.text)
	e := &chatEntry{who: "peer", text: fmt.Sprintf("%s · task %d\n%s", k.from, k.id, k.text)}
	if err := m.send(t, q, e); err != nil {
		hub.Lock()
		delete(hub.running, t)
		delete(hub.tasks, k.id)
		hub.taskGen++
		hub.Unlock()
		return err
	}
	return nil
}

// turnEnded tells the peer when its task's turn stops unreported, then
// starts the next queued task.
func (m *model) turnEnded(t *Tab) {
	hub.Lock()
	if k := hub.running[t]; k != nil {
		last := ""
		for j := len(t.chat) - 1; j >= 0; j-- {
			if t.chat[j].who == "claude" {
				last = t.chat[j].text
				break
			}
		}
		post(k.from, peerEvent{Kind: "turn", ID: k.id, Org: t.org, Text: last})
	}
	hub.Unlock()
	if len(t.queue) > 0 && !t.busy {
		k := t.queue[0]
		t.queue = t.queue[1:]
		if err := m.startTask(t, k); err != nil {
			hub.Lock()
			post(k.from, peerEvent{Kind: "result", ID: k.id, Org: t.org, Text: "failed to start: " + err.Error()})
			hub.Unlock()
		}
	}
}

// peerAnswerWait is how long ask_peer waits (a variable so tests can shorten it).
var peerAnswerWait = 15 * time.Minute

// askPeer is the ask_peer tool: it waits for the peer's answer.
func (t *Tab) askPeer(to, q string) toolResult {
	hub.Lock()
	if to == "" {
		to = hub.last[t]
	}
	if to == "" {
		hub.Unlock()
		return toolResult{Text: "no peer Claude has contacted this session, so there is nobody to ask", IsError: true}
	}
	id := nextID()
	pq := &peerQuestion{ch: make(chan string, 1), tab: t, to: to}
	hub.questions[id] = pq
	post(to, peerEvent{Kind: "question", ID: id, Org: t.org, Text: q})
	hub.Unlock()
	select {
	case a := <-pq.ch:
		return toolResult{Text: to + " answered:\n" + a}
	case <-time.After(peerAnswerWait):
		hub.Lock()
		delete(hub.questions, id)
		hub.Unlock()
		return toolResult{Text: fmt.Sprintf("%s did not answer in 15 minutes (it may not be running desk peer wait)", to), IsError: true}
	}
}

// taskDone is the task_done tool: it sends the result to the task's peer.
// A task desk forgot (it restarted since) goes back to peer, its from.
func (t *Tab) taskDone(id int, peer, status, summary string) toolResult {
	hub.Lock()
	defer hub.Unlock()
	k := hub.tasks[id]
	if k == nil && peer != "" { // ponytail: ids restart with desk, so an old id can name a new task; persist hub.seq if that bites
		post(peer, peerEvent{Kind: "result", ID: id, Org: t.org, Text: status + "\n" + summary})
		return toolResult{Text: fmt.Sprintf("result of task %d sent to %s", id, peer)}
	}
	if k == nil || k.tab != t {
		return toolResult{Text: fmt.Sprintf("no open task %d in this session (desk restarted? pass peer, the task's from)", id), IsError: true}
	}
	delete(hub.tasks, id)
	hub.taskGen++
	if hub.running[t] == k {
		delete(hub.running, t)
	}
	post(k.from, peerEvent{Kind: "result", ID: id, Org: t.org, Text: status + "\n" + summary})
	return toolResult{Text: fmt.Sprintf("result of task %d sent to %s", id, k.from)}
}

// taskFrom names the peer whose task is running in t, for approval prompts.
func taskFrom(t *Tab) string {
	hub.Lock()
	defer hub.Unlock()
	if k := hub.running[t]; k != nil {
		return fmt.Sprintf("%s · task %d", k.from, k.id)
	}
	return ""
}

// peerMain is the `desk peer` client.
func peerMain(args []string) int {
	op := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		op, args = args[0], args[1:]
	}
	if op == "help" {
		fmt.Println(peerHelp)
		return 0
	}
	cwd, _ := os.Getwd()
	fs := flag.NewFlagSet("desk peer", flag.ContinueOnError)
	r := peerReq{Op: op}
	fs.StringVar(&r.From, "from", filepath.Base(cwd), "")
	fs.StringVar(&r.Text, "text", "", "")
	fs.StringVar(&r.Cmd, "cmd", "", "")
	fs.StringVar(&r.Why, "why", "", "")
	fs.IntVar(&r.Timeout, "timeout", 0, "")
	var pos []string
	for { // flags may come after the positional argument
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
	need := map[string]bool{"tell": true, "suggest": true, "do": true, "answer": true}
	if need[op] && len(pos) == 0 {
		fmt.Fprintln(os.Stderr, "desk peer "+op+": missing "+map[bool]string{true: "ID", false: "ORG"}[op == "answer"]+"\n\n"+peerHelp)
		return 2
	}
	if len(pos) > 0 {
		r.Org = pos[0]
		if op == "answer" {
			r.ID, _ = strconv.Atoi(pos[0])
		}
	}
	if r.Text == "" && op != "list" && op != "wait" {
		if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
			b, _ := io.ReadAll(os.Stdin)
			r.Text = strings.TrimSpace(string(b))
		}
	}
	if r.Text == "" && (op == "tell" || op == "do" || op == "answer") {
		fmt.Fprintln(os.Stderr, "desk peer "+op+": no text (pipe it on stdin or pass --text)")
		return 2
	}
	conn, err := dialPeer()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer conn.Close()
	json.NewEncoder(conn).Encode(r)
	var resp peerResp
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		fmt.Fprintln(os.Stderr, "desk closed the connection:", err)
		return 1
	}
	if resp.Err != "" {
		fmt.Fprintln(os.Stderr, resp.Err)
		return 1
	}
	fmt.Println(resp.Text)
	return 0
}

// dialPeer connects to the most recently started desk. ponytail: one desk at a
// time; pick by org across instances if running several becomes common.
func dialPeer() (net.Conn, error) {
	socks, _ := filepath.Glob(filepath.Join(peerDir(), "*.sock"))
	slices.SortFunc(socks, func(a, b string) int {
		ia, _ := os.Stat(a)
		ib, _ := os.Stat(b)
		if ia == nil || ib == nil {
			return 0
		}
		return ib.ModTime().Compare(ia.ModTime())
	})
	for _, s := range socks {
		c, err := net.Dial("unix", s)
		if err == nil {
			return c, nil
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			os.Remove(s) // left by a desk that died
		}
	}
	return nil, errors.New("desk is not running. Ask the user to start desk, then try again.")
}
