package tty

import (
	"bufio"
	"fmt"
	"github.com/lfdominguez/desk/internal/testwait"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
)

// TestMain doubles as the program the tests run in a terminal (so they need
// no shell, on any OS): DESK_TTY_HELPER says what it does.
func TestMain(m *testing.M) {
	switch os.Getenv("DESK_TTY_HELPER") {
	case "":
		os.Exit(m.Run())
	case "echo":
		fmt.Println("ready")
		l, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Printf("got:%s:%s\n", strings.TrimSpace(l), os.Getenv("DESK_TTY_X"))
		os.Exit(3)
	case "size":
		for range 2 {
			w, h, _ := term.GetSize(os.Stdout.Fd())
			fmt.Printf("size:%dx%d\n", w, h)
			bufio.NewReader(os.Stdin).ReadString('\n')
		}
	case "sleep":
		fmt.Println("ready")
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func helper(what string) *exec.Cmd {
	c := exec.Command(os.Args[0])
	c.Env = append(os.Environ(), "DESK_TTY_HELPER="+what, "DESK_TTY_X=x")
	return c
}

// out collects what a PTY prints, until it ends.
type out struct {
	mu   sync.Mutex
	b    strings.Builder
	done chan struct{}
}

func read(p *PTY) *out {
	o := &out{done: make(chan struct{})}
	go func() {
		defer close(o.done)
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			o.mu.Lock()
			o.b.Write(buf[:n])
			o.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return o
}

func (o *out) wait(t *testing.T, s string) {
	t.Helper()
	for end := time.Now().Add(testwait.Long); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		o.mu.Lock()
		got := o.b.String()
		o.mu.Unlock()
		if strings.Contains(got, s) {
			return
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	t.Fatalf("no %q in %q", s, o.b.String())
}

func TestEchoAndExit(t *testing.T) {
	p, err := Start(helper("echo"), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	o := read(p)
	o.wait(t, "ready")
	p.Write([]byte("hello\r"))
	o.wait(t, "got:hello:x")
	if code := p.Wait(); code != 3 {
		t.Fatalf("exit %d", code)
	}
	select {
	case <-o.done: // the output ends with the program
	case <-time.After(testwait.Long):
		t.Fatal("Read didn't end after the exit")
	}
	p.Hangup()
}

func TestResize(t *testing.T) {
	if wine() {
		t.Skip("Wine's ConPTY has no ResizePseudoConsole")
	}
	p, err := Start(helper("size"), 70, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Hangup()
	o := read(p)
	o.wait(t, "size:70x20")
	p.Resize(90, 30)
	p.Write([]byte("\r"))
	o.wait(t, "size:90x30")
}

func TestHangup(t *testing.T) {
	p, err := Start(helper("sleep"), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	o := read(p)
	o.wait(t, "ready")
	p.Hangup()
	code := make(chan int)
	go func() { code <- p.Wait() }()
	select {
	case c := <-code:
		// Signaled; Windows' code is STATUS_CONTROL_C_EXIT, but Wine's 0.
		if runtime.GOOS != "windows" && c != -1 {
			t.Fatalf("exit %d", c)
		}
	case <-time.After(testwait.Long):
		t.Fatal("still running after Hangup")
	}
}

func TestStartError(t *testing.T) {
	if p, err := Start(exec.Command("/nonexistent/desk-tty-test"), 10, 2); err == nil || p != nil {
		t.Fatalf("%v %v", p, err)
	}
}
