module github.com/lfdominguez/desk

go 1.26.7

require (
	charm.land/bubbletea/v2 v2.1.0
	charm.land/glamour/v2 v2.0.1
	charm.land/lipgloss/v2 v2.0.6
	gioui.org v0.10.4-0.20261005201158-fd8b878890c8
	gioui.org/x v0.10.3
	github.com/AlexanderGrooff/mermaid-ascii v0.0.0-20260908213847-5f00e3d9ac9f
	github.com/BurntSushi/toml v1.6.0
	github.com/ProtonMail/gopenpgp/v3 v3.4.1
	github.com/alecthomas/chroma/v2 v2.23.1
	github.com/andybalholm/stroke v0.0.0-20251027184313-5126dd7227a1
	github.com/charmbracelet/ultraviolet v0.0.0-20261008173134-6b8d4baf91b4
	github.com/charmbracelet/x/ansi v0.11.9
	github.com/charmbracelet/x/term v0.2.2
	github.com/charmbracelet/x/vt v0.1.0
	github.com/creack/pty v1.1.24
	github.com/junegunn/fzf v0.74.5
	github.com/nyactl/go-adf v0.2.0
	github.com/oligo/gvcode v0.8.2
	github.com/passbolt/go-passbolt v0.8.3
	github.com/pkg/sftp v1.13.12
	github.com/yuin/goldmark v1.8.6
	golang.org/x/crypto v0.58.0
	golang.org/x/image v0.47.0
	golang.org/x/net v0.61.0
	golang.org/x/sys v0.49.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	gioui.org/shader v1.0.9 // indirect
	git.sr.ht/~jackmordaunt/go-toast v1.0.0 // indirect
	git.wow.st/gmp/jni v0.0.0-20210610011705-34026c7e22d0 // indirect
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/exp/slice v0.0.0-20250327172914-2fdc97757edf // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/cloudflare/circl v1.6.5 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/elliotchance/orderedmap/v2 v2.2.0 // indirect
	github.com/esiqveland/notify v0.11.0 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/godbus/dbus/v5 v5.2.4 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gookit/color v1.5.4 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/junegunn/go-shellwords v0.0.0-20250127100254-2aa3b3277741 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-runewidth v0.0.30 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rdleal/intervalst v1.5.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/sirupsen/logrus v1.10.0 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	github.com/yuin/goldmark-emoji v1.0.5 // indirect
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e // indirect
	golang.org/x/exp/shiny v0.0.0-20261014143641-f0251eb4aa3e // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

// patched copies (scroll and scrollback speed); see third_party/README.md
replace (
	github.com/charmbracelet/ultraviolet => ./third_party/ultraviolet
	github.com/charmbracelet/x/vt => ./third_party/vt
)
