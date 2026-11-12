package ui

import (
	"cmp"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// cfgFolders is the settings' Search folders page, in place of an
// organization: the folders desk searches for organizations, in desk's own
// settings.
const cfgFolders = "\x00folders"

// folderKind is a search folder as an entry; depth stays text so a typed
// one is checked on save like before.
var folderKind = cfgKind{"folder", "Folder", []cfgField{
	{key: "path"},
	{key: "root", toggle: true},
	{key: "only_toml", toggle: true},
	{key: "depth"},
}}

func (u *ui) loadFolders(d *cfgDialog) {
	card, chat := orgs.Langs()
	d.langs = &cfgEntry{kind: "langs", eds: map[string]*widget.Editor{}, src: map[string]int{}, on: map[string]bool{}}
	d.langs.ed("card").SetText(card)
	d.langs.ed("chat").SetText(chat)
	d.langs.old, d.langs.base = true, d.langs.snap()
	for _, f := range orgs.Folders(u.root) {
		p := f.Path
		if rest, ok := strings.CutPrefix(p, u.home); ok && (rest == "" || rest[0] == filepath.Separator) {
			p = "~" + filepath.ToSlash(rest) // ~/Work on Windows too, which orgs.Expand reads back
		}
		d.keep(newEntry(folderKind.key, map[string]any{"path": p, "root": f.Root, "only_toml": f.OnlyToml, "depth": strconv.Itoa(f.Depth)}))
	}
}

// depthOf is e's depth, 2 when it isn't a number.
func depthOf(e *cfgEntry) int {
	n, err := strconv.Atoi(cmp.Or(e.text("depth"), "2"))
	if err != nil {
		return 2
	}
	return n
}

// folders is d's entries as search folders, unchecked: what the preview scans.
func (d *cfgDialog) folders() []orgs.Folder {
	var fs []orgs.Folder
	for _, e := range d.entries {
		fs = append(fs, orgs.Folder{Path: e.text("path"), Root: e.on["root"], OnlyToml: e.on["only_toml"], Depth: min(max(depthOf(e), 0), 6)})
	}
	return fs
}

// saveFolders writes the page to desk's settings, refusing a folder that
// isn't one.
func (u *ui) saveFolders(d *cfgDialog) bool {
	var fs []orgs.Folder
	for _, e := range d.entries {
		p := e.text("path")
		depth, err := strconv.Atoi(cmp.Or(e.text("depth"), "2"))
		if err != nil || depth < 0 || depth > 6 {
			d.err = cmp.Or(p, "A folder") + ": its depth is a number from 0 to 6"
			return false
		}
		if fi, err := os.Stat(orgs.Expand(p)); !filepath.IsAbs(orgs.Expand(p)) || err != nil || !fi.IsDir() {
			d.err = cmp.Or(p, "An empty path") + " is not a folder (give its full path, or one from ~)"
			return false
		}
		fs = append(fs, orgs.Folder{Path: p, Root: e.on["root"], OnlyToml: e.on["only_toml"], Depth: depth})
	}
	if len(fs) == 0 {
		d.err = "Add a folder: desk needs one to find organizations"
		return false
	}
	err := orgs.SaveFolders(fs)
	if err == nil && d.langs.edited() {
		err = orgs.SaveLangs(d.langs.text("card"), d.langs.text("chat"))
	}
	if err != nil {
		d.err = "Not saved: " + firstLine(err.Error())
		return false
	}
	return true
}

// folderCols lays a folder row's cells out: the path takes what the fixed
// columns (depth, only with desk.toml, folder is an org, remove) leave.
func folderCols(gtx layout.Context, cells ...layout.Widget) layout.Dimensions {
	widths := []unit.Dp{0, 120, 150, 120, 30}
	kids := []layout.FlexChild{layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		dim := cells[0](gtx) // takes all it is given, whatever it draws: the columns after it stay put
		dim.Size.X = gtx.Constraints.Max.X
		return dim
	})}
	for i, w := range widths[1:] {
		kids = append(kids, layout.Rigid(kit.HGap(12)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min, gtx.Constraints.Max.X = image.Point{}, gtx.Dp(w)
			dim := cells[i+1](gtx) // drawn at the column's left, which is w wide whatever it draws
			dim.Size.X = gtx.Dp(w)
			return dim
		}))
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
}

// scanFolders fills d's preview with what fs give. With scanWait (desk's
// window) it waits until the folders stop changing that long, then scans
// off the UI goroutine: typing a path ("/", "~") would walk the disk on
// every key. key is fs as d.prevKey has it; a scan for older folders is
// dropped.
func (u *ui) scanFolders(d *cfgDialog, key string, fs []orgs.Folder) {
	if u.scanWait == 0 {
		d.prev, d.skipped = orgs.Scan(fs)
		return
	}
	current := func() bool { return u.cfg == d && d.prevKey == key }
	go func() {
		time.Sleep(u.scanWait)
		still := make(chan bool, 1)
		u.later(func() { still <- current() })
		if !<-still {
			return
		}
		found, skipped := orgs.Scan(fs)
		u.later(func() {
			if current() {
				d.prev, d.skipped = found, skipped
			}
		})
	}()
}

// cfgFolderPage is the search folders: a row each (path, depth, only with
// a desk.toml, the folder itself an organization, remove), Add folder, and
// a preview of the organizations they give, scanned as they change.
func (u *ui) cfgFolderPage(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	fs := d.folders()
	if key := fmt.Sprint(fs); key != d.prevKey {
		d.prevKey = key
		u.scanFolders(d, key, fs)
	}
	th := func(s string) layout.Widget { return k.Lbl(s, 11, kit.CText3, kit.Semi(kit.Sans)) }
	field := func(name, key, hint string) layout.FlexChild {
		return layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(th(name)), layout.Rigid(kit.VGap(6)), layout.Rigid(k.Field(d.langs.ed(key), "English", 0)), layout.Rigid(kit.VGap(4)), layout.Rigid(k.Lbl(hint, 11, kit.CText3, kit.Sans)))
		})
	}
	items := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions { // the languages new cards start with; each card can change its own
			return layout.Inset{Bottom: 22}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					field("Cards and tickets in", "card", "Improve with Claude, tickets and what Claude writes on the card"),
					layout.Rigid(kit.HGap(16)),
					field("Claude speaks with you in", "chat", "The card's conversation"))
			})
		},
		func(gtx layout.Context) layout.Dimensions { // the text size counts at once, as its keys do: Save doesn't wait for it
			return layout.Inset{Bottom: 22}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(th("Text size")), layout.Rigid(kit.VGap(6)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								dim, step := k.Stepper(gtx, "zoom", fmt.Sprintf("%d%%", orgs.Zoom()))
								if step != 0 {
									d.act = func() { u.zoom(step) }
								}
								return dim
							}),
							layout.Rigid(kit.HGap(12)),
							layout.Rigid(k.Lbl("Terminals and the session panel · "+keys.Cap("zoom.in")+" bigger, "+keys.Cap("zoom.out")+" smaller, "+keys.Cap("zoom.reset")+" back to 100%", 11, kit.CText3, kit.Sans)))
					}))
			})
		},
		k.Lbl("Search folders · where desk finds organizations, top to bottom: when two folders give the same name, the higher one wins", 12, kit.CText2, kit.Sans),
		layout.Spacer{Height: 10}.Layout,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 12, Right: 12, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return folderCols(gtx, th("Folder"), th("Depth"), th("Only with desk.toml"), th("Folder is an org"), th(""))
			})
		},
		func(gtx layout.Context) layout.Dimensions {
			if len(d.entries) == 0 {
				return layout.Inset{Top: 8, Bottom: 8, Left: 12}.Layout(gtx, k.Lbl("No folders: add one below.", 13, kit.CText3, kit.Sans))
			}
			var rows []layout.FlexChild
			for i, e := range d.entries {
				if i > 0 {
					rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						kit.FillRRect(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(1)), 0, kit.CLine)
						return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))}
					}))
				}
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.cfgFolderRow(gtx, i, e) }))
			}
			return kit.Boxed(kit.CCard, kit.CLine, kit.RCard, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
			})(gtx)
		},
		func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				dim, ok := k.KBtn(gtx, "cfgAddfolder", kit.Secondary, "+ Add folder", "")
				if ok {
					d.act = func() {
						d.entries = append(d.entries, newEntry(folderKind.key, map[string]any{"depth": "1", "only_toml": true}))
						d.list.ScrollTo(1)
					}
				}
				return dim
			})
		},
		u.cfgPreview,
	}
	return d.list.Layout(gtx, len(items), func(gtx layout.Context, i int) layout.Dimensions { return items[i](gtx) })
}

// finds says what search folder i gives in the preview: how many, the
// first five.
func (d *cfgDialog) finds(i int) string {
	var names []string
	for _, f := range d.prev {
		if f.Folder == i {
			names = append(names, f.Name)
		}
	}
	if len(names) == 0 {
		return "finds none"
	}
	s := fmt.Sprintf("finds %d · %s", len(names), strings.Join(names[:min(len(names), 5)], ", "))
	if len(names) > 5 {
		s += "…"
	}
	return s
}

// chipNote is what a preview chip adds after f's name: new (not among the
// organizations now), else its path when nested below the folder.
func (u *ui) chipNote(f orgs.Found, fs []orgs.Folder) (note string, isNew bool) {
	if !slices.Contains(u.orgList, f.Name) {
		return " · new, from " + fs[f.Folder].Path, true
	}
	if rel, err := filepath.Rel(orgs.Expand(fs[f.Folder].Path), f.Dir); err == nil && strings.Contains(filepath.ToSlash(rel), "/") {
		return " · " + filepath.ToSlash(rel), false // Acme/api on Windows too
	}
	return "", false
}

// skippedText is the skipped subfolders as the preview lists them: the
// first six and how many more, unless all are asked for.
func (d *cfgDialog) skippedText() (list string, more bool) {
	if n := len(d.skipped); !d.showSkipped && n > 6 {
		return strings.Join(d.skipped[:6], ", ") + fmt.Sprintf(" and %d more.", n-6), true
	}
	return strings.Join(d.skipped, ", "), false
}

// cfgFolderRow is search folder i.
func (u *ui) cfgFolderRow(gtx layout.Context, i int, e *cfgEntry) layout.Dimensions {
	k, d := u.k, u.cfg
	id := fmt.Sprintf("cfg%p", e)
	finds := d.finds(i)
	toggle := func(key string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			dim, ok := k.Switch(gtx, id+key, e.on[key])
			if ok {
				d.act = func() { e.on[key] = !e.on[key] }
			}
			return dim
		}
	}
	return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return folderCols(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(k.Field(e.ed("path"), "~/path/to/folder", 0)),
					layout.Rigid(kit.VGap(4)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, finds, 11, kit.CText3, kit.Sans, 1) }))
			},
			func(gtx layout.Context) layout.Dimensions {
				dim, step := k.Stepper(gtx, id+"depth", e.text("depth"))
				if step != 0 {
					d.act = func() { e.ed("depth").SetText(strconv.Itoa(min(max(depthOf(e)+step, 0), 6))) }
				}
				return dim
			},
			toggle("only_toml"),
			toggle("root"),
			func(gtx layout.Context) layout.Dimensions {
				dim, ok := k.Button(gtx, id+"rm", color.NRGBA{}, kit.CHover, kit.RCtl, layout.UniformInset(6), kit.Icon(14, kit.CText2, kit.IcoX))
				if ok {
					d.act = func() { d.entries = slices.DeleteFunc(d.entries, func(x *cfgEntry) bool { return x == e }) }
				}
				return dim
			})
	})
}

// cfgPreview is the organizations the folders give (new ones marked, nested
// ones with their path) and the subfolders skipped for having no desk.toml.
func (u *ui) cfgPreview(gtx layout.Context) layout.Dimensions {
	k, d, s := u.k, u.cfg, u.cfg.set
	fs := d.folders()
	var chips []layout.Widget
	for _, f := range d.prev {
		dot, rim, tone := orgDots[s.state[f.Name]], kit.RGB(0x34353d), kit.CText3
		extra, isNew := u.chipNote(f, fs)
		if isNew {
			dot, rim, tone = kit.CIdle, kit.Mix(kit.CAccSoft, kit.CAcc, .25), kit.CAccText
		}
		chips = append(chips, kit.Boxed(kit.RGB(0x26272e), rim, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 9, Right: 9}, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(kit.Dot(dot)), layout.Rigid(kit.HGap(6)),
				layout.Rigid(k.Lbl(f.Name, 12, kit.CText, kit.Sans)),
				layout.Rigid(k.Lbl(extra, 12, tone, kit.Sans)))
		}))
	}
	kids := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(k.Lbl("Preview", 13, kit.CText, kit.Semi(kit.Sans))), layout.Rigid(kit.HGap(8)),
				layout.Rigid(k.Lbl(fmt.Sprintf("updates as you edit · %d organizations", len(d.prev)), 12, kit.CText3, kit.Sans)))
		}),
		layout.Rigid(kit.VGap(10)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Wrap(gtx, 6, chips) }),
	}
	if len(d.skipped) > 0 {
		list, more := d.skippedText()
		kids = append(kids, layout.Rigid(kit.VGap(14)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			line := []layout.FlexChild{
				layout.Rigid(k.Lbl("Skipped, no desk.toml: ", 12, kit.CText3, kit.Sans)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = max(0, gtx.Constraints.Max.X-gtx.Dp(80)) // room for Show all
					return k.Label(gtx, list, 12, kit.CText2, kit.Sans, 0)
				}),
			}
			if more {
				line = append(line, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					dim, ok := k.Button(gtx, "cfgSkipped", color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Left: 6, Right: 6}, k.Lbl("Show all", 12, kit.CAccText, kit.Semi(kit.Sans)))
					if ok {
						d.act = func() { d.showSkipped = true }
					}
					return dim
				}))
			}
			return layout.Flex{}.Layout(gtx, line...)
		}))
	}
	return layout.Inset{Top: 22, Bottom: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}
