package publiccontent

import (
	"html"
	"net/url"
	"path"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	xhtml "golang.org/x/net/html"
	"golang.org/x/text/unicode/norm"
)

var allowedHTMLTags = map[string]map[string]bool{
	"a":          {"href": true, "title": true},
	"b":          {},
	"blockquote": {},
	"br":         {},
	"code":       {},
	"em":         {},
	"h1":         {},
	"h2":         {},
	"h3":         {},
	"h4":         {},
	"h5":         {},
	"h6":         {},
	"i":          {},
	"img":        {"src": true, "alt": true, "title": true},
	"li":         {},
	"ol":         {},
	"p":          {},
	"pre":        {},
	"strong":     {},
	"ul":         {},
}

// SanitizeMarkdown parses CommonMark into an AST before inspecting the only
// nodes that can carry destinations or raw HTML. It has no network client and
// never dereferences a submitted URL.
func SanitizeMarkdown(raw string) (string, []ValidationIssue) {
	source, document := parseCommonMark(raw)
	issues := astValidationIssues(document, source)
	issues = uniqueIssues(issues)
	if len(issues) != 0 {
		return "", issues
	}
	return raw, nil
}

func normalizedRenderedText(raw string) string {
	source, document := parseCommonMark(raw)
	return normalizeSemanticText(unescapeMarkdownPunctuation(commonMarkVisibleText(document, source)))
}

func commonMarkVisibleText(document ast.Node, source []byte) string {
	var rendered strings.Builder
	inlineHTML := htmlTextState{}
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.CodeSpan:
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			appendHTMLVisibleText(&rendered, string(typed.Text(source)), &inlineHTML)
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			blockHTML := htmlTextState{}
			appendHTMLVisibleText(&rendered, string(typed.Text(source)), &blockHTML)
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			if inlineHTML.hiddenDepth == 0 {
				rendered.Write(typed.Value(source))
			}
		case *ast.String:
			if inlineHTML.hiddenDepth == 0 {
				rendered.Write(typed.Value)
			}
		case *ast.AutoLink:
			if inlineHTML.hiddenDepth == 0 {
				rendered.Write(typed.Label(source))
			}
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			if inlineHTML.hiddenDepth == 0 {
				rendered.Write(typed.Title)
			}
		case *ast.Image:
			if inlineHTML.hiddenDepth == 0 {
				rendered.Write(typed.Title)
			}
		}
		return ast.WalkContinue, nil
	})
	return rendered.String()
}

type htmlTextState struct {
	openTags    []htmlTextTag
	hiddenDepth int
}

type htmlTextTag struct {
	name   string
	hidden bool
}

var htmlVoidTags = map[string]bool{"br": true, "img": true}

// appendHTMLVisibleText uses the HTML tokenizer rather than matching markup
// text. Only text in allowed non-code elements is part of the rendered claim
// surface; unsafe markup is still reported separately by inspectHTML.
func appendHTMLVisibleText(rendered *strings.Builder, raw string, state *htmlTextState) {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(raw))
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return
		case xhtml.TextToken:
			if state.hiddenDepth == 0 {
				rendered.Write(tokenizer.Text())
			}
		case xhtml.StartTagToken:
			name, hasAttributes := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			appendHTMLVisibleAttributes(rendered, tokenizer, tag, hasAttributes, state.hiddenDepth == 0)
			state.open(tag)
		case xhtml.SelfClosingTagToken:
			name, hasAttributes := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			appendHTMLVisibleAttributes(rendered, tokenizer, tag, hasAttributes, state.hiddenDepth == 0)
		case xhtml.EndTagToken:
			name, _ := tokenizer.TagName()
			state.close(strings.ToLower(string(name)))
		}
	}
}

func appendHTMLVisibleAttributes(rendered *strings.Builder, tokenizer *xhtml.Tokenizer, tag string, hasAttributes, visible bool) {
	allowedAttributes, allowed := allowedHTMLTags[tag]
	for hasAttributes {
		key, value, more := tokenizer.TagAttr()
		attribute := strings.ToLower(string(key))
		if visible && allowed && allowedAttributes[attribute] && (attribute == "title" || (tag == "img" && attribute == "alt")) {
			rendered.Write(value)
		}
		hasAttributes = more
	}
}

func (state *htmlTextState) open(name string) {
	if htmlVoidTags[name] {
		return
	}
	hidden := state.hiddenDepth > 0 || name == "code" || name == "pre"
	if _, allowed := allowedHTMLTags[name]; !allowed {
		hidden = true
	}
	state.openTags = append(state.openTags, htmlTextTag{name: name, hidden: hidden})
	if hidden {
		state.hiddenDepth++
	}
}

func (state *htmlTextState) close(name string) {
	for index := len(state.openTags) - 1; index >= 0; index-- {
		if state.openTags[index].name != name {
			continue
		}
		for _, tag := range state.openTags[index:] {
			if tag.hidden {
				state.hiddenDepth--
			}
		}
		state.openTags = state.openTags[:index]
		return
	}
}

func parseCommonMark(raw string) ([]byte, ast.Node) {
	source := []byte(raw)
	return source, goldmark.New().Parser().Parse(text.NewReader(source))
}

func astValidationIssues(document ast.Node, source []byte) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.CodeSpan:
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			if !isAllowedLink(string(typed.Destination)) {
				issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
			}
		case *ast.Image:
			destination := string(typed.Destination)
			if !isControlledLocalAsset(destination) {
				if isDangerousURL(destination) {
					issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
				}
				issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_remote_image"})
			}
		case *ast.AutoLink:
			if !isAllowedLink(string(typed.URL(source))) {
				issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
			}
		case *ast.Text:
			if unsafeSoftBreakDestination(typed, source) {
				issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
			}
		case *ast.RawHTML:
			issues = append(issues, inspectHTML(string(typed.Text(source)))...)
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			issues = append(issues, inspectHTML(string(typed.Text(source)))...)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return uniqueIssues(issues)
}

// Goldmark follows CommonMark by treating a physical line break in a
// destination as ordinary text. Retain the prior rejection rule only for this
// specific destination-shaped soft-break form, after code and HTML nodes have
// already been excluded by the AST walk. Ordinary prose is never scanned.
func unsafeSoftBreakDestination(node *ast.Text, source []byte) bool {
	if !node.SoftLineBreak() {
		return false
	}
	value := string(node.Value(source))
	opening := strings.LastIndex(value, "](")
	if opening < 0 {
		return false
	}
	destination := value[opening+2:]
	for sibling := node.NextSibling(); sibling != nil; sibling = sibling.NextSibling() {
		textNode, ok := sibling.(*ast.Text)
		if !ok {
			return false
		}
		destination += string(textNode.Value(source))
		if closing := strings.IndexByte(destination, ')'); closing >= 0 {
			return isDangerousURL(destination[:closing])
		}
	}
	return false
}

func isAllowedLink(raw string) bool {
	value := canonicalURL(raw)
	if value == "" || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.Scheme == "" {
		return !hasSchemeLikePrefix(value)
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func isDangerousURL(raw string) bool {
	value := canonicalURL(raw)
	return value != "" && (strings.Contains(value, "\\") || strings.HasPrefix(value, "//") || (hasSchemeLikePrefix(value) && !isAllowedLink(value)))
}

func isControlledLocalAsset(raw string) bool {
	value := canonicalURL(raw)
	if value == "" || hasSchemeLikePrefix(value) || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/assets/") {
		return false
	}
	return path.Clean(parsed.Path) == parsed.Path && parsed.Path != "/assets/"
}

func hasSchemeLikePrefix(value string) bool {
	for _, runeValue := range value {
		switch runeValue {
		case ':':
			return true
		case '/', '?', '#':
			return false
		}
	}
	return false
}

func canonicalURL(raw string) string {
	value := strings.TrimSpace(raw)
	for {
		decoded := norm.NFKC.String(html.UnescapeString(value))
		if unescaped, err := url.PathUnescape(decoded); err == nil {
			decoded = unescaped
		}
		if decoded == value {
			break
		}
		value = decoded
	}
	var normalized strings.Builder
	for _, runeValue := range value {
		if unicode.IsSpace(runeValue) || unicode.IsControl(runeValue) || unicode.Is(unicode.Cf, runeValue) {
			continue
		}
		normalized.WriteRune(unicode.ToLower(runeValue))
	}
	return normalized.String()
}

func normalizeSemanticText(raw string) string {
	value := raw
	for {
		decoded := norm.NFKC.String(html.UnescapeString(value))
		if decoded == value {
			return decoded
		}
		value = decoded
	}
}

func unescapeMarkdownPunctuation(value string) string {
	var unescaped strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) && isMarkdownPunctuation(value[i+1]) {
			i++
		}
		unescaped.WriteByte(value[i])
	}
	return unescaped.String()
}

func isMarkdownPunctuation(value byte) bool {
	return strings.ContainsRune(`!"#$%&'()*+,-./:;<=>?@[\]^_`+"`"+`{|}~`, rune(value))
}

func inspectHTML(raw string) []ValidationIssue {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(raw))
	issues := make([]ValidationIssue, 0)
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return issues
		case xhtml.CommentToken, xhtml.DoctypeToken:
			issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_html"})
		case xhtml.EndTagToken:
			name, _ := tokenizer.TagName()
			if _, allowed := allowedHTMLTags[strings.ToLower(string(name))]; !allowed {
				issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_html"})
			}
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			name, hasAttributes := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			allowedAttributes, allowed := allowedHTMLTags[tag]
			if !allowed {
				issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_html"})
			}
			for hasAttributes {
				key, value, more := tokenizer.TagAttr()
				attribute := strings.ToLower(string(key))
				if strings.HasPrefix(attribute, "on") || !allowed || !allowedAttributes[attribute] {
					issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_html"})
				}
				if attribute == "href" && !isAllowedLink(string(value)) {
					issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
				}
				if attribute == "src" && (tag != "img" || !isControlledLocalAsset(string(value))) {
					if isDangerousURL(string(value)) {
						issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
					}
					issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_remote_image"})
				}
				hasAttributes = more
			}
		}
	}
}

func uniqueIssues(issues []ValidationIssue) []ValidationIssue {
	seen := make(map[ValidationIssue]struct{}, len(issues))
	result := make([]ValidationIssue, 0, len(issues))
	for _, issue := range issues {
		if _, exists := seen[issue]; exists {
			continue
		}
		seen[issue] = struct{}{}
		result = append(result, issue)
	}
	return result
}
