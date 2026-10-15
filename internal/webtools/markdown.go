package webtools

import (
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Document is an HTML page as Markdown.
type Document struct {
	Title    string
	Markdown string
	Links    []Link
}

// skipped never carry readable content.
var skipped = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true,
	"canvas": true, "iframe": true, "object": true, "embed": true, "head": true,
	"button": true, "select": true, "input": true, "textarea": true,
}

// chrome is the page frame around the content: left out of the whole page,
// kept inside a chosen content element.
var chrome = map[string]bool{"nav": true, "header": true, "footer": true, "aside": true}

var contentHint = regexp.MustCompile(`(?i)(^|[\s_-])(content|article|post|entry|markdown|main|doc|docs|page-content|theme-default-content|rich_media|body-content)([\s_-]|$)`)

// ToMarkdown parses an HTML page; base resolves relative links.
func ToMarkdown(r io.Reader, base *url.URL) (*Document, error) {
	root, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	if b := find(root, func(n *html.Node) bool { return n.Data == "base" && attr(n, "href") != "" }); b != nil && base != nil {
		if u, err := base.Parse(attr(b, "href")); err == nil {
			base = u
		}
	}
	doc := &Document{}
	if t := find(root, func(n *html.Node) bool { return n.Data == "title" }); t != nil {
		doc.Title = collapse(textOf(t))
	}
	doc.Links = links(root, base)
	body := find(root, func(n *html.Node) bool { return n.Data == "body" })
	if body == nil {
		body = root
	}
	main := mainContent(body)
	w := &writer{base: base, frame: main == body}
	w.node(main)
	doc.Markdown = tidy(w.b.String())
	if doc.Title == "" {
		if h := find(main, func(n *html.Node) bool { return n.Data == "h1" }); h != nil {
			doc.Title = collapse(textOf(h))
		}
	}
	return doc, nil
}

// mainContent is the element holding the page's text: main, article or a
// content-like container, the one with the most text outside links; the whole
// body when none holds a good share of it.
func mainContent(body *html.Node) *html.Node {
	total := textLen(body, false)
	var best *html.Node
	bestScore := 0
	walk(body, func(n *html.Node) bool {
		if n.Type != html.ElementNode || skipped[n.Data] {
			return false
		}
		candidate := n.Data == "main" || n.Data == "article" || attr(n, "role") == "main" ||
			contentHint.MatchString(attr(n, "id")) || contentHint.MatchString(attr(n, "class"))
		if candidate {
			score := textLen(n, false) - textLen(n, true)
			if score > bestScore {
				best, bestScore = n, score
			}
		}
		return true
	})
	if best == nil || bestScore < 200 || bestScore*4 < total {
		return body
	}
	// A wrapper with one big child scores like that child: take the innermost
	// element that still holds most of the text.
	for {
		var inner *html.Node
		for c := best.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && !skipped[c.Data] && (textLen(c, false)-textLen(c, true))*10 >= bestScore*9 {
				inner = c
			}
		}
		if inner == nil {
			return best
		}
		best = inner
	}
}

// textLen counts the readable characters below n; onlyLinks counts those inside anchors.
func textLen(n *html.Node, onlyLinks bool) int {
	count := 0
	var visit func(*html.Node, bool)
	visit = func(n *html.Node, inLink bool) {
		if n.Type == html.ElementNode && skipped[n.Data] {
			return
		}
		if n.Type == html.TextNode && (!onlyLinks || inLink) {
			count += utf8.RuneCountInString(strings.TrimSpace(n.Data))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c, inLink || (n.Type == html.ElementNode && n.Data == "a"))
		}
	}
	visit(n, false)
	return count
}

func links(root *html.Node, base *url.URL) []Link {
	var out []Link
	seen := map[string]bool{}
	walk(root, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return n.Type == html.DocumentNode
		}
		if skipped[n.Data] {
			return false
		}
		if n.Data == "a" {
			if href := resolve(base, attr(n, "href")); href != "" && !seen[href] {
				seen[href] = true
				out = append(out, Link{Text: collapse(textOf(n)), URL: href})
			}
		}
		return true
	})
	return out
}

// resolve makes href absolute; "" for anchors on the same page and scripts.
func resolve(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	lower := strings.ToLower(href)
	if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	return u.String()
}

type writer struct {
	b     strings.Builder
	base  *url.URL
	frame bool // the whole body: leave out nav, header, footer, aside
	lists []listState
	pre   int
}

type listState struct {
	ordered bool
	index   int
}

func (w *writer) last() byte {
	s := w.b.String()
	if s == "" {
		return '\n'
	}
	return s[len(s)-1]
}

func (w *writer) newline() {
	if w.last() != '\n' {
		w.b.WriteByte('\n')
	}
}

func (w *writer) blank() {
	s := w.b.String()
	if s == "" || strings.HasSuffix(s, "\n\n") {
		return
	}
	w.newline()
	w.b.WriteByte('\n')
}

func (w *writer) text(s string) {
	if w.pre > 0 {
		w.b.WriteString(s)
		return
	}
	s = whitespace.ReplaceAllString(s, " ")
	if s == " " || s == "" {
		if c := w.last(); c != ' ' && c != '\n' {
			w.b.WriteByte(' ')
		}
		return
	}
	if c := w.last(); c == ' ' || c == '\n' {
		s = strings.TrimLeft(s, " ")
	}
	w.b.WriteString(s)
}

var anchorMark = map[string]bool{"#": true, "¶": true, "§": true, "🔗": true, "link": true}

var whitespace = regexp.MustCompile(`[\s\x{00a0}]+`)

func (w *writer) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.node(c)
	}
}

// inline renders n's children on their own and returns the result.
func (w *writer) inline(n *html.Node) string {
	sub := &writer{base: w.base, frame: w.frame, pre: w.pre}
	sub.children(n)
	return strings.TrimSpace(sub.b.String())
}

func (w *writer) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.text(n.Data)
		return
	case html.DocumentNode:
		w.children(n)
		return
	case html.ElementNode:
	default:
		return
	}
	tag := n.Data
	if skipped[tag] || (w.frame && chrome[tag]) || hasAttr(n, "hidden") || attr(n, "aria-hidden") == "true" {
		return
	}
	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		text := collapse(w.inline(n))
		if text != "" {
			w.blank()
			level, _ := strconv.Atoi(tag[1:])
			w.b.WriteString(strings.Repeat("#", level) + " " + text)
			w.blank()
		}
	case "p", "section", "article", "main", "figure", "address", "details", "dl", "header", "footer", "nav", "aside":
		w.blank()
		w.children(n)
		w.blank()
	case "div", "figcaption", "summary", "dt", "dd", "center":
		w.newline()
		w.children(n)
		w.newline()
	case "br":
		w.b.WriteByte('\n')
	case "hr":
		w.blank()
		w.b.WriteString("---")
		w.blank()
	case "pre":
		w.blank()
		w.b.WriteString("```\n")
		w.pre++
		w.b.WriteString(strings.Trim(textOf(n), "\n"))
		w.pre--
		w.b.WriteString("\n```")
		w.blank()
	case "code", "kbd", "samp":
		if w.pre > 0 {
			w.children(n)
			return
		}
		if code := collapse(textOf(n)); code != "" {
			w.text("`" + code + "`")
		}
	case "a":
		label := collapse(w.inline(n))
		href := resolve(w.base, attr(n, "href"))
		switch {
		case label == "" || (strings.HasPrefix(strings.TrimSpace(attr(n, "href")), "#") && anchorMark[label]):
			// Heading permalinks (#, ¶) say nothing.
		case href == "":
			w.text(label)
		default:
			w.text("[" + label + "](" + href + ")")
		}
	case "img":
		if alt := collapse(attr(n, "alt")); alt != "" {
			if src := resolve(w.base, attr(n, "src")); src != "" {
				w.text("![" + alt + "](" + src + ")")
			}
		}
	case "strong", "b":
		if text := collapse(w.inline(n)); text != "" {
			w.text("**" + text + "**")
		}
	case "em":
		if text := collapse(w.inline(n)); text != "" {
			w.text("*" + text + "*")
		}
	case "ul", "ol", "menu":
		w.newline()
		w.lists = append(w.lists, listState{ordered: tag == "ol"})
		w.children(n)
		w.lists = w.lists[:len(w.lists)-1]
		if len(w.lists) == 0 {
			w.blank()
		}
	case "li":
		w.newline()
		marker := "- "
		if len(w.lists) > 0 {
			top := &w.lists[len(w.lists)-1]
			top.index++
			if top.ordered {
				marker = strconv.Itoa(top.index) + ". "
			}
		}
		depth := len(w.lists) - 1
		if depth < 0 {
			depth = 0
		}
		w.b.WriteString(strings.Repeat("  ", depth) + marker)
		w.children(n)
		w.newline()
	case "blockquote":
		inner := tidy(w.inline(n))
		if inner != "" {
			w.blank()
			w.b.WriteString("> " + strings.ReplaceAll(inner, "\n", "\n> "))
			w.blank()
		}
	case "table":
		w.table(n)
	default:
		w.children(n)
	}
}

// table writes the rows of a table as a Markdown table; the first row is the head.
func (w *writer) table(n *html.Node) {
	var rows [][]string
	walk(n, func(c *html.Node) bool {
		if c.Type != html.ElementNode {
			return false
		}
		if c != n && c.Data == "table" {
			return false // a nested table stays inside its cell's text
		}
		if c.Data == "tr" {
			var cells []string
			for cell := c.FirstChild; cell != nil; cell = cell.NextSibling {
				if cell.Type == html.ElementNode && (cell.Data == "td" || cell.Data == "th") {
					text := collapse(strings.ReplaceAll(w.inline(cell), "\n", " "))
					cells = append(cells, strings.ReplaceAll(text, "|", `\|`))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return false
		}
		return true
	})
	if len(rows) == 0 {
		return
	}
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	w.blank()
	for i, row := range rows {
		for len(row) < width {
			row = append(row, "")
		}
		w.b.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 {
			w.b.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	w.blank()
}

var blankLines = regexp.MustCompile(`\n{3,}`)

// tidy trims each line's trailing spaces and keeps at most one blank line.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

func collapse(s string) string {
	return strings.TrimSpace(whitespace.ReplaceAllString(s, " "))
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && skipped[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "br" {
			b.WriteByte('\n')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return b.String()
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// walk visits n and, while visit returns true, its descendants.
func walk(n *html.Node, visit func(*html.Node) bool) {
	if !visit(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, visit)
	}
}

func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	var found *html.Node
	walk(n, func(c *html.Node) bool {
		if found != nil {
			return false
		}
		if c.Type == html.ElementNode && match(c) {
			found = c
			return false
		}
		return true
	})
	return found
}
