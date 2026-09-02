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

// openAIRequestError is the OpenAI-compatible error shape used for policy
// rejections. Keep param present and null to match OpenAI-compatible clients
// and Sub2API.
type openAIRequestError struct {
	Error openAIRequestErrorDetail `json:"error"`
}

type openAIRequestErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

type anthropicRequestError struct {
	Type  string                    `json:"type"`
	Error anthropicRequestErrorBody `json:"error"`
}

type anthropicRequestErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type googleRequestError struct {
	Error googleRequestErrorBody `json:"error"`
}

type googleRequestErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// interceptRequestBefore terminates a managed request when its downstream key
// has reached its daily/weekly dollar limit or RPM ceiling. This hook runs
// after model routing but before CPA selects an upstream auth, so no upstream
// authentication or execution occurs for a policy rejection.
func (a *App) interceptRequestBefore(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if !a.requestInterceptorSupported() || a.store == nil {
		return OKEnvelope(RequestInterceptResponse{})
	}

	// Request interceptors do not receive URL query values. Frontend auth only
	// delegates header-authenticated requests here, so the same lookup is
	// intentionally header-only.
	rawKey := policy.ExtractAPIKey(req.Headers, nil)
	if reason, summary, retryAfter, ok := a.store.QuotaForAPIKey(rawKey); ok && reason != "" {
		body, err := quotaErrorBody(req.SourceFormat, reason, summary)
		if err != nil {
			return nil, err
		}
		return directPolicyResponse(body, retryAfter)
	}
	if blocked, retryAfter, rpm, ok := a.store.RateLimitForAPIKey(rawKey); ok && blocked {
		body, err := rateLimitErrorBody(req.SourceFormat, rpm)
		if err != nil {
			return nil, err
		}
		return directPolicyResponse(body, retryAfter)
	}
	return OKEnvelope(RequestInterceptResponse{})
}

func directPolicyResponse(body []byte, retryAfter int) ([]byte, error) {
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
// policy gate belongs before upstream auth selection only.
func (a *App) interceptRequestAfter(raw []byte) ([]byte, error) {
	return OKEnvelope(RequestInterceptResponse{})
}

// canPassPolicyDenialToInterceptor keeps enforcement fail-closed. A limited
// request is allowed past frontend auth only when it has a concrete selected
// model rule, a header credential, and a negotiated request interceptor.
// Unknown keys, query-only keys, disabled keys, model-list requests, and
// unauthorized models remain on the existing authentication-denial path.
func (a *App) canPassPolicyDenialToInterceptor(req FrontendAuthRequest, decision policy.AuthDecision) bool {
	if !a.requestInterceptorSupported() || (!decision.CostLimited && !decision.RateLimited) || !isPolicyRequestPath(req.Path) {
		return false
	}
	if decision.Rule.Alias == "" {
		return false
	}
	return policy.ExtractAPIKey(req.Headers, nil) != ""
}

func isPolicyRequestPath(path string) bool {
	path = strings.ToLower(strings.TrimRight(strings.TrimSpace(path), "/"))
	if path == "" {
		return false
	}
	for _, suffix := range []string{
		"/chat/completions",
		"/completions",
		"/responses",
		"/messages",
		"/messages/count_tokens",
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
	// Gemini native generation paths carry the model in the URL, for example
	// /v1beta/models/gemini-2.5-pro:generateContent. Keep /v1beta/models
	// itself out of this set because it is a model-list endpoint.
	return strings.Contains(path, "/v1beta/models/") && strings.Contains(path, ":")
}

func requestErrorProtocol(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	switch {
	case strings.Contains(format, "gemini"), strings.Contains(format, "google"), strings.Contains(format, "antigravity"):
		return "google"
	case strings.Contains(format, "claude"), strings.Contains(format, "anthropic"), format == "messages":
		return "anthropic"
	default:
		return "openai"
	}
}

func quotaErrorMessage(reason string, summary policy.UsageSummary) string {
	window, usage, limit, resetAt := quotaWindowDetails(reason, summary)
	return fmt.Sprintf(
		"%s quota exceeded. Usage: $%.2f / $%.2f USD. Resets at %s.",
		window,
		usage,
		limit,
		formatQuotaReset(resetAt),
	)
}

func quotaErrorBody(format, reason string, summary policy.UsageSummary) ([]byte, error) {
	message := quotaErrorMessage(reason, summary)
	switch requestErrorProtocol(format) {
	case "anthropic":
		return json.Marshal(anthropicRequestError{
			Type: "error",
			Error: anthropicRequestErrorBody{
				Type:    "rate_limit_error",
				Message: message,
			},
		})
	case "google":
		return json.Marshal(googleRequestError{
			Error: googleRequestErrorBody{
				Code:    http.StatusTooManyRequests,
				Message: message,
				Status:  "RESOURCE_EXHAUSTED",
			},
		})
	default:
		return json.Marshal(openAIRequestError{
			Error: openAIRequestErrorDetail{
				Message: message,
				Type:    "insufficient_quota",
				Param:   nil,
				Code:    quotaErrorCode(reason),
			},
		})
	}
}

func rateLimitErrorBody(format string, rpm int) ([]byte, error) {
	message := "Requests-per-minute limit exceeded for this API key. Please retry later."
	if rpm > 0 {
		message = fmt.Sprintf("Requests-per-minute limit exceeded for this API key (limit: %d requests/minute). Please retry later.", rpm)
	}
	switch requestErrorProtocol(format) {
	case "anthropic":
		return json.Marshal(anthropicRequestError{
			Type: "error",
			Error: anthropicRequestErrorBody{
				Type:    "rate_limit_error",
				Message: message,
			},
		})
	case "google":
		return json.Marshal(googleRequestError{
			Error: googleRequestErrorBody{
				Code:    http.StatusTooManyRequests,
				Message: message,
				Status:  "RESOURCE_EXHAUSTED",
			},
		})
	default:
		return json.Marshal(openAIRequestError{
			Error: openAIRequestErrorDetail{
				Message: message,
				Type:    "rate_limit_error",
				Param:   nil,
				Code:    "rate_limit_exceeded",
			},
		})
	}
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
