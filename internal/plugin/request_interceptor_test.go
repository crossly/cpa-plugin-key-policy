package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"cpa-key-policy/internal/policy"
)

func TestQuotaRequestInterceptorReturnsDynamicDailyOpenAIError(t *testing.T) {
	app, plain, now := configureQuotaResponseTestApp(t, 10, 50)
	app.Store().SetClock(func() time.Time { return now })
	app.Store().RecordUsage("quota-key", "fast", "gpt-5-codex", false, policy.UsageDetail{
		InputTokens: 10_000_000,
		TotalTokens: 10_000_000,
	})

	hdr := http.Header{"Authorization": {"Bearer " + plain}}
	auth := frontendAuthForTest(t, app, "/v1/chat/completions", hdr)
	if !auth.Authenticated || auth.Principal != "quota-key" {
		t.Fatalf("quota-blocked OpenAI auth = %+v, want authenticated for interceptor", auth)
	}

	intercepted := quotaInterceptForTest(t, app, hdr)
	if !intercepted.Terminate || intercepted.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("intercepted response = %+v, want terminating 429", intercepted)
	}
	if got := intercepted.ResponseHeaders.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := intercepted.ResponseHeaders.Get("Retry-After"); got != "50400" {
		t.Fatalf("Retry-After = %q, want 50400", got)
	}

	var body struct {
		Error struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    string  `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(intercepted.ResponseBody, &body); err != nil {
		t.Fatalf("quota response body: %v; body=%s", err, intercepted.ResponseBody)
	}
	if body.Error.Message != "Daily quota exceeded. Usage: $10.00 / $10.00 USD. Resets at 2026-09-03T00:00:00Z." {
		t.Fatalf("message = %q", body.Error.Message)
	}
	if body.Error.Type != "insufficient_quota" || body.Error.Code != "daily_quota_exceeded" || body.Error.Param != nil {
		t.Fatalf("error fields = %+v, want insufficient_quota/daily_quota_exceeded/null", body.Error)
	}
}

func TestQuotaRequestInterceptorReturnsDynamicWeeklyOpenAIError(t *testing.T) {
	app, plain, now := configureQuotaResponseTestApp(t, 0, 50)
	app.Store().SetClock(func() time.Time { return now })
	app.Store().RecordUsage("quota-key", "fast", "gpt-5-codex", false, policy.UsageDetail{
		InputTokens: 50_000_000,
		TotalTokens: 50_000_000,
	})

	hdr := http.Header{"Authorization": {"Bearer " + plain}}
	intercepted := quotaInterceptForTest(t, app, hdr)
	if !intercepted.Terminate || intercepted.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("intercepted response = %+v, want terminating 429", intercepted)
	}
	if got := intercepted.ResponseHeaders.Get("Retry-After"); got != "604800" {
		t.Fatalf("Retry-After = %q, want 604800", got)
	}

	var body openAIRequestError
	if err := json.Unmarshal(intercepted.ResponseBody, &body); err != nil {
		t.Fatalf("quota response body: %v; body=%s", err, intercepted.ResponseBody)
	}
	if body.Error.Message != "Weekly quota exceeded. Usage: $50.00 / $50.00 USD. Resets at 2026-09-09T10:00:00Z." {
		t.Fatalf("message = %q", body.Error.Message)
	}
	if body.Error.Type != "insufficient_quota" || body.Error.Code != "weekly_quota_exceeded" || body.Error.Param != nil {
		t.Fatalf("error fields = %+v, want insufficient_quota/weekly_quota_exceeded/null", body.Error)
	}
}

func TestRequestInterceptorReturnsDynamicRPMError(t *testing.T) {
	app, plain, now := configureQuotaResponseTestApp(t, 0, 0, 1)
	app.Store().SetClock(func() time.Time { return now })
	hdr := http.Header{"Authorization": {"Bearer " + plain}}

	first := frontendAuthForTest(t, app, "/v1/chat/completions", hdr)
	if !first.Authenticated {
		t.Fatalf("first RPM request auth = %+v, want allowed", first)
	}
	second := frontendAuthForTest(t, app, "/v1/chat/completions", hdr)
	if !second.Authenticated {
		t.Fatalf("RPM-limited auth = %+v, want authenticated for interceptor", second)
	}

	intercepted := quotaInterceptForTest(t, app, hdr)
	if !intercepted.Terminate || intercepted.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("intercepted response = %+v, want terminating 429", intercepted)
	}
	if got := intercepted.ResponseHeaders.Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
	var body openAIRequestError
	if err := json.Unmarshal(intercepted.ResponseBody, &body); err != nil {
		t.Fatalf("RPM response body: %v; body=%s", err, intercepted.ResponseBody)
	}
	if body.Error.Type != "rate_limit_error" || body.Error.Code != "rate_limit_exceeded" || body.Error.Param != nil {
		t.Fatalf("error fields = %+v, want rate_limit_error/rate_limit_exceeded/null", body.Error)
	}
	if body.Error.Message != "Requests-per-minute limit exceeded for this API key (limit: 1 requests/minute). Please retry later." {
		t.Fatalf("message = %q", body.Error.Message)
	}
}

func TestRequestInterceptorUsesNativeNonOpenAIErrorShapes(t *testing.T) {
	app, plain, now := configureQuotaResponseTestApp(t, 10, 50)
	app.Store().SetClock(func() time.Time { return now })
	app.Store().RecordUsage("quota-key", "fast", "gpt-5-codex", false, policy.UsageDetail{
		InputTokens: 10_000_000,
		TotalTokens: 10_000_000,
	})
	hdr := http.Header{"Authorization": {"Bearer " + plain}}

	anthropic := policyInterceptForTest(t, app, hdr, "claude")
	var anthropicBody anthropicRequestError
	if err := json.Unmarshal(anthropic.ResponseBody, &anthropicBody); err != nil {
		t.Fatalf("Anthropic response body: %v; body=%s", err, anthropic.ResponseBody)
	}
	if !anthropic.Terminate || anthropic.StatusCode != http.StatusTooManyRequests || anthropicBody.Type != "error" || anthropicBody.Error.Type != "rate_limit_error" {
		t.Fatalf("Anthropic response = %+v body=%+v, want terminating native 429 error", anthropic, anthropicBody)
	}

	google := policyInterceptForTest(t, app, hdr, "gemini")
	var googleBody googleRequestError
	if err := json.Unmarshal(google.ResponseBody, &googleBody); err != nil {
		t.Fatalf("Google response body: %v; body=%s", err, google.ResponseBody)
	}
	if !google.Terminate || google.StatusCode != http.StatusTooManyRequests || googleBody.Error.Code != http.StatusTooManyRequests || googleBody.Error.Status != "RESOURCE_EXHAUSTED" {
		t.Fatalf("Google response = %+v body=%+v, want terminating native 429 error", google, googleBody)
	}
}

func TestPolicyFrontendAuthAllowsExecutableNonOpenAIPaths(t *testing.T) {
	app, plain, now := configureQuotaResponseTestApp(t, 10, 50)
	app.Store().SetClock(func() time.Time { return now })
	app.Store().RecordUsage("quota-key", "fast", "gpt-5-codex", false, policy.UsageDetail{
		InputTokens: 10_000_000,
		TotalTokens: 10_000_000,
	})
	hdr := http.Header{"Authorization": {"Bearer " + plain}}
	for _, path := range []string{"/v1/messages", "/v1beta/models/gemini-2.5-pro:generateContent"} {
		auth := frontendAuthForTest(t, app, path, hdr)
		if !auth.Authenticated {
			t.Fatalf("quota auth for executable path %q = %+v, want authenticated for interceptor", path, auth)
		}
	}
}

func TestPolicyFrontendAuthFailsClosedForNonExecutablePaths(t *testing.T) {
	app, plain, now := configureQuotaResponseTestApp(t, 10, 50)
	app.Store().SetClock(func() time.Time { return now })
	app.Store().RecordUsage("quota-key", "fast", "gpt-5-codex", false, policy.UsageDetail{
		InputTokens: 10_000_000,
		TotalTokens: 10_000_000,
	})
	hdr := http.Header{"Authorization": {"Bearer " + plain}}
	for _, path := range []string{"/v1/models", "/v1/usage"} {
		auth := frontendAuthForTest(t, app, path, hdr)
		if auth.Authenticated {
			t.Fatalf("quota auth for non-executable path %q = %+v, want rejected", path, auth)
		}
	}
}

func TestQuotaRequestInterceptorDoesNotTerminateBelowLimit(t *testing.T) {
	app, plain, now := configureQuotaResponseTestApp(t, 10, 50)
	app.Store().SetClock(func() time.Time { return now })
	app.Store().RecordUsage("quota-key", "fast", "gpt-5-codex", false, policy.UsageDetail{
		InputTokens: 5_000_000,
		TotalTokens: 5_000_000,
	})

	intercepted := quotaInterceptForTest(t, app, http.Header{"Authorization": {"Bearer " + plain}})
	if intercepted.Terminate || intercepted.StatusCode != 0 || len(intercepted.ResponseBody) != 0 {
		t.Fatalf("below-limit response = %+v, want no termination", intercepted)
	}
}

func TestRegistrationDisablesQuotaInterceptorForSchemaOneHost(t *testing.T) {
	app := NewApp()
	yaml := []byte(fmt.Sprintf("enabled: true\nstate_file: %q\nkeys: []\n", filepath.ToSlash(filepath.Join(t.TempDir(), "state.json"))))

	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml, SchemaVersion: 1})
	raw, err := app.HandleMethod(MethodPluginReconfigure, req)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var registration Registration
	if err := json.Unmarshal(env.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if registration.SchemaVersion != 1 || registration.Capabilities.RequestInterceptor {
		t.Fatalf("schema-one registration = %+v, want schema=1 and no request interceptor", registration)
	}
}

func TestRegistrationEnablesQuotaInterceptorForSchemaTwoHost(t *testing.T) {
	app := NewApp()
	yaml := []byte(fmt.Sprintf("enabled: true\nstate_file: %q\nkeys: []\n", filepath.ToSlash(filepath.Join(t.TempDir(), "state.json"))))

	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml, SchemaVersion: SchemaVersion})
	raw, err := app.HandleMethod(MethodPluginReconfigure, req)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var registration Registration
	if err := json.Unmarshal(env.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if registration.SchemaVersion != SchemaVersion || !registration.Capabilities.RequestInterceptor {
		t.Fatalf("schema-two registration = %+v, want schema=%d and request interceptor", registration, SchemaVersion)
	}
}

func TestIsPolicyRequestPathAcceptsCPAPathPrefixes(t *testing.T) {
	for _, path := range []string{
		"/v1/chat/completions",
		"/openai/v1/responses",
		"/backend-api/codex/responses",
		"/v1/messages",
		"/v1beta/models/gemini-2.5-pro:generateContent",
		"/proxy/v1/embeddings",
		"/v1/videos/req_123",
	} {
		if !isPolicyRequestPath(path) {
			t.Errorf("isPolicyRequestPath(%q) = false, want true", path)
		}
	}
	for _, path := range []string{"/v1/models", "/v1/usage", "/v1beta/models", "/v1beta/models/list"} {
		if isPolicyRequestPath(path) {
			t.Errorf("isPolicyRequestPath(%q) = true, want false", path)
		}
	}
}

func configureQuotaResponseTestApp(t *testing.T, daily, weekly float64, rpm ...int) (*App, string, time.Time) {
	t.Helper()
	app := NewApp()
	plain := "cpa_quota_response"
	hash, err := policy.HashKey(plain)
	if err != nil {
		t.Fatal(err)
	}
	rpmLimit := 0
	if len(rpm) > 0 {
		rpmLimit = rpm[0]
	}
	yaml := []byte(fmt.Sprintf(`enabled: true
state_file: %q
keys:
  - id: quota-key
    name: Quota Key
    enabled: true
    key_hash: %q
    rpm: %d
    daily_limit_usd: %.2f
    weekly_limit_usd: %.2f
    models:
      - alias: fast
        provider: codex
        target_model: gpt-5-codex
        input_price_per_million: 1
`, filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")), hash, rpmLimit, daily, weekly))
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml, SchemaVersion: SchemaVersion})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatal(err)
	}
	return app, plain, time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
}

func frontendAuthForTest(t *testing.T, app *App, path string, headers http.Header) FrontendAuthResponse {
	t.Helper()
	req, _ := json.Marshal(FrontendAuthRequest{
		Method:  http.MethodPost,
		Path:    path,
		Headers: headers,
		Body:    []byte(`{"model":"fast"}`),
	})
	raw, err := app.HandleMethod(MethodFrontendAuthAuthenticate, req)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var auth FrontendAuthResponse
	if err := json.Unmarshal(env.Result, &auth); err != nil {
		t.Fatal(err)
	}
	return auth
}

func quotaInterceptForTest(t *testing.T, app *App, headers http.Header) RequestInterceptResponse {
	return policyInterceptForTest(t, app, headers, "openai")
}

func policyInterceptForTest(t *testing.T, app *App, headers http.Header, sourceFormat string) RequestInterceptResponse {
	t.Helper()
	req, _ := json.Marshal(RequestInterceptRequest{
		SourceFormat:   sourceFormat,
		RequestedModel: "fast",
		Headers:        headers,
		Body:           []byte(`{"model":"fast"}`),
	})
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, req)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var response RequestInterceptResponse
	if err := json.Unmarshal(env.Result, &response); err != nil {
		t.Fatal(err)
	}
	return response
}
