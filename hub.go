package main

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Hub is a cardhub's set of AI watchers, one per project. The watchers are
// for AI agents and the screen is for the human: the project on screen always
// has a watcher, but switching project doesn't stop the others, so an agent
// on project A keeps getting reports while you look at project B. Agents can
// add watchers for other projects (op "watch"), so one cardhub can serve
// AI 1 on project A and AI 2 on project B at once.
type Hub struct {
	mu      sync.Mutex
	ws      map[string]*Notifier // by "owner/number"
	screen  string               // the project on screen
	changed func(added bool)
}

// idleAfter is how long an off-screen watcher with no rules and no agent is kept.
const idleAfter = time.Minute

func NewHub(changed func(added bool)) *Hub {
	return &Hub{ws: map[string]*Notifier{}, changed: changed}
}

func boardKey(owner string, number int) string { return fmt.Sprintf("%s/%d", owner, number) }

// ensure returns the watcher for owner/number, starting it if needed. Callers hold h.mu.
func (h *Hub) ensure(owner string, number int) *Notifier {
	key := boardKey(owner, number)
	if n, ok := h.ws[key]; ok {
		return n
	}
	n := NewNotifier(h.changed)
	n.hub, n.owner, n.number = h, owner, number
	n.Serve(owner, number)
	h.ws[key] = n
	if n.changed != nil {
		go n.changed(false)
	}
	return n
}

// Show makes owner/number the project on screen. Its watcher starts if it
// isn't running; every other watcher keeps running.
func (h *Hub) Show(owner string, number int) {
	h.mu.Lock()
	h.screen = boardKey(owner, number)
	n := h.ensure(owner, number)
	h.mu.Unlock()
	if st := n.Status(); !st.Active && st.Conflict == "" {
		n.Serve(owner, number) // a watcher that was closed earlier
	}
	h.Prune(idleAfter)
}

// Watch starts a watcher for owner/number at an agent's request.
func (h *Hub) Watch(owner string, number int) error {
	if owner == "" || number <= 0 {
		return fmt.Errorf("watch: need a project owner and number")
	}
	h.mu.Lock()
	n := h.ensure(owner, number)
	h.mu.Unlock()
	n.mu.Lock()
	n.lastActive = time.Now()
	n.mu.Unlock()
	if st := n.Status(); st.Conflict != "" {
		return fmt.Errorf("%s", st.Conflict)
	}
	return nil
}

// Get returns the watcher for owner/number, or nil.
func (h *Hub) Get(owner string, number int) *Notifier {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ws[boardKey(owner, number)]
}

// Screen returns the on-screen project's watcher, or nil.
func (h *Hub) Screen() *Notifier {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ws[h.screen]
}

// All lists the watchers, the on-screen one first, then by project.
func (h *Hub) All() []*Notifier {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var keys []string
	for k := range h.ws {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if (keys[i] == h.screen) != (keys[j] == h.screen) {
			return keys[i] == h.screen
		}
		return keys[i] < keys[j]
	})
	out := make([]*Notifier, 0, len(keys))
	for _, k := range keys {
		out = append(out, h.ws[k])
	}
	return out
}

func (h *Hub) isScreen(n *Notifier) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return boardKey(n.owner, n.number) == h.screen
}

// Prune closes off-screen watchers nobody has used for d (no rules, no agent).
func (h *Hub) Prune(d time.Duration) {
	if h == nil {
		return
	}
	h.mu.Lock()
	var gone []*Notifier
	for k, n := range h.ws {
		if k != h.screen && n.idle(d) {
			gone = append(gone, n)
			delete(h.ws, k)
		}
	}
	h.mu.Unlock()
	for _, n := range gone {
		n.Stop("no agent used it for a while")
	}
	if len(gone) > 0 && h.changed != nil {
		go h.changed(false)
	}
}

// StopWatcher stops one watcher from the screen (X): its rules are dropped
// and a waiting agent is told. The project on screen gets a fresh watcher the
// next time it's opened, or when an agent asks for it.
func (h *Hub) StopWatcher(board string) {
	h.mu.Lock()
	n := h.ws[board]
	delete(h.ws, board)
	h.mu.Unlock()
	if n != nil {
		n.Stop("stopped from CardHub")
	}
	if h.changed != nil {
		go h.changed(false)
	}
}

// StopAll stops every watcher (cardhub is quitting): waiting agents are told.
func (h *Hub) StopAll(reason string) {
	for _, n := range h.All() {
		n.Stop(reason)
	}
}

// hubStatus totals the watchers for the footer and dialogs.
type hubStatus struct {
	Boards, Rules, Queued, Sent, Waiting int
	Conflict                             string // the on-screen project is watched by another cardhub
}

func (h *Hub) Status() hubStatus {
	var st hubStatus
	if h == nil {
		return st
	}
	scr := h.Screen()
	for _, n := range h.All() {
		s := n.Status()
		if n == scr && s.Conflict != "" {
			st.Conflict = s.Conflict
		}
		if !s.Active {
			continue
		}
		st.Boards++
		st.Rules += s.Rules
		st.Queued += s.Queued
		st.Sent += s.Sent
		if s.Waiting {
			st.Waiting++
		}
	}
	return st
}

// InUse reports whether any AI agent is using a watcher (rules or waiting).
func (h *Hub) InUse() bool {
	st := h.Status()
	return st.Rules > 0 || st.Waiting > 0
}

// Snapshots describes every active watcher for the AI link screen.
func (h *Hub) Snapshots() []LinkSnapshot {
	var out []LinkSnapshot
	for _, n := range h.All() {
		s := n.Snapshot()
		s.OnScreen = h.isScreen(n)
		if s.Active || s.OnScreen {
			out = append(out, s)
		}
	}
	return out
}
