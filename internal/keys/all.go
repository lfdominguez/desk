package keys

import "gioui.org/io/key"

// Scopes are where shortcuts work, in the order the dialog and the docs
// (site/src/content/docs/reference/keys.mdx) show them. A clash is only a
// clash inside one.
var Scopes = []string{"Anywhere", "Board and card menu", "Archive", "Card dialogs", "Typing", "Permission and question box",
	"Command palettes", "Dialogs", "Card terminal", "Card workspace", "Drawings", "Infra workspace", "Command blocks", "Approving a change",
	"Connection box", "Files split"}

func ctl(n key.Name) Chord   { return Chord{n, key.ModShortcut} }
func ctlSh(n key.Name) Chord { return Chord{n, key.ModShortcut | key.ModShift} }
func bare(n key.Name) Chord  { return Chord{n, 0} }
func ws(n key.Name) Chord    { return Chord{n, Mod} }

// All is every shortcut. Handlers match these IDs (Is, Filters) and key
// caps read them (Cap), so a rebinding reaches both.
var All = []Binding{
	{ID: "palette", Scope: "Anywhere", What: "Open the command palette", Def: []Chord{ctl("K")}},
	{ID: "zoom.in", Scope: "Anywhere", What: "Bigger text in terminals and the session panel", Def: []Chord{ctl("="), ctl("+")}},
	{ID: "zoom.out", Scope: "Anywhere", What: "Smaller text", Def: []Chord{ctl("-")}},
	{ID: "zoom.reset", Scope: "Anywhere", What: "Text back to 100%", Def: []Chord{ctl("0")}},
	{ID: "escape", Scope: "Anywhere", What: "Back out of the innermost thing open", Fixed: "esc"},

	{ID: "board.new", Scope: "Board and card menu", What: "New card", Def: []Chord{ctl("N")}},
	{ID: "board.search", Scope: "Board and card menu", What: "Search the cards", Def: []Chord{bare("/"), ctl("F")}, Bare: true},
	{ID: "board.select", Scope: "Board and card menu", What: "Select a card", Fixed: "↑ ↓ ← →"},
	{ID: "board.move", Scope: "Board and card menu", What: "Move the selected card", Fixed: "Ctrl+↑ ↓ ← →"},
	{ID: "board.open", Scope: "Board and card menu", What: "Open the card's session", Fixed: "↵"},
	{ID: "card.edit", Scope: "Board and card menu", What: "Edit the card", Def: []Chord{bare("E")}, Bare: true},
	{ID: "card.terminal", Scope: "Board and card menu", What: "The card's Claude Code terminal", Def: []Chord{bare("T")}, Bare: true},
	{ID: "card.remote", Scope: "Board and card menu", What: "Remote Control on or off (card menu)", Def: []Chord{bare("R")}, Bare: true},
	{ID: "card.workspace", Scope: "Board and card menu", What: "The card's workspace (card menu)", Def: []Chord{bare("W")}, Bare: true},
	{ID: "card.copy", Scope: "Board and card menu", What: "Copy the card's key (card menu)", Def: []Chord{ctl("C")}},
	{ID: "card.archive", Scope: "Board and card menu", What: "Archive the card (card menu)", Def: []Chord{bare(key.NameDeleteBackward)}},
	{ID: "card.column", Scope: "Board and card menu", What: "Move the card to that column (card menu)", Fixed: "1…9"},
	{ID: "undo", Scope: "Board and card menu", What: "Undo an archive or a delete, while its toast shows", Def: []Chord{ctl("Z")}},

	{ID: "arch.check", Scope: "Archive", What: "Check or uncheck the card", Def: []Chord{bare(key.NameSpace)}, Bare: true},
	{ID: "arch.all", Scope: "Archive", What: "Check every card shown", Def: []Chord{ctl("A")}},
	{ID: "arch.delete", Scope: "Archive", What: "Delete the checked cards", Def: []Chord{bare(key.NameDeleteForward)}},
	{ID: "arch.move", Scope: "Archive", What: "Move, open the session", Fixed: "↑ ↓ ↵"},

	{ID: "sheet.commit", Scope: "Card dialogs", What: "Create the card, or save it", Def: []Chord{ctl(key.NameReturn), ctl("S")}},
	{ID: "sheet.improve", Scope: "Card dialogs", What: "Improve with Claude (new card)", Def: []Chord{ctl("J")}},
	{ID: "sheet.delete", Scope: "Card dialogs", What: "Delete the card (card editor)", Def: []Chord{ctl(key.NameDeleteBackward)}},

	{ID: "text.newline", Scope: "Typing", What: "A new line in a message box", Def: []Chord{{key.NameReturn, key.ModAlt}}},
	{ID: "text.send", Scope: "Typing", What: "Send the message", Fixed: "↵"},
	{ID: "text.paste", Scope: "Typing", What: "Paste (an image is saved and linked)", Fixed: "Ctrl+V"},

	{ID: "ask.allow", Scope: "Permission and question box", What: "Allow once", Fixed: "Y or ↵"},
	{ID: "ask.deny", Scope: "Permission and question box", What: "Deny", Fixed: "N"},
	{ID: "ask.always", Scope: "Permission and question box", What: "Allow every use of the tool", Fixed: "S"},
	{ID: "ask.edit", Scope: "Permission and question box", What: "Edit the command first", Fixed: "E"},
	{ID: "ask.pick", Scope: "Permission and question box", What: "Pick an answer", Fixed: "1…9"},

	{ID: "pal.next", Scope: "Command palettes", What: "Next scope", Def: []Chord{bare(key.NameTab)}},
	{ID: "pal.prev", Scope: "Command palettes", What: "Previous scope", Def: []Chord{ctlSh(key.NameTab)}},
	{ID: "pal.move", Scope: "Command palettes", What: "Move, run the item", Fixed: "↑ ↓ ↵"},

	{ID: "settings.save", Scope: "Dialogs", What: "Save the settings", Def: []Chord{ctl("S")}},
	{ID: "dialog.snooze", Scope: "Dialogs", What: "Snooze an alert 1 h, 4 h, 1 day, 1 week", Fixed: "1 2 3 4"},

	{ID: "term.paste", Scope: "Card terminal", What: "Paste an image as its path (else Claude Code gets Ctrl+V)", Fixed: "Ctrl+V"},
	{ID: "term.pastetext", Scope: "Card terminal", What: "Paste an image's path, or the text", Fixed: "Ctrl+Shift+V"},

	{ID: "ws.save", Scope: "Card workspace", What: "Save the file", Def: []Chord{ctl("S")}},
	{ID: "ws.undo", Scope: "Card workspace", What: "Undo, redo in the editor", Fixed: "Ctrl+Z, Ctrl+Shift+Z"},

	{ID: "draw.select", Scope: "Drawings", What: "Selection", Def: []Chord{bare("V"), bare("1")}, Bare: true},
	{ID: "draw.rect", Scope: "Drawings", What: "Rectangle", Def: []Chord{bare("R"), bare("2")}, Bare: true},
	{ID: "draw.diamond", Scope: "Drawings", What: "Diamond", Def: []Chord{bare("D"), bare("3")}, Bare: true},
	{ID: "draw.ellipse", Scope: "Drawings", What: "Ellipse", Def: []Chord{bare("O"), bare("4")}, Bare: true},
	{ID: "draw.arrow", Scope: "Drawings", What: "Arrow", Def: []Chord{bare("A"), bare("5")}, Bare: true},
	{ID: "draw.line", Scope: "Drawings", What: "Line", Def: []Chord{bare("L"), bare("6")}, Bare: true},
	{ID: "draw.draw", Scope: "Drawings", What: "Draw", Def: []Chord{bare("P"), bare("7")}, Bare: true},
	{ID: "draw.text", Scope: "Drawings", What: "Text", Def: []Chord{bare("T"), bare("8")}, Bare: true},
	{ID: "draw.image", Scope: "Drawings", What: "Insert an image", Def: []Chord{bare("9")}, Bare: true},
	{ID: "draw.eraser", Scope: "Drawings", What: "Eraser", Def: []Chord{bare("E"), bare("0")}, Bare: true},
	{ID: "draw.hand", Scope: "Drawings", What: "Hand", Def: []Chord{bare("H")}, Bare: true},
	{ID: "draw.frame", Scope: "Drawings", What: "Frame", Def: []Chord{bare("F")}, Bare: true},
	{ID: "draw.fit", Scope: "Drawings", What: "Show it all", Def: []Chord{ctl("1")}},
	{ID: "draw.dark", Scope: "Drawings", What: "Dark or light canvas", Def: []Chord{{"D", key.ModAlt | key.ModShift}}},
	{ID: "draw.delete", Scope: "Drawings", What: "Delete the selection", Def: []Chord{bare(key.NameDeleteForward), bare(key.NameDeleteBackward)}},
	{ID: "draw.undo", Scope: "Drawings", What: "Undo", Def: []Chord{ctl("Z")}},
	{ID: "draw.redo", Scope: "Drawings", What: "Redo", Def: []Chord{ctlSh("Z"), ctl("Y")}},
	{ID: "draw.copy", Scope: "Drawings", What: "Copy", Def: []Chord{ctl("C")}},
	{ID: "draw.cut", Scope: "Drawings", What: "Cut", Def: []Chord{ctl("X")}},
	{ID: "draw.paste", Scope: "Drawings", What: "Paste", Def: []Chord{ctl("V")}},
	{ID: "draw.duplicate", Scope: "Drawings", What: "Duplicate", Def: []Chord{ctl("D")}},
	{ID: "draw.all", Scope: "Drawings", What: "Select everything", Def: []Chord{ctl("A")}},
	{ID: "draw.group", Scope: "Drawings", What: "Group", Def: []Chord{ctl("G")}},
	{ID: "draw.ungroup", Scope: "Drawings", What: "Ungroup", Def: []Chord{ctlSh("G")}},
	{ID: "draw.forward", Scope: "Drawings", What: "Bring forward", Def: []Chord{ctl("]")}},
	{ID: "draw.backward", Scope: "Drawings", What: "Send backward", Def: []Chord{ctl("[")}},
	{ID: "draw.front", Scope: "Drawings", What: "Bring to front", Def: []Chord{ctlSh("]"), ctl("}")}},
	{ID: "draw.back", Scope: "Drawings", What: "Send to back", Def: []Chord{ctlSh("["), ctl("{")}},
	{ID: "draw.nudge", Scope: "Drawings", What: "Move the selection by 1; with Ctrl, by 10", Fixed: "↑ ↓ ← →"},
	{ID: "draw.edittext", Scope: "Drawings", What: "Edit the selected text", Fixed: "↵"},
	{ID: "draw.pan", Scope: "Drawings", What: "Hold to move around", Fixed: "Space"},

	{ID: "infra.palette", Scope: "Infra workspace", What: "The workspace palette", Def: []Chord{ws("K")}},
	{ID: "infra.pane", Scope: "Infra workspace", What: "New pane (split)", Def: []Chord{ws("T")}},
	{ID: "infra.close", Scope: "Infra workspace", What: "Close the pane", Def: []Chord{ws("W")}},
	{ID: "infra.next", Scope: "Infra workspace", What: "The organization's next tab", Def: []Chord{ws(key.NamePageDown)}},
	{ID: "infra.prev", Scope: "Infra workspace", What: "The organization's previous tab", Def: []Chord{ws(key.NamePageUp)}},
	{ID: "infra.tab", Scope: "Infra workspace", What: "Go to that tab", Fixed: "Ctrl+Shift+1…9"},
	{ID: "infra.claude", Scope: "Infra workspace", What: "Show or hide the Claude panel", Def: []Chord{ws("J")}},
	{ID: "infra.composer", Scope: "Infra workspace", What: "Keyboard to the Claude message box and back", Def: []Chord{ws("L")}},
	{ID: "infra.suggest", Scope: "Infra workspace", What: "Type Claude's suggested command", Def: []Chord{ws("Y")}},
	{ID: "infra.password", Scope: "Infra workspace", What: "Type the server's password", Def: []Chord{ws("P")}},
	{ID: "infra.pin", Scope: "Infra workspace", What: "Pin the screen for Claude, or stop", Def: []Chord{ws("S")}},
	{ID: "infra.group", Scope: "Infra workspace", What: "The group view", Def: []Chord{ws("G")}},
	{ID: "infra.files", Scope: "Infra workspace", What: "Show or hide the files split", Def: []Chord{ws("F")}},
	{ID: "infra.find", Scope: "Infra workspace", What: "Find in the scrollback", Def: []Chord{ws("H")}},
	{ID: "infra.blockup", Scope: "Infra workspace", What: "Pick the previous command block", Def: []Chord{ws(key.NameUpArrow)}},
	{ID: "infra.blockdown", Scope: "Infra workspace", What: "Pick the next command block", Def: []Chord{ws(key.NameDownArrow)}},
	{ID: "infra.copy", Scope: "Infra workspace", What: "Copy the selection", Def: []Chord{ws("C")}},
	{ID: "infra.paste", Scope: "Infra workspace", What: "Paste into the terminal", Def: []Chord{ws("V")}},

	{ID: "block.pin", Scope: "Command blocks", What: "Pin the block for Claude", Fixed: "Space"},
	{ID: "block.copy", Scope: "Command blocks", What: "Copy its output", Fixed: "Y"},
	{ID: "block.leave", Scope: "Command blocks", What: "Leave block mode", Fixed: "esc or ↵"},

	{ID: "approve.yes", Scope: "Approving a change", What: "Approve", Fixed: "Y or ↵"},
	{ID: "approve.always", Scope: "Approving a change", What: "Always allow this in this organization", Fixed: "A"},
	{ID: "approve.edit", Scope: "Approving a change", What: "Edit the command first", Fixed: "E"},
	{ID: "approve.no", Scope: "Approving a change", What: "Deny", Fixed: "N or esc"},

	{ID: "conn.close", Scope: "Connection box", What: "Close the pane", Fixed: "C or esc"},
	{ID: "conn.wait", Scope: "Connection box", What: "Keep waiting", Fixed: "W or ↵"},
	{ID: "conn.retry", Scope: "Connection box", What: "Reconnect, retry now", Fixed: "R"},
	{ID: "conn.accept", Scope: "Connection box", What: "Accept the new host key", Fixed: "A"},
	{ID: "conn.fix", Scope: "Connection box", What: "Fix algorithms", Fixed: "F"},

	{ID: "files.view", Scope: "Files split", What: "Open", Def: []Chord{bare("F3")}},
	{ID: "files.edit", Scope: "Files split", What: "Edit", Def: []Chord{bare("F4")}},
	{ID: "files.copy", Scope: "Files split", What: "Copy to the other side", Def: []Chord{bare("F5")}},
	{ID: "files.move", Scope: "Files split", What: "Move to the other side", Def: []Chord{bare("F6")}},
	{ID: "files.rename", Scope: "Files split", What: "Rename", Def: []Chord{bare("F2")}},
	{ID: "files.mkdir", Scope: "Files split", What: "New folder", Def: []Chord{bare("F7")}},
	{ID: "files.delete", Scope: "Files split", What: "Delete what's marked", Def: []Chord{bare("F8"), bare(key.NameDeleteForward)}},
	{ID: "files.chmod", Scope: "Files split", What: "Change permissions", Def: []Chord{bare("F9")}},
	{ID: "files.mark", Scope: "Files split", What: "Mark or unmark, and move down", Def: []Chord{bare(key.NameSpace)}, Bare: true},
	{ID: "files.all", Scope: "Files split", What: "Mark all, or unmark all", Def: []Chord{ctl("A")}},
	{ID: "files.hidden", Scope: "Files split", What: "Show or hide hidden files", Def: []Chord{ctl("H"), ctl(".")}},
	{ID: "files.reload", Scope: "Files split", What: "Reload", Def: []Chord{ctl("R")}},
	{ID: "files.nav", Scope: "Files split", What: "Move; with Ctrl, also mark what you pass", Fixed: "↑ ↓ PgUp PgDn Home End"},
	{ID: "files.side", Scope: "Files split", What: "The other side", Fixed: "Tab"},
	{ID: "files.open", Scope: "Files split", What: "Open, up a folder", Fixed: "↵, Backspace"},
}
