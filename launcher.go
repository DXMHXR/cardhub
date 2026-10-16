package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// openInTerminal starts cmdline in a new, visible terminal next to the one the
// caller (usually an AI agent) runs in, and returns where it opened it.
//
// Order: $CARDHUB_TERMINAL (a command template, {cmd} is replaced) → cmux (new
// tab in the caller's workspace) → tmux (new window) → the macOS or Linux
// terminal app the caller runs in.
func openInTerminal(cmdline string) (string, error) {
	if tpl := os.Getenv("CARDHUB_TERMINAL"); tpl != "" {
		c := strings.ReplaceAll(tpl, "{cmd}", shellQuote(cmdline))
		if !strings.Contains(tpl, "{cmd}") {
			c = tpl + " " + cmdline
		}
		return "CARDHUB_TERMINAL", exec.Command("sh", "-c", c).Run()
	}
	if ws := os.Getenv("CMUX_WORKSPACE_ID"); ws != "" {
		if bin := cmuxBin(); bin != "" {
			return openCmux(bin, ws, cmdline)
		}
	}
	if os.Getenv("TMUX") != "" {
		return "a new tmux window", exec.Command("tmux", "new-window", "-d", "-n", "cardhub", cmdline).Run()
	}
	switch runtime.GOOS {
	case "darwin":
		return openMac(cmdline)
	case "linux":
		for _, t := range [][]string{
			{"x-terminal-emulator", "-e", "sh", "-c", cmdline},
			{"gnome-terminal", "--", "sh", "-c", cmdline},
			{"konsole", "-e", "sh", "-c", cmdline},
			{"xterm", "-e", "sh", "-c", cmdline},
		} {
			if _, err := exec.LookPath(t[0]); err == nil {
				return "a new " + t[0] + " window", exec.Command(t[0], t[1:]...).Start()
			}
		}
	}
	return "", errors.New("don't know how to open a terminal here; set CARDHUB_TERMINAL " +
		`(e.g. CARDHUB_TERMINAL='alacritty -e sh -c {cmd}') or start cardhub yourself`)
}

func cmuxBin() string {
	if p, err := exec.LookPath("cmux"); err == nil {
		return p
	}
	return os.Getenv("CMUX_BUNDLED_CLI_PATH")
}

var surfaceRef = regexp.MustCompile(`surface:\S+`)

// openCmux adds a background tab named "cardhub" to the caller's workspace.
func openCmux(bin, workspace, cmdline string) (string, error) {
	out, err := exec.Command(bin, "new-surface", "--type", "terminal", "--workspace", workspace,
		"--command", cmdline, "--focus", "false").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cmux new-surface: %s", strings.TrimSpace(string(out)))
	}
	if ref := surfaceRef.FindString(string(out)); ref != "" {
		_ = exec.Command(bin, "rename-tab", "--surface", ref, "cardhub").Run()
	}
	return "a new cmux tab", nil
}

func openMac(cmdline string) (string, error) {
	osa := func(script string) error { return exec.Command("osascript", "-e", script).Run() }
	q := strings.ReplaceAll(strings.ReplaceAll(cmdline, `\`, `\\`), `"`, `\"`)
	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app":
		return "a new iTerm2 window", osa(`tell application "iTerm2" to create window with default profile command "` + q + `"`)
	case "ghostty":
		return "a new Ghostty window", exec.Command("open", "-na", "Ghostty", "--args", "-e", "sh", "-c", cmdline).Run()
	case "WezTerm":
		return "a new WezTerm window", exec.Command("wezterm", "start", "--", "sh", "-c", cmdline).Start()
	}
	return "a new Terminal window", osa(`tell application "Terminal" to do script "` + q + `"`)
}

// shellQuote wraps s for sh when it contains anything but safe characters.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
