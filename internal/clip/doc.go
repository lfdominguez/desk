// Package clip reads an image from the system clipboard, which Gio's
// clipboard can't (it reads only text): the Wayland selection on Linux
// (clip.go), or the X11 CLIPBOARD under an X server (clip_x11.go),
// NSPasteboard on macOS (clip_darwin.go), none elsewhere. It also reads
// and owns the primary selection, which Gio doesn't know (primary.go): a
// middle click's paste, as in X terminals.
package clip
