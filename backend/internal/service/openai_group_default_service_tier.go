package service

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeOpenAIGroupDefaultServiceTier(raw string) string {
	return normalizedOpenAIServiceTierValue(raw)
}

func openAIGroupDefaultServiceTier(group *Group) string {
	if group == nil || group.Platform != PlatformOpenAI {
		return ""
	}
	return normalizeOpenAIGroupDefaultServiceTier(group.OpenAIDefaultServiceTier)
}

func applyOpenAIGroupDefaultServiceTierToBody(body []byte, group *Group) ([]byte, string, error) {
	defaultTier := openAIGroupDefaultServiceTier(group)
	if defaultTier == "" || len(body) == 0 {
		return body, "", nil
	}
	if gjson.GetBytes(body, "service_tier").Exists() {
		return body, "", nil
	}
	updated, err := sjson.SetBytes(body, "service_tier", defaultTier)
	if err != nil {
		return body, "", fmt.Errorf("inject openai default service_tier: %w", err)
	}
	return updated, defaultTier, nil
}

func applyOpenAIGroupDefaultServiceTierToWSResponseCreate(frame []byte, group *Group) ([]byte, string, error) {
	if len(frame) == 0 || !gjson.ValidBytes(frame) {
		return frame, "", nil
	}
	if strings.TrimSpace(gjson.GetBytes(frame, "type").String()) != "response.create" {
		return frame, "", nil
	}
	return applyOpenAIGroupDefaultServiceTierToBody(frame, group)
}
