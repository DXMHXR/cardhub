package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImageURLsFindsMarkdownAndHTMLPictures(t *testing.T) {
	body := "Before ![shot](https://github.com/user-attachments/assets/abc \"title\")\n" +
		`<img width="300" src="https://example.com/a.png?x=1&amp;y=2" alt="a">` + "\n" +
		`<img src="https://private-user-images.githubusercontent.com/1/2.png?jwt=a&amp;b=c">` + "\n" +
		"![again](https://github.com/user-attachments/assets/abc) ![rel](images/local.png) [not](https://x.com/b.png)\n" +
		"![drive](https://lh3.googleusercontent.com/d/1J3v05)"
	got := imageURLs(body, "![c](https://user-images.githubusercontent.com/9/c.gif)")
	want := []string{"https://github.com/user-attachments/assets/abc", "https://private-user-images.githubusercontent.com/1/2.png?jwt=a&b=c",
		"https://user-images.githubusercontent.com/9/c.gif"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestFetchImageKeepsPicturesAndRefusesTheRest(t *testing.T) {
	var pic bytes.Buffer
	_ = png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("the token went to a host that isn't GitHub")
		}
		switch r.URL.Path {
		case "/a.png":
			_, _ = w.Write(pic.Bytes())
		case "/page":
			_, _ = w.Write([]byte("<html>sign in</html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{token: "secret", http: srv.Client()}
	dir := t.TempDir()
	img := c.FetchImage(context.Background(), srv.URL+"/a.png", dir)
	if img.Error != "" || img.W != 40 || img.H != 30 || !strings.HasSuffix(img.File, ".png") {
		t.Fatalf("want a 40×30 png, got %+v", img)
	}
	if _, err := os.Stat(filepath.Join(dir, img.File)); err != nil {
		t.Fatalf("not saved: %v", err)
	}
	if again := c.FetchImage(context.Background(), srv.URL+"/a.png", dir); again != img {
		t.Fatalf("want the saved one again, got %+v", again)
	}
	if bad := c.FetchImage(context.Background(), srv.URL+"/page", dir); bad.Error == "" || bad.File != "" {
		t.Fatalf("an HTML page isn't a picture, got %+v", bad)
	}
	if gone := c.FetchImage(context.Background(), srv.URL+"/nope", dir); !strings.Contains(gone.Error, "404") {
		t.Fatalf("want the 404, got %+v", gone)
	}
}

func TestPruneImagesDropsOldThenLeastRecentlyOpened(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for name, age := range map[string]time.Duration{"old.png": 8 * 24 * time.Hour, "a.png": 3 * time.Hour, "b.png": 2 * time.Hour, "c.png": time.Hour} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, make([]byte, 100), 0o644)
		_ = os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	pruneImages(dir, 7*24*time.Hour, 200) // room for two of the three recent ones
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != "b.png,c.png" {
		t.Fatalf("want b.png,c.png kept, got %v", left)
	}
}
