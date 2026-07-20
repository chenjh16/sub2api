package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeModelMappingAutomationSettings(t *testing.T) {
	settings := normalizeModelMappingAutomationSettings(&ModelMappingAutomationSettings{
		BatchTestConcurrency: 99,
		Rules: []ModelMappingAutoRule{
			{Enabled: true, From: " deepseek-v4-pro ", To: " DeepSeek-V4-Pro ", Source: " manual "},
			{Enabled: false, From: "deepseek-v4-pro", To: "DeepSeek-V4-Pro"},
			{Enabled: true, From: "deepseek-ai/deepseek-v4-flash", To: "deepseek-v4-flash", Source: "auto_discovered"},
			{Enabled: true, From: "Qwen3-Max", To: "qwen3-max", Source: "auto_discovered"},
			{Enabled: true, From: "", To: "target"},
			{Enabled: true, From: "source", To: ""},
		},
	})

	require.Equal(t, modelBatchTestMaxConcurrency, settings.BatchTestConcurrency)
	require.Equal(t, []ModelMappingAutoRule{
		{Enabled: true, From: "deepseek-v4-pro", To: "DeepSeek-V4-Pro", Source: "manual"},
		{Enabled: true, From: "deepseek-v4-flash", To: "deepseek-ai/deepseek-v4-flash", Source: "auto_discovered"},
		{Enabled: true, From: "qwen3-max", To: "Qwen3-Max", Source: "auto_discovered"},
	}, settings.Rules)
}

func TestNormalizeModelMappingAutomationSettingsPreservesCaseSensitiveRules(t *testing.T) {
	settings := normalizeModelMappingAutomationSettings(&ModelMappingAutomationSettings{
		BatchTestConcurrency: 3,
		Rules: []ModelMappingAutoRule{
			{Enabled: true, From: "DeepSeek-V4-Pro", To: "DeepSeek-V4-Pro"},
			{Enabled: true, From: "deepseek-v4-pro", To: "DeepSeek-V4-Pro"},
		},
	})

	require.Len(t, settings.Rules, 2)
}

func TestNormalizeModelMappingAutomationSettingsDefaultConcurrency(t *testing.T) {
	settings := normalizeModelMappingAutomationSettings(&ModelMappingAutomationSettings{
		BatchTestConcurrency: 0,
		Rules:                []ModelMappingAutoRule{},
	})

	require.Equal(t, modelBatchTestDefaultConcurrency, settings.BatchTestConcurrency)
}
