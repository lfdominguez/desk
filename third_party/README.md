# Patched dependencies

Copies of two charmbracelet modules with performance fixes, and of Gio with a fix for a lost
Wayland display, wired in by the `replace` block in go.mod. Tests and testdata are stripped
(Gio's flake files too); licenses kept. Drop each copy once upstream releases the fix.

| Module | Version copied | Patch | Effect |
|---|---|---|---|
| github.com/charmbracelet/ultraviolet | v0.0.0-20261008173134-6b8d4baf91b4 | `Buffer.InsertLineArea` / `DeleteLineArea`: for full-width areas, rotate the row slices instead of copying every cell | scrolling no longer costs width × height cell copies per line |
| github.com/charmbracelet/x/vt | v0.1.0 | `Scrollback.Push`: drop the oldest line by reslicing instead of `slices.Delete` | a full scrollback no longer shifts 10k lines per pushed line |
| github.com/charmbracelet/x/vt | v0.1.0 | `Emulator.Resize`: a shorter main screen scrolls its top lines into the scrollback first | shrinking a pane keeps the prompt on screen instead of cutting off the bottom lines |
| github.com/charmbracelet/x/vt | v0.1.0 | Soft wraps and reflow (reflow.go): autowrap marks the row it leaves (`Screen.wrap`, `Scrollback.wrap`; erasing to a row's end drops the mark; `Wrapped`, `ScrollbackWrapped`); a width change of the main screen joins the marked rows into lines and wraps them again, scrollback included, the cursor kept in its line and on its row (`ResizeMap` says where old positions went); a wide character that doesn't fit, or ends on the last column, wraps whole. Tests in `reflow_test.go` | a pane laid out narrow and then widened shows its long lines whole, not hard-wrapped at the old width |
| gioui.org | v0.10.4-0.20261005201158-fd8b878890c8 | app/os_wayland.go `dispatch`: a failed `wl_display_dispatch_pending` returns the display's error (`lastError`: the protocol error's interface, id and code, or the errno) | a display in error (the compositor dropped the client) no longer spins `wl_display_prepare_read` forever at full CPU: the window gets a `DestroyEvent` with the reason |
| gioui.org | v0.10.4-0.20261005201158-fd8b878890c8 | app/window.go `Window.Event`: a pending event (the platform's `DestroyEvent`) is delivered before the window is created again | a failure to reopen no longer replaces the error that closed the window |

Measured with `seq 1 300000` (1.9 MB) through `vt.Emulator`: 80 columns
7.0s → 1.0s, 200 columns 15.2s → 1.8s. Both modules' own test suites pass
with the patches.
