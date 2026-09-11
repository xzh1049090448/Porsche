package openaicompat

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

var functionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return err
	}
	return nil
}

func hasUnknownFields(raw []byte, allowed map[string]struct{}) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return true
		}
	}
	return false
}

func numberInt(value *json.Number, min, max int64) (*int64, bool) {
	if value == nil {
		return nil, true
	}
	n, err := value.Int64()
	if err != nil || n < min || n > max {
		return nil, false
	}
	return &n, true
}

func numberFloat(value *json.Number, min, max float64, includeMin bool) (*float64, bool) {
	if value == nil {
		return nil, true
	}
	n, err := value.Float64()
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n > max || (includeMin && n < min) || (!includeMin && n <= min) {
		return nil, false
	}
	return &n, true
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     *string   `json:"text"`
	ImageURL *mediaURL `json:"image_url"`
	VideoURL *mediaURL `json:"video_url"`
}

type mediaURL struct {
	URL string `json:"url"`
}

func decodeContent(raw json.RawMessage, allowNull, allowMedia bool, maxBytes int) (any, bool) {
	trimmed := bytes.TrimSpace(raw)
	if allowNull && bytes.Equal(trimmed, []byte("null")) {
		return nil, true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, utf8.ValidString(text) && len(text) <= maxBytes
	}
	if !allowMedia {
		return nil, false
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return nil, false
	}
	for _, encoded := range parts {
		var part contentPart
		if decodeStrict(encoded, &part) != nil {
			return nil, false
		}
		switch part.Type {
		case "text":
			if part.Text == nil || part.ImageURL != nil || part.VideoURL != nil || !utf8.ValidString(*part.Text) || len(*part.Text) > maxBytes {
				return nil, false
			}
		case "image_url":
			if part.Text != nil || part.ImageURL == nil || part.VideoURL != nil {
				return nil, false
			}
			if strings.HasPrefix(strings.ToLower(part.ImageURL.URL), "data:") {
				if whitelabel.ValidateDataImage(part.ImageURL.URL) != nil {
					return nil, false
				}
			} else if whitelabel.ValidateMediaURL(part.ImageURL.URL) != nil {
				return nil, false
			}
		case "video_url":
			if part.Text != nil || part.ImageURL != nil || part.VideoURL == nil || whitelabel.ValidateMediaURL(part.VideoURL.URL) != nil {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return json.RawMessage(append([]byte(nil), raw...)), true
}

func validFunctionName(name string) bool { return functionNamePattern.MatchString(name) }

func validJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}
