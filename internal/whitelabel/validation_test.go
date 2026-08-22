package whitelabel

import (
	"encoding/base64"
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
	if got.RequestID != "req_test" {
		t.Fatalf("request ID = %q, want req_test", got.RequestID)
	}
}

func TestValidateRequestEnforcesChatContract(t *testing.T) {
	valid := []byte(`{
		"model":"example",
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}],
		"max_tokens":1,"n":128,"temperature":1,"top_p":1,"frequency_penalty":0,"presence_penalty":0,
		"stop":["a","b"],"tools":[{"type":"function","function":{"name":"lookup"}}],
		"response_format":{"type":"json_object"},"stream_options":{"include_usage":true}
	}`)
	if err := ValidateRequest(valid, GatewayValidation); err != nil {
		t.Fatalf("valid request rejected: %#v", err)
	}

	for _, body := range [][]byte{
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"unknown":true}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"temperature":2}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"n":129}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"stop":["1","2","3","4","5"]}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"tools":[{"type":"function","function":{}}]}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"response_format":{"type":"xml"}}`),
		[]byte(`{"model":"x","messages":[],"max_tokens":1,"response_format":{"type":"text","extra":true}}`),
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
		"https://[::]/x", "https://[ff00::1]/x", "https://[2001:db8::1]/x",
	} {
		requireCode(t, ValidateMediaURL(raw), CodeInvalidRequest)
	}
	if err := ValidateMediaURL("https://cdn.example.com:443/path"); err != nil {
		t.Fatalf("safe https URL rejected: %#v", err)
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

func oversizedPNGDataURI() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxDataImageBytes+1))
}
