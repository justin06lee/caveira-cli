# caveira tui

The terminal client: the coding agent itself, in Go with [Bubble Tea](https://github.com/charmbracelet/bubbletea) v2 and Lip Gloss v2.

## What it does

You describe a task; caveira reads and searches the project, edits files, runs commands, and checks its own work, streaming everything to the screen as it happens. Its model requests go through your caveira account.

```sh
caveira                          # interactive session in the current directory
caveira "add a --json flag"      # same, with the first message already sent
caveira -p "explain main.go"     # one-shot: run the task, print the reply, exit
caveira -c                       # continue the latest session for this directory
caveira login                    # sign in to your caveira account (or /login in a session)
caveira logout                   # sign out
caveira update                   # update to the latest release (or /update in a session)
```

`cav` is the same program under a shorter name; `curl -fsSL https://caveira.app/install.sh | sh` installs both, and so does `make install` from a clone.

On launch the pixel skull dissolves in and the name types itself in under it while settings, git state, and the previous session load on another goroutine; any key skips the intro. The prompt then appears centred under them, with the model and directory beneath it, in the same place the intro left room for, so nothing moves. The skull is drawn as large as the terminal's height allows, and left out on very small ones. The first prompt slides the box to the bottom as the skull dissolves out, and the transcript opens with a card of the session's model and directory.

The skull is drawn in half-block cells, two pixels to a cell. Terminals with 256 colours or true colour get the coloured skull, bone on a black outline; 16-colour terminals, `NO_COLOR`, and dumb terminals get the one-colour skull, drawn in the terminal's own foreground. caveira asks the terminal for its background colour and switches to a light palette on light backgrounds.

In the transcript, your messages are chat bubbles on the right, with corners cut to quarter cells (as round as a terminal cell gets) and a small tail; each tool call is a line (`Read main.go`, `Bash $ go test ./...`) with its result under it, edits show a numbered diff on tinted rows, and each turn closes with how long it took, how many tools it ran, and the tokens it used. While the model works, the line above the input says what it is up to, for how long, and roughly how many tokens have streamed: a running tool says what it runs (`Running go test`, `Editing main.go`), and while the model thinks or writes it is the underground at work, a new line each step (`Casing the joint…`, `Cutting the wires…`, `Throwing pocket sand…`). Messages you send while it works wait at the bottom of the transcript as dashed bubbles and go out together when the turn ends; `↑` takes the newest back to edit. Typing `/` opens a command palette under the input: arrows choose, `tab` completes, `enter` runs. `/model` opens a picker in the input's place with the models the endpoint serves and the reasoning effort under them: `↑`/`↓` choose the model, `←`/`→` the effort, `enter` switches. Under `--confirm`, approvals are a card with three choices, picked with the arrows and `enter` or with `1`–`3` (`y`, `a`, `n` also work). The footer shows the keys that matter at the moment on the left, and the model, effort, tokens used, and a context-window meter on the right. Nothing in a session is in dollars.

In a session, `enter` sends and `shift+enter` (or `alt+enter`, `ctrl+j`, or a trailing `\`) adds a line; past eight lines the text scrolls inside the box. `esc` interrupts the model mid-turn. `ctrl+t` shows the model's reasoning, `ctrl+o` expands tool output, `pgup`/`pgdn` and the mouse wheel scroll (`ctrl+end` jumps back to the bottom), `up`/`down` recall earlier prompts. Code blocks in replies have a `copy` button in their header: click it to put the code on the clipboard (through the terminal over SSH), or type `/copy` for the last one. `!<command>` runs a shell command yourself, with the output also going to the model. `/help` lists the slash commands: `/model`, `/effort`, `/compact`, `/copy`, `/usage` (or `/cost`), `/clear`, `/session`, `/mcp`, `/skills`, `/login`, `/logout`, `/update`, `/quit`. On a caveira account `/usage` shows the plan (`Lightweight · 10x the usage of Free`) over a bar of this period's usage (`████████░░░░░░░░  48% used · resets Nov 20`) as wide as the terminal allows, amber past 80% and red once it is all used, then this session's tokens; an admin account reads `Admin · unlimited` with no bar. A reply that takes the account past 80% of its usage, or uses the last of it, says so in one quiet line.

A released caveira updates itself. Each interactive launch asks for the latest release, waiting two seconds at most; when there is a newer one it says `updating caveira to v0.3.9...`, downloads it, checks its SHA-256 against the release's `checksums.txt`, checks its signature on a Mac and that it runs, renames it over the old binary (kept as a hidden `.caveira.old` beside it until the new one is up) and starts it. Offline, a slow answer, a bad download, a bad signature or a folder it can't write to all mean the current version starts as usual. A version that doesn't come up twice is put back and not tried again until a newer one is out. A session already running is never touched, and scripted runs (`-p`) never update.

When a newer release is out and wasn't installed, or one lands during a long session (caveira looks again every six hours), caveira says `update available: caveira v0.3.9 · run /update` on the home screen or in the transcript and `/update` in the footer. `/update` and `caveira update` install it by hand; restart caveira to use it. Where caveira cannot write to its folder (installed by root, say), it says the command to run instead. Development builds never ask. `CAVEIRA_NO_AUTO_UPDATE=1` or `no_auto_update` in `config.json` leaves updating to you, and `CAVEIRA_NO_UPDATE_CHECK=1` or `no_update_check` turns the check off altogether.

Sessions are saved as JSON under `~/.caveira/sessions/` after every turn, so `caveira -c` picks up where you left off and `caveira --sessions` lists what is there. When a conversation approaches the model's context window the agent summarizes it into a handoff note and continues from that.

## Signing in

caveira runs on your caveira account. Type `/login` in a session: your browser opens the caveira site's sign-in page with a code on it, and while you sign in there and connect this machine, the session waits in the background and you can keep typing. The transcript shows the page's address, in case the browser did not come up, and the code, to check against the page; `esc` or a second `/login` calls the wait off. Once you connect it, the transcript says `Signed in as` your email, the session card shows the account, and the same session, conversation and all, carries on through it without a restart. Already signed in, `/login` only says as whom; `/logout` first to use another account. `/logout` ends the session on the site and on this machine, and the session says to `/login` again.

The token is kept in `~/.caveira/auth.json`, readable only by you. Model requests go with it to the caveira site's OpenAI-compatible `/api/v1`, which checks your plan and usage and answers them; when the site turns a request down (signed out elsewhere, out of usage, a model your plan does not run), its own message is what the transcript shows.

`caveira login` and `caveira logout` do the same without the interface, for a machine reached over SSH or with no screen: `login` prints the page and the code, tries the browser, waits, and prints who it signed in as. Started without an account, caveira opens anyway, but sends nothing: its home screen and any message you send say to `/login`, and `caveira -p` fails at once and says to run `caveira login`. A key or a model server of your own does not stand in for the account.

## Configuration

caveira works only with a caveira account: every model request goes through it, and there is no setting for a key, an endpoint or a model server. caveira looks, in order of increasing priority, at `~/.caveira/config.json`, then `.env` and `.env.local` files between the repository root and the working directory, then the environment, then flags:

| What | Environment | `config.json` | Flag |
|---|---|---|---|
| Model | `CAVEIRA_MODEL` | `model` | `-m`, `--model` |
| Reasoning effort | `CAVEIRA_REASONING_EFFORT` | `reasoning_effort` | `--effort` |
| Ask before commands and edits | `CAVEIRA_CONFIRM=1` | `confirm` | `--confirm` |
| No updating at launch | `CAVEIRA_NO_AUTO_UPDATE=1` | `no_auto_update` | |
| No update checks | `CAVEIRA_NO_UPDATE_CHECK=1` | `no_update_check` | |
| Soft token limit (default 434,000) | `CAVEIRA_SOFT_TOKEN_LIMIT` | `soft_token_limit` | |

There is no API key to set for the account. The default model is `qwen-3.5` (256K context, multimodal). `glm-5.3` is the stronger, text-only 1M-context model for harder work; switch in the `/model` picker, with `/model glm-5.3`, or by setting `CAVEIRA_MODEL`. Reasoning effort is one of `none minimal low medium high xhigh max`; unset leaves it to the model's default. `/effort <level>` sets it directly. Switches made in a session last for that session.

A session compacts before a request outgrows what it should carry: at 85% of the model's context window or 95% of the soft token limit, whichever comes first, so on a 1M-context model it is the soft limit that counts. Compaction calls no model of its own. Every exchange (a prompt and all that answered it) is recorded as it ends in `~/.caveira/sessions/<id>/`: a file with its whole record, and two notes in `exchanges.json`, one on the prompt and one on the reply, written by the small model on a paid plan and cut from the text on Free. The fresh context gets a brief built from those: where things stand as git tells it (branch, uncommitted files, the files the conversation changed), the last three exchanges at length, older ones as notes, each pointing at its record file to read when a note is not enough, and a turn in progress carries on where it was. The model is told it is near the limit and writes a handoff note that goes in the brief, and the transcript says so (`near the soft limit (412k / 434k) · writing a handoff note…`); when the note can't be had, the brief goes alone. What compaction retires is kept in the session's `archive.jsonl`, so a reopened chat still shows all of it. The desktop app compacts the same way, and `/compact` does it on demand here.

When the model is busy (rate limited, overloaded, or the connection drops, even partway through a reply) the turn waits and sends the same request again, as long as the server's `Retry-After` or rate-limit reset headers ask, otherwise 5s, 10s, 20s… up to 2 minutes at a time, for up to 15 minutes. The status line counts down (`Rate limited · retrying in 23s`), `esc` stops the wait with the turn, and whatever streamed of a reply that broke off is dropped as it streams again. A turn that waits the whole 15 minutes stops and says so; `enter` on an empty box then takes it up again where it was. Running out of credits or usage is never waited out: the turn ends and says so. `-p` mode waits the same way, with the countdown on stderr.

### Tests

The test suite is built with `-tags devserver`, which lets it stand a fake caveira site and a fake model in for the real ones. Builds caveira ships are made without it and talk to caveira.app only. `make test` runs the suite with the tag, then the `Shipped` tests without it, which check that a shipped build ignores every setting that names another server.

Project instructions are read from `CAVEIRA.md`, `AGENTS.md`, or `CLAUDE.md` (the first found in each directory from the repository root down to the working directory) and appended to the system prompt. `caveira --show-system-prompt` prints the prompt it would use here.

By default caveira does not ask before running commands or changing files: it is meant to be left alone with a task. `--confirm` turns on a prompt for every command and file change, with `a` to stop asking for that tool for the rest of the session.

## Tools

The model gets seven built-in tools, described to it in `core/tools`, plus the tools of any MCP servers and, when there are skills, `skill`: `read_file` (numbered lines, paged), `write_file`, `edit_file` (exact-string replacement with a diff back), `bash` (non-interactive, timed out, output bounded), `glob`, `grep` (RE2, skips binaries and build output), and `list_dir`. Write and execute tools are the ones `--confirm` gates.

Answering in words is the default, the way Claude Code behaves: the system prompt (`core/prompt`) keeps tools for work in the project and questions about it, with short examples of both, and each tool's description says what it is not for. `bash` refuses a command that only prints fixed text (`echo hello`), since its output reaches no one; small models reach for it to talk. A reply that is nothing but a tool call written out as text is repaired and run as the call.

## MCP servers

MCP servers are set up in `~/.caveira/mcp.json`, in the shape Claude Code uses, and a project's `.caveira/mcp.json` is laid over it server by server:

```json
{"mcpServers": {
  "files":  {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."], "env": {}},
  "remote": {"type": "http", "url": "https://example.com/mcp", "headers": {"Authorization": "Bearer ${MY_TOKEN}"}},
  "later":  {"command": "uvx", "args": ["some-server"], "disabled": true}
}}
```

`${VAR}` and `${VAR:-default}` are filled from the environment. Servers start in the background when the session does (stdio servers in the working directory), and the first message waits up to 20 seconds for ones still starting; their tools join as `mcp__<server>__<tool>`. Results are cut like command output. A server that cannot start does not stop the session: after the turn a line says so, and `/mcp <name>` says why.

`/mcp` opens a card in the input's place listing every server with its status and tool count: `enter` shows its tools (or its error), `space` turns it off and on, `x` twice removes it. `/mcp search <words>` searches the official MCP Registry; `←`/`→` choose how a server runs when there is more than one way, and `enter` adds it to `~/.caveira/mcp.json`, asking in the input for each value it needs (what you type there stays out of the transcript and the history). `/mcp import` lists the servers set up in Claude Code (`~/.claude.json`, the project's `.mcp.json`), Claude Desktop and Cursor; `space` picks, `enter` copies in, and nothing is copied before that.

MCP calls go through approval like shell commands (under `--confirm`, or a chat set to ask): "don't ask again" covers that server in this project, and is kept in `~/.caveira/mcp-allow.json`, outside the repository, so a repository cannot allow its own servers.

### rea

[rea](https://github.com/morluto/rea), the reverse-engineering server, is set up once on first launch when Node is installed (`npx -y rea-agents@6.7.0 mcp`), and from then on caveira keeps it up to date itself. It is third-party code, so nothing of a new version runs before it checks out. Each launch (in the background, turned off with the update settings above: `CAVEIRA_NO_AUTO_UPDATE`, `CAVEIRA_NO_UPDATE_CHECK`, `no_auto_update`, `no_update_check`) asks GitHub for rea's newest release. A newer one is installed only if:

- the npm package for its tag matches the registry's SHA-512;
- its Sigstore provenance verifies, saying morluto/rea's release workflow built that exact file from the commit the tag points at;
- it matches the hash recorded the first time that version was seen, if it was seen before;
- every dependency npm installs has a valid registry signature (`npm audit signatures`). No install script runs before that check.

The new version then has to start and list its tools in an empty folder. Only then does it replace the old one, and only from the next launch: a running session keeps the version it started with. The previous version is kept, and if the new one fails to start in a real session before it ever has, caveira goes back to the previous one and skips the new one until a newer release comes out. Versions live in `~/.caveira/tools/rea/` with their record in `state.json`; the `mcp.json` entry becomes `{"command": "node", "args": ["${CAVEIRA_REA}", "mcp"]}`, which caveira fills in with the version in use. An entry changed by hand, turned off or removed is left alone, and nothing is downloaded for it.

## Skills

Skills are Agent Skills, a folder with a `SKILL.md` (YAML frontmatter with `name` and `description`, then instructions) and whatever files it uses. caveira looks in the project's `.caveira/skills` and `.claude/skills`, then `~/.caveira/skills` and `~/.claude/skills`; the first of a name wins. Only names and descriptions go in the system prompt, and only when there are skills; the model calls the `skill` tool to load one's instructions, which also lists its files. `/skills` lists them with where each lives: `space` turns one off and on (`~/.caveira/skills.json`), `x` twice removes one caveira installed. `/skills install https://github.com/owner/repo/tree/main/skills/pdf` (or a repository, or `owner/repo/path`) installs the skill in that folder, or every skill under it, into `~/.caveira/skills`. Changes apply from the next message.

## Run from source

```sh
go run .                 # or: go build -o bin/caveira . && ./bin/caveira
go test ./...            # the interface; the agent's tests are in ../core
```

From the repo root, `make tui` builds `tui/bin/caveira`, and `make install` builds and installs `caveira`, with a `cav` link beside it, into `~/.local/bin` (`BINDIR=/usr/local/bin make install` to change that), along with the desktop app; `make update` replaces every installed copy with a fresh build, and only once the build has worked.

## Layout

- `main.go` — flags, configuration, the one-shot `-p` mode, and `caveira login` and `logout`.
- `internal/ui` — the Bubble Tea screens: the home screen with its intro (`home.go`), and the session with its transcript (`items.go`), status line, input, footer, approval card (`view.go`), and command palette (`commands.go`); `theme.go` holds the palette for dark, light, and low-colour terminals.
- `internal/art` — the 11×11 pixel skull mascot (`mascot.go`), rendered as half-block cells in colour or in one colour; `go run ./internal/art/preview` prints it.

The agent itself is not in here: it is the `core` module at the repository root, shared with the desktop app, which this module pulls in with a `replace` directive.

- `core/llm` — streaming client for OpenAI-compatible chat completions: SSE parsing, tool-call assembly, quick retries, and telling a busy model (`busy.go`) from one that is out of credits.
- `core/tools` — the tool set and its registry.
- `core/prompt` — the system prompt: stance, working method, tool guidance, environment, project instructions.
- `core/agent` — the loop: stream a reply, run the tools it asks for, feed results back; approvals, compaction, waiting out a busy model (`retry.go`), sessions.
- `core/config` — settings resolution and what is known about each model.
- `core/cloud` — the caveira account: the device login through the website, and the token it leaves in `~/.caveira/auth.json`.

## Windows

In beta: `irm https://caveira.app/install.ps1 | iex` in PowerShell installs it (see the repository README); unsigned. The terminal client runs in Windows Terminal, the console host and VS Code's terminal, anything with ConPTY. The `bash` tool is named that everywhere, but runs Git Bash if Git for Windows is installed and PowerShell (7 if present, else Windows PowerShell 5.1) if not; the system prompt and the tool's description tell the model which, and `CAVEIRA_SHELL` overrides. A command that times out or is cancelled is ended with everything it started (a job object, where macOS and Linux use a process group). `ctrl+c` and closing the window stop caveira the same way as elsewhere. It does not update itself: `/update` says so, and running the install line again updates it.
