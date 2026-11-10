<div align="center">

<img src="assets/banner.svg" alt="caveira: a coding agent for open-weight models" width="720" />

# caveira

**A coding agent for open-weight models, in your terminal and on your desktop.**

</div>

---

## What it is

caveira is an agentic coding assistant in the spirit of Claude Code, built to drive open-weight models whose refusal behavior has been removed. You give it a task in your project; it reads and searches the code, edits files, runs commands, checks its own work, and tells you what it did. It does not second-guess what you asked for. The only pushback it gives is technical.

This repository is the client: a terminal app (`tui/`) and a desktop app (`desktop/`, Wails with a React frontend) that share one agent (`core/`) and one set of sessions. A chat runs through a caveira account: `/login` opens the caveira site, [caveira.app](https://caveira.app), in your browser; once you connect the machine there, requests go to `https://caveira.app/api/v1` (an OpenAI-compatible API) with your account's token. The site holds the accounts and plans and is not part of this repository.

Every turn, caveira sends the conversation and its tool definitions to the model and streams the reply. When the model calls a tool, caveira runs it, appends the result, and asks again, until the model answers in words. The tools are read a file (numbered lines), write one, replace an exact string (with a diff back), run a shell command, find files by glob, search contents by regex, and list a directory. It also speaks the [Model Context Protocol](https://modelcontextprotocol.io) (`~/.caveira/mcp.json`, Claude Code's `mcpServers` shape) and loads [Agent Skills](https://docs.anthropic.com/en/docs/agents-and-tools/agent-skills) from `~/.caveira/skills`, `~/.claude/skills` and a project's `.caveira/skills`. Conversations are saved under `~/.caveira/sessions/`, and a long one is summarized into a handoff note as it nears the model's context window. One server is set up on first launch when Node is installed: [rea](https://github.com/morluto/rea), a third-party reverse-engineering server, which caveira keeps up to date after verifying each release (see [tui/README.md](tui/README.md#rea)); `x` in `/mcp` removes it.

## Install

```sh
curl -fsSL https://caveira.app/install.sh | sh   # installs `caveira` (and `cav`) into ~/.local/bin
cd your/project && caveira                       # then /login to sign in to your caveira account
```

`install.sh` downloads the latest release built for your machine (macOS or Linux, arm64 or x86-64; Windows below) from the public [caveira-releases](https://github.com/justin06lee/caveira-releases) repository, checks it against the release's `checksums.txt`, and puts it in `~/.local/bin`; run again to update. `CAVEIRA_VERSION=v0.3.10` installs a given release and `CAVEIRA_INSTALL_DIR` another folder. The macOS app is a disk image on the same releases page. Released builds update themselves at launch (the app only to an app signed with caveira's Apple Developer ID); `CAVEIRA_NO_AUTO_UPDATE=1` or `"no_auto_update": true` in `~/.caveira/config.json` leaves updating to `/update`, and `CAVEIRA_NO_UPDATE_CHECK=1` turns checking off. A build from a clone never asks.

Run `caveira`, or `cav` for short. `esc` interrupts, `/help` lists the commands, `caveira -c` continues the last session for the directory, and `caveira -p "…"` runs a task without the interface. The full reference is in [tui/README.md](tui/README.md) and [desktop/README.md](desktop/README.md).

caveira works only with a caveira account: every model request goes through it, and a released build has no setting for a key, an endpoint or a model server. The test suite is built with `-tags devserver`, which lets it stand a fake caveira site and a fake model in for the real ones; see [tui/README.md](tui/README.md).

### Windows (beta)

caveira runs on 64-bit Windows, in beta. Get the terminal client with one line in PowerShell, which puts `caveira.exe` and `cav.exe` in `~\.local\bin` and adds that folder to your PATH:

```powershell
irm https://caveira.app/install.ps1 | iex
```

The app is `caveira-desktop.exe`, in a zip from [caveira.app/download/windows](https://caveira.app/download/windows). It needs the WebView2 runtime (part of Windows 11, and of Edge on Windows 10).

- **The builds are not signed.** SmartScreen says "Windows protected your PC" on the app from a browser: More info, then Run anyway.
- **No self-update.** A running .exe cannot be replaced, and an unsigned download has no signature to check: launch updates and update checks are off, and `/update` (or `caveira update`) says so. Update the terminal client by running the install line again (it checks the download against the release's `checksums.txt`), and the app by downloading the zip again.
- **Commands run in Git Bash when Git for Windows is installed, in PowerShell otherwise** (`CAVEIRA_SHELL=powershell|pwsh|bash|cmd|<path>` chooses); the model is told which, and the `!` command in the terminal client uses the same shell. WSL's bash is not picked on its own.
- **Paths:** `~/.caveira` is `%USERPROFILE%\.caveira`. `auth.json` and `config.json` are written readable by your account alone (the access list is cut down to you, nothing inherited), where on a Mac or Linux they are mode 0600. Paths the model writes with `\` or `/` both work, and tool output uses `/`. Files with CRLF line endings are edited and keep them.

## Build from source

You need [Go](https://go.dev/dl/) 1.27 or newer, and [bun](https://bun.sh) for the desktop app's frontend. Wails is fetched by `go run`, so nothing else is installed globally. On Linux the desktop app also needs GTK 3 and WebKitGTK 4.1 headers (Debian and Ubuntu: `sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`, plus `sqlite3` to import OpenCode chats and `xclip` or `wl-clipboard` for the terminal client's copy button); on macOS, the Xcode command line tools.

```sh
git clone https://github.com/justin06lee/caveira-cli.git && cd caveira-cli
make
```

`make` is the golden path: `make deps test build`. It installs nothing and opens nothing (it only fills the Go and bun download caches). The steps, each of which you can run alone:

| Command | What it does |
|---|---|
| `make deps` | Downloads and verifies the Go modules of `core`, `tui` and `desktop` (and repairs a module cache that something emptied part of). |
| `make test` | `go vet` and `go test` in all three modules. The tests run against fake servers and temporary home folders. |
| `make tui` | Builds the terminal client to `tui/bin/caveira`. Needs only Go. |
| `make desktop` | Builds the desktop app: the frontend with bun, then Wails. Leaves `desktop/build/bin/caveira.app` on macOS and `desktop/build/bin/caveira` on Linux. |
| `make build` | `deps`, `tui` and `desktop`. |
| `make install` | `build`, then puts `caveira` and `cav` in `~/.local/bin` (`BINDIR=…` for elsewhere) and the app in `/Applications` (`APPDIR=…`) or, on Linux, `~/.local/bin/caveira-desktop` with a launcher and icon. |
| `make update` | `install`, but the running app is quit first and the new one opened. Built before anything installed is touched, so a build that fails leaves the old caveira working. |

On Windows, with Go, bun, Git for Windows and GNU make (Git Bash) installed, `make deps test tui desktop` builds `tui/bin/caveira.exe` and `desktop/build/bin/caveira-desktop.exe`; `make install` and `make update` are not done there. From Linux or a Mac, `make windows` cross-compiles both for `WIN_ARCH=amd64` (the default) or `arm64`, with no Windows machine and no C compiler.

`cd tui && go run .` runs the terminal client without installing it. Layout: `core/` is the agent (the OpenAI-compatible client, tools, system prompt, turn loop and sessions, settings, MCP, skills, self-update, and importing chats from other agents); `tui/` is the Bubble Tea v2 terminal client; `desktop/` is the Wails v2 app with its React and TypeScript frontend; `assets/` holds the icon and the banner.

## The models

caveira's hosted models are third-party open-weight models (Qwen from Alibaba, GLM from Z.ai) with their refusal behavior removed, served by a third-party inference provider and reached through caveira's servers. We didn't train them.

The names caveira shows and sends are `qwen-3.5`, `glm-5.2` and `glm-5.3`; the server lists them and decides which your plan may use.

## License

Apache License 2.0; see [LICENSE](LICENSE).
