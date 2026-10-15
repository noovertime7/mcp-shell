package webtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Query is one web search.
type Query struct {
	Text string
	// Site limits results to a domain, e.g. iot.mi.com.
	Site string
	// TimeRange is "", day, week, month or year.
	TimeRange string
	// Language such as zh-CN or en; empty lets the engine choose.
	Language   string
	MaxResults int
}

// Result is one search hit.
type Result struct {
	Title     string
	URL       string
	Snippet   string
	Engine    string
	Published string
}

// Results are the hits of one search and where they came from.
type Results struct {
	Source  string
	Results []Result
	// Notes say why a source was skipped.
	Notes []string
}

// Search asks Bocha (with an API key), then SearXNG, then Bing's result page,
// each when the one before is not set up, fails or finds nothing.
func (c *Client) Search(ctx context.Context, q Query) (*Results, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" {
		return nil, errors.New("query is required")
	}
	if q.MaxResults <= 0 {
		q.MaxResults = 10
	}
	if q.MaxResults > 30 {
		q.MaxResults = 30
	}
	switch q.TimeRange {
	case "", "day", "week", "month", "year":
	default:
		return nil, fmt.Errorf("time_range must be day, week, month or year, not %q", q.TimeRange)
	}
	text := q.Text
	if site := strings.TrimSpace(q.Site); site != "" {
		text = "site:" + site + " " + text
	}
	out := &Results{}
	if c.BochaKey != "" {
		hits, err := c.bocha(ctx, q)
		switch {
		case err != nil:
			out.Notes = append(out.Notes, "博查不可用："+err.Error())
		case len(hits) == 0:
			out.Notes = append(out.Notes, "博查没有结果")
		default:
			out.Source, out.Results = "博查 Bocha", limit(hits, q.MaxResults)
			return out, nil
		}
	}
	if c.SearXNG != "" {
		hits, err := c.searxng(ctx, text, q)
		switch {
		case err != nil:
			out.Notes = append(out.Notes, "SearXNG 不可用："+err.Error())
		case len(hits) == 0:
			out.Notes = append(out.Notes, "SearXNG 没有结果")
		default:
			out.Source, out.Results = "SearXNG "+c.SearXNG, limit(hits, q.MaxResults)
			return out, nil
		}
	}
	hits, err := c.bing(ctx, text, q)
	if err != nil {
		if len(out.Notes) > 0 {
			return nil, fmt.Errorf("%s；Bing 也失败：%w", strings.Join(out.Notes, "；"), err)
		}
		return nil, fmt.Errorf("bing: %w", err)
	}
	out.Source, out.Results = "Bing", limit(hits, q.MaxResults)
	return out, nil
}

func limit(hits []Result, n int) []Result {
	seen := map[string]bool{}
	var out []Result
	for _, h := range hits {
		key := strings.TrimRight(h.URL, "/")
		if h.URL == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, h)
		if len(out) == n {
			break
		}
	}
	return out
}

func (c *Client) get(ctx context.Context, target string, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func (c *Client) searxng(ctx context.Context, text string, q Query) ([]Result, error) {
	params := url.Values{"q": {text}, "format": {"json"}, "categories": {"general"}}
	if q.TimeRange != "" {
		params.Set("time_range", q.TimeRange)
	}
	if q.Language != "" {
		params.Set("language", q.Language)
	}
	body, err := c.get(ctx, c.SearXNG+"/search?"+params.Encode(), "application/json")
	if err != nil {
		return nil, err
	}
	var data struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			Content       string   `json:"content"`
			Engines       []string `json:"engines"`
			Engine        string   `json:"engine"`
			PublishedDate string   `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, errors.New("返回的不是 JSON（需要在 SearXNG 的 settings.yml 的 search.formats 里加上 json）")
	}
	var hits []Result
	for _, r := range data.Results {
		engine := r.Engine
		if len(r.Engines) > 0 {
			engine = strings.Join(r.Engines, ",")
		}
		hits = append(hits, Result{
			Title: collapse(r.Title), URL: r.URL, Snippet: collapse(r.Content),
			Engine: engine, Published: r.PublishedDate,
		})
	}
	return hits, nil
}

var bochaFreshness = map[string]string{"": "noLimit", "day": "oneDay", "week": "oneWeek", "month": "oneMonth", "year": "oneYear"}

// maxSnippet keeps Bocha's page summaries to a few sentences per result.
const maxSnippet = 300

func (c *Client) bocha(ctx context.Context, q Query) ([]Result, error) {
	request := map[string]any{
		"query":     q.Text,
		"freshness": bochaFreshness[q.TimeRange],
		"summary":   true,
		"count":     q.MaxResults,
	}
	if site := strings.TrimSpace(q.Site); site != "" {
		request["include"] = site
	}
	payload, _ := json.Marshal(request)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BochaURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.BochaKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	var data struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			WebPages struct {
				Value []struct {
					Name          string `json:"name"`
					URL           string `json:"url"`
					Snippet       string `json:"snippet"`
					Summary       string `json:"summary"`
					SiteName      string `json:"siteName"`
					DatePublished string `json:"datePublished"`
				} `json:"value"`
			} `json:"webPages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("HTTP %d，返回的不是 JSON", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || (data.Code != 0 && data.Code != http.StatusOK) {
		msg := data.Msg
		if msg == "" {
			msg = strings.TrimSpace(string(body[:minLen(len(body), 200)]))
		}
		return nil, fmt.Errorf("HTTP %d，code %d：%s", resp.StatusCode, data.Code, msg)
	}
	var hits []Result
	for _, r := range data.Data.WebPages.Value {
		snippet := collapse(r.Summary)
		if snippet == "" {
			snippet = collapse(r.Snippet)
		}
		if runes := []rune(snippet); len(runes) > maxSnippet {
			snippet = string(runes[:maxSnippet]) + "…"
		}
		engine := "bocha"
		if r.SiteName != "" {
			engine += " · " + r.SiteName
		}
		hits = append(hits, Result{
			Title: collapse(r.Name), URL: r.URL, Snippet: snippet,
			Engine: engine, Published: r.DatePublished,
		})
	}
	return hits, nil
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var bingTime = map[string]string{"day": `ex1:"ez1"`, "week": `ex1:"ez2"`, "month": `ex1:"ez3"`}

func (c *Client) bing(ctx context.Context, text string, q Query) ([]Result, error) {
	params := url.Values{"q": {text}, "count": {"30"}}
	if f := bingTime[q.TimeRange]; f != "" {
		params.Set("filters", f)
	}
	if strings.HasPrefix(strings.ToLower(q.Language), "en") {
		params.Set("ensearch", "1")
	}
	body, err := c.get(ctx, c.Bing+"?"+params.Encode(), "text/html")
	if err != nil {
		return nil, err
	}
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	var hits []Result
	walk(root, func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.Data != "li" || !hasClass(n, "b_algo") {
			return true
		}
		h2 := find(n, func(e *html.Node) bool { return e.Data == "h2" })
		if h2 == nil {
			return false
		}
		a := find(h2, func(e *html.Node) bool { return e.Data == "a" && attr(e, "href") != "" })
		if a == nil {
			return false
		}
		snippet := ""
		if p := find(n, func(e *html.Node) bool {
			return e.Data == "p" || hasClass(e, "b_lineclamp2") || hasClass(e, "b_lineclamp3") || hasClass(e, "b_caption")
		}); p != nil {
			snippet = collapse(textOf(p))
		}
		hits = append(hits, Result{
			Title: collapse(textOf(a)), URL: unwrapBing(attr(a, "href")), Snippet: snippet, Engine: "bing",
		})
		return false
	})
	if len(hits) == 0 {
		return nil, errors.New("结果页里没有找到搜索结果（可能被要求验证或页面改版）")
	}
	return hits, nil
}

// unwrapBing returns the target of a bing.com/ck/a tracking link: its u
// parameter is "a1" and the URL in unpadded base64url.
func unwrapBing(href string) string {
	u, err := url.Parse(href)
	if err != nil || !strings.HasSuffix(u.Host, "bing.com") || !strings.HasPrefix(u.Path, "/ck/") {
		return href
	}
	value := u.Query().Get("u")
	if !strings.HasPrefix(value, "a1") {
		return href
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value[2:], "="))
	if err != nil || !strings.HasPrefix(string(decoded), "http") {
		return href
	}
	return string(decoded)
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}
