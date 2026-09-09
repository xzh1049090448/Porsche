package publiccontent

import (
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

func FuzzSanitizeMarkdownNeverAcceptsUnsafeURL(f *testing.F) {
	for _, seed := range []string{
		"javascript:alert(1)", "JaVaScRiPt:alert(1)", "j%61vascript%3Aalert(1)",
		"data:text/html,boom", "//example.test/x", "\\\\example.test\\x",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		_, issues := SanitizeMarkdown("[x](" + rawURL + ")")
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
