package ui

// chatSwitch points the message box at card id: what was written for the
// card before stays with it as its draft, and id's own draft comes back,
// the caret at its end.
func (u *ui) chatSwitch(id string) {
	if id == u.chatFor {
		return
	}
	u.keepDraft()
	u.chatFor = id
	text := ""
	if c := u.b.Card(id); c != nil {
		text = c.Draft
	}
	u.chatIn.SetText(text)
	n := u.chatIn.Len()
	u.chatIn.SetCaret(n, n)
}

// keepDraft saves the message box's text as its card's draft (on the board,
// so a quit keeps it too).
func (u *ui) keepDraft() {
	c := u.b.Card(u.chatFor)
	if c == nil || c.Draft == u.chatIn.Text() {
		return
	}
	c.Draft = u.chatIn.Text()
	u.save()
}
