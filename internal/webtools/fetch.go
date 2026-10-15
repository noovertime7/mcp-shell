// Package webtools gives the host a web search and a page reader: search
// results come from SearXNG (or Bing's result page), and fetched pages are
// turned into Markdown with only the main content kept, so a page costs a
// fraction of its HTML.
package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html/charset"
)

// UserAgent looks like a desktop browser: many sites answer bots with less.
const UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

const (
	maxBody      = 8 << 20
	maxRedirects = 5
	cacheTTL     = 15 * time.Minute
)

// Page is a fetched URL in readable form.
type Page struct {
	URL         string
	FinalURL    string
	Status      int
	ContentType string
	Title       string
	// Text is Markdown for HTML pages, pretty JSON or the text itself otherwise.
	Text string
	// Links are the page's links, absolute, in page order without repeats.
	Links []Link
	// Binary is set for content that is not text (images, PDFs, archives).
	Binary  bool
	Size    int
	Fetched time.Time
	Cached  bool
}

// Link is one anchor of a page.
type Link struct {
	Text string
	URL  string
}

// Client fetches and searches through one HTTP client and keeps fetched
// pages for a while, so reading a long page in parts fetches it once.
type Client struct {
	HTTP *http.Client
	// SearXNG is the base URL of a SearXNG instance with JSON output; empty
	// means only Bing's result page is used.
	SearXNG string
	// Bing is the Bing result page used when SearXNG gives nothing.
	Bing string
	// BochaKey enables the Bocha web search API, asked before SearXNG.
	BochaKey string
	// BochaURL is the Bocha web search endpoint.
	BochaURL string

	mu    sync.Mutex
	cache map[string]*Page
}

// New makes a client; proxy is an optional http(s)/socks5 URL for every request.
func New(searxng, proxy string) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("web proxy %q is not a URL", proxy)
		}
		transport.Proxy = http.ProxyURL(u)
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
	return &Client{
		HTTP:     client,
		SearXNG:  strings.TrimRight(searxng, "/"),
		Bing:     "https://cn.bing.com/search",
		BochaURL: "https://api.bochaai.com/v1/web-search",
		cache:    map[string]*Page{},
	}, nil
}

// Fetch reads rawURL (a cached copy younger than 15 minutes unless refresh).
func (c *Client) Fetch(ctx context.Context, rawURL string, refresh bool) (*Page, error) {
	target, err := normalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	if !refresh {
		c.mu.Lock()
		page := c.cache[target]
		c.mu.Unlock()
		if page != nil && time.Since(page.Fetched) < cacheTTL {
			copy := *page
			copy.Cached = true
			return &copy, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", target, err)
	}
	if len(body) > maxBody {
		body = body[:maxBody]
	}
	page := &Page{
		URL:         target,
		FinalURL:    resp.Request.URL.String(),
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Size:        len(body),
		Fetched:     time.Now(),
	}
	if err := page.decode(body); err != nil {
		return nil, err
	}
	c.mu.Lock()
	for key, old := range c.cache {
		if time.Since(old.Fetched) >= cacheTTL {
			delete(c.cache, key)
		}
	}
	c.cache[target] = page
	c.mu.Unlock()
	return page, nil
}

func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("not a URL: %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("only http and https URLs can be fetched, not %s", u.Scheme)
	}
	u.Fragment = ""
	return u.String(), nil
}

func (p *Page) decode(body []byte) error {
	media, _, _ := mime.ParseMediaType(p.ContentType)
	if media == "" {
		media, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	switch {
	case media == "text/html" || media == "application/xhtml+xml" || looksLikeHTML(body, media):
		base, _ := url.Parse(p.FinalURL)
		doc, err := ToMarkdown(decoded(body, p.ContentType), base)
		if err != nil {
			return fmt.Errorf("parse HTML: %w", err)
		}
		p.Title, p.Text, p.Links = doc.Title, doc.Markdown, doc.Links
	case media == "application/json" || strings.HasSuffix(media, "+json"):
		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "", "  ") == nil {
			p.Text = pretty.String()
		} else {
			p.Text = string(body)
		}
	case strings.HasPrefix(media, "text/") || media == "application/xml" || media == "application/javascript":
		text, _ := io.ReadAll(decoded(body, p.ContentType))
		p.Text = string(text)
	default:
		p.Binary = true
	}
	return nil
}

// decoded reads body as UTF-8, converting GBK and other charsets declared in
// the header or the page.
func decoded(body []byte, contentType string) io.Reader {
	reader, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return bytes.NewReader(body)
	}
	return reader
}

func looksLikeHTML(body []byte, media string) bool {
	if media != "" && media != "application/octet-stream" && media != "text/plain" {
		return false
	}
	n := len(body)
	if n > 512 {
		n = 512
	}
	head := strings.ToLower(string(body[:n]))
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html")
}
