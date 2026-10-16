package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// matchProject finds a project by title: an exact (case-insensitive) title
// first, then a unique partial match, e.g. "app" for "My App Project".
func matchProject(refs []ProjectRef, name string) (ProjectRef, error) {
	q := strings.ToLower(strings.TrimSpace(name))
	var exact, partial []ProjectRef
	for _, r := range refs {
		t := strings.ToLower(r.Title)
		switch {
		case t == q:
			exact = append(exact, r)
		case strings.Contains(t, q):
			partial = append(partial, r)
		}
	}
	for _, set := range [][]ProjectRef{exact, partial} {
		switch len(set) {
		case 0:
			continue
		case 1:
			return set[0], nil
		}
		var names []string
		for _, r := range set {
			names = append(names, fmt.Sprintf("%q (%s/%d)", r.Title, r.Owner, r.Number))
		}
		return ProjectRef{}, fmt.Errorf("%q matches several projects: %s; use one of their owner/number",
			name, strings.Join(names, ", "))
	}
	return ProjectRef{}, fmt.Errorf("no project named %q", name)
}

// resolveProjectName looks a project title up among the projects you can open.
func resolveProjectName(name string) (string, int, error) {
	c, err := NewClient()
	if err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	refs, err := c.ListProjects(ctx)
	if err != nil {
		return "", 0, err
	}
	p, err := matchProject(refs, name)
	if err != nil {
		return "", 0, fmt.Errorf("%v (run `cardhub` to pick from a list)", err)
	}
	return p.Owner, p.Number, nil
}
