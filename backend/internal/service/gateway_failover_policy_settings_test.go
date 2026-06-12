package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayFailoverPolicySettingRepo struct {
	data map[string]string
}

func newGatewayFailoverPolicySettingRepo() *gatewayFailoverPolicySettingRepo {
	return &gatewayFailoverPolicySettingRepo{data: map[string]string{}}
}

func (r *gatewayFailoverPolicySettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	value, ok := r.data[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *gatewayFailoverPolicySettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.data[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *gatewayFailoverPolicySettingRepo) Set(_ context.Context, key, value string) error {
	r.data[key] = value
	return nil
}

func (r *gatewayFailoverPolicySettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.data[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (r *gatewayFailoverPolicySettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	for key, value := range settings {
		r.data[key] = value
	}
	return nil
}

func (r *gatewayFailoverPolicySettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	values := make(map[string]string, len(r.data))
	for key, value := range r.data {
		values[key] = value
	}
	return values, nil
}

func (r *gatewayFailoverPolicySettingRepo) Delete(_ context.Context, key string) error {
	delete(r.data, key)
	return nil
}

func gatewayFailoverPolicyJSON(t *testing.T, settings GatewayFailoverPolicySettings) string {
	t.Helper()
	data, err := json.Marshal(settings)
	require.NoError(t, err)
	return string(data)
}

func newOpenAIFailoverPolicyTestService(t *testing.T, settings GatewayFailoverPolicySettings) *OpenAIGatewayService {
	t.Helper()
	repo := newGatewayFailoverPolicySettingRepo()
	repo.data[SettingKeyGatewayFailoverPolicySettings] = gatewayFailoverPolicyJSON(t, settings)
	return &OpenAIGatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
}

func updateGatewayFailoverRule(t *testing.T, settings *GatewayFailoverPolicySettings, id string, update func(*GatewayFailoverRule)) {
	t.Helper()
	for i := range settings.Rules {
		if settings.Rules[i].ID == id {
			update(&settings.Rules[i])
			return
		}
	}
	t.Fatalf("gateway failover rule %q not found", id)
}

func TestGatewayFailoverPolicySettings_DefaultsWhenNotSet(t *testing.T) {
	svc := NewSettingService(newGatewayFailoverPolicySettingRepo(), &config.Config{})

	settings, err := svc.GetGatewayFailoverPolicySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "first", settings.MatchMode)
	require.Len(t, settings.Rules, 4)
	require.True(t, settings.Structured400Enabled)
	require.Equal(t, 10, settings.Structured400CooldownMinutes)
	require.Equal(t, 20, settings.FailureCooldownJitterPercent)
	require.True(t, settings.HTTP5xxCooldownEnabled)
	require.Equal(t, 3, settings.HTTP5xxThreshold)
	require.Equal(t, 30, settings.HTTP5xxWindowSeconds)
	require.Equal(t, 120, settings.HTTP5xxCooldownSeconds)
	require.True(t, settings.TransportCooldownEnabled)
	require.Equal(t, 3, settings.TransportThreshold)
	require.Equal(t, 30, settings.TransportWindowSeconds)
	require.Equal(t, 120, settings.TransportCooldownSeconds)
}

func TestSetGatewayFailoverPolicySettings_RoundTrip(t *testing.T) {
	repo := newGatewayFailoverPolicySettingRepo()
	svc := NewSettingService(repo, &config.Config{})
	want := DefaultGatewayFailoverPolicySettings()
	want.FailureCooldownJitterPercent = 0
	updateGatewayFailoverRule(t, want, "openai_structured_400_rpm", func(rule *GatewayFailoverRule) {
		rule.Enabled = false
	})
	updateGatewayFailoverRule(t, want, "openai_http_5xx_threshold", func(rule *GatewayFailoverRule) {
		rule.Match.Consecutive.Threshold = 2
		rule.Match.Consecutive.WindowSeconds = 10
		rule.Action.CooldownSeconds = 45
		rule.Action.JitterPercent = 0
	})

	require.NoError(t, svc.SetGatewayFailoverPolicySettings(context.Background(), want))
	got, err := svc.GetGatewayFailoverPolicySettings(context.Background())
	require.NoError(t, err)
	normalizedWant, err := normalizeGatewayFailoverPolicySettings(want, true)
	require.NoError(t, err)
	require.Equal(t, normalizedWant, got)
}

func TestGatewayFailoverPolicy_DisablesStructured400Failover(t *testing.T) {
	settings := *DefaultGatewayFailoverPolicySettings()
	updateGatewayFailoverRule(t, &settings, "openai_structured_400_rpm", func(rule *GatewayFailoverRule) {
		rule.Enabled = false
	})
	svc := newOpenAIFailoverPolicyTestService(t, settings)
	body := []byte(`{"error":{"code":"rate_limit_exceeded"},"code":"rate_limit_exceeded","limit_type":"rpm"}`)

	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusBadRequest, "", body))

	account := &Account{ID: 9001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.False(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, body))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestGatewayFailoverPolicy_HTTP5xxThresholdBlocksAccount(t *testing.T) {
	settings := *DefaultGatewayFailoverPolicySettings()
	updateGatewayFailoverRule(t, &settings, "openai_http_5xx_threshold", func(rule *GatewayFailoverRule) {
		rule.Match.Consecutive.Threshold = 2
		rule.Match.Consecutive.WindowSeconds = 60
		rule.Action.CooldownSeconds = 30
		rule.Action.JitterPercent = 0
	})
	svc := newOpenAIFailoverPolicyTestService(t, settings)
	account := &Account{ID: 9002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	before := time.Now()

	require.True(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"bad gateway"}}`)))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))

	require.True(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"bad gateway"}}`)))
	value, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	until, ok := value.(time.Time)
	require.True(t, ok)
	require.WithinDuration(t, before.Add(30*time.Second), until, 2*time.Second)
}

func TestGatewayFailoverPolicy_SuccessClearsHTTP5xxCounter(t *testing.T) {
	settings := *DefaultGatewayFailoverPolicySettings()
	updateGatewayFailoverRule(t, &settings, "openai_http_5xx_threshold", func(rule *GatewayFailoverRule) {
		rule.Match.Consecutive.Threshold = 2
		rule.Match.Consecutive.WindowSeconds = 60
		rule.Action.JitterPercent = 0
	})
	svc := newOpenAIFailoverPolicyTestService(t, settings)
	account := &Account{ID: 9003, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	require.True(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusInternalServerError, http.Header{}, nil))
	svc.clearOpenAIConsecutiveFailures(account)
	require.True(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusInternalServerError, http.Header{}, nil))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestGatewayFailoverPolicy_TransportThresholdBlocksAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := *DefaultGatewayFailoverPolicySettings()
	updateGatewayFailoverRule(t, &settings, "openai_transport_threshold", func(rule *GatewayFailoverRule) {
		rule.Match.Consecutive.Threshold = 2
		rule.Match.Consecutive.WindowSeconds = 60
		rule.Action.CooldownSeconds = 25
		rule.Action.JitterPercent = 0
	})
	svc := newOpenAIFailoverPolicyTestService(t, settings)
	account := &Account{ID: 9004, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	err := errors.New("context deadline exceeded while awaiting headers")

	firstErr := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, err, false)
	var firstFailover *UpstreamFailoverError
	require.ErrorAs(t, firstErr, &firstFailover)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))

	secondErr := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, err, false)
	var secondFailover *UpstreamFailoverError
	require.ErrorAs(t, secondErr, &secondFailover)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}
