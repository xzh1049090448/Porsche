package dto

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDecodeAnnouncementCreateValidatesUnicodeMarkdownSortAndTime(t *testing.T) {
	valid := `{"expected_revision":1,"title":"维护","body_markdown":"正文","effective_at":null,"is_visible":true,"sort_order":10}`
	got, err := DecodeAnnouncementCreateRequest(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpectedRevision != 1 || got.Title != "维护" || got.BodyMarkdown != "正文" || got.EffectiveAt != nil || !got.IsVisible || got.SortOrder != 10 {
		t.Fatalf("unexpected request: %#v", got)
	}

	for _, count := range []int{1, 120} {
		title := strings.Repeat("界", count)
		raw := fmt.Sprintf(`{"expected_revision":1,"title":%q,"body_markdown":"","effective_at":null,"is_visible":false,"sort_order":0}`, title)
		decoded, decodeErr := DecodeAnnouncementCreateRequest(strings.NewReader(raw))
		if decodeErr != nil || utf8.RuneCountInString(decoded.Title) != count {
			t.Fatalf("title rune count %d: decoded=%#v err=%v", count, decoded, decodeErr)
		}
	}
	tooLong := fmt.Sprintf(`{"expected_revision":1,"title":%q,"body_markdown":"","effective_at":null,"is_visible":false,"sort_order":0}`, strings.Repeat("界", 121))
	if _, err = DecodeAnnouncementCreateRequest(strings.NewReader(tooLong)); err == nil {
		t.Fatal("accepted 121-code-point title")
	}

	for _, size := range []int{0, 1, PublicHomeMarkdownLimit} {
		body := strings.Repeat("x", size)
		raw := fmt.Sprintf(`{"expected_revision":1,"title":"t","body_markdown":%q,"effective_at":null,"is_visible":true,"sort_order":1000000}`, body)
		if _, err = DecodeAnnouncementCreateRequest(strings.NewReader(raw)); err != nil {
			t.Fatalf("body bytes %d: %v", size, err)
		}
	}
	tooLargeMarkdown := fmt.Sprintf(`{"expected_revision":1,"title":"t","body_markdown":%q,"effective_at":null,"is_visible":true,"sort_order":0}`, strings.Repeat("x", PublicHomeMarkdownLimit+1))
	if _, err = DecodeAnnouncementCreateRequest(strings.NewReader(tooLargeMarkdown)); err == nil {
		t.Fatal("accepted markdown over 16 KiB")
	}
	for _, sortOrder := range []int{-1, PublicHomeSortOrderMaximum + 1} {
		raw := fmt.Sprintf(`{"expected_revision":1,"title":"t","body_markdown":"","effective_at":null,"is_visible":true,"sort_order":%d}`, sortOrder)
		if _, err = DecodeAnnouncementCreateRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted sort_order %d", sortOrder)
		}
	}

	canonical := `{"expected_revision":1,"title":"t","body_markdown":"","effective_at":"2026-09-15T00:00:00Z","is_visible":true,"sort_order":0}`
	timed, err := DecodeAnnouncementCreateRequest(strings.NewReader(canonical))
	if err != nil {
		t.Fatal(err)
	}
	wantMillis := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	if timed.EffectiveAt == nil || *timed.EffectiveAt != wantMillis {
		t.Fatalf("effective_at=%v want=%d", timed.EffectiveAt, wantMillis)
	}
	for _, timestamp := range []string{
		"2026-09-15T08:00:00+08:00",
		"2026-09-15T00:00:00.000Z",
		"2026-09-15t00:00:00z",
		"2026-09-15T00:00:00Z ",
	} {
		raw := fmt.Sprintf(`{"expected_revision":1,"title":"t","body_markdown":"","effective_at":%q,"is_visible":true,"sort_order":0}`, timestamp)
		if _, err = DecodeAnnouncementCreateRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted noncanonical timestamp %q", timestamp)
		}
	}
}

func TestDecodeAnnouncementUpdateTracksNullAndRequiresMutation(t *testing.T) {
	cleared, err := DecodeAnnouncementUpdateRequest(strings.NewReader(`{"expected_revision":1,"effective_at":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.EffectiveAt.Set || cleared.EffectiveAt.Value != nil {
		t.Fatalf("explicit null lost: %#v", cleared.EffectiveAt)
	}
	absent, err := DecodeAnnouncementUpdateRequest(strings.NewReader(`{"expected_revision":1,"title":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	if absent.EffectiveAt.Set || absent.Title == nil || *absent.Title != "new" {
		t.Fatalf("field presence wrong: %#v", absent)
	}
	for _, raw := range []string{
		`{"expected_revision":1}`,
		`{"expected_revision":0,"title":"new"}`,
		`{"expected_revision":1,"title":"line\nbreak"}`,
		`{"expected_revision":1,"title":"\u0000"}`,
		`{"expected_revision":1,"title":"   "}`,
	} {
		if _, err = DecodeAnnouncementUpdateRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestDecodeAnnouncementDeleteRequiresPositiveExpectedRevision(t *testing.T) {
	if got, err := DecodeAnnouncementDeleteRequest(strings.NewReader(`{"expected_revision":1}`)); err != nil || got.ExpectedRevision != 1 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	for _, raw := range []string{`{}`, `{"expected_revision":0}`, `{"expected_revision":-1}`, `{"expected_revision":1,"x":1}`} {
		if _, err := DecodeAnnouncementDeleteRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestDecodeFAQValidatesQuestionMarkdownSortAndWrites(t *testing.T) {
	for _, count := range []int{1, 200} {
		question := strings.Repeat("问", count)
		raw := fmt.Sprintf(`{"expected_revision":1,"question":%q,"answer_markdown":%q,"is_visible":true,"sort_order":0}`, question, strings.Repeat("x", PublicHomeMarkdownLimit))
		got, err := DecodeFAQCreateRequest(strings.NewReader(raw))
		if err != nil || utf8.RuneCountInString(got.Question) != count || len(got.AnswerMarkdown) != PublicHomeMarkdownLimit {
			t.Fatalf("count=%d got=%#v err=%v", count, got, err)
		}
	}
	for _, size := range []int{0, 1, PublicHomeMarkdownLimit} {
		raw := fmt.Sprintf(`{"expected_revision":1,"question":"q","answer_markdown":%q,"is_visible":true,"sort_order":0}`, strings.Repeat("x", size))
		if _, err := DecodeFAQCreateRequest(strings.NewReader(raw)); err != nil {
			t.Fatalf("answer bytes %d: %v", size, err)
		}
	}
	for _, raw := range []string{
		fmt.Sprintf(`{"expected_revision":1,"question":%q,"answer_markdown":"","is_visible":true,"sort_order":0}`, strings.Repeat("问", 201)),
		fmt.Sprintf(`{"expected_revision":1,"question":"q","answer_markdown":%q,"is_visible":true,"sort_order":0}`, strings.Repeat("x", PublicHomeMarkdownLimit+1)),
		`{"expected_revision":1,"question":"q\ttab","answer_markdown":"","is_visible":true,"sort_order":0}`,
		`{"expected_revision":1,"question":" ","answer_markdown":"","is_visible":true,"sort_order":0}`,
		`{"expected_revision":1,"question":"q","answer_markdown":"","is_visible":true,"sort_order":-1}`,
		`{"expected_revision":1,"question":"q","answer_markdown":"","is_visible":true,"sort_order":1000001}`,
	} {
		if _, err := DecodeFAQCreateRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if got, err := DecodeFAQCreateRequest(strings.NewReader(`{"expected_revision":1,"question":"q","answer_markdown":"","is_visible":false,"sort_order":1000000}`)); err != nil || got.SortOrder != PublicHomeSortOrderMaximum {
		t.Fatalf("max sort got=%#v err=%v", got, err)
	}
	if _, err := DecodeFAQUpdateRequest(strings.NewReader(`{"expected_revision":1}`)); err == nil {
		t.Fatal("accepted FAQ update with no mutation")
	}
	if got, err := DecodeFAQUpdateRequest(strings.NewReader(`{"expected_revision":1,"answer_markdown":""}`)); err != nil || got.AnswerMarkdown == nil || *got.AnswerMarkdown != "" {
		t.Fatalf("empty answer mutation got=%#v err=%v", got, err)
	}
	if _, err := DecodeFAQDeleteRequest(strings.NewReader(`{"expected_revision":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeFAQDeleteRequest(strings.NewReader(`{"expected_revision":0}`)); err == nil {
		t.Fatal("accepted FAQ delete revision zero")
	}
}

func TestDecodeFeaturedModelsPreservesOrderAndRejectsInvalidOrDuplicateKeys(t *testing.T) {
	got, err := DecodeFeaturedModelsSaveRequest(strings.NewReader(`{"expected_revision":1,"featured_model_keys":["zeta-model","alpha-model"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got.FeaturedModelKeys) != "[zeta-model alpha-model]" {
		t.Fatalf("order changed: %#v", got.FeaturedModelKeys)
	}
	keys := make([]string, PublicHomeFeaturedModelLimit)
	for i := range keys {
		keys[i] = fmt.Sprintf("model-%d", i)
	}
	encoded, _ := json.Marshal(map[string]any{"expected_revision": 1, "featured_model_keys": keys})
	if _, err = DecodeFeaturedModelsSaveRequest(strings.NewReader(string(encoded))); err != nil {
		t.Fatalf("limit rejected: %v", err)
	}
	keys = append(keys, "model-extra")
	encoded, _ = json.Marshal(map[string]any{"expected_revision": 1, "featured_model_keys": keys})
	if _, err = DecodeFeaturedModelsSaveRequest(strings.NewReader(string(encoded))); err == nil {
		t.Fatal("accepted 13 featured keys")
	}
	for _, raw := range []string{
		`{"expected_revision":1,"featured_model_keys":["alpha","alpha"]}`,
		`{"expected_revision":1,"featured_model_keys":[""]}`,
		`{"expected_revision":1,"featured_model_keys":[" alpha"]}`,
		`{"expected_revision":1,"featured_model_keys":["Alpha"]}`,
		`{"expected_revision":1,"featured_model_keys":["model--key"]}`,
		`{"expected_revision":0,"featured_model_keys":[]}`,
	} {
		if _, err = DecodeFeaturedModelsSaveRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestDocumentsDraftSaveRequestIsStrictAndBounded(t *testing.T) {
	valid := `{"expected_revision":1,"about":"a","terms":"t","privacy":"p","legal_reviewed":false}`
	if got, err := DecodeDocumentsDraftSaveRequest(strings.NewReader(valid)); err != nil || got.ExpectedRevision != 1 || got.About != "a" || got.LegalReviewed {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	for _, raw := range []string{
		`{"expected_revision":1,"about":"a","terms":"t","privacy":"p"}`,
		`{"expected_revision":0,"about":"a","terms":"t","privacy":"p","legal_reviewed":false}`,
		`{"expected_revision":1,"about":null,"terms":"t","privacy":"p","legal_reviewed":false}`,
		valid + ` {}`,
		`{"expected_revision":1,"about":"a","terms":"t","privacy":"p","legal_reviewed":false,"home":"x"}`,
	} {
		if _, err := DecodeDocumentsDraftSaveRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestPublicHomeDecodersRejectDuplicateUnknownInvalidTrailingAndOversize(t *testing.T) {
	decoders := []struct {
		name  string
		valid string
		call  func(string) error
	}{
		{"announcement", `{"expected_revision":1,"title":"t","body_markdown":"","effective_at":null,"is_visible":true,"sort_order":0}`, func(raw string) error { _, err := DecodeAnnouncementCreateRequest(strings.NewReader(raw)); return err }},
		{"faq", `{"expected_revision":1,"question":"q","answer_markdown":"","is_visible":true,"sort_order":0}`, func(raw string) error { _, err := DecodeFAQCreateRequest(strings.NewReader(raw)); return err }},
		{"featured", `{"expected_revision":1,"featured_model_keys":[]}`, func(raw string) error { _, err := DecodeFeaturedModelsSaveRequest(strings.NewReader(raw)); return err }},
		{"documents", `{"expected_revision":1,"about":"","terms":"","privacy":"","legal_reviewed":false}`, func(raw string) error { _, err := DecodeDocumentsDraftSaveRequest(strings.NewReader(raw)); return err }},
	}
	for _, test := range decoders {
		t.Run(test.name, func(t *testing.T) {
			mutations := []string{
				`{"expected_revision":1,"expected_revision":1}`,
				`{"expected_revision":1,"unknown":{"nested":1,"nested":2}}`,
				test.valid + ` {}`,
				`{"expected_revision":`,
				`[]`,
			}
			for _, raw := range mutations {
				if err := test.call(raw); err == nil {
					t.Fatalf("accepted %s", raw)
				} else if strings.Contains(err.Error(), "nested") {
					t.Fatalf("error echoed submitted key/content: %v", err)
				}
			}
			oversize := test.valid + strings.Repeat(" ", PublicContentRequestBodyLimit-len(test.valid)+1)
			if err := test.call(oversize); !errors.Is(err, ErrPublicContentRequestTooLarge) {
				t.Fatalf("oversize error=%v", err)
			}
		})
	}
}

func TestPublicHomePreviewRevisionRequiresCanonicalPositiveInt64WhenPresent(t *testing.T) {
	for _, raw := range []string{"", "1", "9223372036854775807"} {
		if _, err := ParsePublicHomePreviewRevision(raw); err != nil {
			t.Fatalf("revision %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"0", "-1", "+1", "01", " 1", "9223372036854775808"} {
		if _, err := ParsePublicHomePreviewRevision(raw); err == nil {
			t.Fatalf("accepted revision %q", raw)
		}
	}
}

func TestPublicHomeEveryWriteRequiresPositiveExpectedRevision(t *testing.T) {
	requests := []struct {
		name string
		call func() error
	}{
		{"announcement create", func() error {
			_, err := DecodeAnnouncementCreateRequest(strings.NewReader(`{"expected_revision":0,"title":"t","body_markdown":"","effective_at":null,"is_visible":true,"sort_order":0}`))
			return err
		}},
		{"announcement update", func() error {
			_, err := DecodeAnnouncementUpdateRequest(strings.NewReader(`{"expected_revision":0,"title":"t"}`))
			return err
		}},
		{"announcement delete", func() error {
			_, err := DecodeAnnouncementDeleteRequest(strings.NewReader(`{"expected_revision":0}`))
			return err
		}},
		{"FAQ create", func() error {
			_, err := DecodeFAQCreateRequest(strings.NewReader(`{"expected_revision":0,"question":"q","answer_markdown":"","is_visible":true,"sort_order":0}`))
			return err
		}},
		{"FAQ update", func() error {
			_, err := DecodeFAQUpdateRequest(strings.NewReader(`{"expected_revision":0,"question":"q"}`))
			return err
		}},
		{"FAQ delete", func() error {
			_, err := DecodeFAQDeleteRequest(strings.NewReader(`{"expected_revision":0}`))
			return err
		}},
		{"featured models", func() error {
			_, err := DecodeFeaturedModelsSaveRequest(strings.NewReader(`{"expected_revision":0,"featured_model_keys":[]}`))
			return err
		}},
		{"documents", func() error {
			_, err := DecodeDocumentsDraftSaveRequest(strings.NewReader(`{"expected_revision":0,"about":"","terms":"","privacy":"","legal_reviewed":false}`))
			return err
		}},
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			if err := request.call(); err == nil {
				t.Fatal("accepted expected_revision zero")
			}
		})
	}
}
