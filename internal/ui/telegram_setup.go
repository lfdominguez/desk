package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgsetup"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// Desk Control's setup, from Settings' Telegram part: the account's login
// (its code and 2FA password asked in place, then handed to gotd's flow
// on its goroutine), and Set up / Repair (each organization with a mirror
// gets its forum group, then the folder holds them and the bot's chat).

// tgState is the Telegram part's own state, beyond its fields.
type tgState struct {
	login *tgLogin
	note  string // what the last login or setup came to
	bad   bool   // note is an error
	busy  bool   // setting up
}

func (t tgState) sig() string {
	step := ""
	if t.login != nil {
		step = t.login.step
	}
	return fmt.Sprint(step, t.note, t.busy)
}

// tgLogin is a login under way: step is "busy" while Telegram works,
// "code" or "password" while it waits on the user's answer.
type tgLogin struct {
	step   string
	ed     widget.Editor
	answer chan string
	stop   context.CancelFunc
}

// What setup does as the account; tests replace them.
var (
	tgAccountLogin = func(ctx context.Context, a tgsetup.Account, phone string, ask tgsetup.Asker) error {
		return a.Login(ctx, phone, ask)
	}
	tgAccountLogout = func(ctx context.Context, a tgsetup.Account) error { return a.Logout(ctx) }
	tgForum         = func(ctx context.Context, a tgsetup.Account, title, about, bot string) (tgsetup.Chat, error) {
		return a.Forum(ctx, title, about, bot)
	}
	tgFolder = func(ctx context.Context, a tgsetup.Account, id int, title, bot string, chats []tgsetup.Chat) (int, error) {
		return a.Folder(ctx, id, title, bot, chats)
	}
)

// tgAccount is the saved account as tgsetup needs it.
func tgAccount(t orgs.Telegram) (tgsetup.Account, error) {
	if t.APIID == 0 {
		return tgsetup.Account{}, errors.New("save the app's api_id and api_hash first (my.telegram.org → API development tools)")
	}
	h, err := t.APIHash.Get()
	if err != nil || h == "" {
		return tgsetup.Account{}, errors.New("the app's api_hash isn't there: save it first")
	}
	return tgsetup.Account{ID: t.APIID, Hash: h, Session: orgs.TelegramSession()}, nil
}

func (u *ui) tgSay(s string, bad bool) { u.tg.note, u.tg.bad = s, bad }

// tgLoginStart logs the saved account in, asking for what Telegram wants.
func (u *ui) tgLoginStart() {
	t := orgs.TelegramSettings()
	a, err := tgAccount(t)
	if err == nil && t.Number == "" {
		err = errors.New("save your phone number first")
	}
	if err != nil {
		u.tgSay("Can't log in: "+err.Error(), true)
		return
	}
	ctx, stop := context.WithCancel(context.Background())
	l := &tgLogin{step: "busy", answer: make(chan string, 1), stop: stop}
	l.ed.SingleLine, l.ed.Submit = true, true
	u.tg.login = l
	u.tgSay("", false)
	ask := func(step string) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			u.later(func() {
				if u.tg.login == l {
					l.step = step
					l.ed.SetText("")
					l.ed.Mask = map[bool]rune{true: '•'}[step == "password"]
				}
			})
			select {
			case s := <-l.answer:
				return s, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	go func() {
		err := tgAccountLogin(ctx, a, t.Number, tgsetup.Asker{Code: ask("code"), Password: ask("password")})
		canceled := ctx.Err() != nil
		stop()
		u.later(func() {
			if u.tg.login == l {
				u.tg.login = nil
			}
			switch {
			case canceled:
				u.tgSay("Login canceled.", false)
			case err != nil:
				u.tgSay("Login failed: "+firstLine(err.Error()), true)
			default:
				u.tgSay("Logged in: Set up makes the folder and the groups.", false)
			}
		})
	}()
}

// tgLoginSend hands what the user typed to the login waiting on it.
func (u *ui) tgLoginSend() {
	l := u.tg.login
	s := strings.TrimSpace(l.ed.Text())
	if l.step == "busy" || s == "" {
		return
	}
	l.step = "busy"
	l.ed.SetText("")
	l.answer <- s
}

// tgLogout ends the account's session.
func (u *ui) tgLogout() {
	a := tgsetup.Account{Session: orgs.TelegramSession()}
	if t := orgs.TelegramSettings(); t.APIID != 0 {
		a, _ = tgAccount(t)
		a.Session = orgs.TelegramSession()
	}
	u.tg.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := tgAccountLogout(ctx, a)
		u.later(func() {
			u.tg.busy = false
			if err != nil {
				u.tgSay("Logged out here; Telegram said: "+firstLine(err.Error()), true)
				return
			}
			u.tgSay("Logged out.", false)
		})
	}()
}

// tgOrgsOn are the organizations Desk Control mirrors, by name.
func (u *ui) tgOrgsOn(t orgs.Telegram) (on []string) {
	for _, org := range u.orgList {
		if t.Orgs[org].Mirror != orgs.MirrorOff {
			on = append(on, org)
		}
	}
	return on
}

// tgSetup makes what's missing: each mirrored organization's group, then
// the folder with the bot's chat and every group desk made.
func (u *ui) tgSetup() {
	t := orgs.TelegramSettings()
	a, err := tgAccount(t)
	tok, terr := t.Get()
	switch {
	case err != nil:
	case terr != nil || tok == "":
		err = errors.New("save the bot's token first")
	case !a.LoggedIn():
		err = errors.New("log in first")
	}
	if err != nil {
		u.tgSay("Can't set up: "+err.Error(), true)
		return
	}
	on := u.tgOrgsOn(t)
	u.tg.busy = true
	u.tgSay("Setting up…", false)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		made, folder, note, err := tgSetupRun(ctx, a, tok, t, on)
		u.later(func() {
			u.tg.busy = false
			for org, o := range made { // the groups made stay, even when a later step failed
				orgs.SetTelegramOrg(org, o)
			}
			defer u.rmConfig()
			if folder != 0 && folder != t.FolderID {
				s := orgs.TelegramSettings()
				s.FolderID = folder
				orgs.SaveTelegram(s)
			}
			if err != nil {
				u.tgSay("Set up stopped: "+firstLine(err.Error()), true)
				return
			}
			u.tgSay(note, false)
		})
	}()
}

// tgSetupRun is tgSetup's work off the UI goroutine: the groups it made,
// the folder's id, and what to say.
func tgSetupRun(ctx context.Context, a tgsetup.Account, tok string, t orgs.Telegram, on []string) (made map[string]orgs.TelegramOrg, folder int, note string, err error) {
	made = map[string]orgs.TelegramOrg{}
	me, err := (&tgbot.Bot{Token: tok}).Me(ctx)
	if err != nil {
		return made, 0, "", fmt.Errorf("the bot: %w", err)
	}
	var chats []tgsetup.Chat
	manual := 0
	for _, org := range on {
		o := t.Orgs[org]
		if o.Chat == 0 {
			c, err := tgForum(ctx, a, org, "desk: "+org+"'s cards, a topic each.", me.Username)
			if c.Channel != 0 {
				o.Chat, o.Channel, o.Access = c.ID, c.Channel, c.Access
				made[org] = o
			}
			if err != nil {
				return made, 0, "", fmt.Errorf("%s's group: %w", org, err)
			}
		}
		if o.Channel == 0 {
			manual++ // linked by hand: desk can't name it to the folder
			continue
		}
		chats = append(chats, tgsetup.Chat{ID: o.Chat, Channel: o.Channel, Access: o.Access})
	}
	folder, err = tgFolder(ctx, a, t.FolderID, t.FolderName(), me.Username, chats)
	if err != nil {
		return made, 0, "", err
	}
	note = fmt.Sprintf("Set up: the %s folder holds @%s's chat and %d groups (%d new).", t.FolderName(), me.Username, len(chats), len(made))
	if manual > 0 {
		note += fmt.Sprintf(" %d linked by hand: add those to the folder yourself.", manual)
	}
	if !me.Topics {
		note += " Turn on topics for @" + me.Username + " in @BotFather (Bot Settings) to give the General chat its topics."
	}
	return made, folder, note, nil
}

// tgAccountRow is the account's state and its login, in place.
func (u *ui) tgAccountRow(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	btn := func(id, label string, style kit.Kind, act func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			dim, ok := k.KBtn(gtx, id, style, label, "")
			if ok {
				d.act = act
			}
			return dim
		})
	}
	label := func(s string, c color.NRGBA) layout.FlexChild {
		return layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, s, 13, c, kit.Sans, 0) })
	}
	gap := layout.Rigid(kit.HGap(8))
	if l := u.tg.login; l != nil {
		if l.step == "busy" {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, label("Talking to Telegram…", kit.CText2), btn("tgCancel", "Cancel", kit.Ghost, l.stop))
		}
		ask := "The code Telegram just sent you (in the app, or by SMS)"
		if l.step == "password" {
			ask = "Your account's 2FA password"
		}
		for {
			ev, ok := l.ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				d.act = u.tgLoginSend
			}
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = gtx.Dp(260)
				return k.Label(gtx, ask, 13, kit.CText, kit.Sans, 2)
			}), gap,
			layout.Flexed(1, k.Field(&l.ed, "", 0)), gap,
			btn("tgSend", "Send", kit.Primary, u.tgLoginSend), gap,
			btn("tgCancel", "Cancel", kit.Ghost, l.stop))
	}
	a := tgsetup.Account{Session: orgs.TelegramSession()}
	if a.LoggedIn() {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, label("Logged in.", kit.CGreen), btn("tgLogout", "Log out", kit.Ghost, u.tgLogout))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, label("Not logged in. Save the fields below, then log in.", kit.CText3), btn("tgLogin", "Log in", kit.Secondary, u.tgLoginStart))
}

// mirrorNames are the mirror choices as the part shows them.
var mirrorNames = []string{"Off", "Notify", "Summary", "Full"}

// tgOrgRows is each organization's mirror and whether it has its group.
func (u *ui) tgOrgRows(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	t := orgs.TelegramSettings()
	var rows []layout.FlexChild
	for _, org := range u.orgList {
		ed := d.tgacct.ed("mirror." + org)
		cur := max(0, slices.Index(orgs.Mirrors, ed.Text()))
		group, tone := "no group yet", kit.CText3
		switch o := t.Orgs[org]; {
		case o.Chat != 0 && o.Channel == 0:
			group, tone = "linked by hand", kit.CGreen
		case o.Chat != 0:
			group, tone = "group ready", kit.CGreen
		}
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(140), gtx.Dp(140)
						return k.Label(gtx, org, 13, kit.CText, kit.Sans, 1)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.choice(gtx, "tgMirror"+org, mirrorNames, cur, func(int) color.NRGBA { return color.NRGBA{} }, func(i int) { ed.SetText(orgs.Mirrors[i]) })
					}),
					layout.Rigid(kit.HGap(12)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min = image.Point{}
						return k.Label(gtx, group, 12, tone, kit.Sans, 1)
					}))
			})
		}))
	}
	if len(rows) == 0 {
		return k.Label(gtx, "No organizations found: add a search folder first.", 13, kit.CText3, kit.Sans, 0)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// tgSetupRow is Set up / Repair and what the last login or setup said.
func (u *ui) tgSetupRow(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	tone := kit.CText2
	if u.tg.bad {
		tone = kit.CRedText
	}
	kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		if u.tg.busy {
			return k.Label(gtx, "Working…", 13, kit.CText3, kit.Sans, 1)
		}
		dim, ok := k.KBtn(gtx, "tgSetup", kit.Secondary, "Set up / Repair", "")
		if ok {
			d.act = u.tgSetup
		}
		return dim
	}), layout.Rigid(kit.HGap(12))}
	if u.tg.note != "" {
		kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, u.tg.note, 12, tone, kit.Sans, 0) }))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
}
