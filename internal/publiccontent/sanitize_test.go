package publiccontent

import (
	"net/url"
	"strings"
	"testing"
)

func TestSanitizeMarkdownPreservesSafeTextMarkdownAndControlledAssets(t *testing.T) {
	raw := "# Hello\n\n[Terms](/terms)\n\n![Logo](/assets/logo.svg)\n\n<strong>Safe</strong>"
	got, issues := SanitizeMarkdown(raw)
	if len(issues) != 0 {
		t.Fatalf("SanitizeMarkdown() issues = %#v", issues)
	}
	if got != raw {
		t.Fatalf("SanitizeMarkdown() = %q, want %q", got, raw)
	}
}

func TestSanitizeMarkdownAllowsSchemeNamesInOrdinaryProse(t *testing.T) {
	for _, raw := range []string{
		"The javascript: URL scheme is not permitted in links.",
		"A data: URL is not an allowed image source.",
	} {
		t.Run(raw, func(t *testing.T) {
			got, issues := SanitizeMarkdown(raw)
			if len(issues) != 0 || got != raw {
				t.Fatalf("SanitizeMarkdown(%q) = (%q, %#v), want original prose without issues", raw, got, issues)
			}
		})
	}
}

func TestSanitizeMarkdownRejectsExecutableMarkupAndUnsafeURLs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		code string
	}{
		{name: "script", raw: "<ScRiPt>alert(1)</sCrIpT>", code: "unsafe_html"},
		{name: "event", raw: "<p OnClIcK=\"alert(1)\">click</p>", code: "unsafe_html"},
		{name: "iframe", raw: "<iframe src=\"/assets/a\"></iframe>", code: "unsafe_html"},
		{name: "embed", raw: "<embed src=\"/assets/a\">", code: "unsafe_html"},
		{name: "javascript", raw: "[x](javascript:alert(1))", code: "unsafe_url"},
		{name: "angle_javascript", raw: "[x](<javascript:alert(1)>)", code: "unsafe_url"},
		{name: "data", raw: "[x](data:text/html;base64,WA==)", code: "unsafe_url"},
		{name: "protocol_relative", raw: "[x](//evil.example/path)", code: "unsafe_url"},
		{name: "encoded_javascript", raw: "[x](j%61v%61script%3Aalert(1))", code: "unsafe_url"},
		{name: "html_encoded_javascript", raw: "[x](jav&#x61;script&#58;alert(1))", code: "unsafe_url"},
		{name: "whitespace_javascript", raw: "[x](java\nscript:alert(1))", code: "unsafe_url"},
		{name: "backslash_protocol_relative", raw: "![x](\\\\169.254.169.254/latest/meta-data)", code: "unsafe_url"},
		{name: "remote_image", raw: "![x](https://169.254.169.254/latest/meta-data)", code: "unsafe_remote_image"},
		{name: "html_remote_image", raw: "<img src=\"https://example.test/logo.svg\">", code: "unsafe_remote_image"},
		{name: "html_javascript_image", raw: "<img src=\"javascript:alert(1)\">", code: "unsafe_url"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, issues := SanitizeMarkdown(test.raw)
			if !hasIssueCode(issues, test.code) {
				t.Fatalf("SanitizeMarkdown(%q) issues = %#v, missing %q", test.raw, issues, test.code)
			}
		})
	}
}

func TestSanitizeMarkdownParsesNestedAndReferenceImages(t *testing.T) {
	tests := []string{
		"![x[y]](https://169.254.169.254/latest/meta-data)",
		"![logo][metadata]\n\n[metadata]: https://169.254.169.254/latest/meta-data",
		"![logo][]\n\n[logo]: https://169.254.169.254/latest/meta-data",
		"![remote]\n\n[remote]: https://169.254.169.254/latest/meta-data",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			_, issues := SanitizeMarkdown(raw)
			if !hasIssueCode(issues, "unsafe_remote_image") {
				t.Fatalf("SanitizeMarkdown(%q) issues = %#v, missing unsafe_remote_image", raw, issues)
			}
		})
	}
}

func TestSanitizeMarkdownUsesCommonMarkShortcutAndFirstReferenceDefinition(t *testing.T) {
	for _, test := range []struct {
		name   string
		raw    string
		unsafe bool
		issue  string
	}{
		{
			name:   "shortcut_link",
			raw:    "[click]\n\n[click]: javascript:alert(1)",
			unsafe: true,
			issue:  "unsafe_url",
		},
		{
			name:  "link_first_definition_wins",
			raw:   "[click]\n\n[click]: https://example.test/first\n[click]: javascript:alert(1)",
			issue: "unsafe_url",
		},
		{
			name:   "image_first_definition_wins",
			raw:    "![logo][asset]\n\n[asset]: /assets/logo.svg\n[asset]: https://169.254.169.254/latest/meta-data",
			unsafe: false,
			issue:  "unsafe_remote_image",
		},
		{
			name:   "image_first_definition_remains_unsafe",
			raw:    "![logo][asset]\n\n[asset]: https://169.254.169.254/latest/meta-data\n[asset]: /assets/logo.svg",
			unsafe: true,
			issue:  "unsafe_remote_image",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, issues := SanitizeMarkdown(test.raw)
			if hasIssueCode(issues, test.issue) != test.unsafe {
				t.Fatalf("SanitizeMarkdown(%q) issues = %#v, unsafe=%v", test.raw, issues, test.unsafe)
			}
		})
	}
}

func TestSanitizeMarkdownUsesCommonMarkCodeAndAngleDestinationRules(t *testing.T) {
	for _, test := range []struct {
		name   string
		raw    string
		unsafe bool
	}{
		{name: "controlled_angle_destination", raw: "![logo](</assets/logo.svg>)", unsafe: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, issues := SanitizeMarkdown(test.raw)
			if hasIssueCode(issues, "unsafe_remote_image") != test.unsafe {
				t.Fatalf("SanitizeMarkdown(%q) issues = %#v", test.raw, issues)
			}
		})
	}
}

func TestSanitizeMarkdownUnescapesCommonMarkURLPunctuationWithoutBackslashPathCoercion(t *testing.T) {
	_, issues := SanitizeMarkdown("[x](javascript\\:alert(1))")
	if !hasIssueCode(issues, "unsafe_url") {
		t.Fatalf("escaped javascript URL issues = %#v, missing unsafe_url", issues)
	}
}

func TestSanitizeMarkdownDoesNotMaskHTMLAfterInvalidBacktickFence(t *testing.T) {
	raw := "``` markdown `\n<script>alert(1)</script>"
	_, issues := SanitizeMarkdown(raw)
	if !hasIssueCode(issues, "unsafe_html") {
		t.Fatalf("invalid backtick fence masked HTML: %#v", issues)
	}
}

func TestSanitizeMarkdownRejectsStandaloneDangerousEndTags(t *testing.T) {
	for _, raw := range []string{"</script>", "</iframe>"} {
		t.Run(raw, func(t *testing.T) {
			_, issues := SanitizeMarkdown(raw)
			if !hasIssueCode(issues, "unsafe_html") {
				t.Fatalf("SanitizeMarkdown(%q) issues = %#v, missing unsafe_html", raw, issues)
			}
		})
	}
}

func TestSanitizeMarkdownRejectsNonHTTPSchemesAndDeepOrUnicodeSchemeBypasses(t *testing.T) {
	for _, raw := range []string{
		"[x](file:///etc/passwd)",
		"[x](blob:https://example.test/id)",
		"[x](ftp://example.test/a)",
		"[x](//example.test/a)",
		"[x](j%25252561vascript%2525253Aalert(1))",
		"[x](ｊａｖａｓｃｒｉｐｔ：alert(1))",
		"[x](jаvascript:alert(1))",
	} {
		t.Run(raw, func(t *testing.T) {
			_, issues := SanitizeMarkdown(raw)
			if !hasIssueCode(issues, "unsafe_url") {
				t.Fatalf("SanitizeMarkdown(%q) issues = %#v, missing unsafe_url", raw, issues)
			}
		})
	}
	for _, raw := range []string{
		"![x](file:///etc/passwd)",
		"![x](blob:https://example.test/id)",
		"![x](//169.254.169.254/latest/meta-data)",
	} {
		t.Run(raw, func(t *testing.T) {
			_, issues := SanitizeMarkdown(raw)
			if !hasIssueCode(issues, "unsafe_remote_image") {
				t.Fatalf("SanitizeMarkdown(%q) issues = %#v, missing unsafe_remote_image", raw, issues)
			}
		})
	}
}

func TestSanitizeMarkdownDoesNotTreatCodeOrEscapedSyntaxAsContent(t *testing.T) {
	for _, raw := range []string{
		"`![x](https://169.254.169.254/latest/meta-data)`",
		"\\![x](https://169.254.169.254/latest/meta-data)",
		"```markdown\n![x](https://169.254.169.254/latest/meta-data)\n```",
		"<https://example.test/docs>",
	} {
		t.Run(raw, func(t *testing.T) {
			got, issues := SanitizeMarkdown(raw)
			if len(issues) != 0 || got != raw {
				t.Fatalf("SanitizeMarkdown(%q) = (%q, %#v), want original content with no issues", raw, got, issues)
			}
		})
	}
}

func FuzzSanitizeMarkdownNeverAcceptsUnsafeURL(f *testing.F) {
	for _, seed := range []string{
		"javascript:alert(1)", "JaVaScRiPt:alert(1)", "j%61vascript%3Aalert(1)",
		"j%25252561vascript%2525253Aalert(1)", "data:text/html,boom", "//example.test/x", "\\\\example.test\\x",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		wrappedURL := url.PathEscape(rawURL)
		_, issues := SanitizeMarkdown("[x](<" + wrappedURL + ">)")
		if isDangerousURL(rawURL) && len(issues) == 0 {
			t.Fatalf("unsafe URL accepted: %q", rawURL)
		}
		for _, issue := range issues {
			if strings.Contains(issue.Field, "http") {
				t.Fatalf("issue leaked URL in field: %#v", issue)
			}
		}
	})
}

func hasIssueCode(issues []ValidationIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
