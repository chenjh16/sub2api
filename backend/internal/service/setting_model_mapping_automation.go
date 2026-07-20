package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	modelMappingAutoRulesDefaultJSON       = "[]"
	modelMappingAutoRulesMaxCount          = 1000
	modelMappingAutoRuleMaxModelNameLength = 256
	modelBatchTestDefaultConcurrency       = 3
	modelBatchTestMinConcurrency           = 1
	modelBatchTestMaxConcurrency           = 10
)

type ModelMappingAutoRule struct {
	Enabled   bool   `json:"enabled"`
	From      string `json:"from"`
	To        string `json:"to"`
	Source    string `json:"source,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type ModelMappingAutomationSettings struct {
	Rules                []ModelMappingAutoRule `json:"rules"`
	BatchTestConcurrency int                    `json:"batch_test_concurrency"`
}

func DefaultModelMappingAutomationSettings() *ModelMappingAutomationSettings {
	return &ModelMappingAutomationSettings{
		Rules:                []ModelMappingAutoRule{},
		BatchTestConcurrency: modelBatchTestDefaultConcurrency,
	}
}

func normalizeModelBatchTestConcurrency(value int) int {
	if value < modelBatchTestMinConcurrency {
		return modelBatchTestDefaultConcurrency
	}
	if value > modelBatchTestMaxConcurrency {
		return modelBatchTestMaxConcurrency
	}
	return value
}

func normalizeModelMappingAutoRule(rule ModelMappingAutoRule) (ModelMappingAutoRule, bool) {
	rule.From = strings.TrimSpace(rule.From)
	rule.To = strings.TrimSpace(rule.To)
	rule.Source = strings.TrimSpace(rule.Source)
	rule.UpdatedAt = strings.TrimSpace(rule.UpdatedAt)
	if rule.From == "" || rule.To == "" {
		return ModelMappingAutoRule{}, false
	}
	if len(rule.From) > modelMappingAutoRuleMaxModelNameLength {
		rule.From = rule.From[:modelMappingAutoRuleMaxModelNameLength]
	}
	if len(rule.To) > modelMappingAutoRuleMaxModelNameLength {
		rule.To = rule.To[:modelMappingAutoRuleMaxModelNameLength]
	}
	rule = normalizeLegacyAutoDiscoveredRuleDirection(rule)
	return rule, true
}

func normalizeLegacyAutoDiscoveredRuleDirection(rule ModelMappingAutoRule) ModelMappingAutoRule {
	if !strings.EqualFold(rule.Source, "auto_discovered") {
		return rule
	}

	actualModel := rule.From
	requestModel := ""
	if slashIndex := strings.LastIndex(actualModel, "/"); slashIndex > 0 && slashIndex < len(actualModel)-1 {
		requestModel = strings.ToLower(strings.TrimSpace(actualModel[slashIndex+1:]))
	} else if lowercase := strings.ToLower(actualModel); lowercase != actualModel {
		requestModel = lowercase
	}
	if requestModel == "" || requestModel != rule.To {
		return rule
	}

	rule.From = requestModel
	rule.To = actualModel
	return rule
}

func normalizeModelMappingAutomationSettings(settings *ModelMappingAutomationSettings) *ModelMappingAutomationSettings {
	if settings == nil {
		return DefaultModelMappingAutomationSettings()
	}
	normalized := &ModelMappingAutomationSettings{
		Rules:                make([]ModelMappingAutoRule, 0, len(settings.Rules)),
		BatchTestConcurrency: normalizeModelBatchTestConcurrency(settings.BatchTestConcurrency),
	}
	seen := make(map[string]struct{}, len(settings.Rules))
	for _, rule := range settings.Rules {
		clean, ok := normalizeModelMappingAutoRule(rule)
		if !ok {
			continue
		}
		key := clean.From + "\x00" + clean.To
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized.Rules = append(normalized.Rules, clean)
		if len(normalized.Rules) >= modelMappingAutoRulesMaxCount {
			break
		}
	}
	return normalized
}

func (s *SettingService) GetModelMappingAutomationSettings(ctx context.Context) (*ModelMappingAutomationSettings, error) {
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyModelMappingAutoRules,
		SettingKeyModelBatchTestConcurrency,
	})
	if err != nil {
		return nil, fmt.Errorf("get model mapping automation settings: %w", err)
	}

	settings := DefaultModelMappingAutomationSettings()
	if raw := strings.TrimSpace(values[SettingKeyModelMappingAutoRules]); raw != "" {
		var rules []ModelMappingAutoRule
		if err := json.Unmarshal([]byte(raw), &rules); err == nil {
			settings.Rules = rules
		}
	}
	if raw := strings.TrimSpace(values[SettingKeyModelBatchTestConcurrency]); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			settings.BatchTestConcurrency = n
		}
	}
	return normalizeModelMappingAutomationSettings(settings), nil
}

func (s *SettingService) SetModelMappingAutomationSettings(ctx context.Context, settings *ModelMappingAutomationSettings) error {
	normalized := normalizeModelMappingAutomationSettings(settings)
	rulesJSON, err := json.Marshal(normalized.Rules)
	if err != nil {
		return fmt.Errorf("marshal model mapping auto rules: %w", err)
	}
	return s.settingRepo.SetMultiple(ctx, map[string]string{
		SettingKeyModelMappingAutoRules:     string(rulesJSON),
		SettingKeyModelBatchTestConcurrency: strconv.Itoa(normalized.BatchTestConcurrency),
	})
}
