package server

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/noovertime7/mcp-shell/internal/webtools"
)

type webSearchIn struct {
	Query      string `json:"query" jsonschema:"what to search for; any language"`
	Site       string `json:"site,omitempty" jsonschema:"only results from this domain, e.g. iot.mi.com"`
	TimeRange  string `json:"time_range,omitempty" jsonschema:"only recent results: day, week, month or year"`
	Language   string `json:"language,omitempty" jsonschema:"result language such as zh-CN or en (default: the engine's choice)"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"how many results (default 10, max 30)"`
}

type webFetchIn struct {
	URL        string `json:"url" jsonschema:"page to read (http or https; https:// is added when missing)"`
	Find       string `json:"find,omitempty" jsonschema:"keywords separated by spaces or commas: return only the sections that mention any of them, each under its heading"`
	Offset     int    `json:"offset,omitempty" jsonschema:"character to start at, from the previous result's next: line, for a long page"`
	MaxChars   int    `json:"max_chars,omitempty" jsonschema:"most characters to return (default 12000)"`
	Links      bool   `json:"links,omitempty" jsonschema:"also list the page's links, e.g. a documentation sidebar"`
	Refresh    bool   `json:"refresh,omitempty" jsonschema:"fetch again instead of using the copy kept for 15 minutes"`
	TimeoutSec int    `json:"timeout_sec,omitempty" jsonschema:"timeout in seconds (default 30, max 120)"`
}

const (
	defaultWebChars = 12000
	maxFindMatches  = 40
	maxLinks        = 200
)

func webTimeout(sec int) time.Duration {
	if sec <= 0 {
		sec = 30
	}
	if sec > 120 {
		sec = 120
	}
	return time.Duration(sec) * time.Second
}

func (s *Server) webSearch(ctx context.Context, in webSearchIn) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, webTimeout(0))
	defer cancel()
	res, err := s.web.Search(ctx, webtools.Query{
		Text: in.Query, Site: in.Site, TimeRange: in.TimeRange,
		Language: in.Language, MaxResults: in.MaxResults,
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d results for %q from %s\n", len(res.Results), in.Query, res.Source)
	for _, note := range res.Notes {
		fmt.Fprintf(&b, "note: %s\n", note)
	}
	for i, r := range res.Results {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
		if r.Published != "" {
			fmt.Fprintf(&b, "   published: %s\n", r.Published)
		}
	}
	if len(res.Results) > 0 {
		b.WriteString("\nnext: web_fetch url=<a result> (find=<keywords> returns only the matching sections)\n")
	}
	return b.String(), nil
}

func (s *Server) webFetch(ctx context.Context, in webFetchIn) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, webTimeout(in.TimeoutSec))
	defer cancel()
	page, err := s.web.Fetch(ctx, in.URL, in.Refresh)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if page.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", page.Title)
	}
	fmt.Fprintf(&b, "url: %s", page.FinalURL)
	if page.FinalURL != page.URL {
		fmt.Fprintf(&b, " (redirected from %s)", page.URL)
	}
	fmt.Fprintf(&b, "\nHTTP %d · %s · %s", page.Status, orUnknown(page.ContentType), formatSize(page.Size))
	if page.Cached {
		fmt.Fprintf(&b, " · cached %s ago (refresh=true fetches again)", time.Since(page.Fetched).Round(time.Second))
	}
	b.WriteString("\n")
	if page.Binary {
		b.WriteString("--- not text: images, PDFs and archives are not converted; download it with run_command (curl -L -o <file> <url>) if needed ---\n")
		return b.String(), nil
	}
	if page.Status >= 400 {
		b.WriteString("note: the server answered with an error status; the text below is its error page\n")
	}
	budget := s.webBudget(b.Len())
	limit := in.MaxChars
	if limit <= 0 {
		limit = defaultWebChars
	}
	total := utf8.RuneCountInString(page.Text)
	if strings.TrimSpace(in.Find) != "" {
		b.WriteString(findSections(page.Text, in.Find, limit, budget))
	} else {
		if in.Offset < 0 {
			in.Offset = 0
		}
		part, end := window(page.Text, in.Offset, limit, budget)
		fmt.Fprintf(&b, "--- content: characters %d-%d of %d ---\n%s\n", minInt(in.Offset, total), end, total, part)
		if end < total {
			fmt.Fprintf(&b, "--- more ---\nnext: web_fetch url=%s offset=%d (or find=<keywords> for just the relevant sections)\n", page.URL, end)
		}
	}
	if in.Links {
		b.WriteString(formatLinks(page.Links, in.Find))
	}
	return b.String(), nil
}

// webBudget is how many bytes of page text fit in one result after the header.
func (s *Server) webBudget(used int) int {
	limit := 60000
	if s.cfg != nil && s.cfg.MaxResultBytes > 0 {
		limit = s.cfg.MaxResultBytes
	}
	budget := limit - used - 2000
	if budget < 4000 {
		budget = 4000
	}
	return budget
}

// window returns text from rune offset, at most limit runes and budget bytes,
// and the rune index it ended at.
func window(text string, offset, limit, budget int) (string, int) {
	runes := []rune(text)
	if offset > len(runes) {
		offset = len(runes)
	}
	end, size := offset, 0
	for end < len(runes) && end-offset < limit {
		size += utf8.RuneLen(runes[end])
		if size > budget {
			break
		}
		end++
	}
	return string(runes[offset:end]), end
}

func keywords(find string) []string {
	fields := strings.FieldsFunc(find, func(r rune) bool {
		return r == ' ' || r == ',' || r == '，' || r == '、' || r == '|' || r == '\t' || r == '\n'
	})
	var out []string
	for _, f := range fields {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// findSections returns the paragraphs (blocks between blank lines) that
// mention any keyword, each under the headings it sits in.
func findSections(text, find string, limit, budget int) string {
	words := keywords(find)
	if len(words) == 0 {
		return "--- find has no keywords ---\n"
	}
	var b strings.Builder
	var headings []string
	matches, shown := 0, 0
	lastHeading := ""
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if level := headingLevel(block); level > 0 {
			for len(headings) >= level {
				headings = headings[:len(headings)-1]
			}
			for len(headings) < level-1 {
				headings = append(headings, "")
			}
			headings = append(headings, strings.TrimSpace(strings.TrimLeft(block, "#")))
		}
		lower := strings.ToLower(block)
		hit := false
		for _, w := range words {
			if strings.Contains(lower, w) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		matches++
		if shown >= maxFindMatches || utf8.RuneCountInString(b.String()) > limit || b.Len() > budget {
			continue
		}
		path := strings.Join(nonEmpty(headings), " > ")
		if path != "" && path != lastHeading && headingLevel(block) == 0 {
			fmt.Fprintf(&b, "\n§ %s\n", path)
			lastHeading = path
		}
		b.WriteString(block)
		b.WriteString("\n\n")
		shown++
	}
	if matches == 0 {
		return fmt.Sprintf("--- no section mentions %s; read the page without find, or try other words ---\n", strings.Join(words, " / "))
	}
	head := fmt.Sprintf("--- %d sections mention %s", matches, strings.Join(words, " / "))
	if shown < matches {
		head += fmt.Sprintf("; showing the first %d (narrow find or read with offset)", shown)
	}
	return head + " ---\n" + strings.TrimRight(b.String(), "\n") + "\n"
}

func headingLevel(block string) int {
	if strings.Contains(block, "\n") {
		return 0
	}
	n := 0
	for n < len(block) && block[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(block) || block[n] != ' ' {
		return 0
	}
	return n
}

func nonEmpty(items []string) []string {
	var out []string
	for _, item := range items {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// formatLinks lists links; with find, those whose text or URL mention a keyword first.
func formatLinks(links []webtools.Link, find string) string {
	words := keywords(find)
	matching := func(l webtools.Link) bool {
		lower := strings.ToLower(l.Text + " " + l.URL)
		for _, w := range words {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}
	var ordered []webtools.Link
	if len(words) > 0 {
		for _, l := range links {
			if matching(l) {
				ordered = append(ordered, l)
			}
		}
	}
	if len(ordered) == 0 {
		ordered = links
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- links: %d", len(links))
	if len(ordered) != len(links) {
		fmt.Fprintf(&b, ", %d mention the find keywords", len(ordered))
	}
	if len(ordered) > maxLinks {
		fmt.Fprintf(&b, ", first %d shown", maxLinks)
		ordered = ordered[:maxLinks]
	}
	b.WriteString(" ---\n")
	for _, l := range ordered {
		text := l.Text
		if text == "" {
			text = "(no text)"
		}
		fmt.Fprintf(&b, "- %s: %s\n", text, l.URL)
	}
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown type"
	}
	return s
}

func formatSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1<<20 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
