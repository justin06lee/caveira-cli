# caveira desktop

The desktop client: the same agent as the terminal one, in a window. Go and [Wails](https://wails.io) v2 on the back, React and TypeScript on the front, built with bun.

## What it does

Open a project and talk to caveira about it. It reads, searches, edits, and runs commands in that folder the way the terminal client does, because it is the same agent: the loop, tools, prompt, and settings all come from the shared `core` module at the repository root.

The first launch takes two steps, both skippable and both in Settings afterwards. First it offers to bring over the chats other coding agents left on this computer, from Claude Code, Codex, OpenCode, and Gemini CLI, grouped by app and project; see [Importing](#importing). Then it asks where your projects live, with the folder most of the known projects share already typed in.

Projects are opened with the picker (⌘O, and the whole window when no project is open): a box to type in, and the workspace folder's contents under it, one line each, the way `ls -a` would list them. Typing narrows the list to what starts with the last part of the path, and a slash goes into a folder. Tab completes as far as the matches agree and, when they agree no further, steps through them the way a shell's menu completion does. `..` and enter go up a level for as long as the picker is open; `~/` and `/` start from home or the root. Hidden files show with the eye in the box, or ⌘⇧. as in the Finder (Ctrl+H on Linux, as in GTK), or when what is typed starts with a dot. Enter opens the highlighted folder, or a folder typed with a slash after it; a name that is not there is offered as a new folder. Browse… is the Finder's own dialog.

The window is a sidebar and a conversation, set in Poppins (code stays monospaced). caveira's mark and name sit beside the traffic lights, with New project (⌘O, the picker) under them. Double-clicking the title bar zooms the window and back, or does whatever the Mac's own setting for that says. The sidebar lists your projects as folders, most recently used first; click one to open it and its chats are inside, newest first, so a chat in any project is one click away. A folder shows six chats until you ask for the rest, remembers whether it was open, and puts a dot on itself while a chat inside it is working. Hover a folder for + (a new chat there) and × (take it off the sidebar; the folder and its chats stay on disk), or right-click it for those and Show in Finder (Open in Files on Linux). ⌘N starts a new chat in the project you are in, ⌘O opens another project, and chats keep running when you switch away from them. In the conversation, your messages sit on the right and caveira's replies are plain markdown, code in panels with a copy button. Each tool call is one quiet line (`Read main.go · 11 lines`, `Edit main.go +1 −1`, `Run go test ./...`) that shimmers while it runs and opens to show its output or diff; a failed one turns red. A model's thinking folds into a "Thought" line. While caveira works, a line under the conversation says what it is up to the way the terminal's status line does: the terminal's 11-pixel skull drawn in dots, each dot landing with a flare and fading back in the terminal splash's scattered order, then one of the same rebel verbs (`Plotting…`, `Casing the joint…`) and how long the turn has run. An empty chat asks what the job is, in a line that goes with the time of day. A new chat is named by Qwen3.5 from its first message, a few words a moment after it is sent, in the title bar and the sidebar; until then, and if naming fails, it goes by that message's first line. Each turn closes with how long it took, how many tools it ran, and the tokens it used. Under the composer: the model and reasoning effort (click to switch; the choice becomes the default for new chats). The menu lists the models caveira offers, named for the open-weight model each comes from: Qwen3.5 (`qwen-3.5`), GLM-5.2 (`glm-5.2`), and GLM-5.3 (`glm-5.3`), each beside the mark of the lab that made the weights (Qwen's or Z.ai's), in the text colour. Beside it, its own chip sets the reasoning effort. Then how full the context window is. Nothing in a chat is in dollars. `enter` sends, `shift+enter` adds a line, `esc` stops.

The third chip under the composer is the chat's permissions: Ask permissions, or Bypass permissions (in red), which runs everything without asking. ⇧Tab in the composer flips it, and the choice becomes the default for new chats (Settings has it too). It can be flipped mid-turn: the next tool call goes by it, and bypassing lets through whatever is already waiting. Asking, a command or file change waits in the transcript as a card showing the command, or the lines an edit takes out and puts in, with Deny, Always allow, and Allow.

A chat runs on a caveira account. Signing in (Settings, or the card under a held message) opens the caveira site's sign-in page in the browser with a code, which the window shows too ("Finish signing in in your browser · code ABCD-EFGH", with Cancel) until it is connected there; the account then lives in `~/.caveira/auth.json`, shared with the terminal, and chats send their model requests through the site's OpenAI-compatible proxy with its token, so no model key ships in the app. Signed out, a message sent stays on screen with a sign-in under it, and goes once the sign-in does. A token the site stops taking (run out, or revoked) ends the turn with a notice that offers to sign in again, and Settings says so too.

An account is on a plan: Free (Qwen3.5, light usage), or Lightweight, Middleweight, Heavyweight, and Champion at $20, $50, $100, and $200 a month, which add the GLM models and 10x, 25x, 50x, and 100x the usage of Free. Usage is counted by the caveira site and always shown relative to Free or as the share of the period spent, never in dollars. The plans are laid out the way tojibrowser.net's pricing page is: Free across the top, the three middle weights as cards, Champion across the bottom. A GLM model on Free holds the message with the plans under it. Picking a plan opens its checkout on the caveira site (Stripe) in the browser; the plan is the site's, asked after when the window comes to the front and every five seconds while a message is held (for up to ten minutes), and a held message goes as soon as the account runs it. `plans.go` is where that check lives, and `account.go` the account. The Settings page opens with the account (its email, its plan as `Lightweight · 10x the usage of Free`, a bar for this period's usage and when it resets, Manage, which opens the site's account page for billing, and Sign out) and the plans under it. The bar goes amber past 80% and red once it is all used, and a slim copy of it sits in the sidebar above Settings (click it for the full one); it is asked after again when a turn on the account ends. An admin account reads `Admin · unlimited`, with no bar and no plans.

The app works only with a caveira account and runs the models the account offers; there is no setting for a key, an endpoint or a model server. Settings (⌘,), a page in place of the conversation, holds the account and the plans, the default model and effort, MCP servers and skills, the ask-first switch, the workspace, and light, dark, or system appearance.

Phone companion (Settings › General, off by default) lets the caveira iPhone app (not part of this repository) follow the chats open here. While it is on and you are signed in, the app posts each open chat to caveira.app as it changes, a chat with nothing said in it excepted, and keeps a request open at the site, up to 25 seconds at a time, for what the phone wants: an answer to a permission card, a follow-up, which goes into the chat as if typed here, or a stop. A permission waiting, and a turn done, go to the phone as a notification. The relay is plain, not end-to-end encrypted, so the site holds the open chats; turning the switch off deletes them there, and quitting marks the Mac offline.

MCP servers and skills are the terminal's (see [MCP servers](../tui/README.md#mcp-servers) and [Skills](../tui/README.md#skills)), with the same files. In Settings, MCP servers lists the ones set up for you and for the open project, each with a switch, Remove, and its status; clicking one shows its tools, or the error it failed to start with. Add server… searches the MCP Registry, lets you pick how a server runs, and asks for the values it needs (secrets in password fields); Import… lists servers set up in Claude Code, Claude Desktop and Cursor, and copies only the ones you switch on. Skills lists every skill with where it lives, a switch, and Remove for ones caveira installed, and installs from a GitHub address. A project's servers start with its first chat and are shared by its chats; MCP calls show in the transcript like any tool, with a plug icon, and ask like commands do.

Chats are the same session files the terminal uses, under `~/.caveira/sessions/`, so a chat started in one opens in the other. The model, effort, and ask-first setting are `~/.caveira/config.json`, shared with the terminal; `.env` files and the environment still win over it. What only the app keeps (recent projects, the workspace, appearance) is `~/.caveira/desktop.json`; a plan picked there before accounts existed is let go.

The macOS app updates itself. As it opens, on a background goroutine so the window is usable meanwhile, it asks for the latest release, waiting two seconds at most. When there is a newer one it downloads `caveira.dmg`, checks it against the release's `checksums.txt`, opens it, checks the app in it is signed with caveira's Developer ID (`update.AppRequirement`) and is the version the release says, keeps the old app as `.caveira.app.old` beside it until the new one's window has loaded, puts the new one in place, and updates a terminal client that `install.sh` put in `~/.local/bin` to the same release. The window then restarts into it, unless a chat is running or something is half typed, when a "Restart to use v0.3.9" button waits above Settings; the next launch starts the new version anyway. Any failure leaves the current version running. A version that doesn't come up twice is put back and not tried again until a newer one is out.

The app looks again every six hours, and shows a quiet "Update to v0.3.9" button when a release is out; it does the same steps by hand. Where it cannot write to the app's folder, the button opens the disk image and says to drag caveira to Applications, and no update happens by itself. Builds from a clone never ask. `CAVEIRA_NO_AUTO_UPDATE=1` or `no_auto_update` in `~/.caveira/config.json` leaves updating to you; `CAVEIRA_NO_UPDATE_CHECK=1` or `no_update_check` turns the check off altogether.

An app opened from the Dock (or GNOME's app grid) does not get your shell's environment, so on launch it asks your login shell for it once, the way editors do: the agent's commands find `go`, `bun`, and whatever else your `PATH` has, and a key exported in your shell profile is seen.

## Importing

File → Import from Other Agents…, Settings, or the first launch. Each app's chats are read into caveira's own session format by `core/importer`: tool calls caveira also has become its own (a Claude Code `Edit` is an `edit_file`, with the diff Claude Code kept beside it), the text agents slip into user messages (environment blocks, reminders, slash-command records) is left out, a compacted chat starts from its summary as caveira's own do, and every tool call is answered so the history is one an endpoint accepts when the chat is carried on. Tool results are cut to 24 KB each. A tool caveira does not have keeps its name and input and shows as a line of its own.

Where each app keeps its chats: Claude Code in `~/.claude/projects` (or `$CLAUDE_CONFIG_DIR`), Codex in `~/.codex/sessions` (or `$CODEX_HOME`), OpenCode in `~/.local/share/opencode/opencode.db` (or `$XDG_DATA_HOME`), read with the `sqlite3` that ships with macOS (on Linux, install the `sqlite3` package), Gemini CLI in `~/.gemini/tmp/<project>/chats` (its `logs.json` holds only what was typed, so it is not read). Chats that ran in temporary folders or inside app bundles, subagents' threads, and Codex's own copies of Claude Code chats are left out. The originals are only read.

An imported chat keeps its id in the other app (`claude-<id>`, `codex-<id>`, `opencode-<id>`, `gemini-<id>`), so importing again brings over only what is new. Opening an imported chat brings it up to date when the original has gone on in the other app, unless it has also been carried on in caveira: then the two have parted and caveira's copy is left as it is. Chats with nothing said in them are remembered in `desktop.json` and not offered again. Their folders join the recent projects, up to forty.

## Build and run

From the repository root, `make update` builds both clients, installs `caveira.app` into `/Applications` (and the terminal client into `~/.local/bin`), and opens the app (`make install` does the same without opening it; plain `make` only tests and builds). It builds before it touches anything installed, then quits the running app, swaps in the new one, and opens it. Each local build is signed anew, which macOS treats as a different app for folder access (Documents, Desktop, external drives), so `make update` also clears the old build's grants and the new one asks again.

To build just the app: `make desktop`, which leaves it in `desktop/build/bin/caveira.app` (on Linux, the binary `desktop/build/bin/caveira`; it needs GTK 3 and WebKitGTK headers, see the main README). Wails is run with `go run`, so there is nothing to install besides Go, bun, and the Xcode command line tools. With the full Xcode on the machine, the build also compiles the app's Icon Composer icon; it need not be the selected one or in `/Applications`, since the Makefile asks Spotlight for any Xcode and runs its `actool` directly. Without one the app keeps the classic icon, which macOS 26 shades with glass in the Dock.

On Linux, `make install` installs the binary as `~/.local/bin/caveira-desktop` with a launcher and icon under `~/.local/share`, so it is in the app grid; it needs GTK 3 and WebKitGTK 4.1 headers (see the [Linux section](../README.md#linux) of the main README). The window keeps the system title bar and has no menu bar; the page takes Ctrl+N, Ctrl+O, Ctrl+, and Ctrl+\ itself.

## Working on it

```sh
wails dev                               # live-reloading window (go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0)
go test ./...                           # the backend: turns, approvals, transcripts, settings, the account
cd frontend && bun run build            # typecheck and bundle the frontend
```

WebKit does not paint a window that is behind others, so to look at the UI without bringing the app forward there is a harness: it serves the built frontend with a stand-in for the Wails bridge, backed by the real Go side, for any browser.

```sh
cd frontend && bun run build && cd ..
HOME=/tmp/caveira-home CAVEIRA_HARNESS=localhost:34999 go test -run TestHarness -timeout 0 .
```

Setting `HOME` keeps the harness's chats and settings out of your real ones. To try the import on real chats, link `~/.claude`, `~/.codex`, and `~/.local/share/opencode` into that home; the importer only reads them. The frontend is embedded when the test builds, so restart the harness after `bun run build`. Pages the app would open in the browser are only logged.

With `CAVEIRA_HARNESS_SITE=1` and `-tags devserver` (a shipped build always signs in at caveira.app) the harness makes its own home and runs against a stand-in caveira site, signed out, to look at the account: sign in from the window, then `curl -X POST localhost:34999/site/approve` connects it (`/site/deny`, `/site/wait`, `/site/revoke`, and `/site/plan/lightweight` do what they say). Its model answers in one line; `/site/busy/<n>` has it rate limit the next n turn requests, asking for 8s each, and a turn there gives up after 30s, to look at the countdown and Try again.

## Layout

- `main.go` — the window, its menu (macOS only), and its options for macOS and Linux.
- `app.go` — what the window can ask for: projects, settings, model lists.
- `files.go` — the folder picker's listings, new folders, the workspace, and the end of the first run.
- `imports.go` — finding and importing other agents' chats (the reading itself is `core/importer`).
- `extensions.go` — MCP servers (one manager per project) and skills, for Settings and the chats.
- `update.go` — checking for a newer release and replacing the app with it.
- `chat.go` — chats: building an agent from the settings, running turns, approvals, and turning agent events into the transcript and the events the window hears.
- `names.go` — naming a new chat with the small model from its first message.
- `account.go` — the caveira account: signing in through the browser and out, what the site says the plan is, and asking again while a message is held.
- `plans.go` — the plans, which models each runs, and holding a message until the account runs it; also the models the account offers.
- `transcript.go` — rebuilding a saved chat's transcript from its messages.
- `prefs.go` — `~/.caveira/desktop.json`.
- `shellenv.go` — the login shell's environment for a Dock launch.
- `harness_test.go` — the browser harness.
- `frontend/src/lib` — the bridge to Go, the store, theme, formatting, and the rebel lines (`rebel.ts`).
- `frontend/src/components` — the sidebar, chat pane, transcript, tool rows, composer, settings with the account, the plans and the sign-in (`Plans`), the model makers' marks (`ModelLogos`), the app's own dropdown (`Select`), the working skull (`SkullLoader`), MCP servers and skills (`Extensions`), the first run (`Onboarding`, `ImportPanel`), and the folder picker (`PathPicker`, shown by `Welcome` and `OpenProject`).
- `frontend/src/styles.css` — the whole look: the icon's off-white and black, and the steps between them.
- `build/appicon.png` — the classic icon Wails packages, for macOS before 26.
- `build/linux/caveira.desktop` — the Linux launcher `make` installs, with the binary's path filled in.
- `build/darwin/caveira.icon` — the Icon Composer icon for macOS 26, with glass, highlight, and shadow off so the Dock shows it flat: the eye from `assets/caveira-icon.svg` on a solid `#F8F7F2` fill.

## Windows

In beta, a zip from [caveira.app/download/windows](https://caveira.app/download/windows) (see the repository README); unsigned, so SmartScreen warns: More info, then Run anyway. `caveira-desktop.exe` is a Wails window on the WebView2 runtime; it keeps its log in `%USERPROFILE%\.caveira\logs\desktop.log` as elsewhere. It has no menu bar (the page takes the shortcuts, Ctrl rather than ⌘, as on Linux), "Show in Explorer" opens a project's folder, and it does not update itself: download the zip again for a newer one.
