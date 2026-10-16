package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// The AI link. A running cardhub hosts one watcher per project that AI agents
// use (see Hub): the project on screen, plus any an agent asked for. Watchers
// are independent of the screen: switching project doesn't stop them. Agents
// talk to a watcher through `cardhub notify …` and `cardhub events …`, which
// connect to the unix socket it serves:
//
//	notify add <filter>  → a rule; cards matching it are reported once each
//	events [--wait]      → returns queued reports (waiting for the next if asked)
//
// Quitting cardhub stops every watcher: a waiting `events --wait` is told so.

// Rule is one AI-defined trigger: report cards matching Filter (GitHub
// project filter syntax), each card once while it keeps matching.
type Rule struct {
	ID       int             `json:"id"`
	Filter   string          `json:"filter"`
	Matches  int             `json:"matches"` // cards matching at the last check
	reported map[string]bool // item IDs already reported
}

// NotifyEvent is one card reported to the AI.
type NotifyEvent struct {
	Rule   int       `json:"rule"`
	Filter string    `json:"filter"`
	Number int       `json:"number,omitempty"`
	Title  string    `json:"title"`
	Repo   string    `json:"repo,omitempty"`
	Status string    `json:"status"`
	State  string    `json:"state,omitempty"`
	Labels []string  `json:"labels"`
	URL    string    `json:"url,omitempty"`
	At     time.Time `json:"at"`
}

// LinkLog is one line of AI-link activity shown on the AI link screen.
type LinkLog struct {
	At   time.Time
	Kind string // rule | queued | sent | agent | stop
	Text string
	Card *NotifyEvent // queued entries: the card, so the screen can style it
}

const maxLinkLog = 200

// record appends to the activity log; callers hold n.mu.
func (n *Notifier) record(kind, format string, args ...any) {
	n.log = append(n.log, LinkLog{At: time.Now(), Kind: kind, Text: fmt.Sprintf(format, args...)})
	if len(n.log) > maxLinkLog {
		n.log = n.log[len(n.log)-maxLinkLog:]
	}
}

// request/response are the JSON lines exchanged over the socket.
type request struct {
	Op     string `json:"op"` // ping | add | list | remove | events | watch
	Filter string `json:"filter,omitempty"`
	Owner  string `json:"owner,omitempty"`  // watch: the project to start watching
	Number int    `json:"number,omitempty"` // watch
	ID     int    `json:"id,omitempty"`
	Wait   bool   `json:"wait,omitempty"`
}

type response struct {
	OK      bool          `json:"ok"`
	Error   string        `json:"error,omitempty"`
	Board   string        `json:"board,omitempty"`
	Title   string        `json:"title,omitempty"` // project title, e.g. "Roadmap"
	Rule    *Rule         `json:"rule,omitempty"`
	Rules   []*Rule       `json:"rules,omitempty"`
	Events  []NotifyEvent `json:"events,omitempty"`
	Queued  int           `json:"queued,omitempty"`
	Sent    int           `json:"sent,omitempty"`
	Waiting bool          `json:"waiting,omitempty"`
	Stopped bool          `json:"stopped,omitempty"` // the watcher quit or switched board
}

// Notifier is the watcher for one project. The TUI evaluates its rules on a
// timer and feeds results in with Apply; socket clients read and change it
// concurrently.
type Notifier struct {
	mu         sync.Mutex
	hub        *Hub // the cardhub hosting this watcher (nil in tests)
	owner      string
	number     int
	group      string    // field whose value is reported as a card's status
	lastActive time.Time // last request from an agent (idle watchers are closed)
	board      string    // "owner/number" being served, "" when idle
	title      string    // the project's title, once known
	since      time.Time
	path       string
	ln         net.Listener
	rules      []*Rule
	nextID     int
	queue      []NotifyEvent
	sent       int
	waiter     chan response // the one blocked `events --wait`, if any
	conflict   string        // why this instance couldn't serve its board
	log        []LinkLog     // recent activity, newest last (for the AI link screen)

	// changed is called (from any goroutine) when the TUI should re-render or
	// re-check rules; added is true when a rule was just added.
	changed func(added bool)
}

func NewNotifier(changed func(added bool)) *Notifier {
	return &Notifier{changed: changed}
}

func (n *Notifier) notify(added bool) {
	if n.changed != nil {
		n.changed(added)
	}
}

// socketDir holds one socket per open board.
func socketDir() string {
	if d := os.Getenv("CARDHUB_SOCKET_DIR"); d != "" {
		return d
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "cardhub")
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func socketPath(owner string, number int) string {
	return filepath.Join(socketDir(), fmt.Sprintf("%s-%d.sock", unsafeChars.ReplaceAllString(owner, "_"), number))
}

// Serve starts answering for owner/number (stopping any previous board: its
// rules are dropped and a waiting agent is told the watcher moved on).
func (n *Notifier) Serve(owner string, number int) {
	n.serveAt(socketPath(owner, number), fmt.Sprintf("%s/%d", owner, number))
}

func (n *Notifier) serveAt(path, board string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.conflict = ""
	if alive(path) {
		n.conflict = "another cardhub is already watching " + board + " for AI agents"
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.Remove(path) // stale socket from a cardhub that didn't exit cleanly
	ln, err := net.Listen("unix", path)
	if err != nil {
		n.conflict = "AI link unavailable: " + err.Error()
		return
	}
	n.ln, n.path, n.board, n.since, n.lastActive = ln, path, board, time.Now(), time.Now()
	n.record("agent", "listening for AI agents on %s", board)
	go n.accept(ln)
}

// SetMeta records the project's title (so agents can say --board "Roadmap")
// and the field reported as a card's status, once the project has loaded.
func (n *Notifier) SetMeta(title, group string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.title, n.group = title, group
}

// Meta returns what SetMeta stored ("" until the project has loaded).
func (n *Notifier) Meta() (title, group string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.title, n.group
}

// boardLabel is "Title (owner/number)", or just owner/number before the title is known.
func boardLabel(title, board string) string {
	if title == "" {
		return board
	}
	return title + " (" + board + ")"
}

// Stop ends the link: a waiting agent hears "stopped", rules are cleared.
func (n *Notifier) Stop(reason string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.waiter != nil {
		n.waiter <- response{Stopped: true, Error: reason}
		n.waiter = nil
	}
	if n.ln != nil {
		n.record("stop", "watcher stopped: %s", reason)
		n.ln.Close()
		_ = os.Remove(n.path)
	}
	n.ln, n.path, n.board, n.title = nil, "", "", ""
	n.rules, n.queue, n.sent = nil, nil, 0
}

func (n *Notifier) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go n.handle(conn)
	}
}

func (n *Notifier) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResp(conn, response{Error: "bad request"})
		return
	}
	if req.Op == "events" && req.Wait {
		n.wait(conn, r)
		return
	}
	writeResp(conn, n.do(req))
}

func writeResp(conn net.Conn, resp response) error {
	b, _ := json.Marshal(resp)
	_, err := conn.Write(append(b, '\n'))
	return err
}

// do answers every request except a blocking wait.
func (n *Notifier) do(req request) response {
	if req.Op == "watch" { // ask this cardhub to host a watcher for another project
		if n.hub == nil {
			return response{Error: "this cardhub can't host other watchers"}
		}
		if err := n.hub.Watch(req.Owner, req.Number); err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true, Board: fmt.Sprintf("%s/%d", req.Owner, req.Number)}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lastActive = time.Now()
	switch req.Op {
	case "ping":
		return response{OK: true, Board: n.board, Title: n.title}
	case "add":
		if req.Filter == "" {
			return response{Error: "empty filter"}
		}
		for _, r := range n.rules {
			if r.Filter == req.Filter {
				return response{OK: true, Board: n.board, Title: n.title, Rule: r} // already watching this
			}
		}
		n.nextID++
		rule := &Rule{ID: n.nextID, Filter: req.Filter, reported: map[string]bool{}}
		n.rules = append(n.rules, rule)
		n.record("rule", "rule %d added: %s", rule.ID, rule.Filter)
		go n.notify(true) // check it now: cards matching already are reported once
		return response{OK: true, Board: n.board, Title: n.title, Rule: rule}
	case "list":
		return response{OK: true, Board: n.board, Title: n.title, Rules: n.rules, Queued: len(n.queue), Sent: n.sent,
			Waiting: n.waiter != nil}
	case "remove":
		for i, r := range n.rules {
			if r.ID == req.ID {
				n.rules = append(n.rules[:i:i], n.rules[i+1:]...)
				n.record("rule", "rule %d removed: %s", r.ID, r.Filter)
				go n.notify(false)
				return response{OK: true, Board: n.board}
			}
		}
		return response{Error: fmt.Sprintf("no rule %d", req.ID)}
	case "events":
		evs := n.queue
		n.queue = nil
		n.sent += len(evs)
		if len(evs) > 0 {
			n.record("sent", "sent %s to an agent (events)", plural(len(evs), "report"))
		}
		go n.notify(false)
		return response{OK: true, Board: n.board, Events: evs}
	}
	return response{Error: "unknown op " + req.Op}
}

// wait blocks until there are events (or the watcher stops, or the agent
// goes away). Only one agent may wait at a time.
func (n *Notifier) wait(conn net.Conn, r *bufio.Reader) {
	n.mu.Lock()
	if len(n.queue) > 0 {
		evs := n.queue
		n.queue = nil
		n.mu.Unlock()
		n.deliver(conn, evs)
		return
	}
	if n.waiter != nil {
		n.mu.Unlock()
		writeResp(conn, response{Error: "another agent is already waiting on this cardhub"})
		return
	}
	ch := make(chan response, 1)
	n.waiter = ch
	n.lastActive = time.Now()
	n.record("agent", "an agent is waiting (events --wait)")
	n.mu.Unlock()
	go n.notify(false)

	gone := make(chan struct{})
	go func() { // the client never sends more; a read returning means it hung up
		r.ReadByte()
		close(gone)
	}()
	select {
	case resp := <-ch:
		if resp.Stopped {
			writeResp(conn, resp)
			return
		}
		n.deliver(conn, resp.Events)
	case <-gone:
		n.mu.Lock()
		if n.waiter == ch {
			n.waiter = nil
			n.lastActive = time.Now()
			n.record("agent", "the waiting agent left")
		}
		select { // events handed over just as the agent left go back in the queue
		case resp := <-ch:
			if !resp.Stopped {
				n.queue = append(resp.Events, n.queue...)
			}
		default:
		}
		n.mu.Unlock()
		go n.notify(false)
	}
}

// deliver sends events to an agent, putting them back if it can't be reached.
func (n *Notifier) deliver(conn net.Conn, evs []NotifyEvent) {
	if err := writeResp(conn, response{OK: true, Events: evs}); err != nil {
		n.mu.Lock()
		n.queue = append(evs, n.queue...)
		n.mu.Unlock()
		return
	}
	n.mu.Lock()
	n.sent += len(evs)
	n.record("sent", "sent %s to the waiting agent", plural(len(evs), "report"))
	n.mu.Unlock()
	go n.notify(false)
}

// Filters lists the rules to check on the next sync.
func (n *Notifier) Filters() map[int]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[int]string{}
	for _, r := range n.rules {
		out[r.ID] = r.Filter
	}
	return out
}

// Apply takes the cards currently matching each rule (by rule ID), queues the
// ones not reported yet, oldest issue first, and hands them to a waiting agent.
// A card that stops matching is forgotten, so it's reported again if it comes back.
func (n *Notifier) Apply(matches map[int][]Item, groupField string, at time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	var fresh []NotifyEvent
	for _, r := range n.rules {
		items, ok := matches[r.ID]
		if !ok {
			continue // rule added after this check started
		}
		r.Matches = len(items)
		now := map[string]bool{}
		for _, it := range items {
			now[it.ID] = true
			if r.reported[it.ID] {
				continue
			}
			fresh = append(fresh, NotifyEvent{
				Rule: r.ID, Filter: r.Filter, Number: it.Number, Title: it.Title, Repo: it.Repo,
				Status: colName(it, groupField), State: it.State, Labels: it.Labels, URL: it.URL, At: at,
			})
		}
		r.reported = now
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Number < fresh[j].Number })
	for i := range fresh {
		e := fresh[i]
		n.record("queued", "#%d [%s] %s · rule %d", e.Number, e.Status, e.Title, e.Rule)
		n.log[len(n.log)-1].Card = &e
	}
	n.queue = append(n.queue, fresh...)
	if n.waiter != nil && len(n.queue) > 0 {
		n.waiter <- response{OK: true, Events: n.queue}
		n.waiter, n.queue = nil, nil
	}
}

// idle reports whether nothing uses this watcher: no rules, no waiting agent,
// nothing queued, and no agent request for at least d.
func (n *Notifier) idle(d time.Duration) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.rules) == 0 && n.waiter == nil && len(n.queue) == 0 && time.Since(n.lastActive) >= d
}

// Status is what the footer shows about the AI link.
type linkStatus struct {
	Active   bool // serving a board
	Conflict string
	Rules    int
	Queued   int
	Sent     int
	Waiting  bool
}

func (n *Notifier) Status() linkStatus {
	if n == nil {
		return linkStatus{}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return linkStatus{Active: n.ln != nil, Conflict: n.conflict, Rules: len(n.rules),
		Queued: len(n.queue), Sent: n.sent, Waiting: n.waiter != nil}
}

// LinkSnapshot is everything the AI link screen shows about one watcher.
type LinkSnapshot struct {
	linkStatus
	OnScreen           bool
	Board, Title, Path string
	Since              time.Time
	Rules              []RuleView
	Log                []LinkLog // newest first
}

type RuleView struct {
	ID       int
	Filter   string
	Matches  int
	Reported int
}

func (n *Notifier) Snapshot() LinkSnapshot {
	st := n.Status()
	if n == nil {
		return LinkSnapshot{}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	snap := LinkSnapshot{linkStatus: st, Board: n.board, Title: n.title, Path: n.path, Since: n.since}
	for _, r := range n.rules {
		snap.Rules = append(snap.Rules, RuleView{r.ID, r.Filter, r.Matches, len(r.reported)})
	}
	for i := len(n.log) - 1; i >= 0; i-- {
		snap.Log = append(snap.Log, n.log[i])
	}
	return snap
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// alive reports whether a cardhub is answering on path.
func alive(path string) bool {
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

var errNotOpen = errors.New("cardhub isn't open")
