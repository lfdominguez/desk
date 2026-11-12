package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/term"
	"github.com/lfdominguez/desk/internal/testtmp"
)

// TestMain doubles as the programs tests run in terminals (prog), so they
// need no shell, on any OS.
func TestMain(m *testing.M) {
	if steps := os.Getenv("DESK_UI_PROG"); steps != "" {
		var s []string
		json.Unmarshal([]byte(steps), &s)
		os.Exit(runProg(s))
	}
	if s, ok := os.LookupEnv("DESK_UI_FAKE_" + strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")); ok {
		os.Exit(fakeScript(s, os.Args[1:])) // fakeBin's program, on Windows
	}
	fakeDir, _ = testtmp.Dir("desk-ui-fakes")
	// A -cover test binary run without GOCOVERDIR (qa mutate's runs) would
	// end each prog with a warning on its terminal.
	var cover string
	if os.Getenv("GOCOVERDIR") == "" {
		cover, _ = testtmp.Dir("desk-ui-cover")
		os.Setenv("GOCOVERDIR", cover)
	}
	code := m.Run()
	os.RemoveAll(fakeDir)
	if cover != "" {
		os.RemoveAll(cover)
	}
	os.Exit(code)
}

// prog is the test binary running steps, in order: "out:text" prints
// text, "read" waits for a line, "cat" copies its input to its output,
// "raw" does too in raw mode (no echo, no line editing: what a TUI sees),
// "exit:n" ends with n, "code:x" reads a line and ends with 0 when it's x,
// else says the login failed and ends with 1.
func prog(steps ...string) *exec.Cmd {
	j, _ := json.Marshal(steps)
	c := exec.Command(os.Args[0])
	c.Env = append(os.Environ(), "DESK_UI_PROG="+string(j))
	return c
}

func runProg(steps []string) int {
	in := bufio.NewReader(os.Stdin)
	for _, st := range steps {
		what, arg, _ := strings.Cut(st, ":")
		switch what {
		case "out":
			fmt.Print(arg)
		case "read":
			in.ReadString('\n')
		case "cat":
			io.Copy(os.Stdout, in)
		case "raw": // what a TUI does: keys as they come, shown by the program (ESC as ^[)
			term.MakeRaw(os.Stdin.Fd())
			buf := make([]byte, 64)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil {
					return 0
				}
				os.Stdout.WriteString(strings.ReplaceAll(string(buf[:n]), "\x1b", "^["))
			}
		case "exit":
			n, _ := strconv.Atoi(arg)
			return n
		case "code":
			if l, _ := in.ReadString('\n'); strings.TrimSpace(l) == arg {
				return 0
			}
			fmt.Println("Login failed: That code wasn't accepted.")
			return 1
		}
	}
	return 0
}
