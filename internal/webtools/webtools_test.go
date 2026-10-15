package webtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const docPage = `<!doctype html><html><head><title>传感器 sensor</title><style>.x{}</style><script>var a=1</script></head>
<body>
<header><a href="/">首页</a><nav><a href="/zh/a.html">侧边栏 A</a><a href="/zh/b.html">侧边栏 B</a></nav></header>
<main><div class="theme-default-content">
<h1 id="s"><a class="header-anchor" href="#s">#</a> 传感器 sensor</h1>
<p>监听<strong>压力</strong>数据，详见 <a href="../guide/x.html">指南</a>。这一段需要足够长才能被当作正文，所以多写一些文字来凑够长度。这一段需要足够长才能被当作正文，所以多写一些文字来凑够长度。这一段需要足够长才能被当作正文，所以多写一些文字来凑够长度。</p>
<h2>参数</h2>
<table><tr><th>参数名</th><th>说明</th></tr><tr><td>callback</td><td>回调 | 函数</td></tr></table>
<ul><li>一</li><li>二<ol><li>二点一</li></ol></li></ul>
<pre><code>sensor.subscribePressure({
  callback() {}
})</code></pre>
<p hidden>隐藏的内容</p>
<p>使用 <code>interval</code> 设置频率。</p>
</div></main>
<footer>版权所有 © 小米</footer>
</body></html>`

func TestMarkdownKeepsTheContentAndDropsTheFrame(t *testing.T) {
	base, _ := url.Parse("https://iot.mi.com/vela/quickapp/zh/features/system/sensor.html")
	doc, err := ToMarkdown(strings.NewReader(docPage), base)
	if err != nil {
		t.Fatal(err)
	}
	md := doc.Markdown
	for _, want := range []string{
		"# 传感器 sensor",
		"监听**压力**数据，详见 [指南](https://iot.mi.com/vela/quickapp/zh/features/guide/x.html)。",
		"## 参数",
		"| 参数名 | 说明 |\n| --- | --- |\n| callback | 回调 \\| 函数 |",
		"- 一\n- 二\n  1. 二点一",
		"```\nsensor.subscribePressure({\n  callback() {}\n})\n```",
		"使用 `interval` 设置频率。",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	for _, unwanted := range []string{"侧边栏", "版权所有", "隐藏的内容", "var a", "# #"} {
		if strings.Contains(md, unwanted) {
			t.Errorf("markdown keeps %q:\n%s", unwanted, md)
		}
	}
	if doc.Title != "传感器 sensor" {
		t.Errorf("title %q", doc.Title)
	}
	// Links still list the whole page, sidebar included.
	var urls []string
	for _, l := range doc.Links {
		urls = append(urls, l.URL)
	}
	if !strings.Contains(strings.Join(urls, " "), "https://iot.mi.com/zh/a.html") {
		t.Errorf("links %v", urls)
	}
}

func TestFetchDecodesGBKAndKeepsPagesForAWhile(t *testing.T) {
	var hits int32
	gbk, _ := simplifiedchinese.GBK.NewEncoder().String(`<html><head><title>天气</title></head><body><p>今天晴，最高温度二十六度。</p></body></html>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/gbk", http.StatusFound)
		case "/gbk":
			w.Header().Set("Content-Type", "text/html; charset=gbk")
			fmt.Fprint(w, gbk)
		case "/logo.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("\x89PNG\r\n\x1a\n"))
		case "/data":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"a":1}`)
		}
	}))
	defer srv.Close()
	c, _ := New("", "")
	ctx := context.Background()
	page, err := c.Fetch(ctx, srv.URL+"/old", false)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "天气" || !strings.Contains(page.Text, "今天晴") || page.FinalURL != srv.URL+"/gbk" {
		t.Fatalf("page %+v", page)
	}
	again, _ := c.Fetch(ctx, srv.URL+"/old", false)
	if !again.Cached || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("second read should come from the cache, hits=%d", hits)
	}
	if fresh, _ := c.Fetch(ctx, srv.URL+"/old", true); fresh.Cached {
		t.Fatal("refresh should fetch again")
	}
	if img, _ := c.Fetch(ctx, srv.URL+"/logo.png", false); !img.Binary {
		t.Fatal("an image is not text")
	}
	if data, _ := c.Fetch(ctx, srv.URL+"/data", false); data.Text != "{\n  \"a\": 1\n}" {
		t.Fatalf("json %q", data.Text)
	}
	if _, err := c.Fetch(ctx, "ftp://example.com/a", false); err == nil {
		t.Fatal("only http and https")
	}
}

func TestSearchAsksSearXNGThenBing(t *testing.T) {
	target := "https://iot.mi.com/vela/quickapp/zh/features/system/sensor.html"
	tracked := "https://www.bing.com/ck/a?!&&p=x&u=a1" + base64.RawURLEncoding.EncodeToString([]byte(target)) + "&ntb=1"
	var searxngUp atomic.Bool
	searxngUp.Store(true)
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			if !searxngUp.Load() {
				http.Error(w, "down", 500)
				return
			}
			asked = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"results":[{"title":" 传感器 ","url":"https://a.example/1","content":"压力 数据","engines":["bing","360search"]},
				{"title":"重复","url":"https://a.example/1/","content":""},{"title":"二","url":"https://b.example/2","content":"x"}]}`)
		case "/bing":
			fmt.Fprintf(w, `<html><body><ol id="b_results">
				<li class="b_algo"><h2><a href="%s">传感器 sensor | Xiaomi Vela</a></h2><div class="b_caption"><p>监听压力数据</p></div></li>
				<li class="b_ad"><h2><a href="https://ad.example">广告</a></h2></li>
				</ol></body></html>`, tracked)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	c.Bing = srv.URL + "/bing"
	res, err := c.Search(context.Background(), Query{Text: "传感器", Site: "iot.mi.com", TimeRange: "month", MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Source, "SearXNG") || len(res.Results) != 2 || res.Results[0].Engine != "bing,360search" {
		t.Fatalf("searxng results %+v", res)
	}
	if q, _ := url.ParseQuery(asked); q.Get("q") != "site:iot.mi.com 传感器" || q.Get("time_range") != "month" || q.Get("format") != "json" {
		t.Fatalf("searxng asked %s", asked)
	}
	searxngUp.Store(false)
	res, err = c.Search(context.Background(), Query{Text: "传感器"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "Bing" || len(res.Results) != 1 || res.Results[0].URL != target || res.Results[0].Snippet != "监听压力数据" {
		t.Fatalf("bing results %+v", res)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "SearXNG") {
		t.Fatalf("notes %v", res.Notes)
	}
	if _, err := c.Search(context.Background(), Query{Text: "x", TimeRange: "decade"}); err == nil {
		t.Fatal("bad time range")
	}
}

func TestBochaComesFirstAndFallsBackWhenItFails(t *testing.T) {
	var bochaFails atomic.Bool
	var sent map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/web-search":
			auth = r.Header.Get("Authorization")
			json.NewDecoder(r.Body).Decode(&sent)
			if bochaFails.Load() {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"code":403,"msg":"余额不足"}`)
				return
			}
			fmt.Fprint(w, `{"code":200,"data":{"webPages":{"value":[
				{"name":"传感器 sensor","url":"https://iot.mi.com/s.html","snippet":"短","summary":"`+strings.Repeat("长", 400)+`","siteName":"小米","datePublished":"2024-08-20T16:40:18+08:00"}]}}}`)
		case "/search":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"results":[{"title":"备用","url":"https://b.example/","content":"x"}]}`)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	c.BochaKey, c.BochaURL = "sk-test", srv.URL+"/v1/web-search"
	res, err := c.Search(context.Background(), Query{Text: "传感器", Site: "iot.mi.com", TimeRange: "week", MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test" || sent["include"] != "iot.mi.com" || sent["freshness"] != "oneWeek" || sent["count"] != float64(5) {
		t.Fatalf("bocha request %s %v", auth, sent)
	}
	hit := res.Results[0]
	if res.Source != "博查 Bocha" || hit.Engine != "bocha · 小米" || len([]rune(hit.Snippet)) != maxSnippet+1 {
		t.Fatalf("bocha results %+v", res)
	}
	bochaFails.Store(true)
	res, err = c.Search(context.Background(), Query{Text: "传感器"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Source, "SearXNG") || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "余额不足") {
		t.Fatalf("fallback %+v", res)
	}
}
