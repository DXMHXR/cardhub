package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	_ "golang.org/x/image/webp"
)

// Pictures in a detail (GUI only): Herdr's panel draws images from files, not
// URLs, so cardhub -gui downloads the ones a description or comment shows into
// gui/images/ and tells the panel each one's file and size. GitHub's
// attachments (github.com/user-attachments/…) need the token, like the API.

var (
	mdImage   = regexp.MustCompile(`!\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	htmlImage = regexp.MustCompile(`(?i)<img\b[^>]*?\bsrc\s*=\s*["']([^"']+)["']`)
)

// maxImages is the most pictures one detail downloads; maxImageBytes the
// largest picture it takes.
const (
	maxImages     = 30
	maxImageBytes = 15 << 20
	imagesKeep    = 7 * 24 * time.Hour // how long a picture nobody opens stays in gui/images
	imagesMax     = 200 << 20          // gui/images never holds more: the least recently opened go first
	imagesPrune   = 6 * time.Hour      // how often a running cardhub -gui cleans up
)

// attachment reports whether u is a picture attached on GitHub (dragged into
// an issue, PR or comment): the only pictures a detail downloads. Others (a
// Google Drive link, say) stay links.
func attachment(u string) bool {
	pu, err := url.Parse(u)
	if err != nil || pu.Scheme != "https" {
		return false
	}
	switch pu.Hostname() {
	case "user-images.githubusercontent.com", "private-user-images.githubusercontent.com":
		return true
	case "github.com":
		return strings.HasPrefix(pu.Path, "/user-attachments/assets/") || strings.Contains(pu.Path, "/assets/")
	}
	return false
}

// imageURLs lists the GitHub attachments pictured in texts, in order, once each.
func imageURLs(texts ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range texts {
		for _, re := range []*regexp.Regexp{mdImage, htmlImage} {
			for _, m := range re.FindAllStringSubmatch(t, -1) {
				u := strings.ReplaceAll(m[1], "&amp;", "&")
				if attachment(u) && !seen[u] && len(out) < maxImages {
					seen[u] = true
					out = append(out, u)
				}
			}
		}
	}
	return out
}

// guiImage is a picture of the detail, as the panel gets it: a file in
// gui/images and its size in pixels, or why there is none.
type guiImage struct {
	File  string `json:"file,omitempty"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
	Error string `json:"error,omitempty"`
}

type imageMsg struct {
	seq int // the detail's: a picture for a closed detail is dropped
	url string
	img guiImage
}

// imageFile is where u is kept in dir: named by its URL, so a detail opened
// again finds its pictures already there.
func imageFile(u string) string {
	sum := sha1.Sum([]byte(u))
	return hex.EncodeToString(sum[:8])
}

// FetchImage downloads the picture at u into dir, unless it's already there.
func (c *Client) FetchImage(ctx context.Context, u, dir string) guiImage {
	base := imageFile(u)
	if old, _ := filepath.Glob(filepath.Join(dir, base+".*")); len(old) > 0 {
		if img, err := imageSize(old[0]); err == nil {
			now := time.Now()
			_ = os.Chtimes(old[0], now, now) // still in use: keep it past the prune
			return img
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return guiImage{Error: "bad link"}
	}
	if pu, _ := url.Parse(u); pu != nil && pu.Scheme == "https" && pu.Hostname() == "github.com" && c.token != "" {
		// net/http drops it when GitHub redirects to its storage host
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	hc := c.http
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return guiImage{Error: "couldn't load it"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return guiImage{Error: fmt.Sprintf("couldn't load it (HTTP %d)", resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return guiImage{Error: "couldn't load it"}
	}
	if len(data) > maxImageBytes {
		return guiImage{Error: "too large to show here"}
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width == 0 || cfg.Height == 0 {
		return guiImage{Error: "not a picture the panel can show"}
	}
	name := base + "." + format
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return guiImage{Error: "couldn't save it"}
	}
	tmp := filepath.Join(dir, "."+name+".tmp")
	if os.WriteFile(tmp, data, 0o644) != nil || os.Rename(tmp, filepath.Join(dir, name)) != nil {
		_ = os.Remove(tmp)
		return guiImage{Error: "couldn't save it"}
	}
	return guiImage{File: name, W: cfg.Width, H: cfg.Height}
}

func imageSize(path string) (guiImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return guiImage{}, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width == 0 || cfg.Height == 0 {
		return guiImage{}, fmt.Errorf("not a picture")
	}
	return guiImage{File: filepath.Base(path), W: cfg.Width, H: cfg.Height}, nil
}

// detailImages starts downloading the pictures of the loaded detail, each
// reported on its own as it lands. Only the GUI shows them.
func (m *model) detailImages() tea.Cmd {
	d := m.detail.data
	if m.gui == nil || m.gui.dir == "" || d == nil || m.client == nil {
		return nil
	}
	texts := []string{d.Body}
	for _, c := range d.Comments {
		texts = append(texts, c.Body)
	}
	urls := imageURLs(texts...)
	if len(urls) == 0 {
		return nil
	}
	m.detail.images = map[string]guiImage{}
	c, seq, dir := m.client, m.detail.seq, filepath.Join(m.gui.dir, "images")
	cmds := make([]tea.Cmd, 0, len(urls))
	for _, u := range urls {
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			return imageMsg{seq: seq, url: u, img: c.FetchImage(ctx, u, dir)}
		})
	}
	return tea.Batch(cmds...)
}

func (m *model) gotImage(msg imageMsg) {
	if !m.detail.open || msg.seq != m.detail.seq || m.detail.images == nil {
		return
	}
	m.detail.images[msg.url] = msg.img
}

// pruneImages keeps gui/images small: it drops pictures nobody opened for
// keep, then the least recently opened ones until the rest fit in max bytes.
// Opening a detail touches its pictures (FetchImage), so what's on screen is
// the newest and goes last. The panel drops its copies of what's gone.
func pruneImages(dir string, keep time.Duration, max int64) {
	type pic struct {
		path string
		at   time.Time
		size int64
	}
	var pics []pic
	var total int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if time.Since(info.ModTime()) > keep {
			_ = os.Remove(p)
			continue
		}
		pics = append(pics, pic{p, info.ModTime(), info.Size()})
		total += info.Size()
	}
	sort.Slice(pics, func(i, j int) bool { return pics[i].at.Before(pics[j].at) })
	for _, p := range pics {
		if total <= max {
			break
		}
		if os.Remove(p.path) == nil {
			total -= p.size
		}
	}
}

// pruneImagesEvery cleans gui/images now and then every imagesPrune while
// cardhub -gui runs.
func pruneImagesEvery(dir string) {
	for {
		pruneImages(dir, imagesKeep, imagesMax)
		time.Sleep(imagesPrune)
	}
}
