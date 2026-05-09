package admin

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type quotaTouchHandlerAccountRepo struct {
	service.AccountRepository
	updates map[int64]map[string]any
}

func (r *quotaTouchHandlerAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	return &service.Account{
		ID:          id,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"quota_daily_limit": float64(10)},
	}, nil
}

func (r *quotaTouchHandlerAccountRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	if r.updates == nil {
		r.updates = map[int64]map[string]any{}
	}
	r.updates[id] = updates
	return nil
}

type quotaTouchHandlerUpstream struct{}

func (u quotaTouchHandlerUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (u quotaTouchHandlerUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"id":"resp_touch"}`))),
		Header:     make(http.Header),
	}, nil
}

func TestAccountHandlerQuotaTouch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &quotaTouchHandlerAccountRepo{}
	accountTestSvc := service.NewAccountTestService(repo, nil, nil, nil, quotaTouchHandlerUpstream{}, nil, nil)
	handler := NewAccountHandler(newStubAdminService(), nil, nil, nil, nil, nil, nil, accountTestSvc, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/quota-touch", handler.QuotaTouch)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/quota-touch", bytes.NewReader([]byte(`{"account_ids":[101]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"success":1`)
	require.Contains(t, repo.updates[101], "quota_daily_start")
}
