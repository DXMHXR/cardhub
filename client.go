package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Exit codes for `cardhub notify` and `cardhub events`, so agents can branch on them.
const (
	exitOK      = 0
	exitError   = 1
	exitUsage   = 2
	exitStopped = 3 // the watcher quit (or switched board) while an agent waited
	exitNotOpen = 4 // no cardhub is open for the board
)

const agentUsage = `AI link: let an AI agent hear about card changes from the cardhub you have open.

  cardhub notify add '<filter>'   report cards matching a GitHub project filter,
                                  e.g. 'is:open label:bug' (each card once).
                                  Opens cardhub in a new terminal/tab if it isn't open.
  cardhub notify list             show rules, queued reports, and whether an agent waits
  cardhub notify remove <id>      stop a rule
  cardhub events --wait           block until there are reports, print them, exit
  cardhub events                  print queued reports now (possibly none)

Flags (any of the above):
  --board <owner/number|url>      which open board (needed only if several are open)
  --json                          JSON output, one report per line
  --no-open                       notify add: fail instead of opening cardhub

Exit codes: 0 ok · 1 error · 2 usage · 3 watcher stopped (cardhub quit) · 4 cardhub isn't open
`

type agentFlags struct {
	board  string
	json   bool
	wait   bool
	noOpen bool
}

// parseAgentFlags accepts flags before or after positional arguments.
func parseAgentFlags(name string, args []string) (agentFlags, []string, error) {
	var f agentFlags
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	fs.StringVar(&f.board, "board", "", "")
	fs.BoolVar(&f.json, "json", false, "")
	fs.BoolVar(&f.wait, "wait", false, "")
	fs.BoolVar(&f.noOpen, "no-open", false, "")
	var rest []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return f, nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return f, rest, nil
}

func runNotify(args []string) int {
	f, rest, err := parseAgentFlags("notify", args)
	if err != nil || len(rest) == 0 {
		fmt.Fprint(os.Stderr, agentUsage)
		return exitUsage
	}
	switch rest[0] {
	case "add":
		if len(rest) < 2 {
			fmt.Fprintln(os.Stderr, "usage: cardhub notify add '<filter>'   e.g. 'is:open label:bug'")
			return exitUsage
		}
		path, code := findOrOpen(f)
		if code != exitOK {
			return code
		}
		resp, err := call(path, request{Op: "add", Filter: strings.Join(rest[1:], " ")})
		if err != nil {
			return fail(err)
		}
		if f.json {
			return printJSON(resp)
		}
		fmt.Printf("rule %d on %s: report cards matching %q (cards matching now are reported once)\n",
			resp.Rule.ID, resp.Board, resp.Rule.Filter)
	case "list":
		path, code := findBoard(f.board)
		if code != exitOK {
			return code
		}
		resp, err := call(path, request{Op: "list"})
		if err != nil {
			return fail(err)
		}
		if f.json {
			return printJSON(resp)
		}
		waiting := "no agent waiting"
		if resp.Waiting {
			waiting = "an agent is waiting"
		}
		fmt.Printf("%s · %d queued · %d sent · %s\n", resp.Board, resp.Queued, resp.Sent, waiting)
		if len(resp.Rules) == 0 {
			fmt.Println("no rules yet: cardhub notify add '<filter>'")
		}
		for _, r := range resp.Rules {
			fmt.Printf("  %d  %-40s %d matching\n", r.ID, r.Filter, r.Matches)
		}
	case "remove":
		id, err := strconv.Atoi(strings.Join(rest[1:], ""))
		if err != nil {
			fmt.Fprintln(os.Stderr, "usage: cardhub notify remove <rule id>   (see cardhub notify list)")
			return exitUsage
		}
		path, code := findBoard(f.board)
		if code != exitOK {
			return code
		}
		if _, err := call(path, request{Op: "remove", ID: id}); err != nil {
			return fail(err)
		}
		fmt.Printf("rule %d removed\n", id)
	default:
		fmt.Fprint(os.Stderr, agentUsage)
		return exitUsage
	}
	return exitOK
}

func runEvents(args []string) int {
	f, rest, err := parseAgentFlags("events", args)
	if err != nil || len(rest) > 0 {
		fmt.Fprint(os.Stderr, agentUsage)
		return exitUsage
	}
	path, code := findBoard(f.board)
	if code != exitOK {
		return code
	}
	resp, err := call(path, request{Op: "events", Wait: f.wait})
	if errors.Is(err, errWatcherGone) {
		fmt.Fprintln(os.Stderr, "watcher stopped: cardhub exited")
		return exitStopped
	}
	if err != nil {
		return fail(err)
	}
	if resp.Stopped {
		fmt.Fprintln(os.Stderr, "watcher stopped: "+resp.Error)
		return exitStopped
	}
	for _, e := range resp.Events {
		if f.json {
			b, _ := json.Marshal(e)
			fmt.Println(string(b))
			continue
		}
		ref := e.Title
		if e.Number > 0 {
			ref = fmt.Sprintf("#%d %s", e.Number, e.Title)
		}
		fmt.Printf("%s  [%s]  %s  (rule %d: %s)\n", ref, e.Status, e.URL, e.Rule, e.Filter)
	}
	return exitOK
}

var errWatcherGone = errors.New("cardhub closed the connection")

// call sends one request and reads one response line.
func call(path string, req request) (response, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return response{}, errNotOpen
	}
	defer conn.Close()
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return response{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return response{}, errWatcherGone // cardhub exited while we waited
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return response{}, err
	}
	if !resp.OK && !resp.Stopped {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// openBoards lists the boards a running cardhub is serving, cleaning up stale sockets.
func openBoards() map[string]string {
	out := map[string]string{}
	paths, _ := filepath.Glob(filepath.Join(socketDir(), "*.sock"))
	for _, p := range paths {
		resp, err := call(p, request{Op: "ping"})
		if err != nil {
			_ = os.Remove(p)
			continue
		}
		out[resp.Board] = p
	}
	return out
}

// findBoard picks the socket for --board, or the only open board.
func findBoard(board string) (string, int) {
	if board != "" {
		owner, number, _, err := ParseTarget([]string{board})
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: --board:", err)
			return "", exitUsage
		}
		p := socketPath(owner, number)
		if !alive(p) {
			fmt.Fprintf(os.Stderr, "cardhub isn't open on %s/%d. Open it with: cardhub %s %d\n", owner, number, owner, number)
			return "", exitNotOpen
		}
		return p, exitOK
	}
	open := openBoards()
	switch len(open) {
	case 0:
		fmt.Fprintln(os.Stderr, "cardhub isn't open. Open it with: cardhub <owner> <number>")
		return "", exitNotOpen
	case 1:
		for _, p := range open {
			return p, exitOK
		}
	}
	var names []string
	for b := range open {
		names = append(names, b)
	}
	sort.Strings(names)
	fmt.Fprintf(os.Stderr, "several boards are open (%s); pick one with --board <owner/number>\n", strings.Join(names, ", "))
	return "", exitUsage
}

// findOrOpen is findBoard, but opens cardhub in a new terminal when the board
// isn't open (unless --no-open), then waits for it to start answering.
func findOrOpen(f agentFlags) (string, int) {
	board := f.board
	if board == "" {
		if open := openBoards(); len(open) > 0 {
			return findBoard("")
		}
		last, ok := loadLast()
		if !ok {
			fmt.Fprintln(os.Stderr, "cardhub isn't open and there's no last board: pass --board <owner/number>")
			return "", exitNotOpen
		}
		board = fmt.Sprintf("%s/%d", last.Owner, last.Number)
	}
	owner, number, _, err := ParseTarget([]string{board})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: --board:", err)
		return "", exitUsage
	}
	p := socketPath(owner, number)
	if alive(p) {
		return p, exitOK
	}
	if f.noOpen {
		fmt.Fprintf(os.Stderr, "cardhub isn't open on %s/%d (and --no-open was given)\n", owner, number)
		return "", exitNotOpen
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "cardhub"
	}
	where, err := openInTerminal(fmt.Sprintf("%s %s %d", shellQuote(exe), shellQuote(owner), number))
	if err != nil {
		fmt.Fprintln(os.Stderr, "couldn't open cardhub:", err)
		return "", exitNotOpen
	}
	fmt.Fprintf(os.Stderr, "opened cardhub on %s/%d in %s\n", owner, number, where)
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if alive(p) {
			return p, exitOK
		}
	}
	fmt.Fprintln(os.Stderr, "cardhub didn't start within 30s; check the new terminal for an error (try: cardhub doctor)")
	return "", exitNotOpen
}

func printJSON(v any) int {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
	return exitOK
}

func fail(err error) int {
	if errors.Is(err, errNotOpen) {
		fmt.Fprintln(os.Stderr, "cardhub isn't open")
		return exitNotOpen
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	return exitError
}
