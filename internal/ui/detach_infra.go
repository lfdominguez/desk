package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/layout"
	dbus "github.com/godbus/dbus/v5"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// infraWake redraws the window that draws the infra workspace, from its
// goroutines: a detached session's beside its chat, else the main one.
func (u *ui) infraWake() {
	if w := u.infraWin.Load(); w != nil {
		w.Invalidate()
		return
	}
	u.w.Invalidate()
}

// infraElsewhere is the main window's servers view while a detached
// window draws the workspace beside its chat: where, and a way to have it
// here (that window then shows its chat alone).
func (u *ui) infraElsewhere(gtx layout.Context) layout.Dimensions {
	k, by := u.k, u.infraBy
	key := by
	if c := u.b.Card(by); c != nil {
		key = c.Key()
	}
	kit.Fill(gtx, kit.CColumn)
	var act func()
	d := layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(k.Lbl("The servers are beside "+key+"'s session, in its own window", 14, kit.CText, kit.Semi(kit.Sans))),
			layout.Rigid(kit.VGap(14)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.Button(gtx, "infraHere", kit.CAcc, kit.Mix(kit.CAcc, kit.CWhite, .12), kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 14, Right: 14},
					k.Lbl("Show them here", 13, kit.CWhite, kit.Semi(kit.Sans)))
				if ok {
					act = func() {
						u.noSplit[by] = true // that window: the chat alone
						u.infraBy = ""
						u.infraWin.Store(nil)
						u.tkGen++
						if d := u.detached[by]; d != nil {
							d.w.Invalidate()
						}
					}
				}
				return d
			}))
	})
	if act != nil {
		act()
	}
	return d
}

// hyprFocus focuses this process's window titled title through
// Hyprland's socket, as `hyprctl dispatch` does: false off Hyprland or
// when it can't. Hyprland 0.56's Lua config takes hl.dsp.focus; older ones
// focuswindow.
func hyprFocus(title string) bool {
	sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	if sig == "" {
		return false
	}
	sock := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "hypr", sig, ".socket.sock")
	out, err := hyprCall(sock, "j/clients")
	if err != nil {
		return false
	}
	var clients []struct {
		Address string `json:"address"`
		Pid     int    `json:"pid"`
		Title   string `json:"title"`
	}
	if json.Unmarshal(out, &clients) != nil {
		return false
	}
	for _, c := range clients {
		if c.Pid != os.Getpid() || c.Title != title {
			continue
		}
		for _, cmd := range []string{
			fmt.Sprintf("dispatch hl.dsp.focus({ window = 'address:%s' })", c.Address),
			"dispatch focuswindow address:" + c.Address,
		} {
			if r, err := hyprCall(sock, cmd); err == nil && strings.TrimSpace(string(r)) == "ok" {
				return true
			}
		}
		return false
	}
	return false
}

// hyprCall sends one request on Hyprland's socket and reads its answer.
func hyprCall(sock, req string) ([]byte, error) {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, req); err != nil {
		return nil, err
	}
	return io.ReadAll(c)
}

// kwinRaise is the KWin script kwinFocus loads: KWin 6's windowList and
// activeWindow, else KWin 5's clientList and activeClient. A script may
// activate any window, past focus-stealing prevention.
const kwinRaise = `const want = %s, pid = %d;
const all = workspace.windowList ? workspace.windowList() : workspace.clientList();
for (const w of all) {
	if (w.pid !== pid || w.caption !== want) continue;
	w.minimized = false;
	if (workspace.windowList) workspace.activeWindow = w; else workspace.activeClient = w;
	break;
}
`

// kwinFocus focuses this process's window titled title on KDE, as kdotool
// does: a KWin script over the session bus, loaded under one name (the last
// one unloaded first) and started. False off KWin; whether the script found
// the window it can't tell.
func kwinFocus(title string) bool {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer bus.Close()
	js, _ := json.Marshal(title)
	f := filepath.Join(os.TempDir(), fmt.Sprintf("desk-raise-%d.js", os.Getpid()))
	if os.WriteFile(f, fmt.Appendf(nil, kwinRaise, js, os.Getpid()), 0o600) != nil {
		return false
	}
	const name = "desk-raise"
	o := bus.Object("org.kde.KWin", "/Scripting")
	if o.Call("org.kde.kwin.Scripting.unloadScript", 0, name).Err != nil {
		return false // no KWin
	}
	var id int32
	if o.Call("org.kde.kwin.Scripting.loadScript", 0, f, name).Store(&id) != nil || id < 0 {
		return false
	}
	return o.Call("org.kde.kwin.Scripting.start", 0).Err == nil
}
