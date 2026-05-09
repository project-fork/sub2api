//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type quotaTouchAccountRepo struct {
	AccountRepository
	accounts map[int64]*Account
	updates  map[int64]map[string]any
}

func (r *quotaTouchAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if account, ok := r.accounts[id]; ok {
		cloned := *account
		return &cloned, nil
	}
	return nil, ErrAccountNotFound
}

func (r *quotaTouchAccountRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	if r.updates == nil {
		r.updates = map[int64]map[string]any{}
	}
	r.updates[id] = updates
	if account, ok := r.accounts[id]; ok {
		if account.Extra == nil {
			account.Extra = map[string]any{}
		}
		for key, value := range updates {
			account.Extra[key] = value
		}
	}
	return nil
}

type quotaTouchUpstream struct {
	statusCode int
	err        error
	calls      int
	body       string
}

func (u *quotaTouchUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (u *quotaTouchUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		u.body = string(data)
	}
	if u.err != nil {
		return nil, u.err
	}
	status := u.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_touch"}`)),
		Header:     make(http.Header),
	}, nil
}

func TestAccountTestService_QuotaTouchOpenAI_UpdatesAfterUpstreamSuccess(t *testing.T) {
	now := time.Now().UTC()
	repo := &quotaTouchAccountRepo{accounts: map[int64]*Account{
		101: {
			ID:          101,
			Name:        "openai-touch",
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra: map[string]any{
				"quota_daily_limit":       float64(10),
				"quota_weekly_limit":      float64(20),
				"quota_daily_reset_mode":  "fixed",
				"quota_daily_reset_hour":  float64(now.Add(time.Hour).Hour()),
				"quota_weekly_reset_mode": "fixed",
				"quota_weekly_reset_day":  float64(now.Weekday()),
				"quota_weekly_reset_hour": float64(now.Add(2 * time.Hour).Hour()),
			},
		},
	}}
	upstream := &quotaTouchUpstream{statusCode: http.StatusOK}
	svc := NewAccountTestService(repo, nil, nil, nil, upstream, nil, nil)

	result, err := svc.TouchOpenAIQuota(context.Background(), []int64{101})

	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Len(t, result.Results, 1)
	require.True(t, result.Results[0].Success)
	require.Contains(t, upstream.body, `"stream":false`)
	require.Contains(t, repo.updates[101], "quota_daily_start")
	require.Contains(t, repo.updates[101], "quota_weekly_start")
	require.Contains(t, repo.updates[101], "quota_daily_reset_at")
	require.Contains(t, repo.updates[101], "quota_weekly_reset_at")
}

func TestAccountTestService_QuotaTouchOpenAI_OAuthUsesStreamingPayload(t *testing.T) {
	repo := &quotaTouchAccountRepo{accounts: map[int64]*Account{
		104: {
			ID:          104,
			Name:        "openai-oauth-touch",
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "access-token",
				"chatgpt_account_id": "chatgpt-account-id",
			},
			Extra: map[string]any{},
		},
	}}
	upstream := &quotaTouchUpstream{statusCode: http.StatusOK}
	svc := NewAccountTestService(repo, nil, nil, nil, upstream, nil, nil)

	result, err := svc.TouchOpenAIQuota(context.Background(), []int64{104})

	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Len(t, result.Results, 1)
	require.True(t, result.Results[0].Success)
	require.Contains(t, upstream.body, `"stream":true`)
	require.Contains(t, upstream.body, `"store":false`)
	require.NotContains(t, upstream.body, `"max_output_tokens"`)
}

func TestAccountTestService_QuotaTouchOpenAI_DoesNotUpdateAfterUpstreamFailure(t *testing.T) {
	repo := &quotaTouchAccountRepo{accounts: map[int64]*Account{
		102: {
			ID:          102,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra:       map[string]any{"quota_daily_limit": float64(10)},
		},
	}}
	upstream := &quotaTouchUpstream{statusCode: http.StatusTooManyRequests}
	svc := NewAccountTestService(repo, nil, nil, nil, upstream, nil, nil)

	result, err := svc.TouchOpenAIQuota(context.Background(), []int64{102})

	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Len(t, result.Results, 1)
	require.False(t, result.Results[0].Success)
	require.Nil(t, repo.updates)
}

func TestAccountTestService_QuotaTouchOpenAI_AllowsAccountsWithoutQuotaLimit(t *testing.T) {
	repo := &quotaTouchAccountRepo{accounts: map[int64]*Account{
		103: {
			ID:          103,
			Name:        "openai-no-limit",
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra:       map[string]any{},
		},
	}}
	upstream := &quotaTouchUpstream{statusCode: http.StatusOK}
	svc := NewAccountTestService(repo, nil, nil, nil, upstream, nil, nil)

	result, err := svc.TouchOpenAIQuota(context.Background(), []int64{103})

	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Len(t, result.Results, 1)
	require.True(t, result.Results[0].Success)
	require.False(t, result.Results[0].Touched)
	require.Equal(t, "quota window already initialized", result.Results[0].Message)
	require.Nil(t, repo.updates)
}
