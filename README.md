# CardHub

A live terminal dashboard for **GitHub Projects** boards. CardHub shows your board as kanban columns (or a table, for table views), keeps it in sync with GitHub, shows cards moving through your workflow, and can tell AI agents when cards change.

![CardHub showing the GitHub Public Roadmap board](docs/board.png)

**Why:** my team tracks QA bugs on a GitHub Projects board. I wanted that board live in my terminal instead of a browser tab, and I wanted my AI agents to start on a bug the moment QA files it, instead of polling GitHub every 30 minutes and spending tokens on "nothing changed". CardHub does both: it's the board I keep open, and the watcher my agents wait on. Close it and the automation stops too.

## Features

- **Live board.** Columns follow your project's Status (or a view's grouping) in a grid that wraps to fit any terminal 30+ columns wide. Dotted connectors show the workflow; when a card changes column, a white dot streams along the connectors it crossed, and recently changed cards get a yellow ●.
- **Table views as tables.** A view with the table layout shows its columns as on GitHub. On a narrow terminal the right-hand columns are hidden first; Title (shortened), Status and iteration fields (like a release plan) always stay.
- **Projects and saved views.** `P` and `V` open searchable menus. A view hides the columns its filter leaves out, like on GitHub. CardHub reopens the last board and view you watched.
- **Two boards at once.** `\` opens a second board: another project, or the same one in another view, side by side or stacked.
- **Search with GitHub filters, plus OR.** `/` takes free text or GitHub's filter syntax (`label:bug assignee:@me is:open`) and runs on GitHub, so totals stay exact. It also takes `OR` (or `|`), which GitHub lacks: `assignee:@me OR no:assignee` (up to 3 sides). Each view remembers its own search, also across restarts.
- **Stats.** How many cards moved between columns this session, per project and route (`In Progress → Done`), plus the latest moves.
- **AI link.** Agents (Claude Code or any agent that runs commands) ask CardHub to report card changes, then wait without polling. One CardHub serves several agents on several projects.
- **Gentle on the API.** Loads only the cards on screen (about 1 point per refresh), adapts its refresh rate to your hourly budget, and shows that budget live.

## Install

Download the archive for your system from [Releases](https://github.com/DXMHXR/cardhub/releases/latest) and put `cardhub` on your PATH, or with **Go 1.24+**:

```sh
go install github.com/DXMHXR/cardhub@latest     # or, from a clone: make install
```

If `cardhub` isn't found afterwards, add Go's bin folder to your PATH: `export PATH="$PATH:$(go env GOPATH)/bin"`. On macOS, allow a downloaded binary with `xattr -d com.apple.quarantine <path>/cardhub`.

**Updating:** when a release is out the header shows `↑ vX.Y.Z`. Press `U` or run `cardhub update`: it downloads the release, checks it against `checksums.txt` and replaces itself. A build from source only shows the notice: `git pull && make build`.

<details>
<summary>Install with an AI agent</summary>

Paste this into Claude Code or any coding agent that can run commands:

```text
Install CardHub (https://github.com/DXMHXR/cardhub), a terminal dashboard for GitHub Projects boards, on this machine:

1. If Go 1.24 or newer is installed (`go version`), run `go install github.com/DXMHXR/cardhub@latest`.
   Otherwise download the archive for this OS and CPU from https://github.com/DXMHXR/cardhub/releases/latest,
   check it against checksums.txt, and unpack the `cardhub` binary.
2. Put `cardhub` in a folder that's already on my PATH (prefer ~/.local/bin if it's on PATH; set GOBIN for go install).
   Only if none is suitable, add Go's bin folder to PATH in my shell config, and tell me what you changed.
   On macOS, if a downloaded binary is blocked, remove the quarantine flag with xattr.
3. Make sure GitHub access works: if the GitHub CLI is installed, run
   `gh auth status` and, if the read:project or repo scope is missing, ask me before running
   `gh auth refresh --scopes read:project,repo`. Never print or store my token.
4. Run `cardhub version` and `cardhub doctor`, fix anything doctor marks with ✘ (asking me before changing my
   GitHub login).
5. Run `cardhub skill install` so AI agents on this machine know how to use CardHub, and finish by telling me
   the installed version and how to start it: `cardhub`.
```

</details>

## Quick start

```sh
gh auth login --scopes read:project,repo   # 1. sign in to GitHub (once)
cardhub doctor                             # 2. check everything is ready
cardhub                                    # 3. pick a project and go
cardhub skill install                      # 4. optional: teach your AI agents to use it
```

CardHub takes its token from `GITHUB_TOKEN`, then `GH_TOKEN`, then `gh auth token`. It needs these scopes:

| Scope | Needed for |
|---|---|
| `read:project` | reading boards (required) |
| `repo` | card titles in private repositories (else "(no access)") |
| `read:org` | your organizations' projects in the `P` menu |

A fine-grained token needs Organization → *Projects: Read* and Repository → *Issues*, *Pull requests*, *Metadata: Read*. `cardhub doctor` checks all of this and prints the command that fixes anything missing.

## Usage

```sh
cardhub                                              # last board, or pick one from a list
cardhub "Roadmap"                                    # by project name (a unique part works)
cardhub https://github.com/orgs/my-org/projects/3   # by URL (…/views/6 opens that view)
cardhub -view Bug my-org 3                           # by owner and number, in a saved view
cardhub -once my-org 3                               # print column counts and exit
cardhub doctor | version | update                    # check setup · version · install the latest release
```

`-interval 30s` sets the fastest refresh rate.

| Key | Action |
|---|---|
| `←↓↑→` / `hjkl` | move between columns and cards, or table rows |
| `⏎` / `o` | open the selected card on GitHub (`O` opens the board) |
| `P` / `V` | switch project / saved view |
| `/` | search (`esc` clears) |
| `\` · `W` · `B` | split screen · move to the other board · swap their places |
| `R` · `Space` · `+`/`-` | refresh now · pause or resume · refresh faster/slower |
| `Tab` / `1` `2` `3` | Board, Stats and AI link screens |
| `X` / `C` | AI link screen: stop a watcher / copy the sample prompt |
| `U` · `Q` | install the new release · quit (`Ctrl+C` quits at once) |

![Saved view menu over the board](docs/views.png)

## AI link: let AI agents react to card changes

An agent asks the CardHub you have open to report specific changes, then waits, using no tokens until something happens.

```sh
cardhub notify add 'is:open label:bug' --board "Roadmap"   # report cards matching this filter
cardhub events --wait --json --board "Roadmap"             # block until there are reports
cardhub notify list                                        # rules, match counts, waiting agent
cardhub notify remove 2                                    # stop rule 2
cardhub guide                                              # the full manual agents read
```

- A rule takes any GitHub project filter (`label:needs-triage`, `status:"Ready for Test"`, `assignee:@me`, `iteration:@current`…). It fires when a card *starts* matching, once per card; cards that already match are reported straight away.
- `events --wait` prints the reports and exits (`--json`: one card per line with `number`, `title`, `status`, `labels`, `url`, `rule`). Reports wait in a queue while no agent listens.
- If CardHub isn't open, `notify add` opens it in a new cmux tab, tmux window or terminal window (`CARDHUB_TERMINAL='<cmd> {cmd}'` picks another; `--no-open` doesn't).
- Each project has one *watcher*; they keep running when you switch boards or pause, and stop when CardHub quits (waiting agents are told). `--board` can be left out while only one project is watched. Only the first CardHub open on a project has its AI link; a second shows **⚠ AI LINK ELSEWHERE**.
- Exit codes: `0` ok · `1` error · `2` usage · `3` watcher stopped · `4` CardHub isn't open.

![AI link screen with a rule and a waiting agent](docs/ailink.png)

**Teach your agents once:** `cardhub skill install` adds a small skill to Claude Code, Codex, opencode, Cursor and `~/.agents/skills`. It tells the agent to read `cardhub guide`, which always matches the installed version. Then just say:

> Use cardhub to watch the Roadmap project for cards labelled needs-triage, and triage each one as it comes in.

Without the skill, add ``Before using CardHub, run `cardhub guide`.`` to the agent's instructions.

## API cost

GitHub doesn't push board updates, so CardHub polls GraphQL; a token gets 5,000 points an hour, shared with other tools. One request covers every column with its visible cards and exact total (about 1 point; a search with `OR` costs 3–4). Scrolling loads more, up to 100 cards per column. CardHub uses at most ~15% of the hourly budget and half of what's left before the reset, slowing down as needed (`Every: 1m0s (auto)`). AI link rules cost about 1 point per watched project per check. CardHub only reads; it never changes cards.

## Troubleshooting

| Message | Fix |
|---|---|
| `no GitHub token found` | `gh auth login --scopes read:project,repo`, or set `GITHUB_TOKEN` |
| `token … expired or revoked` | `gh auth login` again, or create a new token |
| `missing the read:project scope` | `gh auth refresh --scopes read:project,repo` |
| `project … not found` | check the owner and number in the URL, and that you can open it on GitHub |
| cards show `(no access)` | `gh auth refresh --scopes repo` |
| an org's projects are missing under `P` | add `read:org`, or open the board by URL |
| `CardHub isn't open` (exit 4) | open the board, or let `cardhub notify add` open it |

## License

MIT. See [LICENSE](LICENSE).
