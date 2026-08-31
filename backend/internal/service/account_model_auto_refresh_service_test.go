package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type accountModelAutoRefreshRepoStub struct {
	AccountRepository
	account *Account
	updated *Account
	extra   map[string]any
}

func (s *accountModelAutoRefreshRepoStub) GetByID(context.Context, int64) (*Account, error) {
	if s.account == nil {
		return nil, nil
	}
	account := *s.account
	account.Credentials = shallowCopyMap(s.account.Credentials)
	return &account, nil
}

func (s *accountModelAutoRefreshRepoStub) Update(_ context.Context, account *Account) error {
	updated := *account
	updated.Credentials = shallowCopyMap(account.Credentials)
	s.updated = &updated
	return nil
}

func (s *accountModelAutoRefreshRepoStub) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	s.extra = shallowCopyMap(updates)
	return nil
}

func TestAccountModelAutoRefreshDue(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	last := now.Add(-20 * time.Minute)

	require.True(t, accountModelAutoRefreshDue(accountModelAutoRefreshConfig{
		Enabled:         true,
		IntervalMinutes: 10,
		LastRunAt:       &last,
	}, now))

	require.False(t, accountModelAutoRefreshDue(accountModelAutoRefreshConfig{
		Enabled:         true,
		IntervalMinutes: 30,
		LastRunAt:       &last,
	}, now))

	require.False(t, accountModelAutoRefreshDue(accountModelAutoRefreshConfig{
		Enabled:         false,
		IntervalMinutes: 10,
		LastRunAt:       &last,
	}, now))
}

func TestApplyAccountModelAutoRefreshFetchedModelsPreservesEnabledMappings(t *testing.T) {
	credentials := map[string]any{
		"model_candidates": []any{"gpt-5", "DeepSeek-V4-Pro"},
		"model_mapping": map[string]any{
			"gpt-5": "gpt-5",
		},
		"model_selection_enabled": true,
	}

	applyAccountModelAutoRefreshFetchedModels(credentials, []string{"deepseek-v4-pro", "glm-5.1"})

	require.Equal(t, []string{"DeepSeek-V4-Pro", "glm-5.1", "gpt-5"}, credentials["model_candidates"])
	require.Equal(t, map[string]any{"gpt-5": "gpt-5"}, credentials["model_mapping"])
	require.Equal(t, true, credentials["model_selection_enabled"])
}

func TestApplyAccountModelAutoRefreshFilteredModelsFiltersOnlyFetchedFailures(t *testing.T) {
	credentials := map[string]any{
		"model_candidates": []string{"old-custom", "bad-model", "alias-model", "keep-model"},
		"model_mapping": map[string]any{
			"bad-model":    "bad-model",
			"alias-model":  "bad-model",
			"keep-model":   "keep-model",
			"manual-alias": "external-model",
		},
	}

	applyAccountModelAutoRefreshFilteredModels(
		credentials,
		[]string{"bad-model", "keep-model", "new-model"},
		[]string{"keep-model", "new-model"},
	)

	require.Equal(t, []string{"alias-model", "keep-model", "new-model", "old-custom"}, credentials["model_candidates"])
	require.Equal(t, true, credentials["model_selection_enabled"])
	require.Equal(t, map[string]any{
		"keep-model":   "keep-model",
		"manual-alias": "external-model",
		"new-model":    "new-model",
	}, credentials["model_mapping"])
}

func TestAccountModelAutoRefreshConfigFromAccountNormalizesInterval(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{
			accountModelAutoRefreshEnabledKey:         "true",
			accountModelAutoRefreshIntervalMinutesKey: "1",
		},
	}

	cfg := accountModelAutoRefreshConfigFromAccount(account)

	require.True(t, cfg.Enabled)
	require.Equal(t, accountModelAutoRefreshMinIntervalMinutes, cfg.IntervalMinutes)
}

func TestPersistAutoRefreshSuccessMergesIntoLatestCredentials(t *testing.T) {
	repo := &accountModelAutoRefreshRepoStub{account: &Account{
		ID: 7,
		Credentials: map[string]any{
			accountModelAutoRefreshEnabledKey: true,
			"api_key":                         "latest-token",
			"model_candidates":                []string{"manual-model"},
		},
	}}
	svc := &AccountModelAutoRefreshService{accountRepo: repo}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	err := svc.persistAutoRefreshSuccess(
		context.Background(),
		7,
		[]string{"new-model"},
		nil,
		nil,
		false,
		now,
	)

	require.NoError(t, err)
	require.NotNil(t, repo.updated)
	require.Equal(t, "latest-token", repo.updated.Credentials["api_key"])
	require.Equal(t, []string{"manual-model", "new-model"}, repo.updated.Credentials["model_candidates"])
	require.Equal(t, now.Format(time.RFC3339), repo.updated.Credentials[accountModelAutoRefreshLastSuccessAtKey])
}

func TestPersistAutoRefreshFailurePreservesLatestSuccessAndAdminFields(t *testing.T) {
	lastSuccess := "2026-07-09T08:00:00Z"
	repo := &accountModelAutoRefreshRepoStub{account: &Account{
		ID: 8,
		Credentials: map[string]any{
			accountModelAutoRefreshEnabledKey:       true,
			accountModelAutoRefreshLastSuccessAtKey: lastSuccess,
			"api_key":                               "latest-token",
		},
	}}
	svc := &AccountModelAutoRefreshService{accountRepo: repo}
	now := time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC)

	svc.persistAutoRefreshFailure(context.Background(), 8, "upstream failed", now, 3, 0, 3)

	require.NotNil(t, repo.updated)
	require.Equal(t, "latest-token", repo.updated.Credentials["api_key"])
	require.Equal(t, lastSuccess, repo.updated.Credentials[accountModelAutoRefreshLastSuccessAtKey])
	require.Equal(t, "upstream failed", repo.updated.Credentials[accountModelAutoRefreshLastErrorKey])
	require.Equal(t, 3, repo.updated.Credentials[accountModelAutoRefreshLastFailedCountKey])
}

func TestPersistAutoRefreshSkipsWriteAfterAdminDisablesIt(t *testing.T) {
	repo := &accountModelAutoRefreshRepoStub{account: &Account{
		ID:          9,
		Credentials: map[string]any{"api_key": "latest-token"},
	}}
	svc := &AccountModelAutoRefreshService{accountRepo: repo}

	err := svc.persistAutoRefreshSuccess(
		context.Background(),
		9,
		[]string{"new-model"},
		nil,
		nil,
		false,
		time.Now(),
	)

	require.NoError(t, err)
	require.Nil(t, repo.updated)
}

func TestRefreshOneAccountPersistsUpstreamCapabilityMetadata(t *testing.T) {
	account := &Account{
		ID:       10,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			accountModelAutoRefreshEnabledKey: true,
			"api_key":                         "test-key",
			"base_url":                        "https://provider.example/v1",
		},
	}
	repo := &accountModelAutoRefreshRepoStub{account: account}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"data": [{
				"id": "gpt-image-2",
				"reasoning": false,
				"input_modalities": ["text"],
				"context_window": 128000,
				"max_output_tokens": 8192
			}]
		}`)),
	}}
	testSvc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg:          upstreamModelSyncTestConfig(),
	}
	svc := &AccountModelAutoRefreshService{
		accountRepo:    repo,
		accountTestSvc: testSvc,
	}

	svc.refreshOneAccount(
		context.Background(),
		account,
		accountModelAutoRefreshConfig{Enabled: true},
		time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	)

	require.Contains(t, repo.extra, UpstreamModelMetadataExtraKey)
	require.NotNil(t, repo.updated)
	require.Equal(t, []string{"gpt-image-2"}, repo.updated.Credentials["model_candidates"])
}
