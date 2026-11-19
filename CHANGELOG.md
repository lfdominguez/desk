# Changelog

What changed in each desk release. Releases are dated (`2026.11.05`, then `2026.11.05.1` the same
day). A release's section here is its notes on GitHub. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- An autopilot's cards show under it: indented right below it in the sidebar's Agent Sessions, as
  nodes wired from its session on its canvas (where it now opens; a click opens a card's session),
  and next to it in the Company view, tied to it by dashed tethers.
- **Server panes act like Konsole or Kitty**: Shift+Page Up/Down page through the scrollback,
  Shift+Insert pastes and Ctrl+Insert copies, and Ctrl+click opens a URL (underlined while Ctrl
  is held over it), whole even when the terminal wrapped it over several rows.
- **The primary selection, as in X terminals**: what you select in a pane can be pasted with a
  middle click in any window, and a middle click in a pane pastes the text last selected with the
  mouse anywhere. On macOS, Windows and GNOME, it pastes what you last selected in desk.

### Changed

- Only one desk runs on a board at a time: a second one says desk is already running and quits,
  instead of each saving its own copy of the board over the other's.

### Fixed

- A power cut while saving can no longer leave the board empty: it is synced to disk before it
  replaces the old one. desk also keeps an earlier copy, `board.json.bak` (at most about an hour
  older), and opens it when `board.json` doesn't read.
- Ctrl+← and Ctrl+→ move a word in a server pane and a card's terminal, as in any terminal.
  Shift, Alt and Ctrl with the arrows, Home, End, Delete, Page Up/Down and the F-keys reach the
  program now instead of sending nothing, and Ctrl+Backspace sends `^H`.
- While a pane shows the server password's box, nothing pasted reaches its prompt (a pasted line
  could have run behind the box), and the password is never typed once the prompt has gone or
  you cancelled, however close the timing. With boxes on two panes, each one's Cancel and Try
  again act on its own pane.

### Security

- desk's data folder (`~/.local/share/desk`: the board, conversations, pasted images) is readable
  by you alone. It was readable by every user of the machine, and conversations hold commands,
  their output and servers' details. Existing files are fixed when desk starts.

## [2026.11.19.1] - 2026-11-19

### Changed

- Getting a server's password for a sudo prompt (<kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>P</kbd>)
  shows in the pane what desk is doing, step by step with its time, as the connection box does:
  looking it up in the vault, waiting for your master password, unlocking, typing it. It replaces
  the "Loading password…" dialog. **Cancel** (C or Esc) stops the fetch, and a password that comes
  after never gets typed. A failed fetch stays with its error, and **Try again** (R).

### Fixed

- Claude no longer fails to start ("bwrap: Can't mkdir …" or "Can't mount on symlink destination")
  when another organization's folder is a symlink, such as a synced folder kept elsewhere: its wall
  now goes on the folder the link points at.

## [2026.11.19] - 2026-11-19

### Added

- **Autopilot for several tickets.** Check tickets in an organization's Tickets view and start an
  autopilot: a card per ticket, run by one Claude that starts them (as many at once as you pick),
  answers their questions and permission requests, and steers them, without doing their work.
  Only what it can't decide reaches you, and only its card notifies you. At the Supervised level
  it decides reading and local edits; at Autopilot, everything inside the cards' folders. Changes
  outside them always ask you, unless you allowed them in the dialog in your own words. desk holds
  it to that: it can allow a change outside only by quoting what you allowed. Its cards' tickets
  move once you move it to Done. An optional budget for the whole group pauses every card past
  it. The board's search finds its cards.

### Changed

- Typing or copying a server's password from a vault shows "Loading password…" until it arrives,
  with Cancel (Esc). On a slow vault, Enter at sudo's prompt no longer goes before the password.
- When the infra Claude exits with an error, its chat says why (what Claude Code printed), not
  only the exit status.

### Fixed

- desk no longer crashes when a program in a terminal asks for a scroll region taller than the
  pane.

## [2026.11.17] - 2026-11-17

### Added

- **The infra Claude edits files properly.** It reads and changes a server's files with its own
  tools (`file_read`, `file_edit`, `file_write`) over the connection, instead of `printf … | cat >`
  in your pane. Every change asks you as a diff; the file is written atomically keeping its owner
  and mode, only if it's still what Claude read, and desk keeps the old content. Files up to 64 MB
  (a big one read a window at a time), binary ones as base64, on Linux, BSD and macOS servers;
  `sudo` works with passwordless sudo.
- **Cards write the files their servers get.** A card's Claude writes a config, unit or script with
  its own context and agents and hands it over with `desk peer file`; the infra Claude checks it
  against the server and places it, and you approve the diff, which names the card.
- **Servers reached from another one.** A machine reached only from one of your servers, with a
  key that lives there, gets `#@ on` and `#@ via` in the servers file: desk connects through that
  server and runs the ssh there, so panes, the files panel, the file tools and port forwards work
  on it and the key stays put. The infra Claude adds these itself (`add_host`, approved) instead of
  running ssh inside a pane.
- **Ticket workflow.** An organization whose statuses mean something of their own (its "Test" is
  "deployed to the testing environment") gets a Ticket workflow in its settings: write how its
  tickets move, or where to read it, and Learn has Haiku turn it, with the Jira projects' statuses,
  into each column's status, the statuses others move tickets to, and rules for the card's Claude,
  all shown to check before Save. Then every card's Claude follows the rules and can move its
  ticket itself (`ticket_status`), a ticket someone moves takes its card to the matching column,
  and desk never moves such tickets back. Moving a card yourself still moves its ticket, and a
  ticket desk couldn't move (someone else's) doesn't pull the card back.
- **Who am I.** Tell desk who you are (Settings → General): your role, what you know, how you like
  the work done. The Claudes desk starts for you (card sessions and their terminal, workspace,
  leads, drawings, infra, Improve with Claude) are told, to focus their answers and choices on you.
  An organization can add who you are there, or, with Extend from global off, replace the global
  one.
- **Model providers.** An organization's settings take a Model provider: OpenRouter or any other
  Anthropic-compatible endpoint, its API key, and the models it puts in place of Opus, Sonnet and
  Haiku. Each tier says what it is for, with the models that come close to it, or a custom one with
  a warning about what desk may get wrong on a model it doesn't know. **Models** picks which of
  desk's Claudes run on it (card sessions, Improve with Claude, Explain, workspace, leads, drawings,
  infra) and which stay on the Claude subscription, and the model each company role runs on. A
  session on a provider shows it in place of the plan's limits.

### Fixed

- Telling a card's Claude it's done ("you're done", "that's all") moves the card to **Done**; it
  stayed in Review. Claude finishing by itself, or after desk's own messages, still only puts it in
  Review.
- Improve with Claude runs under the organization's own Claude profile on macOS and Windows.
- git and ssh in sessions of an organization on the default Claude account work again when
  `~/.ssh/config` or `/etc/ssh/ssh_config` is a symlink (NixOS, home-manager): the sandbox showed
  them as nobody's and ssh refused them ("Bad owner or permissions"). Profiles already had the fix.

## [2026.11.16.5] - 2026-11-16

### Changed

- The Company view is dark: dark paper and muted pastels in place of its bright cream sky.

## [2026.11.16.4] - 2026-11-16

### Changed

- The sidebar's sessions show their status as an icon instead of a colored dot: a spinner while
  Claude is working, a speech bubble on your turn, a struck-through circle when blocked, a cross
  when failed, an eye for review.
- Reload and Settings in the sidebar's Organizations header are icons (a circling arrow, a gear),
  named on hover.

## [2026.11.16.3] - 2026-11-16

### Changed

- **Pasted images in the message box.** An image pasted in a session's message box goes in as
  `[Image 1]`, as in Claude Code: a badge in the text that edits as one piece (arrows step over
  it; Backspace, Delete or the thumbnail's × removes the whole image), with its thumbnail and name
  above the box. It reaches Claude as an image of the message instead of a file path, so a
  message holding one is never run as a `!` shell command, and the images stay with an unsent
  draft.

### Fixed

- Ctrl+Backspace and Ctrl+Delete delete a word again in desk's text boxes (Linux and Windows).

## [2026.11.16.2] - 2026-11-16

### Added

- **The company.** Every card's Claude can hand work to desk's roles with the Agent tool: a tech
  lead who plans and briefs the others, an architect, a senior developer, QA, DevOps, security,
  and the organization's own CTO, CISO and CFO. Each gets a brief and answers with a short result
  ending in a verdict (ready, blocked, partial). The organization's own CLAUDE.md, skills, agents
  and workflows come first; one of its agents named like a role takes its place, and its
  `desk.toml` can map, skip or turn off desk's roles (`[company]`). Cards get them whether or
  not they run as an agent.
- **Department leads.** Each organization has an Engineering, a Security and a Finance lead:
  long-running Claudes that only read and keep the department's decisions from card to card. A
  card's Claude briefs one with `ask_lead` (objective, where to look, limits, done-when, the
  decision needed); the answer comes back into its conversation. The infra Claude is the
  organization's Infra lead.
- **The Company view.** The sidebar's Company shows every organization's Claudes as a pastel
  solar system around you: executives, department leads and cards on three rings, halos while
  they work, stickers when a card needs you, hand-offs flying between them, and sticky notes for
  the cards waiting on you. A click opens the card's session.
- **Who decides what.** Every card's Claude is told the organization's decision rights
  (architecture: CTO, security: CISO, spending: CFO, merge: tech lead, deploy and access: you),
  which `[company.decides]` changes.
- **Budgets.** `card_budget` and `day_budget` in `[company]` cap what an organization's sessions
  spend; a card over one pauses, what other Claudes send it waits, and your next message
  continues it.
- **The company log.** Every hand-off between an organization's Claudes (subagents, leads, the
  infra Claude) and what came back, verdict included, goes to `<Org>/.desk/company/log.jsonl`.

### Security

- **One organization's Claudes stay in it.** A card's Claude could reach another organization
  through desk: its card tools took whatever card they were told to act as (another
  organization's alerts, tickets, CI, drawings, workspace), `desk peer` could talk to any
  organization's infra Claude under any name and list them all, an infra Claude's
  `message_card` could write to another organization's card, the infra tabs' server tools
  answered any caller, and a drawing's name could lead out of its folder. desk now knows each
  call's Claude by its process (Linux, macOS, Windows) and holds it to its own card and
  organization; your own programs outside desk's Claudes keep full reach.
- **Each session walled into its organization (Linux with bubblewrap, and macOS).** A session's Claude,
  and an infra tab's local panes, now find every other organization's folder empty, as well as
  desk's board, chats and state and the other Claude accounts' profiles; its own folder, its added
  projects and pasted images stay. On Linux what it starts stays in its own process namespace, so
  a background process can't leave it; on macOS the system's sandbox (`sandbox-exec`) refuses
  those folders to it and to everything it starts.

### Fixed

- Hyprland (and other Wayland compositors) no longer reports desk as not responding at start, after
  the display wakes, or while its window sits on another workspace, with NVIDIA's driver: drawing a
  window the compositor isn't showing waited in the driver for a frame that never came, so desk
  missed the compositor's pings. A hidden window now draws when it is shown again. A window away
  from its events for more than 3 seconds also leaves a line in `crash.log` and its stacks in
  `stall.log`.

## [2026.11.16.1] - 2026-11-16

### Fixed

- A long command waiting for approval in the infra workspace no longer pushes Approve and Deny out
  of the window: the sheet stays inside the window and the command scrolls, the buttons always in
  view. The vault unlock and exit questions scroll the same way in a short window.

## [2026.11.16] - 2026-11-16

### Fixed

- A card's Claude replying to the infra Claude (`desk peer tell`: the answer to its question, the
  go-ahead it waited for) no longer sits in the infra chat until you type something: once that
  session has a conversation, the message starts its next turn by itself, right away or when its
  current turn ends.
- With two cards' sessions in their own windows (or one there and one in the main window), each
  with its servers beside it, the servers no longer stay stuck in the first window: they move to
  the window you work in, on that card's own infra tab, and opening a server from the main window
  shows it there instead of in the other card's window.

## [2026.11.15.2] - 2026-11-15

### Added

- **The AppImage updates itself** with AppImageUpdate (or AppImageLauncher, Gear Lever…): it
  carries where its updates are published, and each release has its `.zsync`, so an update
  downloads only what changed.
- The AppImage brings its own glibc and uses it on distributions older than Ubuntu 22.04 (Debian
  11, RHEL 8…), where it didn't start before.

### Fixed

- The AppImage opens its window on systems without the GLES library (`libgles2`), which it now
  brings along.

## [2026.11.15.1] - 2026-11-15

### Fixed

- The AppImage is in the release again: 2026.11.15 shipped without it. This is the one that opens
  under X11 and brings its libraries (see 2026.11.15).

## [2026.11.15] - 2026-11-15

### Added

- **Exit confirmation**: closing desk while cards have Claude at work (a turn, background
  subagents or shells, a `!` command) asks first and names those cards. Exit anyway with Enter, or
  stay with Esc.
- **The AppImage opens under X11** as well as Wayland, and brings the libraries it needs, so it
  runs on Ubuntu 22.04 and later and other distributions without installing anything. Under X11,
  Ctrl+V of an image pastes it, as under Wayland.

### Changed

- PipeWire is only needed for the infra workspace's virtual camera, loaded when the camera
  starts: desk starts without it, and the .deb and AUR package only recommend it.

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

[Unreleased]: https://github.com/lfdominguez/desk/compare/2026.11.19.1...HEAD
[2026.11.19.1]: https://github.com/lfdominguez/desk/compare/2026.11.19...2026.11.19.1
[2026.11.19]: https://github.com/lfdominguez/desk/compare/2026.11.17...2026.11.19
[2026.11.17]: https://github.com/lfdominguez/desk/compare/2026.11.16.5...2026.11.17
[2026.11.16.5]: https://github.com/lfdominguez/desk/compare/2026.11.16.4...2026.11.16.5
[2026.11.16.4]: https://github.com/lfdominguez/desk/compare/2026.11.16.3...2026.11.16.4
[2026.11.16.3]: https://github.com/lfdominguez/desk/compare/2026.11.16.2...2026.11.16.3
[2026.11.16.2]: https://github.com/lfdominguez/desk/compare/2026.11.16.1...2026.11.16.2
[2026.11.16.1]: https://github.com/lfdominguez/desk/compare/2026.11.16...2026.11.16.1
[2026.11.16]: https://github.com/lfdominguez/desk/compare/2026.11.15.2...2026.11.16
[2026.11.15.2]: https://github.com/lfdominguez/desk/compare/2026.11.15.1...2026.11.15.2
[2026.11.15.1]: https://github.com/lfdominguez/desk/compare/2026.11.15...2026.11.15.1
[2026.11.15]: https://github.com/lfdominguez/desk/compare/2026.11.12.1...2026.11.15
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
