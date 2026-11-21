package orgs

import "path/filepath"

// Telegram is settings.toml's [telegram], Desk Control: the bot (Phone:
// its token, the private chat it's paired with, which is the General
// chat, and always), the user's own account for setup (an app's api_id
// and api_hash from my.telegram.org, and the phone number to log in with;
// the session is TelegramSession), the folder, and each organization's
// mirror and group. It belongs to no organization: their desk.toml may be
// shared, these chats are the user's.
type Telegram struct {
	Phone
	APIID    int                    `toml:"api_id,omitempty"`
	APIHash  Secret                 `toml:"api_hash,omitempty"`
	Number   string                 `toml:"phone_number,omitempty"`
	Folder   string                 `toml:"folder,omitempty"`    // "Desk Control" when empty
	FolderID int                    `toml:"folder_id,omitempty"` // the folder made, to update it
	Orgs     map[string]TelegramOrg `toml:"orgs,omitempty"`
}

// TelegramOrg is one organization in Desk Control: what goes to its group
// and the group.
type TelegramOrg struct {
	Mirror  string `toml:"mirror,omitempty"`  // MirrorOff ("") … MirrorFull
	Chat    int64  `toml:"chat_id,omitempty"` // its forum supergroup, as the Bot API counts
	Channel int64  `toml:"channel,omitempty"` // and as MTProto does, with Access: to put it in the folder
	Access  int64  `toml:"access_hash,omitempty"`
}

// What an organization's group gets: nothing, the cards that need the
// user, those plus Claude's replies, or the whole session.
const (
	MirrorOff     = ""
	MirrorNotify  = "notify"
	MirrorSummary = "summary"
	MirrorFull    = "full"
)

// Mirrors are the mirror settings in order, as Settings offers them.
var Mirrors = []string{MirrorOff, MirrorNotify, MirrorSummary, MirrorFull}

// FolderName is the folder's name, "Desk Control" unless set.
func (t Telegram) FolderName() string {
	if t.Folder == "" {
		return "Desk Control"
	}
	return t.Folder
}

// TelegramSettings is settings.toml's [telegram].
func TelegramSettings() Telegram { return loadSettings().Telegram }

// SaveTelegram keeps [telegram] as t, the rest as it is.
func SaveTelegram(t Telegram) error {
	s := loadSettings()
	s.Telegram = t
	return saveSettings(s)
}

// SetTelegramOrg keeps org's part of Desk Control as o, the rest as it is.
func SetTelegramOrg(org string, o TelegramOrg) error {
	s := loadSettings()
	if s.Telegram.Orgs == nil {
		s.Telegram.Orgs = map[string]TelegramOrg{}
	}
	s.Telegram.Orgs[org] = o
	return saveSettings(s)
}

// TelegramSession is where the account's login is kept (0600): desk's
// state folder, beside nothing it would be backed up with.
func TelegramSession() string {
	d, err := XDGDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		d = filepath.Join(".local", "state")
	}
	return filepath.Join(d, "desk", "telegram.session")
}

// migrateTelegram moves an older settings.toml's [phone] into [telegram],
// once: what reads or saves settings sees only the new.
func migrateTelegram(s *settings) {
	if s.Telegram.Phone == (Phone{}) && s.Phone != (Phone{}) {
		s.Telegram.Phone, s.Phone = s.Phone, Phone{}
	}
}
