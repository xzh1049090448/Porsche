package publiccontent

import (
	"html"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	xhtml "golang.org/x/net/html"
)

var markdownDestinationPattern = regexp.MustCompile(`(?s)!?\[[^\]]*\]\(\s*(.*?)\s*\)`)

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

// SanitizeMarkdown accepts safe Markdown and a deliberately small HTML
// allowlist. It never dereferences a URL; rejected content returns an empty
// result so callers cannot accidentally render unvalidated markup.
func SanitizeMarkdown(raw string) (string, []ValidationIssue) {
	issues := inspectMarkdownDestinations(raw)
	issues = append(issues, inspectHTML(raw)...)
	issues = uniqueIssues(issues)
	if len(issues) != 0 {
		return "", issues
	}
	return raw, nil
}

func inspectMarkdownDestinations(raw string) []ValidationIssue {
	matches := markdownDestinationPattern.FindAllStringSubmatch(raw, -1)
	issues := make([]ValidationIssue, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		destination := match[1]
		isImage := strings.HasPrefix(match[0], "!")
		if isDangerousURL(destination) {
			issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
			continue
		}
		if isImage && !isControlledLocalAsset(destination) {
			issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_remote_image"})
		}
	}
	return issues
}

func inspectHTML(raw string) []ValidationIssue {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(raw))
	issues := make([]ValidationIssue, 0)
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case xhtml.ErrorToken:
			return issues
		case xhtml.CommentToken, xhtml.DoctypeToken:
			issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_html"})
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			name, hasAttributes := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			allowedAttributes, allowed := allowedHTMLTags[tag]
			if !allowed {
				issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_html"})
			}
			if !hasAttributes {
				continue
			}
			for {
				key, value, more := tokenizer.TagAttr()
				attribute := strings.ToLower(string(key))
				if strings.HasPrefix(attribute, "on") || !allowed || !allowedAttributes[attribute] {
					issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_html"})
				}
				if attribute == "href" && isDangerousURL(string(value)) {
					issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
				}
				if attribute == "src" {
					if isDangerousURL(string(value)) {
						issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
					} else if tag == "img" && !isControlledLocalAsset(string(value)) {
						issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_remote_image"})
					}
				}
				if !more {
					break
				}
			}
		}
	}
}

func isDangerousURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "<")
	raw = strings.TrimSuffix(raw, ">")
	value := normalizeURL(raw)
	return strings.HasPrefix(value, "javascript:") ||
		strings.HasPrefix(value, "vbscript:") ||
		strings.HasPrefix(value, "data:") ||
		strings.HasPrefix(value, "//")
}

func isControlledLocalAsset(raw string) bool {
	if isDangerousURL(raw) {
		return false
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "<")
	raw = strings.TrimSuffix(raw, ">")
	parsed, err := url.Parse(normalizeURL(raw))
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/assets/") {
		return false
	}
	return path.Clean(parsed.Path) == parsed.Path && parsed.Path != "/assets/"
}

func normalizeURL(raw string) string {
	value := strings.TrimSpace(raw)
	for i := 0; i < 3; i++ {
		decoded := html.UnescapeString(value)
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
		if unicode.IsSpace(runeValue) || unicode.IsControl(runeValue) {
			continue
		}
		if runeValue == '\\' {
			normalized.WriteByte('/')
			continue
		}
		normalized.WriteRune(unicode.ToLower(runeValue))
	}
	return normalized.String()
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
