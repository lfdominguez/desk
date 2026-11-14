# Changelog

What changed in each desk release. Releases are dated (`2026.11.05`, then `2026.11.05.1` the same
day). A release's section here is its notes on GitHub. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Fixed

- **Shift+Enter** makes a new line in a message box again, as Alt+Enter does (the last release left
  it doing nothing).
- After a monitor switched off and back on, the window desk reopened froze along with every
  detached window: stale, stretched and deaf to clicks and keys. It now draws and responds again.
- A window the graphics driver holds back (a detached session on a workspace that isn't on screen,
  on NVIDIA) no longer keeps every other desk window, the main one too, from drawing or opening.

## [2026.11.12.1] - 2026-11-12

### Added

- **Compact** in a session's message box summarizes Claude's conversation (`/compact`). The card
  says "Compacting the conversation…" while it runs, and the conversation then shows the context's
  size before and after, also when Claude compacts by itself.

## [2026.11.12] - 2026-11-12

### Added

- **Change any keyboard shortcut**: **Settings → Keyboard shortcuts** (or **Keyboard shortcuts**
  in the command palette) lists every shortcut of desk and its infra workspace by where it works.
  **Change** (or a click on a shortcut's keys) takes the next key you press, a key another shortcut there already uses is named
  before you save, **Reset** and **Reset all** put the defaults back. Saved in `settings.toml`
  under `[keys]`; buttons, tips and the workspace editor's open tabs take your keys at once.

### Changed

- **No shortcut is Shift alone any more**: every key with a modifier has Ctrl or Alt in it, since
  Shift alone types (Shift+2 is `@` on many layouts). What moved:
  - A new line in a message box (a session's, the workspace's, a drawing's chat, the permission
    and approval boxes' edit, the infra Claude's): **Alt+Enter**, was Shift+Enter, which now does
    nothing.
  - The palettes' previous scope: **Ctrl+Shift+Tab**, was Shift+Tab.
  - Drawings: show it all is **Ctrl+1**, was Shift+1; arrows move by 10 with **Ctrl**, was Shift.
  - The files split: **Ctrl**+arrows, PgUp/PgDn, Home/End mark what they pass, was Shift; rename
    is **F2** only (Shift+F6 is gone).

### Fixed

- Passbolt items are read in about a tenth of a second instead of several: desk no longer downloads
  every secret of the vault to find one, but only the item's own (a name is looked up once per
  session in the list of names; an item's id goes straight to it).

## [2026.11.11.1] - 2026-11-11

### Fixed

- Card sessions and the infra Claude start when Claude Code isn't on `PATH` but is where its
  installer puts it, `~/.local/bin` (on Windows `%USERPROFILE%\.local\bin\claude.exe`), instead of
  failing with `executable file not found in %PATH%`.

## [2026.11.11] - 2026-11-11

### Added

- **A card's session in its own window**: the session header's pop-out, the card menu's **Open in
  new window** (O) or Ctrl+Shift+O open just that session in a separate window, with its own
  message box and keyboard, while the board stays in the main one (which shows where the session
  went, with Show window, which brings it to the front on Hyprland and KDE, and Bring it back
  here). Its workspace, drawings, terminal, card editor and server split open in that window too.
  Closing the window puts the session back with what you'd written; a card still in its own window
  when desk closes gets it again at the next start.
- **Remote Control for a card's session**: **Remote** in its terminal's header, or **R** in the
  card menu, restarts the card's Claude Code terminal with Remote Control, to go on from
  claude.ai/code or the Claude app (its link and QR code show in the terminal). It stays on for the
  card until turned off.
- **Each step of a server connection, with its time**: a pane that opens a new SSH connection
  shows, live in its connection box, every step (the config, the name lookup, the TCP connect or
  jump host, the vault's password, key exchange, host key, each authentication method, the
  shell) and how long it took, a wait on you apart from network time. A failed step stays with
  its error; once connected, the pane's header says "connected in …" and lists the steps on hover.
  A pane that reuses an open connection shows none.
- **Find and replace in the workspace editor**: Ctrl+F finds (plain text, any case unless the
  query has capitals, every match marked and counted), Ctrl+H replaces one or all (one undo),
  Ctrl+G goes to a line.
- A message you write to a card's Claude and don't send stays with that card: opening another
  card's session no longer wipes it, and it comes back when you return, even after closing desk.
- In a card's session the keyboard stays in the message box: selecting or copying chat text, or
  clicking anywhere in the conversation, no longer takes it away, so you can keep typing. Any
  selected chat text is now copied as you let go of the mouse, as Claude's replies already were.
- Quick open: Ctrl+P in a card's workspace opens the palette on its files (a **Files** tab).
- Line commands in the workspace editor: Alt+↑/↓ move lines, Ctrl+Shift+D duplicates them,
  Ctrl+/ comments them out or back in.
- Word wrap in the workspace editor: Alt+Z or the foot's **Wrap**, for every tab, kept.
- A card's workspace keeps its open tabs and opens them again.
- The workspace's tree and chat are as wide as you drag the lines beside them, kept for every card;
  in a narrow window they give way to the editor.

### Changed

- **The Windows installer is a wizard**: the license, then Install for you alone (in
  `%LOCALAPPDATA%\Apps\desk`, no administrator rights) or Advanced: for everyone in Program Files,
  another folder, the desktop shortcut. desk goes on your `PATH` (the system's for everyone), in
  the Start Menu and on the desktop, and the last page can launch it. An install made for everyone
  by an earlier MSI is replaced in place.
- A card's drawing's Claude works in the card's folder and its workspace's projects, and reads
  nowhere else unless you ask it to.
- The workspace's tabs scroll sideways when they don't fit, instead of being squeezed out.
- The workspace's tree shows a file the card's Claude creates, and reads open folders again when
  you open the workspace; it leaves out `.worktrees`, `__pycache__` and `.venv` too.
- A server with a `#@ password` no longer has it read from the vault on every connection: desk
  looks it up only when the server asks for a password, so a server your key logs into connects
  without waiting on the vault (or on unlocking it).

### Fixed

- desk's window comes back when the compositor drops it (on Wayland, a monitor that powers off to
  save energy and back on can do that), with the board and running sessions as they were. Before,
  desk went on running without a window, one CPU core at full use. Each loss is noted in
  `crash.log`, and desk exits only after ten minutes without being able to open a window.
- desk no longer crashes when a server pane to the left of the focused one closes (its shell
  ended, or Ctrl+W), nor can the infra workspace's active tab go astray when another tab closes.
- Opening a card in its Claude Code terminal no longer leaves "signal: killed" as an error in its
  conversation, nor marks the card failed: that was the conversation's own process, which desk
  stops on purpose so the terminal can take the session over. Archiving a card with a session
  running did the same.
- Labels wrap inside their shape, which grows taller to hold them, instead of running over its
  edges.
- Arrows a Claude draws with bends have sharp corners, no longer curves that hook at each bend.
- A Claude's drawing tools no longer fail with "desk's board didn't answer" while desk's window
  is hidden; the edit used to land anyway after the Claude had given up on it.
- Saving a file opened through a symlink in the workspace replaced the link with a plain file.
- Quitting desk no longer loses the workspace editor's changes not saved: they come back, still
  unsaved, when you open that card's workspace again.
- Typing in the workspace editor no longer stalls on big files: the highlighting runs in the
  background (a 100 KB file stalled each key for about 0.2 s, a 4 MB one for seconds).
- A double or triple click in Claude's replies (a word, a block) counts by when the clicks
  happened, not by when desk got round to them: on a busy machine it no longer turns into two
  single clicks.
- The infra Claude's commands in a pane wait for the shell's next prompt before reporting, so the
  command it runs right after no longer finds the pane "busy".
- Home and End work in vim, less and other programs that switch the terminal to application
  cursor keys, in a card's terminal and in server panes.
- Dragging the edge of the infra workspace's Claude panel follows the pointer: the panel jumped
  back to its old width and forth again while it moved.
- A card's terminal and server panes no longer flicker while Claude Code (or any program using
  synchronized output) redraws: a screen half redrawn never shows.

## [2026.11.09.1] - 2026-11-09

### Changed

- **The Done archive, reworked**: cards in sections by date (Today, Yesterday, This week, This
  month, then by month) or by organization, compact rows (key, title, organization, labels, links,
  cost, when it finished), and filter chips with counts for Done, Archived, each organization and
  the top labels.
  - A click previews a card beside the list: its notes, labels, links, numbers and last five
    messages, with Open session, Restore or Archive, and Delete.
  - Checkboxes, on rows and whole sections, and a bar to archive, restore or delete many cards at
    once; one undo puts them all back.
  - Keys: ↑/↓ preview, Space checks, Ctrl+A checks all, Enter opens the session, Delete deletes,
    Esc unchecks.
- Cards remember when they went to Done, which the archive sorts by.

### Fixed

- Dead keys work in terminals on Linux: on a layout with them (US International, for one), a
  quote, tilde or accent typed in a card's terminal or a server pane came out only when the next
  key went down before it came up, and was lost otherwise.

## [2026.11.09] - 2026-11-09

### Added

- **Drawings**: Excalidraw diagrams for each organization (**Drawings** under it in the sidebar)
  and each card (the shapes icon in its panel), kept as `.excalidraw` files that excalidraw.com
  opens too. desk's own editor has Excalidraw's tools, keys and styles, hand-drawn or clean, light
  or dark: shapes, arrows that stay attached, lines, free hand, text, the eraser, frames, groups,
  images from a file or the clipboard, and the organization's library.
  - Each drawing has a Claude of its own in a chat beside the canvas; what you select goes with
    your message.
  - Card sessions, the infra Claude and the drawing's Claude can read drawings and draw in them;
    the workspace Claude reads them. An edit shows live, marked with the Claude's name, and
    <kbd>Ctrl</kbd>+<kbd>Z</kbd> takes it back.
  - A change nobody asked for comes as a suggestion over the drawing, to **Approve**, send back
    with a comment (**Rework**) or **Dismiss**.
  - Copy and paste go both ways with excalidraw.com. **Export** writes a PNG, an SVG or the
    `.excalidraw` file; **Import a file…** brings in a drawing or a library; **Share** has the
    drawing's Claude publish it as a claude.ai artifact and keeps the link.
- In the infra workspace, <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>Page Down</kbd> and
  <kbd>Page Up</kbd> go to the organization's next and previous tab (⌘ on macOS).
- **A Review column**, between Blocked and Done ("Ready for review", in cyan, with its own
  sidebar view and notification). Existing boards get it on start.

### Changed

- **Claude no longer moves a card to Done.** `DESK-STATUS: done` puts it in Review; only you move
  it to Done (drag, <kbd>Ctrl</kbd>+arrows, Move to, the card editor). In ticket sync Review is
  in progress: a GitHub issue still closes only in Done.

### Fixed

- A card no longer reports done while its subagents or background shells still run: it stays
  Working, and goes to Review once a turn ends with nothing left running.
- A card whose Claude is working is always in In progress: every turn, the ones Claude starts
  itself when a subagent finishes included, takes it out of any column, Review and Done too, even
  when it was dragged there by mistake.
- In the infra workspace the keyboard follows the console: picking a server in the sidebar, or
  closing a pane (<kbd>Ctrl</kbd>+<kbd>D</kbd>) while another one stays, leaves the server's
  console ready to type in, with no extra click. It doesn't take the keys while you are typing in
  the Claude panel.

## [2026.11.08] - 2026-11-08

### Added

- **Show the server's login message**, a switch in an organization's settings
  (`clear_login = false` in its `desk.toml`), keeps what a server prints at login (banner,
  message of the day) in its panes. By default the screen is still cleared once desk's
  shell integration is in.
- The server picker (double-click **Servers**) moves its selection with <kbd>↑</kbd>,
  <kbd>↓</kbd>, <kbd>Page Up</kbd> and <kbd>Page Down</kbd>, and <kbd>Enter</kbd> opens the
  selected server. A long list shows a scrollbar.
- The infra workspace's toolbar icons (new pane, broadcast, directory, group view, Claude panel)
  show a tip with their name and key on hover.

### Changed

- **Importing from Ásbrú refuses while Ásbrú is open**, in Settings and in
  `desk servers import-asbru`: Ásbrú writes its connections only when it closes, so its file could
  be days old. Close it from its menu or tray icon, then import. An export is read anyway.
- The workspace's tree and tabs show an icon for each file, colored by its kind (Go, Markdown,
  Dockerfile…), and folders a folder that opens.

### Fixed

- Importing from Ásbrú one group at a time no longer drops hosts whose name another group's host
  already took (each client's `tracker`, say): they come with their group in front
  (`Globex-tracker`). Hosts already there are recognized by address and port, not by name, and
  accents leave names as their plain letter instead of vanishing.
- A terminal pane whose shell prompt is exactly as wide as the pane (fish's default prompt in a
  git repository with a long branch name, in a narrow pane) now gets its shell integration, so a
  restored pane brings back its directory, environment and history instead of staying busy.

## [2026.11.07.1] - 2026-11-07

### Changed

- **The Team view is now a canvas.** A card's full window switches **Canvas | Conversation**, and
  the canvas draws the card's process as nodes and wires, as Blender draws nodes: the card, its
  tickets and the workspace Claude feed the session, the session hands work to its subagents and
  shells, its pull requests and commits go to their CI, and all of it to Done. Each Claude's node
  shows its avatar and what it's on. Drag or scroll to pan, Ctrl/⌘+scroll to zoom, Fit to see it
  all; a click on a node opens it (a conversation, a shell's output, the workspace, a link). Any
  card can show it; a card that runs as an agent opens on it. The handoffs list is behind
  **Handoffs**, and a question Claude asks now shows over the canvas too.

## [2026.11.07] - 2026-11-07

desk now runs on macOS and Windows as well as Linux. Both are new and not fully tested: Windows
was tested under Wine and Proton on Linux, and macOS in a QEMU VM without a GPU, where the window
can't open. If something breaks on either, please
[open an issue](https://github.com/lfdominguez/desk/issues).

### Added

- **macOS.** desk runs on macOS 12 and newer, as a universal `desk.app` (Apple Silicon and Intel)
  in each release's `desk-<version>-macos.zip`, and from the flake on Apple Silicon.
  - Image paste (<kbd>⌘</kbd>+<kbd>V</kbd>) reads the system pasteboard.
  - An organization's Claude profile runs with `CLAUDE_CONFIG_DIR` instead of bubblewrap.
  - Local shells use bash 4.4 or newer if it's on `PATH`, else zsh.
  - Opened from Finder or the Dock, desk takes `PATH` from your login shell, so `claude` is found.
  - Files live where they do on Linux (`~/.config/desk`, `~/.local/share/desk`).
  - The app is ad-hoc signed, not with an Apple Developer ID: Gatekeeper asks the first time.
  - The infra workspace's virtual camera stays Linux-only.
- **Windows.** desk runs on Windows 10 (1809 or newer) and 11, x64: each release has an
  installer, `desk-<version>-windows-amd64.msi` (Program Files, `PATH`, Start Menu; a newer one
  replaces the old install), and a portable `desk-<version>-windows-amd64.zip`.
  - Terminals run in a Windows pseudo console (ConPTY). Local panes and `!` commands use PowerShell
    7 (`pwsh`) when it's on `PATH`, else Windows PowerShell, with command blocks; a restored
    PowerShell pane gets its folder and environment variables back. `token_cmd` and a servers
    file's `#@ hosts-cmd` run with `cmd /C`.
  - The local file panel and Claude's `changes` tool work without `sh`; chmod takes octal and
    symbolic modes, though only the read-only flag means anything on Windows.
  - An organization's Claude profile runs with `CLAUDE_CONFIG_DIR` instead of bubblewrap.
  - Files live where they do on Linux, under your user folder (`.config\desk`, `.local\share\desk`).
  - Neither file is code-signed: SmartScreen asks the first time (More info → Run anyway).
  - The infra workspace's virtual camera stays Linux-only.
- **A card's workspace:** its projects in a tree, an editor, and a read-only Claude that answers
  about the code.
- **desk's own SSH client.** Server panes, file transfers and port forwards no longer run the `ssh`
  program. desk reads the servers file as OpenSSH 10.5 does and connects with its own client, one
  shared connection per server.
- **Text size** for the terminals and the session panel, with zoom keys in every view.

### Changed

- Relative paths in a servers file (`IdentityFile`, `Include`…) resolve from the folder of the file
  that holds them.
- The Ásbrú import drops algorithms desk's SSH client doesn't support.
- Shortcut hints in messages show the keys as symbols (<kbd>⌃⇧K</kbd>, or <kbd>⌘K</kbd> on
  macOS).
- Everything desk starts finds `desk` first on its `PATH` (desk's own folder), so card sessions
  can run `desk peer` from a portable or unpacked desk too.

### Fixed

- A card's session has the claude.ai Artifact tools (Claude Code turns them off for
  stream-json sessions unless asked).
- A card lists only the artifacts its session published with the Artifact tool, not every
  artifact link that shows up in a command's output or Claude's reply.
- Chat code blocks draw tabs, and code blocks inside list items stay in the item.
- Closing a server pane while another pane of the same server was still connecting broke the
  second pane.
- A broken settings file is kept as it is, instead of being overwritten when you zoom.
- Demo mode: the servers' Files tab and the `changes` tool work.
- Two cards made in the same instant could get the same id.
- Changing permissions with no file selected in the Files panel failed with "missing operand"; it
  now does nothing.

## [2026.11.05.1] - 2026-11-05

### Added

- The documentation at [desk.domivega.com](https://desk.domivega.com).

### Changed

- **Improve** runs in the folder you picked, and the folder field filters as you type.

## [2026.11.05] - 2026-11-05

### Added

- A documentation site on GitHub Pages, with screenshots made from desk itself.

### Changed

- An organization's files move to `<Org>/.desk/`: `desk.toml`, `ssh_config`, and the infra folder
  (`.desk/infra`).
- Servers come only from the organization's `ssh_config`.

## [2026.11.04] - 2026-11-04

The first release: `.deb`, AppImage, and the AUR package `desk-kanban`.

[Unreleased]: https://github.com/lfdominguez/desk/compare/2026.11.12.1...HEAD
[2026.11.12.1]: https://github.com/lfdominguez/desk/compare/2026.11.12...2026.11.12.1
[2026.11.12]: https://github.com/lfdominguez/desk/compare/2026.11.11.1...2026.11.12
[2026.11.11.1]: https://github.com/lfdominguez/desk/compare/2026.11.11...2026.11.11.1
[2026.11.11]: https://github.com/lfdominguez/desk/compare/2026.11.09.1...2026.11.11
[2026.11.09.1]: https://github.com/lfdominguez/desk/compare/2026.11.09...2026.11.09.1
[2026.11.09]: https://github.com/lfdominguez/desk/compare/2026.11.08...2026.11.09
[2026.11.08]: https://github.com/lfdominguez/desk/compare/2026.11.07.1...2026.11.08
[2026.11.07.1]: https://github.com/lfdominguez/desk/compare/2026.11.07...2026.11.07.1
[2026.11.07]: https://github.com/lfdominguez/desk/compare/2026.11.05.1...2026.11.07
[2026.11.05.1]: https://github.com/lfdominguez/desk/compare/2026.11.05...2026.11.05.1
[2026.11.05]: https://github.com/lfdominguez/desk/compare/2026.11.04...2026.11.05
[2026.11.04]: https://github.com/lfdominguez/desk/releases/tag/2026.11.04
