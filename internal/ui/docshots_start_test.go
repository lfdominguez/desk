package ui

import (
	"image"

	"gioui.org/io/key"
)

// The "Getting started" and "Reference" sections' shots.
func init() {
	scenes = append(scenes,
		scene{"start-demo", func(s *shooter) {
			s.shot("start-demo", cut{"views", dp(0, 310, 233, 470), false}) // the sidebar's Sessions views
		}},
		scene{"start-settings", func(s *shooter) {
			s.u.openConfig(cfgFolders)
			s.u.cfg.set.sub[cfgFolders] = "folders"
			s.settle()
			s.shot("start-settings")
			s.u.openConfig("Acme")
			s.settle()
			s.shot("start-accounts")
		}},
		scene{"start-newcard", func(s *shooter) {
			s.key("N", key.ModShortcut)
			s.typ("Add rate limiting to the public API: 100 requests a minute per key")
			s.shot("start-newcard",
				cut{"org", startPicker(s), true},
				cut{"create", around(union(s.at("shOpt"), s.at("shSave")), 8), true},
			)
		}},
	)
}

// startPicker is the new-card dialog's organization row: the pill, the
// organization and folder fields, and the organizations to pick from.
func startPicker(s *shooter) image.Rectangle {
	r := union(s.at("pillorg"), s.at("orgAcme"), s.at("orgInitech"))
	r.Max.X = s.at("shSave").Max.X // as wide as the dialog's fields
	return around(r, 8)
}
