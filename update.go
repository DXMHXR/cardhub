package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Updates: CardHub checks GitHub for a newer release (at most once an hour,
// cached between runs), shows it in the header, and can replace its own
// binary with the release archive for this OS and CPU after checking it
// against the release's checksums.txt (U on screen, or `cardhub update`).

const (
	repoSlug      = "DXMHXR/cardhub"
	checkEvery    = time.Hour
	updateTimeout = 2 * time.Minute
)

var updateHTTP = &http.Client{Timeout: updateTimeout}

type updateCache struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"` // e.g. "v0.2.3"
}

func updateCachePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "cardhub", "update.json")
}

func loadUpdateCache() updateCache {
	var c updateCache
	if b, err := os.ReadFile(updateCachePath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func saveUpdateCache(c updateCache) {
	p := updateCachePath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(p, b, 0o644)
}

// latestRelease asks GitHub for the newest release's tag.
func latestRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+repoSlug+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := updateHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.Tag, nil
}

// checkLatest returns the newest release, from the cache when it's fresh.
func checkLatest(ctx context.Context, force bool) (string, error) {
	c := loadUpdateCache()
	if !force && c.Latest != "" && time.Since(c.Checked) < checkEvery {
		return c.Latest, nil
	}
	tag, err := latestRelease(ctx)
	if err != nil {
		return c.Latest, err
	}
	saveUpdateCache(updateCache{Checked: time.Now(), Latest: tag})
	return tag, nil
}

// semver parses "v1.2.3" into its numbers.
func semver(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		if j := strings.IndexAny(p, "-+"); j >= 0 { // v1.2.3-rc1, v1.2.3+dirty
			p = p[:j]
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// newer reports whether release tag b is newer than version a.
func newer(a, b string) bool {
	va, okA := semver(a)
	vb, okB := semver(b)
	if !okA || !okB {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return vb[i] > va[i]
		}
	}
	return false
}

// selfUpdatable reports whether this binary is a release build that may
// replace itself (a build from a source checkout is updated with git instead).
func selfUpdatable() bool {
	info, ok := debug.ReadBuildInfo()
	return ok && isRelease(info.Main.Version)
}

// ---- in the TUI ----

type updateCheckMsg struct{ latest string }

type updateDoneMsg struct {
	tag string
	err error
}

// checkUpdateCmd looks for a newer release in the background; errors are
// ignored (no network, rate limit): the check simply happens again later.
func checkUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		tag, _ := checkLatest(ctx, false)
		return updateCheckMsg{tag}
	}
}

func updateCmd(tag string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
		defer cancel()
		_, err := installRelease(ctx, tag)
		return updateDoneMsg{tag, err}
	}
}

// ---- installing a release ----

// installRelease downloads tag's archive for this OS and CPU, checks it
// against checksums.txt and replaces the running binary. It returns the path
// it replaced.
func installRelease(ctx context.Context, tag string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	return exe, installReleaseAt(ctx, tag, exe)
}

// installReleaseAt puts tag's binary at exe.
func installReleaseAt(ctx context.Context, tag, exe string) error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("no release archive for %s; build it with go install github.com/%s@%s", runtime.GOOS, repoSlug, tag)
	}
	ver := strings.TrimPrefix(tag, "v")
	name := fmt.Sprintf("cardhub_%s_%s_%s", ver, runtime.GOOS, runtime.GOARCH)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/", repoSlug, tag)

	sums, err := download(ctx, base+"checksums.txt")
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name+".tar.gz" {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("%s has no archive for %s/%s", tag, runtime.GOOS, runtime.GOARCH)
	}
	archive, err := download(ctx, base+name+".tar.gz")
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != want {
		return errors.New("the downloaded archive doesn't match checksums.txt; nothing was changed")
	}
	bin, err := untarFile(archive, name+"/cardhub")
	if err != nil {
		return err
	}

	// write next to the binary, then rename over it (atomic; a running
	// cardhub keeps the old file open and is unaffected)
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".cardhub-update-*")
	if err != nil {
		return fmt.Errorf("can't write to %s (%v); update with: go install github.com/%s@%s", filepath.Dir(exe), err, repoSlug, tag)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return err
	}
	return nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := updateHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", filepath.Base(url), resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 100<<20))
}

// untarFile returns the file at path inside a .tar.gz.
func untarFile(archive []byte, path string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s isn't in the archive", path)
		}
		if err != nil {
			return nil, err
		}
		if h.Name == path {
			return io.ReadAll(io.LimitReader(tr, 100<<20))
		}
	}
}

// ---- cardhub update ----

func runUpdate() int {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	tag, err := checkLatest(ctx, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "couldn't check for a new release:", err)
		return exitError
	}
	cur := versionLabel()
	if !newer(cur, tag) {
		fmt.Printf("CardHub %s is the latest release.\n", cur)
		return exitOK
	}
	if !selfUpdatable() {
		fmt.Printf("CardHub %s is out (this is %s, a build from source).\n", tag, cur)
		fmt.Println("Update the checkout with git pull and make build, or install the release:")
		fmt.Printf("  go install github.com/%s@%s\n", repoSlug, tag)
		return exitOK
	}
	fmt.Printf("Updating CardHub %s → %s…\n", cur, tag)
	path, err := installRelease(ctx, tag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update failed:", err)
		return exitError
	}
	fmt.Printf("Updated %s to %s. Restart any open CardHub to use it.\n", path, tag)
	return exitOK
}
