package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
