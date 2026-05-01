package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixtureSite returns an httptest server with a tiny multi-page site:
// "/" links to /about and /pricing; /about links back to / and to a
// non-existent path (404); /image.png is a binary that must be skipped.
func fixtureSite() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<h1>Home</h1>
			<a href="/about">About</a>
			<a href="/pricing">Pricing</a>
			<a href="/image.png">Logo</a>
			<a href="mailto:hi@example.de">mail</a>
			<a href="#section">anchor</a>
		</body></html>`))
	})
	mux.HandleFunc("/about", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>About</h1>
			<a href="/">Home</a><a href="/missing">missing</a></body></html>`))
	})
	mux.HandleFunc("/pricing", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>Pricing</h1></body></html>`))
	})
	mux.HandleFunc("/image.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4E, 0x47})
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	return httptest.NewServer(mux)
}

func TestCrawl_VisitsExpectedURLs(t *testing.T) {
	srv := fixtureSite()
	defer srv.Close()

	ch, err := Crawl(context.Background(), srv.URL+"/", Options{
		Delay:    1 * time.Millisecond, // tests should be fast
		MaxPages: 50,
	})
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	got := map[string]bool{}
	for p := range ch {
		got[strings.TrimPrefix(p.URL, srv.URL)] = true
		if !strings.Contains(p.HTML, "<html") {
			t.Errorf("page %s missing html: %q", p.URL, p.HTML)
		}
	}

	for _, want := range []string{"/", "/about", "/pricing"} {
		if !got[want] {
			t.Errorf("expected to fetch %q, got %v", want, keys(got))
		}
	}
	if got["/image.png"] {
		t.Errorf("image.png should be skipped (wrong content type)")
	}
}

func TestCrawl_HonorsMaxPages(t *testing.T) {
	srv := fixtureSite()
	defer srv.Close()

	ch, err := Crawl(context.Background(), srv.URL+"/", Options{
		Delay:    1 * time.Millisecond,
		MaxPages: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for range ch {
		count++
	}
	if count != 1 {
		t.Fatalf("expected 1 page (MaxPages=1), got %d", count)
	}
}

func TestCrawl_RejectsNonHTTPSeed(t *testing.T) {
	if _, err := Crawl(context.Background(), "not-a-url", Options{}); err == nil {
		t.Fatal("expected error for hostless URL")
	}
}

func TestIsContentExt(t *testing.T) {
	allowed := []string{"", ".html", ".htm"}
	cases := map[string]bool{
		"/":           true,
		"/leistungen": true,
		"/foo.html":   true,
		"/image.png":  false,
		"/file.pdf":   false,
		"/x.HTM":      true,
	}
	for path, want := range cases {
		if got := isContentExt(path, allowed); got != want {
			t.Errorf("isContentExt(%q): got %v, want %v", path, got, want)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
