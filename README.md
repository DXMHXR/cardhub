# cardhub

A live terminal dashboard for GitHub Projects (v2) boards. It shows your board as kanban columns, keeps it in sync with GitHub, and animates cards as they move through your workflow.

## Quick start

```sh
# 1. Install (needs Go 1.24+, see Requirements)
go install github.com/DXMHXR/cardhub@latest

# 2. Sign in to GitHub with the access cardhub needs (once)
gh auth login --scopes read:project,repo

# 3. Check everything is ready
cardhub doctor

# 4. Run it. The first time, you pick a project from a list.
cardhub
```

After the first run, `cardhub` reopens the board and view you last watched.

## Requirements

| What | Why | Check |
|---|---|---|
| **Go 1.24 or newer** | to build cardhub | `go version` (install: https://go.dev/dl or `brew install go`) |
| **A GitHub login with access to projects** | cardhub reads boards from GitHub's API | `cardhub doctor` |
| A terminal at least 30 columns wide | the board layout adapts to any width above that | – |
| GitHub CLI (`gh`) *(optional)* | the easiest way to sign in; cardhub reuses its login | `gh --version` (install: https://cli.github.com) |

cardhub doesn't need `gh` to run. It only uses `gh` to read your login token, and only when no token is set in the environment.

## Install

```sh
go install github.com/DXMHXR/cardhub@latest
```

Or from a clone of this repo:

```sh
git clone https://github.com/DXMHXR/cardhub && cd cardhub
make install          # same as: go install .
```

Either way, this builds the `cardhub` command into Go's bin folder, usually `~/go/bin`. If `cardhub` isn't found afterwards, that folder isn't on your PATH yet. Add this line to `~/.zshrc` or `~/.bashrc` (`make install` prints it for you):

```sh
export PATH="$PATH:$(go env GOPATH)/bin"
```

Other `make` targets: `make build` builds `./cardhub` here without installing, `make test` runs the tests, and `make uninstall` removes the installed command.

## Sign in to GitHub

cardhub looks for a token in this order and uses the first one it finds:

1. the `GITHUB_TOKEN` environment variable
2. the `GH_TOKEN` environment variable
3. your GitHub CLI login (`gh auth token`)

**The token needs these permissions:**

| Scope | Needed for | Without it |
|---|---|---|
| `read:project` (or `project`) | reading boards, columns and views | cardhub can't run |
| `repo` | titles of cards that live in **private** repositories | those cards show as "(no access)" |
| `read:org` | listing your organizations' projects in the project menu | org projects may be missing from `P`, though opening them by URL still works |

**Option A: GitHub CLI (easiest)**

```sh
gh auth login --scopes read:project,repo        # new login
gh auth refresh --scopes read:project,repo      # already logged in: add the scopes
```

**Option B: a personal access token**

- **Classic token:** create one at https://github.com/settings/tokens with the `read:project`, `repo` and `read:org` scopes.
- **Fine-grained token:** grant Organization → *Projects: Read*, and Repository → *Issues*, *Pull requests* and *Metadata: Read*. Your organization may need to approve fine-grained tokens first.

Then:

```sh
export GITHUB_TOKEN=<your token>     # add to ~/.zshrc to keep it
```

## Check your setup: `cardhub doctor`

```
cardhub doctor — checking what cardhub needs to run

 ✔ GitHub CLI installed
 ✔ GitHub token found          from gh auth token
 ✔ Signed in as octocat         API budget 4746/5000 left, resets in 21m
 ✔ Can read projects
 ✔ Can read private repositories
 ✔ 6 open projects you can watch

 ✔ Ready. Run `cardhub` to pick a project, or `cardhub <project-url>`.
```

Anything missing is marked ✘ or !, with the exact command that fixes it.

## Usage

```sh
cardhub                                              # last board, or pick from a list
cardhub https://github.com/orgs/my-org/projects/3   # open a board by its URL
cardhub my-org 3                                     # same, by owner and number
cardhub https://github.com/orgs/my-org/projects/3/views/6   # open a saved view
cardhub -view Bug my-org 3                           # saved view by name or number
cardhub -once my-org 3                               # print column counts and exit
cardhub doctor                                       # check your setup
cardhub notify add 'is:open label:bug'      # AI link (see below)
```

The project URL is the address of the board in your browser.

### Keys

| Key | Action |
|---|---|
| `←↓↑→` / `hjkl` | move between columns and cards |
| `⏎` / `o` | open the selected card on GitHub (`O` opens the board) |
| `P` | switch project (dialog: type to search, `⏎` to open) |
| `V` | switch saved view |
| `/` | search, using free text or GitHub filters like `label:bug assignee:@me is:open` (`esc` clears) |
| `R` | sync now |
| `+` / `-` | refresh faster or slower |
| `Tab` / `1` `2` `3` | switch between the Board, Stats and AI link screens |
| `Q` | quit |

### Screens

- **Board:** columns follow the view's grouping (Status by default) in a responsive grid. Columns sit side by side while they fit and wrap onto new rows when they don't. Each column shows 3 cards and scrolls with ↑/↓. Animated dotted connectors show the workflow direction. When a card changes column, the connectors it crossed light up for 90 seconds: yellow for forward moves, orange `◀` for moves back, with a `×N` count. A yellow ● marks recently changed cards.
- **Stats:** a bar per column with a trend sparkline, plus breakdowns by state and assignee.
- **AI link:** the link's state (board, socket, whether an agent is waiting, reports sent and queued), the rules agents have set with how many cards match each, and an activity log of rules added, cards queued, reports sent and agents connecting or leaving. Until an agent connects, it also shows the commands to run and a sample prompt for this board; press `C` to copy the prompt to the clipboard.

## AI link: let an AI agent react to card changes

The cardhub you have open can tell an AI agent (Claude Code, or any agent that can run commands) when cards change. The agent doesn't run its own polling loop. It asks cardhub to report specific changes and waits, using no model tokens until something happens. Quitting cardhub stops the watcher, and a waiting agent is told so.

```sh
cardhub notify add 'is:open label:bug'   # report cards matching this filter
cardhub events --wait --json                      # block until there are reports
```

- **`notify add '<filter>'`** takes any GitHub project filter. Each matching card is reported once, cards that already match are reported straight away, and a card that stops matching and later matches again is reported again. If cardhub isn't open, this opens it: a new **cmux** tab in the agent's workspace, a new **tmux** window, or a new iTerm2 / Terminal / Ghostty / WezTerm window (Linux: your terminal emulator). Set `CARDHUB_TERMINAL='<cmd> {cmd}'` to choose another, or pass `--no-open`.
- **`events --wait`** blocks until there are reports, prints them (`--json`: one card per line with `number`, `title`, `status`, `labels`, `url` and the matching `rule`), and exits. Reports that arrive while no agent waits are queued for the next call. `events` without `--wait` returns the queue immediately.
- **`notify list`** shows rules, match counts and whether an agent is waiting. **`notify remove <id>`** stops a rule.
- **Several boards open?** Add `--board <owner/number>`. Otherwise commands use the only open board, and `notify add` falls back to your last board.

Exit codes: `0` ok · `1` error · `2` usage · `3` watcher stopped (cardhub quit) · `4` cardhub isn't open.

**How the right agent gets the message:** cardhub never picks a terminal. The agent session that runs `events --wait` in the background owns that process, and the agent's harness (Claude Code, for one) wakes that session when the command exits. Only one agent can wait per open cardhub. The footer's AI link box shows its state, for example `1 rule · 3 sent · ● agent waiting`, and screen `3` shows the details.

Rules are checked on every sync against the whole project, whatever view or search is on screen. They cost about 1 point per sync for all rules together, and cover the first 50 matches per rule. cardhub stays read-only: agents change cards with their own tools (for example `gh project item-edit`).

**Example agent instructions:**

> Run `cardhub notify add 'is:open label:bug' --board my-org/3`, then run `cardhub events --wait --json` in the background. When it returns, handle each reported card, oldest first, then run `events --wait` again. If it exits with code 3, cardhub was closed: stop and say so.

## Troubleshooting

| Message | Fix |
|---|---|
| `no GitHub token found` | `gh auth login --scopes read:project,repo`, or set `GITHUB_TOKEN` |
| `GitHub rejected your token … (expired or revoked)` | `gh auth login` again, or create a new token |
| `missing the read:project scope` | `gh auth refresh --scopes read:project,repo` |
| `project … not found` | check the owner and number in the board's URL, and that you can open it on GitHub |
| cards show `(no access)` | add the `repo` scope: `gh auth refresh --scopes repo` |
| an org's projects don't appear under `P` | add the `read:org` scope, or open the board by URL |
| `cardhub: command not found` | add Go's bin folder to PATH (see Install) |
| `cardhub isn't open` (exit 4) from `events` | open the board (`cardhub <owner> <number>`), or let `cardhub notify add` open it |

`cardhub doctor` checks all of these at once.

## How it stays cheap on the GitHub API

GitHub doesn't push live updates for project boards, so cardhub polls its GraphQL API. Every token gets 5,000 points per hour, shared with `gh` and other tools. cardhub keeps its share small:

- **The board loads only what's on screen.** One request covers every column and fetches the visible cards plus a few extra for scrolling, along with each column's exact total. That costs about 1 point per sync, whatever the board's size. Scrolling further loads more (`loading…` shows briefly), up to the first 100 cards of a column.
- **Only the Stats screen loads every card** (about 3 points per 100 cards), and only while it's open.
- **The refresh rate adjusts itself.** cardhub uses at most ~15% of the hourly budget, and at most half of what's left before it resets. `-interval` (default 30s) and `+`/`-` set the fastest rate, and 5 minutes is the slowest. When it has slowed down, the footer shows `Every: 1m0s (auto)`, and the API bar shows how much budget is used and when it resets.

Moves are detected even while only some cards are loaded. They're exact for cards on screen, and inferred from changes in column totals for the rest. The `/` search also runs on GitHub, so column totals stay correct. Archived cards are hidden.

## License

MIT. See [LICENSE](LICENSE).
