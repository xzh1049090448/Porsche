package publiccontent

import (
	"html"
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode"

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

type markdownSpan struct{ start, end int }

type markdownLink struct {
	isImage     bool
	destination string
	reference   string
}

// SanitizeMarkdown parses the small Markdown surface that can carry a URL,
// then checks raw HTML through a fixed allowlist. Parsing is in-memory only:
// URLs are canonicalized but never requested.
func SanitizeMarkdown(raw string) (string, []ValidationIssue) {
	ignored := markdownCodeSpans(raw)
	references := markdownReferenceDefinitions(raw, ignored)
	links, autolinks := markdownLinks(raw, ignored)
	ignored = append(ignored, autolinks...)

	issues := make([]ValidationIssue, 0)
	for _, link := range links {
		destination := link.destination
		if link.reference != "" {
			destination = references[link.reference]
		}
		if destination == "" {
			continue
		}
		if link.isImage {
			if !isControlledLocalAsset(destination) {
				if isDangerousURL(destination) {
					issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
				}
				issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_remote_image"})
			}
			continue
		}
		if !isAllowedLink(destination) {
			issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_url"})
		}
	}
	issues = append(issues, inspectHTML(maskMarkdownSpans(raw, ignored))...)
	issues = uniqueIssues(issues)
	if len(issues) != 0 {
		return "", issues
	}
	return raw, nil
}

func markdownCodeSpans(raw string) []markdownSpan {
	spans := fencedCodeSpans(raw)
	for i := 0; i < len(raw); {
		if inMarkdownSpan(i, spans) {
			i = spanEnd(i, spans)
			continue
		}
		if raw[i] == '\\' {
			i += minInt(2, len(raw)-i)
			continue
		}
		if raw[i] != '`' {
			i++
			continue
		}
		run := markerRun(raw, i, '`')
		end := findInlineCodeEnd(raw, i+run, run)
		if end < 0 {
			i += run
			continue
		}
		spans = append(spans, markdownSpan{start: i, end: end + run})
		i = end + run
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	return spans
}

func fencedCodeSpans(raw string) []markdownSpan {
	spans := make([]markdownSpan, 0)
	for lineStart := 0; lineStart < len(raw); {
		lineEnd := nextLineEnd(raw, lineStart)
		markerAt, marker, width, ok := fenceAt(raw[lineStart:lineEnd])
		if !ok {
			lineStart = lineEnd
			continue
		}
		_ = markerAt
		end := len(raw)
		for candidate := lineEnd; candidate < len(raw); {
			candidateEnd := nextLineEnd(raw, candidate)
			_, closingMarker, closingWidth, closing := fenceAt(raw[candidate:candidateEnd])
			if closing && closingMarker == marker && closingWidth >= width {
				end = candidateEnd
				break
			}
			candidate = candidateEnd
		}
		spans = append(spans, markdownSpan{start: lineStart, end: end})
		lineStart = end
	}
	return spans
}

func fenceAt(line string) (int, byte, int, bool) {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0, 0, false
	}
	width := markerRun(line, i, line[i])
	return i, line[i], width, width >= 3
}

func markdownReferenceDefinitions(raw string, ignored []markdownSpan) map[string]string {
	references := make(map[string]string)
	for lineStart := 0; lineStart < len(raw); {
		lineEnd := nextLineEnd(raw, lineStart)
		if inMarkdownSpan(lineStart, ignored) {
			lineStart = lineEnd
			continue
		}
		line := raw[lineStart:lineEnd]
		i := 0
		for i < len(line) && i < 3 && line[i] == ' ' {
			i++
		}
		if i < len(line) && line[i] == '[' {
			if closing, ok := markdownLabelEnd(line, i); ok && closing < len(line) && line[closing] == ':' {
				if destination := markdownDestination(line[closing+1:]); destination != "" {
					references[normalizeReference(line[i+1:closing-1])] = destination
				}
			}
		}
		lineStart = lineEnd
	}
	return references
}

func markdownLinks(raw string, ignored []markdownSpan) ([]markdownLink, []markdownSpan) {
	links := make([]markdownLink, 0)
	autolinks := make([]markdownSpan, 0)
	for i := 0; i < len(raw); {
		if inMarkdownSpan(i, ignored) {
			i = spanEnd(i, ignored)
			continue
		}
		if raw[i] == '\\' {
			i += minInt(2, len(raw)-i)
			continue
		}
		if raw[i] == '<' {
			if end := strings.IndexByte(raw[i+1:], '>'); end >= 0 {
				end += i + 1
				candidate := raw[i+1 : end]
				if isAutolinkCandidate(candidate) {
					links = append(links, markdownLink{destination: candidate})
					autolinks = append(autolinks, markdownSpan{start: i, end: end + 1})
					i = end + 1
					continue
				}
			}
		}

		isImage := raw[i] == '!' && i+1 < len(raw) && raw[i+1] == '['
		labelStart := i
		if isImage {
			labelStart++
		}
		if labelStart >= len(raw) || raw[labelStart] != '[' {
			i++
			continue
		}
		labelEnd, ok := markdownLabelEnd(raw, labelStart)
		if !ok {
			i++
			continue
		}
		label := raw[labelStart+1 : labelEnd-1]
		if labelEnd < len(raw) && raw[labelEnd] == '(' {
			if end, content, ok := markdownParenEnd(raw, labelEnd); ok {
				links = append(links, markdownLink{isImage: isImage, destination: markdownDestination(content)})
				i = end
				continue
			}
		}
		if labelEnd < len(raw) && raw[labelEnd] == '[' {
			if referenceEnd, ok := markdownLabelEnd(raw, labelEnd); ok {
				reference := raw[labelEnd+1 : referenceEnd-1]
				if reference == "" {
					reference = label
				}
				links = append(links, markdownLink{isImage: isImage, reference: normalizeReference(reference)})
				i = referenceEnd
				continue
			}
		}
		i = labelEnd
	}
	return links, autolinks
}

func markdownLabelEnd(raw string, start int) (int, bool) {
	depth := 0
	for i := start; i < len(raw); i++ {
		if raw[i] == '\\' {
			i++
			continue
		}
		switch raw[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func markdownParenEnd(raw string, start int) (int, string, bool) {
	depth := 0
	inAngle := false
	for i := start; i < len(raw); i++ {
		if raw[i] == '\\' {
			i++
			continue
		}
		if raw[i] == '<' {
			inAngle = true
		}
		if raw[i] == '>' {
			inAngle = false
		}
		if inAngle {
			continue
		}
		switch raw[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1, raw[start+1 : i], true
			}
		}
	}
	return 0, "", false
}

func markdownDestination(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "<") {
		if end := strings.IndexByte(content, '>'); end >= 0 {
			return content[1:end]
		}
		return content
	}
	for i, runeValue := range content {
		if unicode.IsSpace(runeValue) {
			remaining := strings.TrimSpace(content[i:])
			if strings.HasPrefix(remaining, "\"") || strings.HasPrefix(remaining, "'") || strings.HasPrefix(remaining, "(") {
				return content[:i]
			}
		}
	}
	return content
}

func isAutolinkCandidate(value string) bool {
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return false
	}
	value = canonicalURL(value)
	return strings.HasPrefix(value, "http:") || strings.HasPrefix(value, "https:") || strings.Contains(value, ":") || strings.HasPrefix(value, "//")
}

func isAllowedLink(raw string) bool {
	value := canonicalURL(raw)
	if value == "" || strings.HasPrefix(value, "//") {
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
	return value != "" && (strings.HasPrefix(value, "//") || (hasSchemeLikePrefix(value) && !isAllowedLink(value)))
}

func isControlledLocalAsset(raw string) bool {
	value := canonicalURL(raw)
	if value == "" || hasSchemeLikePrefix(value) || strings.HasPrefix(value, "//") {
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
	value = strings.TrimPrefix(value, "<")
	value = strings.TrimSuffix(value, ">")
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
		if runeValue == '\\' {
			normalized.WriteByte('/')
			continue
		}
		normalized.WriteRune(unicode.ToLower(runeValue))
	}
	return normalized.String()
}

func normalizedRenderedText(raw string) string {
	value := raw
	for {
		decoded := norm.NFKC.String(html.UnescapeString(value))
		if decoded == value {
			return decoded
		}
		value = decoded
	}
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
					issues = append(issues, ValidationIssue{Field: "content", Code: "unsafe_remote_image"})
				}
				hasAttributes = more
			}
		}
	}
}

func maskMarkdownSpans(raw string, spans []markdownSpan) string {
	if len(spans) == 0 {
		return raw
	}
	masked := []byte(raw)
	for _, span := range spans {
		for i := span.start; i < span.end && i < len(masked); i++ {
			masked[i] = ' '
		}
	}
	return string(masked)
}

func inMarkdownSpan(index int, spans []markdownSpan) bool {
	for _, span := range spans {
		if index >= span.start && index < span.end {
			return true
		}
	}
	return false
}

func spanEnd(index int, spans []markdownSpan) int {
	for _, span := range spans {
		if index >= span.start && index < span.end {
			return span.end
		}
	}
	return index + 1
}

func nextLineEnd(raw string, start int) int {
	if end := strings.IndexByte(raw[start:], '\n'); end >= 0 {
		return start + end + 1
	}
	return len(raw)
}

func markerRun(raw string, start int, marker byte) int {
	i := start
	for i < len(raw) && raw[i] == marker {
		i++
	}
	return i - start
}

func findInlineCodeEnd(raw string, start, width int) int {
	for i := start; i < len(raw); {
		if raw[i] == '`' && markerRun(raw, i, '`') == width {
			return i
		}
		i++
	}
	return -1
}

func normalizeReference(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
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
