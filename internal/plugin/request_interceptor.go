package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpa-key-policy/internal/policy"
)

// requestInterceptorSupported reports whether the host negotiated schema 2,
// which is the first schema that supports direct request termination.
func (a *App) requestInterceptorSupported() bool {
	return a != nil && a.hostSchemaVersion.Load() >= SchemaVersion
}

// openAIQuotaError is the stable downstream shape used for quota exhaustion.
// Keep param present and null to match OpenAI-compatible clients and Sub2API.
type openAIQuotaError struct {
	Error openAIQuotaErrorDetail `json:"error"`
}

type openAIQuotaErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

// interceptRequestBefore terminates an OpenAI-compatible request when the
// managed downstream key has reached its daily or weekly dollar limit. This
// hook runs after model routing but before CPA selects an upstream auth, so no
// upstream authentication or execution occurs for a quota rejection.
func (a *App) interceptRequestBefore(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if !a.requestInterceptorSupported() || a.store == nil || !isOpenAIRequestFormat(req.SourceFormat) {
		return OKEnvelope(RequestInterceptResponse{})
	}

	// Request interceptors do not receive URL query values. Only pass a quota
	// request through from frontend auth when the credential is available in a
	// header, which is the supported OpenAI client authentication form.
	rawKey := policy.ExtractAPIKey(req.Headers, nil)
	reason, summary, retryAfter, ok := a.store.QuotaForAPIKey(rawKey)
	if !ok || reason == "" {
		return OKEnvelope(RequestInterceptResponse{})
	}

	body, err := quotaErrorBody(reason, summary)
	if err != nil {
		return nil, err
	}
	headers := http.Header{
		"Content-Type": []string{"application/json"},
	}
	if retryAfter > 0 {
		headers.Set("Retry-After", strconv.Itoa(retryAfter))
	}

	return OKEnvelope(RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusTooManyRequests,
		ResponseHeaders: headers,
		ResponseBody:    body,
	})
}

// interceptRequestAfter is intentionally a no-op. Advertising one request
// interceptor capability makes CPA call both methods on the same adapter; the
// quota gate belongs before upstream auth selection only.
func (a *App) interceptRequestAfter(raw []byte) ([]byte, error) {
	return OKEnvelope(RequestInterceptResponse{})
}

// canPassQuotaToInterceptor keeps quota enforcement fail-closed. A quota-blocked
// key is allowed past frontend auth only for an OpenAI-compatible path whose
// request interceptor can produce the direct response. Query-only keys remain
// rejected by the old auth path because the interceptor cannot see query values.
func (a *App) canPassQuotaToInterceptor(req FrontendAuthRequest, decision policy.AuthDecision) bool {
	if !a.requestInterceptorSupported() || !decision.CostLimited || !isOpenAIQuotaPath(req.Path) {
		return false
	}
	return policy.ExtractAPIKey(req.Headers, nil) != ""
}

func isOpenAIRequestFormat(format string) bool {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		return true
	}
	return strings.Contains(format, "openai") || format == "responses" || format == "chat-completions" || format == "chat_completions"
}

func isOpenAIQuotaPath(path string) bool {
	path = strings.ToLower(strings.TrimRight(strings.TrimSpace(path), "/"))
	if path == "" {
		return false
	}
	for _, suffix := range []string{
		"/chat/completions",
		"/completions",
		"/responses",
		"/embeddings",
		"/images/generations",
		"/images/edits",
		"/videos",
		"/videos/generations",
		"/videos/edits",
		"/videos/extensions",
	} {
		if strings.HasSuffix(path, suffix) || strings.Contains(path, suffix+"/") {
			return true
		}
	}
	return false
}

func quotaErrorBody(reason string, summary policy.UsageSummary) ([]byte, error) {
	window, usage, limit, resetAt := quotaWindowDetails(reason, summary)
	message := fmt.Sprintf(
		"%s quota exceeded. Usage: $%.2f / $%.2f USD. Resets at %s.",
		window,
		usage,
		limit,
		formatQuotaReset(resetAt),
	)
	return json.Marshal(openAIQuotaError{
		Error: openAIQuotaErrorDetail{
			Message: message,
			Type:    "insufficient_quota",
			Param:   nil,
			Code:    quotaErrorCode(reason),
		},
	})
}

func quotaWindowDetails(reason string, summary policy.UsageSummary) (string, float64, float64, time.Time) {
	if reason == "weekly_exceeded" {
		return "Weekly", summary.WeeklyUSD, summary.WeeklyLimitUSD, summary.WeeklyResetAt
	}
	return "Daily", summary.DailyUSD, summary.DailyLimitUSD, summary.DailyResetAt
}

func quotaErrorCode(reason string) string {
	if reason == "weekly_exceeded" {
		return "weekly_quota_exceeded"
	}
	return "daily_quota_exceeded"
}

func formatQuotaReset(resetAt time.Time) string {
	if resetAt.IsZero() {
		return "the quota window reset"
	}
	return resetAt.UTC().Format(time.RFC3339)
}
