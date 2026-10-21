package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const usage = `CardHub %s — live terminal view of a GitHub Projects (v2) board

Usage:
  cardhub                        reopen the last board, or pick one from a list
  cardhub "<project name>"       e.g. cardhub "Roadmap" (a unique part of the name works too)
  cardhub <project-url>          e.g. https://github.com/orgs/my-org/projects/3
  cardhub <owner> <number>       e.g. cardhub my-org 3
  cardhub doctor                 check your setup (token, scopes, projects)
  cardhub version                print the version
  cardhub update                 install the latest release in place
  cardhub notify | events        AI link: let an AI agent hear about card changes
                                 (cardhub notify -h for details)
  cardhub guide                  how AI agents use the AI link (the manual they read)
  cardhub skill install          teach your AI agent CLIs to use CardHub

Examples:
  cardhub -view Bug my-org 3     open a saved view by name or number
  cardhub https://github.com/orgs/my-org/projects/3/views/6
  cardhub -once my-org 3         print column counts and exit (no UI)

Auth: GITHUB_TOKEN or GH_TOKEN, or your GitHub CLI login (` + "`gh auth token`" + `).
The token needs the read:project and repo scopes. Run ` + "`cardhub doctor`" + ` to check.

Flags:
`

func main() {
	interval := flag.Duration("interval", 30*time.Second, "fastest re-sync interval (min 5s; slows down automatically to save API budget)")
	group := flag.String("group", "", "single-select field to group columns by (default: the view's grouping, else Status)")
	viewArg := flag.String("view", "", "saved project view to show, by name or number (default: all items)")
	once := flag.Bool("once", false, "print a one-shot summary and exit (no UI)")
	flag.Usage = func() { fmt.Fprintf(os.Stderr, usage, versionLabel()); flag.PrintDefaults() }
	// AI link commands have their own flags
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-version", "-v":
			fmt.Println("CardHub " + versionLabel())
			if c := loadUpdateCache(); newer(versionLabel(), c.Latest) { // no network: what the last check found
				fmt.Printf("CardHub %s is out: run cardhub update\n", c.Latest)
			}
			return
		case "update":
			os.Exit(runUpdate())
		case "notify":
			os.Exit(runNotify(os.Args[2:]))
		case "events":
			os.Exit(runEvents(os.Args[2:]))
		case "guide":
			os.Exit(runGuide())
		case "skill":
			os.Exit(runSkill(os.Args[2:]))
		}
	}
	flag.Parse()

	args := flag.Args()
	if len(args) == 1 {
		switch args[0] {
		case "doctor":
			os.Exit(runDoctor())
		case "help":
			flag.Usage()
			return
		}
	}

	// No target: reopen the last board, or start on the project picker.
	var owner string
	var number, urlView int
	if len(args) == 0 {
		if last, ok := loadLast(); ok {
			owner, number = last.Owner, last.Number
			if *viewArg == "" {
				*viewArg = last.View
			}
		}
	} else {
		var err error
		if owner, number, urlView, err = ParseTarget(args); err != nil {
			// not a URL or owner/number: treat the words as a project title
			if owner, number, err = resolveProjectName(strings.Join(args, " ")); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(2)
			}
		}
	}
	if *viewArg == "" && urlView > 0 {
		*viewArg = fmt.Sprint(urlView)
	}
	if *interval < 5*time.Second {
		*interval = 5 * time.Second
	}
	client, err := NewClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if *once {
		if owner == "" {
			fmt.Fprintln(os.Stderr, "error: -once needs a project: cardhub -once <owner> <number>")
			os.Exit(2)
		}
		if err := printSummary(client, owner, number, *group, *viewArg); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	m := newModel(client, owner, number, *interval, *group, *viewArg)
	var p *tea.Program
	m.hub = NewHub(func(added bool) { p.Send(linkMsg{added}) })
	p = tea.NewProgram(m, tea.WithAltScreen())
	if owner != "" {
		m.hub.Show(owner, number) // the AI link: cardhub notify / events
	}
	quitWithTerminal(p)
	_, err = p.Run()
	m.hub.StopAll("CardHub quit") // waiting agents hear the watchers stopped
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// quitWithTerminal quits p when its terminal goes away (the window or tab was
// closed): on SIGHUP, or when writing to it fails. Without it a cardhub that
// never got the hangup kept running unseen, holding its AI link.
func quitWithTerminal(p *tea.Program) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-hup:
			case <-tick.C:
				if _, err := os.Stdout.Write(nil); err == nil { // a closed terminal gives EIO
					continue
				}
			}
			p.Quit()
			return
		}
	}()
}

func printSummary(c *Client, owner string, number int, group, viewArg string) error {
	ctx := context.Background()
	meta, err := c.FetchMeta(ctx, owner, number)
	if err != nil {
		return err
	}
	filter, title := "", "all items"
	if viewArg != "" {
		i := FindView(meta.Views, viewArg)
		if i < 0 {
			return fmt.Errorf("no view %q in this project", viewArg)
		}
		filter, title = meta.Views[i].Filter, meta.Views[i].Name
		if group == "" {
			group = meta.Views[i].GroupBy
		}
	}
	group = resolveGroup(meta.Fields, group)
	b, err := c.FetchColumns(ctx, owner, number, meta, group, filter, nil)
	if err != nil {
		return err
	}
	m := newModel(c, owner, number, 0, group, "")
	m.applyBoard(b)
	fmt.Printf("%s › %s — %d items, grouped by %s\n", b.Title, title, m.visibleCount(), m.groupField)
	for _, col := range m.columns {
		fmt.Printf("  %-40s %5d\n", trunc(col.name, 40), col.total)
	}
	fmt.Printf("API budget: %d/%d · this sync cost %d · auto interval %s\n", b.RateLimit.Remaining, b.RateLimit.Limit,
		b.Cost, autoInterval(30*time.Second, b.Cost, b.RateLimit, time.Now()))
	if viewArg == "" && len(meta.Views) > 0 {
		fmt.Println("Views (use -view <name|number>):")
		for _, v := range meta.Views {
			fmt.Printf("  %3d  %-32s %s\n", v.Number, trunc(v.Name, 32), v.Filter)
		}
	}
	return nil
}
