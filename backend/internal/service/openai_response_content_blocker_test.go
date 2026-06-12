package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newOpenAIContentFailoverTestService(t *testing.T, settings GatewayFailoverPolicySettings) *OpenAIGatewayService {
	t.Helper()
	return newOpenAIFailoverPolicyTestService(t, settings)
}

func gatewayFailoverPolicyWith200ContentRule(t *testing.T, enabled bool, keywords ...string) GatewayFailoverPolicySettings {
	t.Helper()
	settings := *DefaultGatewayFailoverPolicySettings()
	updateGatewayFailoverRule(t, &settings, "openai_200_content_text", func(rule *GatewayFailoverRule) {
		rule.Enabled = enabled
		rule.Match.MaxScanBytes = gatewayFailoverPolicyDefaultScanBytes
		rule.Action.CooldownSeconds = 30
		rule.Action.JitterPercent = 0
		conditions := make([]GatewayFailoverValueCondition, 0, len(keywords))
		for _, keyword := range keywords {
			conditions = append(conditions, GatewayFailoverValueCondition{
				Op:    GatewayFailoverRuleOpContains,
				Value: keyword,
			})
		}
		rule.Match.MessageConditionGroup = &GatewayFailoverValueConditionGroup{
			Logic:      GatewayFailoverRuleLogicAny,
			Conditions: conditions,
		}
	})
	return settings
}

func TestOpenAI200ContentRuleDefaultDisabledDoesNotMatch(t *testing.T) {
	svc := newOpenAIContentFailoverTestService(t, *DefaultGatewayFailoverPolicySettings())

	detector := svc.newOpenAI200ContentBlockerDetector(context.Background(), nil)
	match := detector.ObservePayload([]byte(`{"message":"当前繁忙，休息十分钟"}`))

	require.Nil(t, match)
}

func TestOpenAI200ContentRuleDetectsJSONMessage(t *testing.T) {
	settings := gatewayFailoverPolicyWith200ContentRule(t, true, "当前繁忙，休息十分钟")
	svc := newOpenAIContentFailoverTestService(t, settings)

	detector := svc.newOpenAI200ContentBlockerDetector(context.Background(), nil)
	match := detector.ObservePayload([]byte(`{"choices":[{"delta":{"content":"当前繁忙，休息十分钟，tg频道：https://t.me/UniverseFederation"}}]}`))

	require.NotNil(t, match)
	require.True(t, match.decision.Failover)
	require.Equal(t, "openai_200_content_text", match.decision.Rule.ID)
	require.Equal(t, http.StatusOK, match.event.StatusCode)
	require.Contains(t, match.event.UpstreamMessage, "当前繁忙，休息十分钟")
}

func TestOpenAI200ContentRuleDetectsSplitStreamingText(t *testing.T) {
	settings := gatewayFailoverPolicyWith200ContentRule(t, true, "站点维护中")
	svc := newOpenAIContentFailoverTestService(t, settings)

	detector := svc.newOpenAI200ContentBlockerDetector(context.Background(), nil)
	require.Nil(t, detector.ObservePayload([]byte(`{"type":"response.output_text.delta","delta":"站点"}`)))

	match := detector.ObservePayload([]byte(`{"type":"response.output_text.delta","delta":"维护中"}`))
	require.NotNil(t, match)
	require.Equal(t, "openai_200_content_text", match.decision.Rule.ID)
}

func TestOpenAI200ContentRuleFailoverAppliesCooldown(t *testing.T) {
	settings := gatewayFailoverPolicyWith200ContentRule(t, true, "公益服务器压力很大")
	svc := newOpenAIContentFailoverTestService(t, settings)
	account := &Account{ID: 9101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	before := time.Now()

	failoverErr := svc.checkOpenAI200ContentBlocker(
		context.Background(),
		nil,
		account,
		http.Header{"X-Request-Id": []string{"req-content-blocked"}},
		"req-content-blocked",
		[]byte(`{"message":"公益服务器压力很大，休息十分钟换key开放"}`),
	)

	require.NotNil(t, failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	value, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	until, ok := value.(time.Time)
	require.True(t, ok)
	require.WithinDuration(t, before.Add(30*time.Second), until, 2*time.Second)
}
