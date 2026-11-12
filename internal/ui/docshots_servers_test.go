package ui

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/sshc/sshtest"
)

// The Servers section's scenes: Globex's infra workspace over the demo's
// faked hosts and scripted infra Claude.

// srvMod is the workspace's shortcut modifier (Ctrl+Shift).
const srvMod = key.ModCtrl | key.ModShift

// TestSrvDemoClaude is the scripted infra Claude when srvClaude runs this
// test binary as it: the demo runs desk itself as demo-infra, which a test
// binary isn't (it would run every test again instead).
func TestSrvDemoClaude(t *testing.T) {
	sock := os.Getenv("DESK_SRV_CLAUDE")
	if sock == "" {
		t.Skip("only as the docs shots' scripted infra Claude")
	}
	DemoInfra(sock, os.Stdin, os.Stdout)
	os.Exit(0)
}

// srvClaude points the infra Claude at TestSrvDemoClaude in this binary.
func srvClaude() {
	exe, _ := os.Executable()
	infra.ClaudeCmd = func(args ...string) *exec.Cmd {
		c := exec.Command(exe, "-test.run=^TestSrvDemoClaude$")
		c.Env = append(os.Environ(), "DESK_SRV_CLAUDE="+demoSock(args), "DESK_DOCSHOTS=")
		return c
	}
}

// srvStart opens Globex's workspace with the scripted infra Claude.
func srvStart(s *shooter) {
	srvClaude()
	rb := filepath.Join(s.u.root, "Globex", ".desk", "infra", "runbooks")
	os.MkdirAll(rb, 0o755)
	os.WriteFile(filepath.Join(rb, "rotate-billing-logs.md"), []byte("# Rotate the billing logs\n\nWhen /var fills up on a web server.\n\n"+
		"1. See how full it is:\n   ```sh\n   df -h /var\n   ```\n2. Rotate:\n   ```sh\n   sudo logrotate -f /etc/logrotate.d/{{service|billing}}\n   ```\n"), 0o644)
	s.infra()
	s.u.view = "srv:Globex"
	s.wait("Globex's servers", 10*time.Second, func() bool { return s.u.infraOrg("Globex") != nil })
}

// srvHost is host's state in Globex's tree.
func srvHost(s *shooter, host string) (infra.Host, bool) {
	if o := s.u.infraOrg("Globex"); o != nil {
		for _, g := range o.Groups {
			for _, h := range g.Hosts {
				if h.Name == host {
					return h, true
				}
			}
		}
	}
	return infra.Host{}, false
}

// srvOpen opens host's pane and waits until it's connected.
func srvOpen(s *shooter, group, host string) {
	s.u.infra.OpenHost("Globex", group, host)
	s.wait(host+" connected", 15*time.Second, func() bool {
		h, _ := srvHost(s, host)
		return h.State == infra.Connected
	})
}

// srvAsk sends q to the active tab's Claude from its composer.
func srvAsk(s *shooter, q string) {
	s.clickAt(f32.Pt(2100, 1340), pointer.ButtonPrimary) // the composer (Ctrl+Shift+L toggles it, from a state we don't see)
	s.typ(q)
	s.key(key.NameReturn, 0)
}

// srvState waits until host is in st.
func srvState(s *shooter, host string, st infra.State, timeout time.Duration) {
	s.t.Helper()
	s.wait(host+" "+string(st), timeout, func() bool {
		h, _ := srvHost(s, host)
		return h.State == st
	})
}

// srvTurn sends q and waits for Claude's turn to end.
func srvTurn(s *shooter, host, q string) {
	s.t.Helper()
	srvAsk(s, q)
	srvState(s, host, infra.Working, 10*time.Second)
	srvState(s, host, infra.Connected, 30*time.Second)
}

// srvFake makes host's pane run sh (a bash -c script) instead of the demo's
// faked server, as long as when says so.
func srvFake(host, sh string, when func() bool) {
	old := infra.HostCmd
	infra.HostCmd = func(h string) *exec.Cmd {
		if h == host && (when == nil || when()) {
			return exec.Command("bash", "-c", sh)
		}
		return old(h)
	}
}

// srvHostKey has db-02 refuse its host key as a changed one: db-02 is an
// in-process SSH server here (not the demo's faked shell), and known_hosts
// holds another key for it, until Accept new key replaces it.
func srvHostKey(s *shooter) {
	home := os.Getenv("HOME")
	key := func() string {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		k, _ := ssh.NewPublicKey(pub)
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
	}
	id := filepath.Join(home, "db-02.key")
	signer := sshtest.WriteKey(s.t, id, "")
	srv := sshtest.New(s.t, sshtest.Opts{Real: true, Dir: s.t.TempDir(), Keys: []ssh.PublicKey{signer.PublicKey()},
		Shell: []string{"bash", "--rcfile", filepath.Join(filepath.Dir(home), "host.rc"), "-i"}})
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.MkdirAll(filepath.Dir(kh), 0o700)
	os.WriteFile(kh, []byte("192.0.2.11 "+key()+"\n192.0.2.12 "+key()+"\ndb-02 "+key()+"\n"), 0o600)
	file := filepath.Join(s.u.root, "Globex", ".desk", "ssh_config")
	servers, _ := os.ReadFile(file)
	os.WriteFile(file, []byte(fmt.Sprintf("Match originalhost db-02\n    HostName 127.0.0.1\n    Port %d\n    HostKeyAlias db-02\n"+
		"    IdentityFile %s\n    IdentityAgent none\n    SetEnv DESK_DEMO_HOST=db-02\n", srv.Port, id)+string(servers)), 0o644)
	old := infra.HostCmd
	infra.HostCmd = func(h string) *exec.Cmd {
		if h == "db-02" {
			return nil // SSH, to the server above
		}
		return old(h)
	}
}

// srvCard is DESK-5, Globex's card in progress.
func srvCard(s *shooter) string { return s.u.cardOfKey("DESK-5").ID }

func init() {
	scenes = append(scenes,
		scene{"srv-workspace", func(s *shooter) {
			srvStart(s)
			srvOpen(s, "web", "web-02")
			time.Sleep(300 * time.Millisecond)
			s.settle()
			s.shot("srv-workspace",
				cut{"tabs", dp(233, 45, 1600, 75), false},
				cut{"pane-head", dp(233, 75, 1200, 104), false},
				cut{"toolbar", dp(1320, 47, 1600, 73), true},
				cut{"sidebar", dp(0, 525, 233, 615), true},
			)
			srvTurn(s, "web-02", "web-02 answers 502 since 14:25. Can you see why?")
			s.shot("srv-turn1",
				cut{"blocks", dp(233, 108, 1197, 502), true},
				cut{"claude", dp(1200, 75, 1600, 950), true},
				cut{"composer", dp(1205, 848, 1595, 943), false},
			)
			srvAsk(s, "Yes, go ahead.")
			srvState(s, "web-02", infra.Waiting, 30*time.Second)
			sheet := dp(553, 122, 1280, 323)
			s.shot("srv-approval", cut{"sheet", sheet, true})
			s.key("Y", 0)
			srvState(s, "web-02", infra.Waiting, 30*time.Second) // the restart, next
			s.key("E", 0)
			s.shot("srv-approval-edit", cut{"sheet", sheet, true})
			s.key(key.NameReturn, 0)
			srvState(s, "web-02", infra.Connected, 30*time.Second)
			s.shot("srv-done", cut{"claude", dp(1200, 75, 1600, 950), true})
		}},
		scene{"srv-picker", func(s *shooter) {
			srvStart(s)
			srvOpen(s, "db", "db-01")
			s.u.serversClicked("Globex", time.Now())
			s.u.serversClicked("Globex", time.Now())
			s.settle()
			s.shot("srv-picker", cut{"box", dp(518, 106, 1082, 425), true})
		}},
		scene{"srv-split", func(s *shooter) {
			srvStart(s)
			srvOpen(s, "web", "web-02")
			time.Sleep(1100 * time.Millisecond) // a recording is named by the second: two in one clash
			s.key("T", srvMod)                  // a second pane on web-02
			time.Sleep(800 * time.Millisecond)
			s.settle()
			s.shot("srv-split")
			s.clickAt(f32.Pt(2046, 89), pointer.ButtonPrimary) // the toolbar's broadcast fan
			s.shot("srv-broadcast", cut{"toolbar", dp(1320, 47, 1600, 73), true})
		}},
		scene{"srv-group", func(s *shooter) {
			srvStart(s)
			srvOpen(s, "web", "web-01")
			srvOpen(s, "web", "web-02")
			s.key("G", srvMod)
			s.shot("srv-group", cut{"cards", dp(233, 75, 912, 125), true}, cut{"ask", dp(233, 898, 1600, 950), true})
		}},
		scene{"srv-conn", func(s *shooter) {
			srvClaude()
			srvFake("web-01", "sleep 600", nil)
			srvFake("db-01", "sleep 0.3; echo 'ssh: connect to host 192.0.2.21 port 22: No route to host'; exit 255", nil)
			srvHostKey(s)
			s.infra()
			s.u.view = "srv:Globex"
			s.wait("Globex's servers", 10*time.Second, func() bool { return s.u.infraOrg("Globex") != nil })
			s.u.infra.OpenHost("Globex", "web", "web-01")
			srvState(s, "web-01", infra.Waiting, 20*time.Second) // past the connect wait
			s.shot("srv-timeout", cut{"box", dp(531, 466, 898, 586), true})
			s.u.infra.OpenHost("Globex", "db", "db-01")
			time.Sleep(1500 * time.Millisecond)
			s.settle()
			s.shot("srv-failed", cut{"box", dp(504, 418, 928, 636), true})
			s.u.infra.OpenHost("Globex", "db", "db-02")
			time.Sleep(1500 * time.Millisecond)
			s.settle()
			s.shot("srv-hostkey", cut{"box", dp(434, 403, 998, 650), true})
			s.clickAt(f32.Pt(1160, 1010), pointer.ButtonPrimary) // Accept new key
			srvState(s, "db-02", infra.Connected, 15*time.Second)
			s.shot("srv-hostkey-accepted")
		}},
		scene{"srv-card", func(s *shooter) {
			srvStart(s)
			go infra.PeerMain([]string{"do", "Globex", "--from", "DESK-5", "--text",
				"The billing export job on web-02 fails since 14:25 with 502s from the API. Find out why and fix it; check `systemctl status billing-api` after."})
			s.wait("DESK-5's task", 10*time.Second, func() bool { _, ok := s.u.cardTask("DESK-5"); return ok })
			s.u.view = ""
			s.settle()
			s.shot("srv-card-board", cut{"card", dp(930, 98, 1240, 274), true})
			s.u.openSession(srvCard(s))
			time.Sleep(3 * time.Second)
			s.settle()
			s.shot("srv-card-split",
				cut{"header", dp(240, 56, 805, 82), true},
				cut{"peer", dp(240, 584, 800, 790), true},
				cut{"task", dp(1208, 418, 1592, 536), true})
			s.clickAt(f32.Pt(1454, 89), pointer.ButtonPrimary) // its tab's ×: the task goes with it (the peers' hub outlives the scene)
			s.wait("DESK-5's task closed", 10*time.Second, func() bool { _, ok := s.u.cardTask("DESK-5"); return !ok })
		}},
		scene{"srv-asbru", func(s *shooter) {
			src := filepath.Join(os.Getenv("HOME"), "asbru.yml")
			os.WriteFile(src, []byte(`environments:
  g: {_is_group: 1, name: Prod, parent: __PAC__ROOT__}
  w: {_is_group: 1, name: Web, parent: g}
  a: {name: www-01, ip: 192.0.2.31, user: deploy, method: SSH, parent: w}
  b: {name: www-02, ip: 192.0.2.32, user: deploy, method: SSH, parent: w}
  c: {name: build, ip: 192.0.2.40, user: ci, method: SSH, parent: g}
  d: {name: win-01, ip: 192.0.2.41, method: RDP, parent: g}
  l: {_is_group: 1, name: Lab, parent: __PAC__ROOT__}
  p: {name: lab-pi, ip: 192.0.2.50, method: SSH, parent: l}
`), 0o644)
			f0 := asbruFile
			asbruFile = func() string { return src }
			defer func() { asbruFile = f0 }()
			srvClaude()
			s.infra()
			s.u.openConfig("Acme")
			s.settle()
			part := dp(446, 284, 1088, 393)
			s.shot("srv-asbru-page", cut{"servers", part, true})
			s.click("cfgAsbru")
			s.shot("srv-asbru-pick", cut{"servers", part, true})
			s.click("cfgAsbru:Prod")
			s.shot("srv-asbru-done", cut{"servers", part, false}, cut{"toast", dp(520, 889, 1078, 929), true})
		}},
	)
	// The palette, empty and with a query each: the box is as tall as what it lists.
	for _, p := range []struct {
		q   string
		box image.Rectangle
	}{
		{"", dp(565, 126, 1268, 701)},
		{"open", dp(565, 126, 1268, 532)},
		{"forward 5432", dp(565, 126, 1268, 343)},
		{"share", dp(565, 126, 1268, 365)},
		{"report", dp(565, 126, 1268, 343)},
		{"rotate", dp(565, 126, 1268, 343)},
	} {
		name := "srv-palette"
		if p.q != "" {
			name += "-" + strings.Fields(p.q)[0]
		}
		scenes = append(scenes, scene{name, func(s *shooter) {
			srvStart(s)
			srvOpen(s, "web", "web-01")
			s.key("K", srvMod)
			s.typ(p.q)
			s.shot(name, cut{"box", p.box, true})
		}})
	}
}
