package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const quotaTouchMaxAccounts = 200

type AccountQuotaTouchResult struct {
	AccountID int64  `json:"account_id"`
	Success   bool   `json:"success"`
	Touched   bool   `json:"touched"`
	Message   string `json:"message,omitempty"`
}

type AccountQuotaTouchResponse struct {
	Success int                       `json:"success"`
	Failed  int                       `json:"failed"`
	Results []AccountQuotaTouchResult `json:"results"`
}

func (s *AccountTestService) TouchOpenAIQuota(ctx context.Context, accountIDs []int64) (*AccountQuotaTouchResponse, error) {
	out := &AccountQuotaTouchResponse{Results: make([]AccountQuotaTouchResult, 0, len(accountIDs))}
	if s == nil || s.accountRepo == nil {
		return out, errorsAccountQuotaTouch("account service is not available")
	}
	if len(accountIDs) == 0 {
		return out, nil
	}
	if len(accountIDs) > quotaTouchMaxAccounts {
		accountIDs = accountIDs[:quotaTouchMaxAccounts]
	}

	for _, id := range accountIDs {
		result := s.touchOpenAIQuotaAccount(ctx, id)
		if result.Success {
			out.Success++
		} else {
			out.Failed++
		}
		out.Results = append(out.Results, result)
	}
	return out, nil
}

func errorsAccountQuotaTouch(message string) error {
	return fmt.Errorf("quota touch: %s", message)
}

func (s *AccountTestService) touchOpenAIQuotaAccount(ctx context.Context, accountID int64) AccountQuotaTouchResult {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return AccountQuotaTouchResult{AccountID: accountID, Message: err.Error()}
	}
	if account == nil {
		return AccountQuotaTouchResult{AccountID: accountID, Message: ErrAccountNotFound.Error()}
	}
	if account.Platform != PlatformOpenAI {
		return AccountQuotaTouchResult{AccountID: accountID, Message: "quota touch only supports OpenAI accounts"}
	}
	if !account.IsSchedulable() {
		return AccountQuotaTouchResult{AccountID: accountID, Message: "account is not schedulable"}
	}

	if err := s.callOpenAIQuotaTouchUpstream(ctx, account); err != nil {
		return AccountQuotaTouchResult{AccountID: accountID, Message: err.Error()}
	}

	updates := buildQuotaTouchExtraUpdates(account, time.Now().UTC())
	if len(updates) == 0 {
		return AccountQuotaTouchResult{AccountID: accountID, Success: true, Message: "quota window already initialized"}
	}
	if err := s.accountRepo.UpdateExtra(ctx, accountID, updates); err != nil {
		return AccountQuotaTouchResult{AccountID: accountID, Message: err.Error()}
	}
	return AccountQuotaTouchResult{AccountID: accountID, Success: true, Touched: true}
}

func (s *AccountTestService) callOpenAIQuotaTouchUpstream(ctx context.Context, account *Account) error {
	if s.httpUpstream == nil {
		return errorsAccountQuotaTouch("http upstream is not available")
	}

	apiURL, token, isOAuth, chatgptAccountID, err := s.resolveOpenAIQuotaTouchTarget(account)
	if err != nil {
		return err
	}
	payload := createOpenAIQuotaTouchPayload(openai.DefaultTestModel, isOAuth)
	payloadBytes, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if isOAuth {
		req.Host = "chatgpt.com"
		req.Header.Set("accept", "application/json")
		if chatgptAccountID != "" {
			req.Header.Set("chatgpt-account-id", chatgptAccountID)
		}
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	return nil
}

func (s *AccountTestService) resolveOpenAIQuotaTouchTarget(account *Account) (apiURL string, token string, isOAuth bool, chatgptAccountID string, err error) {
	if account.IsOAuth() {
		token = account.GetOpenAIAccessToken()
		if token == "" {
			err = errorsAccountQuotaTouch("no access token available")
			return
		}
		return chatgptCodexAPIURL, token, true, account.GetChatGPTAccountID(), nil
	}
	if account.Type == AccountTypeAPIKey {
		token = account.GetOpenAIApiKey()
		if token == "" {
			err = errorsAccountQuotaTouch("no API key available")
			return
		}
		baseURL := account.GetOpenAIBaseURL()
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		normalizedBaseURL := baseURL
		if s.cfg != nil {
			if validated, validateErr := s.validateUpstreamBaseURL(baseURL); validateErr == nil {
				normalizedBaseURL = validated
			} else {
				err = validateErr
				return
			}
		}
		return buildOpenAIResponsesURL(normalizedBaseURL), token, false, "", nil
	}
	err = errorsAccountQuotaTouch("unsupported OpenAI account type")
	return
}

func createOpenAIQuotaTouchPayload(modelID string, isOAuth bool) map[string]any {
	payload := map[string]any{
		"model": modelID,
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "hi"},
				},
			},
		},
		"instructions":       "Reply with ok.",
		"stream":             isOAuth,
		"parallel_tool_calls": false,
	}
	if isOAuth {
		payload["store"] = false
	} else {
		payload["max_output_tokens"] = 1
	}
	return payload
}

func buildQuotaTouchExtraUpdates(account *Account, now time.Time) map[string]any {
	updates := map[string]any{}
	if account == nil {
		return updates
	}
	nowStr := now.UTC().Format(time.RFC3339)
	if account.GetQuotaDailyLimit() > 0 {
		if account.getExtraTime("quota_daily_start").IsZero() {
			updates["quota_daily_start"] = nowStr
		}
		if account.GetQuotaDailyResetMode() == "fixed" {
			resetAt := account.getExtraTime("quota_daily_reset_at")
			if resetAt.IsZero() || !now.Before(resetAt) {
				tz, err := time.LoadLocation(account.GetQuotaResetTimezone())
				if err != nil {
					tz = time.UTC
				}
				updates["quota_daily_reset_at"] = nextFixedDailyReset(account.GetQuotaDailyResetHour(), tz, now).UTC().Format(time.RFC3339)
			}
		}
	}
	if account.GetQuotaWeeklyLimit() > 0 {
		if account.getExtraTime("quota_weekly_start").IsZero() {
			updates["quota_weekly_start"] = nowStr
		}
		if account.GetQuotaWeeklyResetMode() == "fixed" {
			resetAt := account.getExtraTime("quota_weekly_reset_at")
			if resetAt.IsZero() || !now.Before(resetAt) {
				tz, err := time.LoadLocation(account.GetQuotaResetTimezone())
				if err != nil {
					tz = time.UTC
				}
				updates["quota_weekly_reset_at"] = nextFixedWeeklyReset(account.GetQuotaWeeklyResetDay(), account.GetQuotaWeeklyResetHour(), tz, now).UTC().Format(time.RFC3339)
			}
		}
	}
	return updates
}
