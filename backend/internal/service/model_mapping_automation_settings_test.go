package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeModelMappingAutomationSettings(t *testing.T) {
	settings := normalizeModelMappingAutomationSettings(&ModelMappingAutomationSettings{
		BatchTestConcurrency: 99,
		Rules: []ModelMappingAutoRule{
			{Enabled: true, From: " DeepSeek-V4-Pro ", To: " deepseek-v4-pro ", Source: " manual "},
			{Enabled: false, From: "DeepSeek-V4-Pro", To: "deepseek-v4-pro"},
			{Enabled: true, From: "", To: "target"},
			{Enabled: true, From: "source", To: ""},
			{Enabled: true, From: "Qwen3-Max", To: "qwen3-max"},
		},
	})

	require.Equal(t, modelBatchTestMaxConcurrency, settings.BatchTestConcurrency)
	require.Equal(t, []ModelMappingAutoRule{
		{Enabled: true, From: "DeepSeek-V4-Pro", To: "deepseek-v4-pro", Source: "manual"},
		{Enabled: true, From: "Qwen3-Max", To: "qwen3-max"},
	}, settings.Rules)
}

func TestNormalizeModelMappingAutomationSettingsDefaultConcurrency(t *testing.T) {
	settings := normalizeModelMappingAutomationSettings(&ModelMappingAutomationSettings{
		BatchTestConcurrency: 0,
		Rules:                []ModelMappingAutoRule{},
	})

	require.Equal(t, modelBatchTestDefaultConcurrency, settings.BatchTestConcurrency)
}
