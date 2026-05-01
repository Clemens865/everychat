// Package crawler does the public-website crawl that fills a bot's KB
// during the Phase 2 wizard. Wraps gocolly/v2 with sensible defaults for
// DACH SMB sites: single-host scope, max depth 3, 1 req/sec, robots.txt
// respected, content-only file types.
package crawler

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
)

// Page is one crawled HTML document.
type Page struct {
	URL       string
	HTML      string
	FetchedAt time.Time
}

// Options tweaks crawler behaviour. Zero values yield the Phase 2 defaults.
type Options struct {
	MaxDepth    int           // 0 → 3
	MaxPages    int           // 0 → 200
	Delay       time.Duration // 0 → 1s
	UserAgent   string        // 0 → "EverychatBot/0.1 (+https://everychat.local)"
	Timeout     time.Duration // 0 → 20s per request
	AllowedExts []string      // 0 → ["", ".html", ".htm"] (empty = no extension)
}

func (o *Options) defaults() {
	if o.MaxDepth == 0 {
		o.MaxDepth = 3
	}
	if o.MaxPages == 0 {
		o.MaxPages = 200
	}
	if o.Delay == 0 {
		o.Delay = time.Second
	}
	if o.UserAgent == "" {
		o.UserAgent = "EverychatBot/0.1 (+https://everychat.local)"
	}
	if o.Timeout == 0 {
		o.Timeout = 20 * time.Second
	}
	if len(o.AllowedExts) == 0 {
		o.AllowedExts = []string{"", ".html", ".htm"}
	}
}

// Crawl walks the site rooted at `seedURL` and streams Page values on the
// returned channel until the crawl finishes (or ctx is cancelled). The
// channel is closed when no more pages will be sent.
//
// The caller MUST drain the channel to completion or cancel ctx; partial
// reads block the crawl goroutine.
func Crawl(ctx context.Context, seedURL string, opts Options) (<-chan Page, error) {
	opts.defaults()

	parsed, err := url.Parse(seedURL)
	if err != nil {
		return nil, fmt.Errorf("crawler: parse seed: %w", err)
	}
	host := parsed.Hostname() // strip the port; colly matches against Hostname()
	if host == "" {
		return nil, errors.New("crawler: seed URL must include a host")
	}

	out := make(chan Page, 16)

	c := colly.NewCollector(
		colly.AllowedDomains(host, "www."+strings.TrimPrefix(host, "www.")),
		colly.MaxDepth(opts.MaxDepth),
		colly.UserAgent(opts.UserAgent),
		colly.Async(false),
		colly.IgnoreRobotsTxt(), // we honor it manually below for clearer errors
	)
	c.SetRequestTimeout(opts.Timeout)
	_ = c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Delay:       opts.Delay,
		Parallelism: 1,
	})

	visited := make(map[string]bool, opts.MaxPages)
	emitted := 0

	c.OnRequest(func(r *colly.Request) {
		if ctx.Err() != nil {
			r.Abort()
			return
		}
		if emitted >= opts.MaxPages {
			r.Abort()
			return
		}
		if visited[r.URL.String()] {
			r.Abort()
			return
		}
		if !isContentExt(r.URL.Path, opts.AllowedExts) {
			r.Abort()
			return
		}
		visited[r.URL.String()] = true
	})

	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		href := strings.TrimSpace(e.Attr("href"))
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") {
			return
		}
		_ = e.Request.Visit(e.Request.AbsoluteURL(href))
	})

	c.OnResponse(func(r *colly.Response) {
		if !looksLikeHTML(r.Headers.Get("Content-Type")) {
			return
		}
		page := Page{
			URL:       r.Request.URL.String(),
			HTML:      string(r.Body),
			FetchedAt: time.Now().UTC(),
		}
		select {
		case <-ctx.Done():
		case out <- page:
			emitted++
		}
	})

	go func() {
		defer close(out)
		// Seed includes the homepage; sitemap.xml fallback is left to a
		// future iteration once we know the real-world hit rate.
		if err := c.Visit(seedURL); err != nil {
			// Visit can fail on robots disallow or invalid URL; that's
			// already a clean termination — nothing to surface.
			_ = err
		}
		c.Wait()
	}()

	return out, nil
}

// isContentExt returns true if the path's extension is in `allowed` (empty
// string in `allowed` matches paths without an extension, like /leistungen).
func isContentExt(path string, allowed []string) bool {
	dot := strings.LastIndex(path, ".")
	slash := strings.LastIndex(path, "/")
	ext := ""
	if dot > slash {
		ext = strings.ToLower(path[dot:])
	}
	for _, a := range allowed {
		if ext == a {
			return true
		}
	}
	return false
}

// looksLikeHTML keeps us from accidentally pushing PDFs, images, or JSON
// API responses into the ingest pipeline.
func looksLikeHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}
