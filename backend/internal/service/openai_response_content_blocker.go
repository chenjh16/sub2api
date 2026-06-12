package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAI200ContentBlockerFailoverMessage = "OpenAI upstream 200 response matched content blocker"

type openAI200ContentBlockerDetector struct {
	enabled       bool
	keywords      []string
	lowerKeywords []string
	maxScanBytes  int
	scannedBytes  int
	buffer        strings.Builder
}

func (s *OpenAIGatewayService) newOpenAI200ContentBlockerDetector(ctx context.Context) *openAI200ContentBlockerDetector {
	if s == nil || s.settingService == nil {
		return &openAI200ContentBlockerDetector{}
	}
	settings := s.settingService.GetGatewayContentBlockerSettingsCached(ctx)
	if settings == nil || !settings.Enabled || len(settings.Keywords) == 0 {
		return &openAI200ContentBlockerDetector{}
	}
	maxScanBytes := settings.MaxScanBytes
	if maxScanBytes <= 0 {
		maxScanBytes = DefaultGatewayContentBlockerSettings().MaxScanBytes
	}
	keywords := make([]string, 0, len(settings.Keywords))
	lowerKeywords := make([]string, 0, len(settings.Keywords))
	for _, raw := range settings.Keywords {
		keyword := strings.TrimSpace(raw)
		if keyword == "" {
			continue
		}
		keywords = append(keywords, keyword)
		lowerKeywords = append(lowerKeywords, strings.ToLower(keyword))
	}
	if len(keywords) == 0 {
		return &openAI200ContentBlockerDetector{}
	}
	return &openAI200ContentBlockerDetector{
		enabled:       true,
		keywords:      keywords,
		lowerKeywords: lowerKeywords,
		maxScanBytes:  maxScanBytes,
	}
}

func (d *openAI200ContentBlockerDetector) ObservePayload(payload []byte) (bool, string) {
	if d == nil || !d.enabled || len(d.lowerKeywords) == 0 || d.scannedBytes >= d.maxScanBytes {
		return false, ""
	}
	text := openAI200ContentBlockerExtractText(payload)
	return d.observeText(text)
}

func (d *openAI200ContentBlockerDetector) observeText(text string) (bool, string) {
	if d == nil || !d.enabled || text == "" || d.scannedBytes >= d.maxScanBytes {
		return false, ""
	}
	remaining := d.maxScanBytes - d.scannedBytes
	if len(text) > remaining {
		text = text[:remaining]
	}
	d.scannedBytes += len(text)
	d.buffer.WriteString(text)

	haystack := strings.ToLower(d.buffer.String())
	for i, keyword := range d.lowerKeywords {
		if strings.Contains(haystack, keyword) {
			return true, d.keywords[i]
		}
	}
	return false, ""
}

func openAI200ContentBlockerExtractText(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return ""
	}
	if bytes.Contains(trimmed, []byte("data:")) || bytes.Contains(trimmed, []byte("event:")) {
		var b strings.Builder
		forEachOpenAISSEDataPayload(string(trimmed), func(data []byte) {
			text := openAI200ContentBlockerExtractText(data)
			if text == "" {
				return
			}
			b.WriteString(text)
		})
		if b.Len() > 0 {
			return b.String()
		}
	}
	if gjson.ValidBytes(trimmed) {
		if text := openAI200ContentBlockerExtractKnownJSONText(trimmed); text != "" {
			return text
		}
	}

	var decoded any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return string(trimmed)
	}
	var b strings.Builder
	openAI200ContentBlockerAppendJSONStrings(&b, decoded)
	if b.Len() == 0 {
		return string(trimmed)
	}
	return b.String()
}

func openAI200ContentBlockerExtractKnownJSONText(payload []byte) string {
	var b strings.Builder
	for _, path := range []string{
		"delta",
		"text",
		"content",
		"message",
		"error.message",
		"response.error.message",
		"choices.#.delta.content",
		"choices.#.message.content",
		"choices.#.text",
		"output.#.content.#.text",
		"output.#.content.#.content",
		"item.content.#.text",
		"item.content.#.content",
		"response.output.#.content.#.text",
		"response.output.#.content.#.content",
	} {
		appendGJSONStrings(&b, gjson.GetBytes(payload, path))
	}
	return b.String()
}

func appendGJSONStrings(b *strings.Builder, value gjson.Result) {
	if !value.Exists() {
		return
	}
	if value.IsArray() {
		for _, item := range value.Array() {
			appendGJSONStrings(b, item)
		}
		return
	}
	if value.IsObject() {
		for _, item := range value.Map() {
			appendGJSONStrings(b, item)
		}
		return
	}
	if value.Type == gjson.String {
		b.WriteString(value.String())
	}
}

func openAI200ContentBlockerAppendJSONStrings(b *strings.Builder, value any) {
	switch v := value.(type) {
	case string:
		b.WriteString(v)
	case []any:
		for _, item := range v {
			openAI200ContentBlockerAppendJSONStrings(b, item)
		}
	case map[string]any:
		for _, item := range v {
			openAI200ContentBlockerAppendJSONStrings(b, item)
		}
	}
}

func (s *OpenAIGatewayService) checkOpenAI200ContentBlocker(ctx context.Context, c *gin.Context, account *Account, upstreamRequestID string, payload []byte) *UpstreamFailoverError {
	detector := s.newOpenAI200ContentBlockerDetector(ctx)
	matched, keyword := detector.ObservePayload(payload)
	if !matched {
		return nil
	}
	return s.newOpenAI200ContentBlockerFailoverError(c, account, upstreamRequestID, keyword)
}

func (s *OpenAIGatewayService) newOpenAI200ContentBlockerFailoverError(c *gin.Context, account *Account, upstreamRequestID string, _ string) *UpstreamFailoverError {
	cooldownMinutes := DefaultGatewayContentBlockerSettings().CooldownMinutes
	if s != nil && s.settingService != nil {
		if settings := s.settingService.GetGatewayContentBlockerSettingsCached(context.Background()); settings != nil && settings.CooldownMinutes > 0 {
			cooldownMinutes = settings.CooldownMinutes
		}
	}
	if cooldownMinutes < gatewayContentBlockerMinCooldownMinutes {
		cooldownMinutes = gatewayContentBlockerMinCooldownMinutes
	}
	if cooldownMinutes > gatewayContentBlockerMaxCooldownMinutes {
		cooldownMinutes = gatewayContentBlockerMaxCooldownMinutes
	}
	if s != nil && account != nil {
		s.BlockAccountScheduling(account, time.Now().Add(time.Duration(cooldownMinutes)*time.Minute), "content_blocker")
	}

	message := openAI200ContentBlockerFailoverMessage
	if c != nil {
		setOpsUpstreamError(c, http.StatusBadGateway, message, "")
		event := OpsUpstreamErrorEvent{
			Platform:           PlatformOpenAI,
			UpstreamStatusCode: http.StatusOK,
			UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
			Kind:               "failover",
			Message:            message,
		}
		if account != nil {
			event.Platform = account.Platform
			event.AccountID = account.ID
			event.AccountName = account.Name
		}
		appendOpsUpstreamError(c, event)
	}

	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"message": "Upstream request failed",
			"code":    "content_blocked",
		},
	})
	return &UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: body,
	}
}

func openAI200ContentBlockerMatchedAfterOutputError() error {
	return fmt.Errorf("openai upstream 200 response matched content blocker after client output started")
}
