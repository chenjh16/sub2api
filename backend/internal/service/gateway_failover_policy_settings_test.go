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
	require.Len(t, settings.Rules, 7)
	require.Equal(t, "openai_structured_400_cooldown", settings.Rules[0].ID)
	require.NotNil(t, settings.Rules[0].Match.JSONConditionGroup)
	require.Equal(t, GatewayFailoverRuleLogicAny, settings.Rules[0].Match.JSONConditionGroup.Logic)
	require.Equal(t, 3, len(settings.Rules[0].Match.JSONConditionGroup.Conditions))
	require.Equal(t, int(openAIUpstreamCooldownFallback/time.Second), settings.Rules[0].Action.CooldownSeconds)
	require.Equal(t, "openai_request_too_large_tier_limit", settings.Rules[2].ID)
	require.Equal(t, []int{http.StatusRequestEntityTooLarge}, settings.Rules[2].Match.StatusCodes)
	require.NotNil(t, settings.Rules[2].Match.JSONConditionGroup)
	require.True(t, settings.Rules[2].Action.ClearSessionBinding)
	require.Equal(t, "openai_get_channel_failed_overloaded", settings.Rules[3].ID)
	require.Equal(t, 130, settings.Rules[3].Priority)
	require.Equal(t, gatewayFailoverPolicyGetChannelFailedCooldownSec, settings.Rules[3].Action.CooldownSeconds)
	require.True(t, settings.Rules[3].Action.ClearSessionBinding)
	require.Equal(t, "openai_http_5xx_threshold", settings.Rules[4].ID)
	require.Equal(t, gatewayFailoverPolicyDefaultHTTP5xxThreshold, settings.Rules[4].Match.Consecutive.Threshold)
	require.Equal(t, gatewayFailoverPolicyDefaultHTTP5xxCooldownSecond, settings.Rules[4].Action.CooldownSeconds)
	require.Equal(t, "openai_transport_threshold", settings.Rules[5].ID)
	require.Equal(t, gatewayFailoverPolicyDefaultTransportThreshold, settings.Rules[5].Match.Consecutive.Threshold)
	require.Equal(t, "openai_200_content_text", settings.Rules[6].ID)
	require.False(t, settings.Rules[6].Enabled)
	require.Equal(t, []int{http.StatusOK}, settings.Rules[6].Match.StatusCodes)
	require.Equal(t, gatewayFailoverPolicyDefaultScanBytes, settings.Rules[6].Match.MaxScanBytes)
	require.NotNil(t, settings.Rules[6].Match.MessageConditionGroup)
	require.Equal(t, GatewayFailoverRuleLogicAny, settings.Rules[6].Match.MessageConditionGroup.Logic)
}

func TestSetGatewayFailoverPolicySettings_RoundTrip(t *testing.T) {
	repo := newGatewayFailoverPolicySettingRepo()
	svc := NewSettingService(repo, &config.Config{})
	want := DefaultGatewayFailoverPolicySettings()
	updateGatewayFailoverRule(t, want, "openai_structured_400_rpm", func(rule *GatewayFailoverRule) {
		rule.Enabled = false
		rule.Action.JitterPercent = 0
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

func TestGatewayFailoverPolicy_ConditionGroupsSupportNestedLogic(t *testing.T) {
	rule := GatewayFailoverRule{
		ID:       "nested_http",
		Enabled:  true,
		Event:    GatewayFailoverRuleEventHTTPResponse,
		Priority: 1,
		Match: GatewayFailoverRuleMatch{
			StatusCodes: []int{http.StatusBadRequest},
			JSONConditionGroup: &GatewayFailoverJSONConditionGroup{
				Logic: GatewayFailoverRuleLogicAll,
				Conditions: []GatewayFailoverJSONCondition{
					{Paths: []string{"error.code", "code"}, Op: GatewayFailoverRuleOpEquals, Value: "rate_limit_exceeded"},
				},
				Groups: []GatewayFailoverJSONConditionGroup{
					{
						Logic: GatewayFailoverRuleLogicAny,
						Conditions: []GatewayFailoverJSONCondition{
							{Path: "limit_type", Op: GatewayFailoverRuleOpEquals, Value: "rpm"},
							{Path: "error.limit_type", Op: GatewayFailoverRuleOpEquals, Value: "cooldown"},
						},
					},
				},
			},
			HeaderConditionGroup: &GatewayFailoverHeaderConditionGroup{
				Logic: GatewayFailoverRuleLogicAny,
				Conditions: []GatewayFailoverHeaderCondition{
					{Name: "x-upstream", Op: GatewayFailoverRuleOpContains, Value: "ikun"},
					{Name: "x-fallback", Op: GatewayFailoverRuleOpExists},
				},
			},
			MessageConditionGroup: &GatewayFailoverValueConditionGroup{
				Logic: GatewayFailoverRuleLogicAny,
				Conditions: []GatewayFailoverValueCondition{
					{Op: GatewayFailoverRuleOpContains, Value: "当前繁忙"},
					{Op: GatewayFailoverRuleOpContains, Value: "公益服务器压力很大"},
				},
			},
			BodyConditionGroup: &GatewayFailoverValueConditionGroup{
				Logic: GatewayFailoverRuleLogicAll,
				Conditions: []GatewayFailoverValueCondition{
					{Op: GatewayFailoverRuleOpContains, Value: "UniverseFederation"},
					{Op: GatewayFailoverRuleOpNotContains, Value: "fatal"},
				},
			},
		},
		Action: GatewayFailoverRuleAction{Failover: true},
	}

	event := openAIFailoverRuleEvent{
		Event:           GatewayFailoverRuleEventHTTPResponse,
		StatusCode:      http.StatusBadRequest,
		Headers:         http.Header{"X-Upstream": []string{"AI-ikun886"}},
		UpstreamMessage: "当前繁忙，休息十分钟",
		Body:            []byte(`{"error":{"code":"rate_limit_exceeded"},"limit_type":"rpm","message":"TG https://t.me/UniverseFederation"}`),
	}
	require.True(t, matchesOpenAIFailoverRule(rule, event))

	event.Body = []byte(`{"error":{"code":"rate_limit_exceeded"},"limit_type":"tpm","message":"TG https://t.me/UniverseFederation"}`)
	require.False(t, matchesOpenAIFailoverRule(rule, event))

	transportRule := GatewayFailoverRule{
		ID:      "nested_transport",
		Enabled: true,
		Event:   GatewayFailoverRuleEventTransportError,
		Match: GatewayFailoverRuleMatch{
			TransportConditionGroup: &GatewayFailoverValueConditionGroup{
				Logic: GatewayFailoverRuleLogicAny,
				Conditions: []GatewayFailoverValueCondition{
					{Op: GatewayFailoverRuleOpContains, Value: "timeout"},
					{Op: GatewayFailoverRuleOpRegex, Value: "connection.*reset"},
				},
			},
		},
		Action: GatewayFailoverRuleAction{Failover: true},
	}
	require.True(t, matchesOpenAIFailoverRule(transportRule, openAIFailoverRuleEvent{
		Event:          GatewayFailoverRuleEventTransportError,
		TransportError: "context deadline exceeded: timeout",
	}))
	require.False(t, matchesOpenAIFailoverRule(transportRule, openAIFailoverRuleEvent{
		Event:          GatewayFailoverRuleEventTransportError,
		TransportError: "certificate expired",
	}))
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

func TestGatewayFailoverPolicy_RequestTooLargeTierLimitFailsOverAndClearsSession(t *testing.T) {
	settings := *DefaultGatewayFailoverPolicySettings()
	updateGatewayFailoverRule(t, &settings, "openai_request_too_large_tier_limit", func(rule *GatewayFailoverRule) {
		rule.Action.JitterPercent = 0
	})
	svc := newOpenAIFailoverPolicyTestService(t, settings)
	sessionHash := "request-large-session"
	groupID := int64(42)
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 9005},
	}
	svc.cache = cache
	account := &Account{ID: 9005, Name: "tier-0", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"code":"request_too_large","limit_bytes":5242880,"message":"Request body exceeds your tier limit (5MB for tier 0). Please upgrade your plan or split the context.","tier":0,"type":"invalid_request_error"}}`)
	ctx := WithOpenAIForwardSession(context.Background(), &groupID, sessionHash)

	require.True(t, svc.shouldFailoverOpenAIUpstreamResponseWithContext(ctx, account, http.StatusRequestEntityTooLarge, http.Header{}, "", body))
	require.True(t, svc.handleOpenAIAccountUpstreamError(ctx, account, http.StatusRequestEntityTooLarge, http.Header{}, body))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, 1, cache.deletedSessions["openai:"+sessionHash])
	_, exists := cache.sessionBindings["openai:"+sessionHash]
	require.False(t, exists)
}

func TestGatewayFailoverPolicy_GetChannelFailedOverloadedCoolsForOneHour(t *testing.T) {
	settings := *DefaultGatewayFailoverPolicySettings()
	updateGatewayFailoverRule(t, &settings, "openai_get_channel_failed_overloaded", func(rule *GatewayFailoverRule) {
		rule.Action.JitterPercent = 0
	})
	svc := newOpenAIFailoverPolicyTestService(t, settings)
	sessionHash := "anyrouter-overloaded-session"
	groupID := int64(42)
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 30},
	}
	svc.cache = cache
	account := &Account{ID: 30, Name: "API-Anyrouter-OpenAI", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"code":"get_channel_failed","message":"当前模型 gpt-5.5 负载已经达到上限，请稍后重试 (request id: 20260616000048338386674Tqai8xjr)","param":"","type":"new_api_error"}}`)
	ctx := WithOpenAIForwardSession(context.Background(), &groupID, sessionHash)
	before := time.Now()

	require.True(t, svc.shouldFailoverOpenAIUpstreamResponseWithContext(ctx, account, http.StatusInternalServerError, http.Header{}, "", body))
	require.True(t, svc.handleOpenAIAccountUpstreamError(ctx, account, http.StatusInternalServerError, http.Header{}, body))
	value, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	until, ok := value.(time.Time)
	require.True(t, ok)
	require.WithinDuration(t, before.Add(time.Hour), until, 2*time.Second)
	require.Equal(t, 1, cache.deletedSessions["openai:"+sessionHash])
	_, exists := cache.sessionBindings["openai:"+sessionHash]
	require.False(t, exists)
}

func TestGatewayFailoverPolicy_GetChannelFailedRequiresOverloadMessage(t *testing.T) {
	svc := newOpenAIFailoverPolicyTestService(t, *DefaultGatewayFailoverPolicySettings())
	account := &Account{ID: 30, Name: "API-Anyrouter-OpenAI", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"code":"get_channel_failed","message":"no available channel","param":"","type":"new_api_error"}}`)

	require.True(t, svc.shouldFailoverOpenAIUpstreamResponseWithContext(context.Background(), account, http.StatusInternalServerError, http.Header{}, "", body))
	require.True(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusInternalServerError, http.Header{}, body))
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
