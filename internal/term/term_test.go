package term

import (
	"bufio"
	"fmt"
	"github.com/lfdominguez/desk/internal/testwait"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// TestMain doubles as the program the tests run (so they need no shell, on
// any OS): DESK_TERM_HELPER says what it does.
func TestMain(m *testing.M) {
	switch os.Getenv("DESK_TERM_HELPER") {
	case "":
		// A -cover test binary run without GOCOVERDIR (qa mutate's runs)
		// would end each helper with a warning on its screen.
		if os.Getenv("GOCOVERDIR") == "" {
			d, _ := os.MkdirTemp("", "desk-term-cover")
			os.Setenv("GOCOVERDIR", d)
			code := m.Run()
			os.RemoveAll(d)
			os.Exit(code)
		}
		os.Exit(m.Run())
	case "echo":
		fmt.Println("hi")
		l, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Println("got:" + strings.TrimSpace(l))
	case "env":
		fmt.Printf("%s:%s:%s", os.Getenv("FOO"), os.Getenv("DESK_TERM_TEST"), os.Getenv("TERM"))
	case "sleep":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

// helper is the test binary as a program doing what.
func helper(what string) *exec.Cmd {
	c := exec.Command(os.Args[0])
	c.Env = append(os.Environ(), "DESK_TERM_HELPER="+what)
	return c
}

// TestEcho: what the program prints lands on the grid, typed keys reach
// it, and its exit is seen.
func TestEcho(t *testing.T) {
	changed := make(chan struct{}, 64)
	tm, err := Start(helper("echo"), 40, 5, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close()
	screen := func() string {
		runs, _, _, _ := tm.Grid(0)
		var b strings.Builder
		for _, row := range runs {
			for _, r := range row {
				b.WriteString(r.Text)
			}
			b.WriteByte('\n')
		}
		return b.String()
	}
	wait := func(what string) {
		for end := time.Now().Add(testwait.Long); !strings.Contains(screen(), what); {
			if time.Now().After(end) {
				t.Fatalf("no %q on screen:\n%s", what, screen())
			}
			select {
			case <-changed:
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	wait("hi")
	for _, r := range "ok" {
		tm.SendKey(uv.KeyPressEvent{Code: r, Text: string(r)})
	}
	tm.SendKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	wait("got:ok")
	for end := time.Now().Add(testwait.Long); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if done, code := tm.Exited(); done {
			if code != 0 {
				t.Fatalf("exit %d", code)
			}
			return
		}
	}
	t.Fatal("exit not seen")
}
