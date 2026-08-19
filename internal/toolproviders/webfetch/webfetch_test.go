package webfetch

import (
	"strings"
	"testing"
)

func TestHTMLToTextExtractsWeChatArticleBody(t *testing.T) {
	html := `<html><head><script>window.cgiData = { noisy: "` + strings.Repeat("x", 1024) + `" }</script></head>
<body>
<div id="js_content" class="rich_media_content">
  <section><p>第一段&nbsp;内容</p><div><p>第二段 &amp; 更多</p></div></section>
</div>
<script>window.after = "ignore me"</script>
</body></html>`

	got := HTMLToText("https://mp.weixin.qq.com/s/example", html)
	if !strings.Contains(got, "第一段 内容") || !strings.Contains(got, "第二段 & 更多") {
		t.Fatalf("expected article text, got %q", got)
	}
	if strings.Contains(got, "window.cgiData") || strings.Contains(got, "ignore me") {
		t.Fatalf("expected scripts to be ignored, got %q", got)
	}
}

func TestFetchReadLimitUsesLargerWindowForWeChat(t *testing.T) {
	if got := FetchReadLimit("https://mp.weixin.qq.com/s/example", 10000); got < 1<<20 {
		t.Fatalf("FetchReadLimit for WeChat = %d, want a large bounded window", got)
	}
	// The regular-URL window is floored at minReadWindow: maxLen*3 (30 KB
	// at the default cap) stops inside the <head> of any app-shell page,
	// so extraction ran on markup only and returned nothing.
	if got := FetchReadLimit("https://example.com/post", 10000); got != minReadWindow {
		t.Fatalf("FetchReadLimit for regular URL = %d, want minReadWindow (%d)", got, minReadWindow)
	}
	// A caller asking for more than the floor still gets 3x its cap.
	if got := FetchReadLimit("https://example.com/post", 1<<20); got != 3<<20 {
		t.Fatalf("FetchReadLimit for a large cap = %d, want %d", got, 3<<20)
	}
}

// TestHTMLToTextSurvivesHugeHead is the regression guard for the fetch
// that returned a screenful of blank lines instead of a README: an
// app-shell page whose <head> (preload links, meta tags, inline SVG
// sprite) dwarfs the article body.
func TestHTMLToTextSurvivesHugeHead(t *testing.T) {
	var head strings.Builder
	for i := 0; i < 2000; i++ {
		head.WriteString(`<link rel="preload" href="https://cdn.example/asset-` + strings.Repeat("a", 40) + `.js">` + "\n")
	}
	html := `<!DOCTYPE html><html><head><meta charset="utf-8">` + head.String() +
		`</head><body><svg><path d="` + strings.Repeat("M0 0L1 1", 500) + `"/></svg>` +
		`<article><h1>Anthropic Art</h1><p>一个用于生成插画的 Agent Skill。</p></article>` +
		`<noscript>enable javascript</noscript></body></html>`

	got := HTMLToText("https://github.com/HalfAI1102/anthropic-art", html)
	if !strings.Contains(got, "Anthropic Art") || !strings.Contains(got, "Agent Skill") {
		t.Fatalf("article body not extracted; got %q", firstChars(got, 300))
	}
	if strings.Contains(got, "preload") || strings.Contains(got, "M0 0L1 1") || strings.Contains(got, "enable javascript") {
		t.Errorf("non-content markup leaked into the text: %q", firstChars(got, 300))
	}
	if strings.TrimSpace(got) == "" {
		t.Error("extraction returned blank text")
	}
}

// TestHTMLToTextReportsEmptyExtraction pins the behaviour that keeps a
// failed extraction from being misread as an anti-bot block — the
// misdiagnosis that previously sent a repo lookup into a headless
// anti-detect browser and a filesystem-wide `find`.
func TestHTMLToTextReportsEmptyExtraction(t *testing.T) {
	html := `<!DOCTYPE html><html><head><title>x</title></head><body><script>var a=1</script></body></html>`

	got := HTMLToText("https://github.com/HalfAI1102/anthropic-art", html)
	if got == "" {
		t.Fatal("empty extraction returned a blank string; the model cannot tell this from a real empty page")
	}
	if !strings.Contains(got, "NOT an anti-bot block") {
		t.Errorf("notice should rule out the anti-bot misdiagnosis; got %q", got)
	}
	if !strings.Contains(got, "raw.githubusercontent.com/HalfAI1102/anthropic-art/HEAD/README.md") {
		t.Errorf("notice should point at the raw README for a GitHub URL; got %q", got)
	}

	// Non-HTML payloads stay untouched — an empty .txt is genuinely empty.
	if got := HTMLToText("https://example.com/a.txt", ""); got != "" {
		t.Errorf("non-HTML empty body should stay empty, got %q", got)
	}
}

func TestNormalizeURLRewritesGitHubBlobToRaw(t *testing.T) {
	cases := map[string]string{
		"https://github.com/HalfAI1102/anthropic-art/blob/main/skill/SKILL.md": "https://raw.githubusercontent.com/HalfAI1102/anthropic-art/main/skill/SKILL.md",
		"https://github.com/o/r/blob/v1.2.3/a/b/c.md":                          "https://raw.githubusercontent.com/o/r/v1.2.3/a/b/c.md",
		// Untouched: repo root still renders its README in <body>, and
		// non-blob paths have no unambiguous raw equivalent.
		"https://github.com/HalfAI1102/anthropic-art": "https://github.com/HalfAI1102/anthropic-art",
		"https://github.com/o/r/tree/main/skill":      "https://github.com/o/r/tree/main/skill",
		"https://example.com/blob/main/x.md":          "https://example.com/blob/main/x.md",
		"::not a url::":                               "::not a url::",
	}
	for in, want := range cases {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
