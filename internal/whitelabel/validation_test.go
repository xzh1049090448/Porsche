package whitelabel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateRequestRejectsUnsafeMediaAndOversize(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/a.png",
		"https://127.0.0.1/a.png",
		"https://u:p@example.com/a.png",
		"https://example.com:444/a.png",
	} {
		requireCode(t, ValidateMediaURL(raw), CodeInvalidRequest)
	}
	requireCode(t, ValidateDataImage(oversizedPNGDataURI()), CodeInvalidRequest)
}

func TestErrorResponseNeverLeaksUpstream(t *testing.T) {
	got := PublicError(ErrUpstreamUnavailable("vendor.example secret"), "req_test")
	if strings.Contains(got.Error.Message, "vendor") || strings.Contains(got.Error.Message, "secret") {
		t.Fatalf("upstream detail leaked: %#v", got)
	}
	if got.Error.Code != CodeGatewayUpstreamUnavailable || got.Error.Type != TypeAPI || got.Status != 503 {
		t.Fatalf("unexpected public error: %#v", got)
	}
	if got.Error.RequestID != "req_test" {
		t.Fatalf("request ID = %q, want req_test", got.Error.RequestID)
	}
}

func TestPublicInvalidRequestErrorMatchesContract(t *testing.T) {
	raw, err := json.Marshal(PublicError(invalidRequest(CodeInvalidRequest), "req_test"))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	const want = `{"error":{"code":"invalid_request","message":"Invalid request.","type":"invalid_request_error","request_id":"req_test"}}`
	if string(raw) != want {
		t.Fatalf("error JSON = %s, want %s", raw, want)
	}
}

func TestValidateRequestEnforcesChatContract(t *testing.T) {
	valid := []byte(`{
		"model":"example",
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}],
		"max_tokens":1,"n":128,"temperature":1.5,"top_p":1,"frequency_penalty":-1.5,"presence_penalty":1.5,"stream":true,"seed":42,
		"stop":["a","b"],"tools":[{"type":"function","function":{"name":"lookup"}}],
		"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"},"strict":true}},"stream_options":{"include_usage":true}
	}`)
	if err := ValidateRequest(valid, GatewayValidation); err != nil {
		t.Fatalf("valid request rejected: %#v", err)
	}
	if err := ValidateRequest([]byte(`{"model":"x","messages":[],"max_tokens":16385}`), GatewayValidation); err != nil {
		t.Fatalf("large positive max_tokens rejected: %#v", err)
	}
	requireCode(t, ValidateRequest([]byte(`{"model":"x","messages":[],"max_tokens":1,"unknown":true}`), GatewayValidation), Code("unsupported_parameter"))

	for _, body := range [][]byte{
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"stream":"true"}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"seed":1.5}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"temperature":0}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"temperature":2}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"top_p":0}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"frequency_penalty":-2}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"presence_penalty":2}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"n":129}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"stop":["1","2","3","4","5"]}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"tools":[{"type":"function","function":{}}]}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"response_format":{"type":"xml"}}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"response_format":{"type":"text","extra":true}}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"response_format":{"type":"json_schema"}}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":"not-an-object"}}}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"stream_options":{"unexpected":true}}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"stream_options":{"include_usage":"true"}}`),
	} {
		requireCode(t, ValidateRequest(body, GatewayValidation), CodeInvalidRequest)
	}
	requireCode(t, ValidateRequest([]byte(`{"model":"x","messages":[]}`), GatewayValidation), CodeMissingMaxTokens)
	requireCode(t, ValidateRequest([]byte(`{"model":"x","messages":[],"max_tokens":1,"n":2}`), PlatformValidation), CodeInvalidRequest)
}

func TestValidateMediaURLRejectsLocalAndMappedAddresses(t *testing.T) {
	for _, raw := range []string{
		"https://localhost./x", "https://[::1]/x", "https://[::ffff:127.0.0.1]/x",
		"https://LOCALHOST/x", "https:///x", "https://:443/x", "https://example.com:0/x",
		"https://192.0.2.1/x", "https://0.0.0.0/x", "https://10.0.0.1/x", "https://224.0.0.1/x",
		"https://240.0.0.1/x", "https://999.1.1.1/x", "https://[fe80::1]/x", "https://[fc00::1]/x",
		"https://0x7f.1/x", "https://0x7f.0.1/x", "https://0x7f.0x0.0x0.0x1/x",
		"https://0x7f.0.0.1/x", "https://0x7f000001/x",
		"https://[::]/x", "https://[ff00::1]/x", "https://[2001:db8::1]/x",
	} {
		requireCode(t, ValidateMediaURL(raw), CodeInvalidRequest)
	}
	if err := ValidateMediaURL("https://cdn.example.com:443/path"); err != nil {
		t.Fatalf("safe https URL rejected: %#v", err)
	}
}

func TestValidateRequestAcceptsSafeVideoURLAndRejectsUnsafeSources(t *testing.T) {
	valid := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://cdn.example.com/video.mp4"}}]}],"max_tokens":1}`)
	if err := ValidateRequest(valid, GatewayValidation); err != nil {
		t.Fatalf("safe video_url rejected: %#v", err)
	}
	for _, source := range []string{
		"data:video/mp4;base64,AAAA",
		"http://cdn.example.com/video.mp4",
		"https://127.0.0.1/video.mp4",
		"https://u:p@cdn.example.com/video.mp4",
		"https://0x7f000001/video.mp4",
	} {
		body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":` + mustJSON(t, source) + `}}]}],"max_tokens":1}`)
		requireCode(t, ValidateRequest(body, GatewayValidation), CodeInvalidRequest)
	}
}

func TestValidateDataImageRejectsInvalidMimeSVGAndBase64(t *testing.T) {
	for _, raw := range []string{
		"data:image/svg+xml;base64,PHN2Zy8+",
		"data:text/plain;base64,aGVsbG8=",
		"data:image/png;base64,not base64!",
		"data:image/png,AAAA",
	} {
		requireCode(t, ValidateDataImage(raw), CodeInvalidRequest)
	}
	if err := ValidateDataImage("data:image/png;base64,aGVsbG8="); err != nil {
		t.Fatalf("valid PNG data image rejected: %#v", err)
	}
}

func TestValidateRequestAcceptsEightMiBDataImageWithinBodyLimit(t *testing.T) {
	image := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 8*1024*1024))
	body := chatRequestWithImage(t, image)
	if len(body) > MaxRequestBodyBytes {
		t.Fatalf("8 MiB data image request size = %d, want at most %d", len(body), MaxRequestBodyBytes)
	}
	if err := ValidateRequest(body, GatewayValidation); err != nil {
		t.Fatalf("8 MiB data image request rejected: %#v", err)
	}

	overLimit := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 8*1024*1024+1))
	requireCode(t, ValidateRequest(chatRequestWithImage(t, overLimit), GatewayValidation), CodeInvalidRequest)
}

func TestValidateRequestRejectsTooManyOrMalformedTools(t *testing.T) {
	tools := strings.Repeat(`{"type":"function","function":{"name":"lookup"}},`, MaxTools)
	requireCode(t, ValidateRequest([]byte(`{"model":"x","messages":[],"max_tokens":1,"tools":[`+tools+`{"type":"function","function":{"name":"lookup"}}]}`), GatewayValidation), CodeInvalidRequest)
	requireCode(t, ValidateRequest([]byte(`{"model":"x","messages":[],"max_tokens":1,"tools":[{"type":"other","function":{"name":"lookup"}}]}`), GatewayValidation), CodeInvalidRequest)
	requireCode(t, ValidateRequest([]byte(`{"model":"x","messages":[],"max_tokens":1,"tools":[{"type":"function","function":{"name":"lookup","extra":true}}]}`), GatewayValidation), CodeInvalidRequest)
}

func TestValidateRequestLimitsBodyMessagesAndText(t *testing.T) {
	tooLarge := make([]byte, MaxRequestBodyBytes+1)
	requireCode(t, ValidateRequest(tooLarge, GatewayValidation), CodeRequestTooLarge)

	messages := strings.Repeat(`{"role":"user","content":"x"},`, MaxMessages)
	requireCode(t, ValidateRequest([]byte(`{"model":"x","messages":[`+messages+`{"role":"user","content":"x"}],"max_tokens":1}`), GatewayValidation), CodeInvalidRequest)

	text := strings.Repeat("x", MaxTextContentBytes+1)
	requireCode(t, ValidateRequest([]byte(`{"model":"x","messages":[{"role":"user","content":"`+text+`"}],"max_tokens":1}`), GatewayValidation), CodeInvalidRequest)
}

func requireCode(t *testing.T, err *Error, want Code) {
	t.Helper()
	if err == nil || err.Code != want {
		t.Fatalf("code = %#v, want %q", err, want)
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal(%q) error = %v", value, err)
	}
	return string(raw)
}

func oversizedPNGDataURI() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxDataImageBytes+1))
}

func chatRequestWithImage(t *testing.T, image string) []byte {
	t.Helper()
	return []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":` + mustJSON(t, image) + `}}]}],"max_tokens":1}`)
}
