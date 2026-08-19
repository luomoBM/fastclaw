// Package webfetch bundles built-in web_fetch providers. Every provider takes
// the same {url, max_length} arg shape and returns plain text the LLM can
// read directly. Per-call credentials/endpoint come from the
// toolproviders.Request.Config so providers stay stateless.
package webfetch

import (
	"fmt"
	stdhtml "html"
	"net/url"
	"regexp"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// Category is the tool category these providers plug into.
const Category = "web_fetch"

// DefaultMaxLen is the cap when the caller doesn't pass max_length. Mirrors
// the value that the legacy direct-only web_fetch tool used so swapping in
// a chain doesn't change reply truncation behaviour.
const DefaultMaxLen = 10000

// RegisterAll installs every built-in web_fetch provider in r.
func RegisterAll(r *toolproviders.Registry) {
	r.Register(&Direct{})
	r.Register(&Jina{})
	r.Register(&Firecrawl{})
}

type args struct {
	URL    string
	MaxLen int
}

func parseArgs(raw map[string]any) (args, error) {
	var a args
	if s, ok := raw["url"].(string); ok {
		a.URL = strings.TrimSpace(s)
	}
	if a.URL == "" {
		return a, fmt.Errorf("url is required")
	}
	switch v := raw["max_length"].(type) {
	case float64:
		a.MaxLen = int(v)
	case int:
		a.MaxLen = v
	}
	if a.MaxLen <= 0 {
		a.MaxLen = DefaultMaxLen
	}
	return a, nil
}

// truncate caps text at maxLen with a visible marker so the LLM knows the
// page was longer than what it received and can ask for a higher cap (or
// pick a more specific URL) instead of treating the cut as authoritative.
func truncate(text string, maxLen int) string {
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "\n[...truncated]"
}

func isWeChatArticle(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "mp.weixin.qq.com" || strings.HasSuffix(host, ".mp.weixin.qq.com")
}

// minReadWindow is the floor on how much HTML we pull before extracting.
//
// maxLen*3 (30 KB at the default cap) sounds generous until you meet a
// real page: GitHub, and any app-shell site, ships 100 KB+ of <head> —
// preload <link>s, meta tags, inline SVG sprite sheets — before the
// first byte of article text. A 30 KB window stops inside that prelude,
// so extraction ran on pure markup and produced a page of blank lines.
// That empty result got misread as "the site is blocking us", which is
// what sent a repo lookup down an anti-detect-browser detour.
//
// The window is a read cap, not an output cap — truncate() still holds
// the model-facing text to maxLen — so a larger floor costs bounded
// memory and buys correctness on the pages people actually fetch.
const minReadWindow = 512 << 10

func FetchReadLimit(rawURL string, maxLen int) int64 {
	if isWeChatArticle(rawURL) {
		return 2 << 20
	}
	if limit := int64(maxLen) * 3; limit > minReadWindow {
		return limit
	}
	return minReadWindow
}

func HTMLToText(rawURL, htmlBody string) string {
	if isWeChatArticle(rawURL) {
		if article := extractElementByID(htmlBody, "js_content"); article != "" {
			return stripHTML(article)
		}
	}
	text := stripHTML(htmlBody)
	if looksLikeHTML(htmlBody) && text == "" {
		return noTextExtractedNotice(rawURL, len(htmlBody))
	}
	return text
}

// looksLikeHTML keeps the empty-extraction notice off non-HTML payloads:
// fetching an empty .txt or a zero-length JSON body should stay empty
// rather than acquire a paragraph of HTML-extraction advice.
func looksLikeHTML(body string) bool {
	head := body
	if len(head) > 4096 {
		head = head[:4096]
	}
	lower := strings.ToLower(head)
	return strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "<html")
}

// noTextExtractedNotice replaces the silent blank result that a
// markup-only extraction produces.
//
// Returning "" here is actively harmful: a model that asked for a page
// and got nothing back concludes the site blocked it, and the recovery
// it reaches for is a headless anti-detect browser — minutes of work to
// solve a problem that was never anti-bot. Naming the failure mode and
// pointing at the cheap alternative (raw file / API for GitHub, a
// narrower URL otherwise) keeps that misdiagnosis from starting.
func noTextExtractedNotice(rawURL string, htmlLen int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[web_fetch: fetched %d bytes of HTML from %s but extracted no readable text. "+
		"This is an EXTRACTION miss, NOT an anti-bot block — do not switch to a headless browser on account of it.",
		htmlLen, rawURL)
	if owner, repo, ok := parseGitHubRepo(rawURL); ok {
		fmt.Fprintf(&b, " For this GitHub repo, fetch the raw README instead: "+
			"https://raw.githubusercontent.com/%s/%s/HEAD/README.md — "+
			"or list its files via https://api.github.com/repos/%s/%s/contents/.",
			owner, repo, owner, repo)
	} else {
		b.WriteString(" Try a more specific URL (the article/document itself rather than an index or app shell).")
	}
	b.WriteString("]")
	return b.String()
}

// NormalizeURL rewrites URLs whose HTML form is strictly worse than an
// equivalent plain-text form. Currently GitHub blob links: the rendered
// page is an app shell that buries ~4 KB of file content under hundreds
// of KB of chrome, while raw.githubusercontent.com serves exactly the
// bytes the model asked for. Pure and conservative — anything it does
// not recognise is returned untouched.
func NormalizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if strings.ToLower(u.Hostname()) != "github.com" {
		return rawURL
	}
	// /<owner>/<repo>/blob/<ref>/<path...>
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 5 || parts[2] != "blob" {
		return rawURL
	}
	return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/" +
		strings.Join(parts[3:], "/")
}

// parseGitHubRepo pulls owner/repo out of any github.com URL.
func parseGitHubRepo(rawURL string) (owner, repo string, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || strings.ToLower(u.Hostname()) != "github.com" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), true
}

func extractElementByID(htmlBody, id string) string {
	re := regexp.MustCompile(`(?is)<([a-z0-9]+)\b[^>]*\bid\s*=\s*['"]` + regexp.QuoteMeta(id) + `['"][^>]*>`)
	loc := re.FindStringSubmatchIndex(htmlBody)
	if loc == nil {
		return ""
	}
	tagName := strings.ToLower(htmlBody[loc[2]:loc[3]])
	start := loc[0]
	searchFrom := loc[1]
	tagRe := regexp.MustCompile(`(?is)<\s*(/?)\s*` + regexp.QuoteMeta(tagName) + `\b[^>]*>`)
	depth := 1
	for _, m := range tagRe.FindAllStringSubmatchIndex(htmlBody[searchFrom:], -1) {
		tagStart := searchFrom + m[0]
		tagEnd := searchFrom + m[1]
		closing := m[2] >= 0 && htmlBody[searchFrom+m[2]:searchFrom+m[3]] == "/"
		selfClosing := strings.HasSuffix(strings.TrimSpace(htmlBody[tagStart:tagEnd]), "/>")
		if closing {
			depth--
			if depth == 0 {
				return htmlBody[start:tagEnd]
			}
			continue
		}
		if !selfClosing {
			depth++
		}
	}
	return ""
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

var bodyRe = regexp.MustCompile(`(?is)<body\b[^>]*>(.*)</body\s*>`)

// minMainContentBytes is the bar a candidate <main>/<article> must clear
// before we trust it over the full body. Guards against pages that use
// <article> for teaser cards in a sidebar — picking one of those would
// throw away the real content.
const minMainContentBytes = 200

// extractMainContent narrows a document to its semantic content region.
// Tries <main>, then the largest <article>; returns "" when neither is
// present or substantial, leaving the caller on the full body.
//
// Without this, "readable text" still begins with the site's entire nav
// tree. On GitHub that chrome alone overruns the default 10 KB output
// cap, so a repo fetch truncated before reaching the README — text was
// extracted, but not the text anyone asked for.
func extractMainContent(html string) string {
	if main := extractLargestElement(html, "main"); len(main) >= minMainContentBytes {
		return main
	}
	if article := extractLargestElement(html, "article"); len(article) >= minMainContentBytes {
		return article
	}
	return ""
}

// extractLargestElement returns the longest balanced <tag>…</tag> span in
// html, matching nesting depth the way extractElementByID does. Longest
// rather than first: GitHub's README <article> is preceded by smaller
// ones, and blog indexes list many <article> teasers before the body.
func extractLargestElement(html, tag string) string {
	openRe := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>`)
	tagRe := regexp.MustCompile(`(?is)<\s*(/?)\s*` + tag + `\b[^>]*>`)

	var best string
	for _, open := range openRe.FindAllStringIndex(html, -1) {
		// Skip opens already covered by the best span found so far —
		// nested elements can't beat their own parent on length.
		if len(best) > 0 && open[0] < strings.Index(html, best)+len(best) && open[0] >= strings.Index(html, best) {
			continue
		}
		depth := 1
		searchFrom := open[1]
		for _, m := range tagRe.FindAllStringSubmatchIndex(html[searchFrom:], -1) {
			tagEnd := searchFrom + m[1]
			closing := m[2] >= 0 && html[searchFrom+m[2]:searchFrom+m[3]] == "/"
			if closing {
				depth--
				if depth == 0 {
					if span := html[open[0]:tagEnd]; len(span) > len(best) {
						best = span
					}
					break
				}
				continue
			}
			if !strings.HasSuffix(strings.TrimSpace(html[searchFrom+m[0]:tagEnd]), "/>") {
				depth++
			}
		}
	}
	return best
}

var commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// nonContentBlockREs matches element types that contribute markup but no
// prose. script/style were always dropped; the rest were not, and on a
// modern page they are the bulk of the document — an inline SVG sprite
// sheet alone can outweigh the article. Left in place they survived tag
// stripping as thousands of blank lines, which is what a GitHub fetch
// used to return instead of the README.
//
// One pattern per tag rather than a single alternation with a
// backreference: RE2 has no backreferences, so `</\1>` will not compile.
var nonContentBlockREs = func() []*regexp.Regexp {
	tags := []string{"script", "style", "svg", "noscript", "template", "iframe", "canvas", "head"}
	res := make([]*regexp.Regexp, 0, len(tags))
	for _, tag := range tags {
		res = append(res, regexp.MustCompile(`(?is)<`+tag+`\b[^>]*>.*?</\s*`+tag+`\s*>`))
	}
	return res
}()

// selfClosingNoiseRe drops void elements that carry no text. <link> and
// <meta> only ever appear in <head>, but pages with malformed or absent
// <head> markup leak them into the body path.
var selfClosingNoiseRe = regexp.MustCompile(`(?is)<(link|meta|base|source|track)\b[^>]*/?>`)

// blockBoundaryRe marks block-level element edges so the tag strip below
// leaves line breaks rather than fusing every paragraph, list item and
// table row into a single unreadable line.
var blockBoundaryRe = regexp.MustCompile(
	`(?is)<(/?)(p|div|br|li|tr|h[1-6]|section|article|header|footer|blockquote|pre)\b[^>]*>`)

// stripHTML reduces an HTML document to the prose a reader would see:
// prefer the <body> subtree, drop non-content elements wholesale, then
// drop remaining tags and collapse whitespace. Mirrors the helper that
// lived in the agent's web_fetch tool so the Direct provider produces
// identical output.
func stripHTML(html string) string {
	html = commentRe.ReplaceAllString(html, " ")

	// Drop non-content elements first so the region detection below can't
	// be fooled by markup quoted inside a <script> string or an inline
	// SVG's <title>.
	for _, re := range nonContentBlockREs {
		html = re.ReplaceAllString(html, " ")
	}
	html = selfClosingNoiseRe.ReplaceAllString(html, " ")

	// Prefer <body>. Everything discarded here is <head> — hundreds of
	// preload/meta/icon tags on a typical app-shell page — which used to
	// consume the entire read window and the entire output budget before
	// any real text was reached.
	if m := bodyRe.FindStringSubmatch(html); m != nil {
		html = m[1]
	}

	// Narrow further to the semantic content region when the page marks
	// one. Body alone still leads with site chrome — GitHub's nav and
	// menus run past the default 10 KB output cap, so a repo fetch spent
	// its whole budget on "Skip to content / Toggle navigation" and
	// truncated before reaching the README.
	if main := extractMainContent(html); main != "" {
		html = main
	}

	// Keep block boundaries as newlines so lists and paragraphs don't
	// collapse into one run-on line once the tags are gone.
	html = blockBoundaryRe.ReplaceAllString(html, "\n")

	text := htmlTagRe.ReplaceAllString(html, " ")

	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = stdhtml.UnescapeString(text)

	text = spaceRe.ReplaceAllString(text, " ")

	// Drop whitespace-only lines before collapsing blank runs. Stripped
	// markup leaves lines holding a single space, which `\n{3,}` cannot
	// see — that is why a fetched page arrived as screenfuls of
	// apparently-blank output with the real text pushed past the cap.
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		kept = append(kept, strings.TrimSpace(line))
	}
	text = strings.Join(kept, "\n")
	text = nlRe.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}

var (
	spaceRe = regexp.MustCompile(`[ \t]+`)
	nlRe    = regexp.MustCompile(`\n{3,}`)
)
