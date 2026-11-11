# Patched dependencies

Copies of two charmbracelet modules with performance fixes, wired in by the
`replace` block in go.mod. Tests and testdata are stripped; licenses kept. Drop each copy once upstream releases the fix.

| Module | Version copied | Patch | Effect |
|---|---|---|---|
| github.com/charmbracelet/ultraviolet | v0.0.0-20261008173134-6b8d4baf91b4 | `Buffer.InsertLineArea` / `DeleteLineArea`: for full-width areas, rotate the row slices instead of copying every cell | scrolling no longer costs width × height cell copies per line |
| github.com/charmbracelet/x/vt | v0.1.0 | `Scrollback.Push`: drop the oldest line by reslicing instead of `slices.Delete` | a full scrollback no longer shifts 10k lines per pushed line |
| github.com/charmbracelet/x/vt | v0.1.0 | `Emulator.Resize`: a shorter main screen scrolls its top lines into the scrollback first | shrinking a pane keeps the prompt on screen instead of cutting off the bottom lines |
| github.com/charmbracelet/x/vt | v0.1.0 | Soft wraps and reflow (reflow.go): autowrap marks the row it leaves (`Screen.wrap`, `Scrollback.wrap`; erasing to a row's end drops the mark; `Wrapped`, `ScrollbackWrapped`); a width change of the main screen joins the marked rows into lines and wraps them again, scrollback included, the cursor kept in its line and on its row (`ResizeMap` says where old positions went); a wide character that doesn't fit, or ends on the last column, wraps whole. Tests in `reflow_test.go` | a pane laid out narrow and then widened shows its long lines whole, not hard-wrapped at the old width |
| github.com/charmbracelet/x/vt | v0.1.0 | Home and End honor DECCKM like the arrows (`ESC O H`/`ESC O F`); mode 2026 is a known mode (DECRQM reports it) and `Synchronized` says an update is open (at most `SyncMax`). Tests in `key_test.go` | Home/End work in vim and less; renderers hold the last screen mid redraw, so Claude Code doesn't flicker |

Measured with `seq 1 300000` (1.9 MB) through `vt.Emulator`: 80 columns
7.0s → 1.0s, 200 columns 15.2s → 1.8s. Both modules' own test suites pass
with the patches.
