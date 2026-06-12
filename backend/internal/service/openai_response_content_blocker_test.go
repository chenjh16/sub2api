package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newOpenAIContentBlockerTestService(t *testing.T, settings *GatewayContentBlockerSettings) *OpenAIGatewayService {
	t.Helper()
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	if settings != nil {
		raw, err := json.Marshal(settings)
		require.NoError(t, err)
		repo.values[SettingKeyGatewayContentBlockerSettings] = string(raw)
	}
	return &OpenAIGatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
}

func TestGetGatewayContentBlockerSettings_DefaultsWhenNotSet(t *testing.T) {
	svc := NewSettingService(&openAIFastPolicyRepoStub{values: map[string]string{}}, &config.Config{})

	settings, err := svc.GetGatewayContentBlockerSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Empty(t, settings.Keywords)
	require.Equal(t, 10, settings.CooldownMinutes)
	require.Equal(t, 65536, settings.MaxScanBytes)
}

func TestSetGatewayContentBlockerSettings_NormalizesKeywords(t *testing.T) {
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})

	err := svc.SetGatewayContentBlockerSettings(context.Background(), &GatewayContentBlockerSettings{
		Enabled:         true,
		Keywords:        []string{" 当前繁忙 ", "", "当前繁忙", "站点维护中"},
		CooldownMinutes: 10,
		MaxScanBytes:    65536,
	})
	require.NoError(t, err)

	settings, err := svc.GetGatewayContentBlockerSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"当前繁忙", "站点维护中"}, settings.Keywords)
}

func TestSetGatewayContentBlockerSettings_EnabledRejectsOutOfRange(t *testing.T) {
	svc := NewSettingService(&openAIFastPolicyRepoStub{values: map[string]string{}}, &config.Config{})

	err := svc.SetGatewayContentBlockerSettings(context.Background(), &GatewayContentBlockerSettings{
		Enabled:         true,
		Keywords:        []string{"当前繁忙"},
		CooldownMinutes: 0,
		MaxScanBytes:    65536,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cooldown_minutes")

	err = svc.SetGatewayContentBlockerSettings(context.Background(), &GatewayContentBlockerSettings{
		Enabled:         true,
		Keywords:        []string{"当前繁忙"},
		CooldownMinutes: 10,
		MaxScanBytes:    1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_scan_bytes")
}

func TestOpenAI200ContentBlockerDetectsJSONMessage(t *testing.T) {
	svc := newOpenAIContentBlockerTestService(t, &GatewayContentBlockerSettings{
		Enabled:         true,
		Keywords:        []string{"当前繁忙，休息十分钟"},
		CooldownMinutes: 10,
		MaxScanBytes:    65536,
	})

	detector := svc.newOpenAI200ContentBlockerDetector(context.Background())
	matched, keyword := detector.ObservePayload([]byte(`{"choices":[{"delta":{"content":"当前繁忙，休息十分钟，tg频道：https://t.me/UniverseFederation"}}]}`))

	require.True(t, matched)
	require.Equal(t, "当前繁忙，休息十分钟", keyword)
}

func TestOpenAI200ContentBlockerDetectsSplitStreamingText(t *testing.T) {
	svc := newOpenAIContentBlockerTestService(t, &GatewayContentBlockerSettings{
		Enabled:         true,
		Keywords:        []string{"站点维护中"},
		CooldownMinutes: 10,
		MaxScanBytes:    65536,
	})

	detector := svc.newOpenAI200ContentBlockerDetector(context.Background())
	matched, _ := detector.ObservePayload([]byte(`{"type":"response.output_text.delta","delta":"站点"}`))
	require.False(t, matched)

	matched, keyword := detector.ObservePayload([]byte(`{"type":"response.output_text.delta","delta":"维护中"}`))
	require.True(t, matched)
	require.Equal(t, "站点维护中", keyword)
}

func TestOpenAI200ContentBlockerDisabledDoesNotMatch(t *testing.T) {
	svc := newOpenAIContentBlockerTestService(t, &GatewayContentBlockerSettings{
		Enabled:         false,
		Keywords:        []string{"当前繁忙"},
		CooldownMinutes: 10,
		MaxScanBytes:    65536,
	})

	detector := svc.newOpenAI200ContentBlockerDetector(context.Background())
	matched, _ := detector.ObservePayload([]byte(`{"message":"当前繁忙"}`))
	require.False(t, matched)
}
