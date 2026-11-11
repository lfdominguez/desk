package sshc

import (
	"context"
	"errors"
	"github.com/lfdominguez/desk/internal/sshc/sshtest"
	"github.com/lfdominguez/desk/internal/testwait"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// handTicks gives the keepalive a clock the test ticks, and tells it each
// reply counted; every is the interval it was set for.
func handTicks(t *testing.T) (ticks chan time.Time, replies chan struct{}, every *atomic.Int64) {
	ticks, replies, every = make(chan time.Time), make(chan struct{}, 16), new(atomic.Int64)
	oldT, oldR := aliveTicks, aliveReplied
	aliveTicks = func(d time.Duration) (<-chan time.Time, func()) { every.Store(int64(d)); return ticks, func() {} }
	aliveReplied = func() { replies <- struct{}{} }
	t.Cleanup(func() { aliveTicks, aliveReplied = oldT, oldR })
	return ticks, replies, every
}

// tick ticks the keepalive's clock, or fails when nothing takes the tick.
func tick(t *testing.T, ticks chan time.Time) {
	t.Helper()
	select {
	case ticks <- time.Now():
	case <-time.After(testwait.Long):
		t.Fatal("the keepalive took no tick")
	}
}

func waitDone(t *testing.T, c *Client) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(testwait.Long):
		t.Fatal("still alive")
	}
}

func TestKeepaliveLost(t *testing.T) {
	ticks, _, every := handTicks(t)
	c, _, w := pwClient(t, "  ServerAliveInterval 2\n  ServerAliveCountMax 3\n", sshtest.Opts{NoKeepalive: true})
	// The first tick sends the request; each later one with it unanswered
	// is a miss, and the third miss loses the connection.
	for range 3 {
		tick(t, ticks)
	}
	if c.Err() != nil || time.Duration(every.Load()) != 2*time.Second {
		t.Fatalf("lost early: %v, every %v", c.Err(), time.Duration(every.Load()))
	}
	tick(t, ticks)
	waitDone(t, c)
	if !errors.Is(c.Err(), ErrLost) || !w.logged("Timeout, server 127.0.0.1 not responding.") {
		t.Fatalf("err %v, logs %q", c.Err(), w.logs)
	}
}

func TestKeepaliveAnswered(t *testing.T) {
	ticks, replies, _ := handTicks(t)
	c, _, _ := pwClient(t, "  ServerAliveInterval 1\n  ServerAliveCountMax 1\n", sshtest.Opts{})
	for range 3 { // a request, its reply, the next: never a miss
		tick(t, ticks)
		select {
		case <-replies:
		case <-time.After(testwait.Long):
			t.Fatal("no reply")
		}
	}
	if c.Err() != nil {
		t.Fatalf("lost: %v", c.Err())
	}
}

func TestServerGone(t *testing.T) {
	c, s, _ := pwClient(t, "", sshtest.Opts{})
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
	s.Drop()
	waitDone(t, c)
	if c.Err() == nil || errors.Is(c.Err(), ErrLost) {
		t.Fatalf("err %v", c.Err())
	}
}

func TestClose(t *testing.T) {
	c, _, _ := pwClient(t, "  User me\n", sshtest.Opts{})
	if c.Config().User() != "me" {
		t.Fatal("config")
	}
	c.Close()
	c.Close()
	waitDone(t, c)
	if !errors.Is(c.Err(), net.ErrClosed) {
		t.Fatalf("err %v", c.Err())
	}
	ran := false
	c.onClose(func() { ran = true })
	if !ran {
		t.Fatal("stop not run on a closed client")
	}
}

func TestPoolShares(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw"}
	o := w.opts(base(""), s)
	var p Pool
	ctx := context.Background()
	var wg sync.WaitGroup
	clients := make([]*Client, 8)
	releases := make([]func(), 8)
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, rel, err := p.Get(ctx, "srv", o)
			if err != nil {
				t.Error(err)
				return
			}
			clients[i], releases[i] = c, rel
		}()
	}
	wg.Wait()
	for _, c := range clients {
		if c != clients[0] {
			t.Fatal("not shared")
		}
	}
	if s.Count() != 1 {
		t.Fatalf("%d connections", s.Count())
	}
	releases[0]()
	p.mu.Lock()
	if e := p.m[o.File+"\x00srv"]; e.timer != nil {
		t.Fatal("lingering while in use")
	}
	p.mu.Unlock()
	for _, r := range releases {
		r()
		r() // twice is once
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.m["\x00srv"]; e != nil {
		t.Fatal("key")
	}
	if e := p.m[o.File+"\x00srv"]; e == nil || e.refs != 0 || e.timer == nil {
		t.Fatalf("entry %+v", e)
	}
}

func TestPoolLinger(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	o := w.opts(base(""), s)
	p := Pool{Linger: 100 * time.Millisecond}
	ctx := context.Background()
	w.ans = []string{"pw"}
	c, rel, err := p.Get(ctx, "srv", o)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	// Taken back while it lingers: the same client, kept past the linger.
	c2, rel2, err := p.Get(ctx, "srv", o)
	if err != nil || c2 != c {
		t.Fatalf("not reused: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if c.Err() != nil {
		t.Fatal("closed while in use")
	}
	rel2()
	waitDone(t, c)
	p.mu.Lock()
	if p.m[o.File+"\x00srv"] != nil {
		t.Fatal("closed client kept")
	}
	p.mu.Unlock()
	w.ans = []string{"pw"}
	c3, rel3, err := p.Get(ctx, "srv", o)
	if err != nil || c3 == c || s.Count() != 2 {
		t.Fatalf("not redialed: %v, %d connections", err, s.Count())
	}
	rel3()
}

func TestPoolDefaultLinger(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	var p Pool
	w.ans = []string{"pw"}
	c, rel, err := p.Get(context.Background(), "srv", w.opts(base(""), s))
	if err != nil {
		t.Fatal(err)
	}
	rel()
	time.Sleep(100 * time.Millisecond)
	if c.Err() != nil {
		t.Fatal("closed before 60 s")
	}
	c.Close()
}

func TestPoolDropsDead(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	o := w.opts(base(""), s)
	var p Pool
	w.ans = []string{"pw"}
	c, rel, err := p.Get(context.Background(), "srv", o)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	s.Drop()
	waitDone(t, c)
	w.ans = []string{"pw"}
	c2, rel2, err := p.Get(context.Background(), "srv", o)
	if err != nil || c2 == c {
		t.Fatalf("dead client kept: %v", err)
	}
	rel2()
}

func TestPoolDialError(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	o := w.opts(base(""), s)
	var p Pool
	if _, _, err := p.Get(context.Background(), "srv", o); err == nil {
		t.Fatal("dialed without the password")
	}
	w.ans = []string{"pw"}
	if _, rel, err := p.Get(context.Background(), "srv", o); err != nil {
		t.Fatalf("not retried: %v", err)
	} else {
		rel()
	}
}

func TestPoolWaiterCancelled(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	o := w.opts(base(""), s)
	asked, answer := make(chan struct{}), make(chan struct{})
	o.Prompt = func(string, bool) (string, error) {
		close(asked)
		<-answer
		return "pw", nil
	}
	var p Pool
	done := make(chan error)
	go func() {
		_, rel, err := p.Get(context.Background(), "srv", o)
		if err == nil {
			rel()
		}
		done <- err
	}()
	<-asked
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := p.Get(ctx, "srv", o); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	close(answer)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.m[o.File+"\x00srv"]; e.refs != 0 {
		t.Fatalf("refs %d", e.refs)
	}
}

// blockingPrompt answers "pw" once answer closes; asked closes on the
// first question.
func blockingPrompt(o *Options) (asked, answer chan struct{}) {
	asked, answer = make(chan struct{}), make(chan struct{})
	var once sync.Once
	o.Prompt = func(string, bool) (string, error) {
		once.Do(func() { close(asked) })
		<-answer
		return "pw", nil
	}
	return asked, answer
}

func TestPoolFirstCallerCancelled(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	o := w.opts(base(""), s)
	asked, answer := blockingPrompt(&o)
	var p Pool
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error)
	go func() {
		_, _, err := p.Get(ctx, "srv", o)
		first <- err
	}()
	<-asked
	second := make(chan error)
	go func() {
		_, rel, err := p.Get(context.Background(), "srv", o)
		if err == nil {
			rel()
		}
		second <- err
	}()
	for {
		p.mu.Lock()
		refs := p.m[o.File+"\x00srv"].refs
		p.mu.Unlock()
		if refs == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first: %v", err)
	}
	close(answer)
	if err := <-second; err != nil {
		t.Fatalf("second failed with the first: %v", err)
	}
}

// lateCtx is cancelled with nothing to tell AfterFunc: its Done never
// closes, as when the closing goroutine hasn't run yet.
type lateCtx struct {
	context.Context
	gone *atomic.Bool
}

func (c lateCtx) Done() <-chan struct{} { return nil }
func (c lateCtx) Err() error {
	if c.gone.Load() {
		return context.Canceled
	}
	return nil
}

// TestDialCancelledDuringAuth: a dial cancelled while it authenticates
// fails, even when the handshake then goes through.
func TestDialCancelledDuringAuth(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	o := w.opts(base(""), s)
	var gone atomic.Bool
	o.Prompt = func(string, bool) (string, error) { gone.Store(true); return "pw", nil }
	c, err := Dial(lateCtx{context.Background(), &gone}, "srv", o)
	if c != nil {
		c.Close()
	}
	if !errors.Is(err, context.Canceled) || s.Count() != 1 {
		t.Fatalf("err %v, %d connections", err, s.Count())
	}
}

func TestPoolLastWaiterCancels(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	o := w.opts(base(""), s)
	asked, answer := blockingPrompt(&o)
	var p Pool
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		_, _, err := p.Get(ctx, "srv", o)
		done <- err
	}()
	<-asked
	p.mu.Lock()
	e := p.m[o.File+"\x00srv"]
	p.mu.Unlock()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	p.mu.Lock()
	if p.m[o.File+"\x00srv"] != nil {
		t.Fatal("a cancelled dial kept for the next Get")
	}
	p.mu.Unlock()
	close(answer)
	<-e.ready
	if e.err == nil {
		t.Fatal("dial not cancelled")
	}
	w.ans = []string{"pw"}
	o.Prompt = w.opts(base(""), s).Prompt
	c, rel, err := p.Get(context.Background(), "srv", o)
	if err != nil {
		t.Fatalf("no fresh dial: %v", err)
	}
	rel()
	c.Close()
}

func TestPoolOrphanClosed(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw"}
	o := w.opts(base(""), s)
	var p Pool
	key := o.File + "\x00srv"
	e := &entry{ready: make(chan struct{}), cancel: func() {}}
	p.m = map[string]*entry{key: e}
	p.dial(context.Background(), key, e, "srv", o)
	if e.err != nil {
		t.Fatal(e.err)
	}
	waitDone(t, e.c)
	if p.m[key] != nil {
		t.Fatal("orphan kept")
	}
}
