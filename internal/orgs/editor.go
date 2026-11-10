package orgs

import (
	"errors"
	"io/fs"

	"github.com/BurntSushi/toml"
)

// Editor is how the user left a card workspace's panes and editor, for every
// card: the widths of its tree and chat in dp (0, the default), word wrap.
type Editor struct {
	Tree int  `toml:"tree,omitempty"`
	Chat int  `toml:"chat,omitempty"`
	Wrap bool `toml:"wrap,omitempty"`
}

// EditorSettings is the settings' editor part, the zero Editor when unset.
// It reads the file: read it once, not per frame.
func EditorSettings() Editor { return loadSettings().Editor }

// SetEditor keeps e as the editor settings, the others as they are; a file
// that doesn't parse isn't rewritten (it would lose its folders).
func SetEditor(e Editor) error {
	var s settings
	if _, err := toml.DecodeFile(SettingsFile(), &s); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.Editor = e
	return saveSettings(s)
}
