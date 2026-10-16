package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func card(id string, n int, status string) Item {
	return Item{ID: id, Number: n, Title: "card " + id, Values: map[string]string{"Status": status}}
}

// startTestNotifier serves on a short socket path (unix sockets cap path length).
func startTestNotifier(t *testing.T) (*Notifier, string) {
	dir, err := os.MkdirTemp("/tmp", "ch")
	if err != nil {
		t.Skip("no /tmp:", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "b.sock")
	n := NewNotifier(nil)
	n.serveAt(path, "o/1")
	t.Cleanup(func() { n.Stop("test over") })
	return n, path
}

func TestNotifierReportsEachCardOnce(t *testing.T) {
	n, path := startTestNotifier(t)
	resp, err := call(path, request{Op: "add", Filter: "label:bug"})
	if err != nil || resp.Rule.ID != 1 {
		t.Fatalf("add: %v %+v", err, resp)
	}
	id := resp.Rule.ID

	// cards already matching are reported once, oldest issue first
	n.Apply(map[int][]Item{id: {card("b", 9, "Backlog"), card("a", 7, "Backlog")}}, "Status", time.Now())
	resp, _ = call(path, request{Op: "events"})
	if len(resp.Events) != 2 || resp.Events[0].Number != 7 || resp.Events[1].Number != 9 {
		t.Fatalf("first report = %+v", resp.Events)
	}

	// same matches again: nothing new
	n.Apply(map[int][]Item{id: {card("a", 7, "Backlog"), card("b", 9, "Backlog")}}, "Status", time.Now())
	if resp, _ = call(path, request{Op: "events"}); len(resp.Events) != 0 {
		t.Fatalf("repeat should report nothing, got %+v", resp.Events)
	}

	// a stops matching (triaged), then comes back (re-labelled): reported again
	n.Apply(map[int][]Item{id: {card("b", 9, "Backlog")}}, "Status", time.Now())
	n.Apply(map[int][]Item{id: {card("a", 7, "Backlog"), card("b", 9, "Backlog")}}, "Status", time.Now())
	if resp, _ = call(path, request{Op: "events"}); len(resp.Events) != 1 || resp.Events[0].Number != 7 {
		t.Fatalf("returning card = %+v", resp.Events)
	}
	if st := n.Status(); st.Sent != 3 || st.Rules != 1 {
		t.Errorf("status = %+v", st)
	}
}

func TestNotifierWaitAndStop(t *testing.T) {
	n, path := startTestNotifier(t)
	resp, _ := call(path, request{Op: "add", Filter: "is:open"})
	id := resp.Rule.ID

	got := make(chan response, 1)
	go func() { r, _ := call(path, request{Op: "events", Wait: true}); got <- r }()
	waitFor(t, func() bool { return n.Status().Waiting })

	if _, err := call(path, request{Op: "events", Wait: true}); err == nil {
		t.Error("a second waiting agent should be refused")
	}
	n.Apply(map[int][]Item{id: {card("x", 3, "Todo")}}, "Status", time.Now())
	select {
	case r := <-got:
		if len(r.Events) != 1 || r.Events[0].Number != 3 || r.Events[0].Status != "Todo" {
			t.Fatalf("waiter got %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting agent was not woken")
	}

	// quitting cardhub tells a waiting agent the watcher stopped
	go func() { r, _ := call(path, request{Op: "events", Wait: true}); got <- r }()
	waitFor(t, func() bool { return n.Status().Waiting })
	n.Stop("cardhub quit")
	select {
	case r := <-got:
		if !r.Stopped {
			t.Fatalf("want stopped, got %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting agent not told about stop")
	}
	if alive(path) {
		t.Error("socket should be gone after Stop")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not reached")
}
