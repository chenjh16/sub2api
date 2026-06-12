package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAI200ContentBlockerFailoverMessage = "OpenAI upstream 200 response matched failover policy"

type openAI200ContentRuleMatch struct {
	decision openAIFailoverRuleDecision
	event    openAIFailoverRuleEvent
}

type openAI200ContentBlockerDetector struct {
	rules            []GatewayFailoverRule
	headers          http.Header
	maxScanBytes     int
	scannedTextBytes int
	scannedBodyBytes int
	textBuffer       strings.Builder
	bodyBuffer       strings.Builder
	done             bool
}

func (s *OpenAIGatewayService) newOpenAI200ContentBlockerDetector(ctx context.Context, headers http.Header) *openAI200ContentBlockerDetector {
	if s == nil {
		return &openAI200ContentBlockerDetector{}
	}
	settings := s.openAIGatewayFailoverPolicySettings(ctx)
	if settings == nil || len(settings.Rules) == 0 {
		return &openAI200ContentBlockerDetector{}
	}

	rules := make([]GatewayFailoverRule, 0, len(settings.Rules))
	maxScanBytes := 0
	for _, rule := range settings.Rules {
		if !openAI200ContentFailoverRuleCandidate(rule) {
			continue
		}
		rules = append(rules, rule)
		if ruleMax := openAI200ContentRuleMaxScanBytes(rule); ruleMax > maxScanBytes {
			maxScanBytes = ruleMax
		}
	}
	if len(rules) == 0 {
		return &openAI200ContentBlockerDetector{}
	}
	return &openAI200ContentBlockerDetector{
		rules:        rules,
		headers:      headers.Clone(),
		maxScanBytes: maxScanBytes,
	}
}

func openAI200ContentFailoverRuleCandidate(rule GatewayFailoverRule) bool {
	if !rule.Enabled || rule.Event != GatewayFailoverRuleEventHTTPResponse {
		return false
	}
	if !matchesGatewayFailoverStatus(rule.Match, http.StatusOK) {
		return false
	}
	return rule.Match.JSONConditionGroup != nil ||
		rule.Match.MessageConditionGroup != nil ||
		rule.Match.BodyConditionGroup != nil
}

func openAI200ContentRuleMaxScanBytes(rule GatewayFailoverRule) int {
	maxScanBytes := rule.Match.MaxScanBytes
	if maxScanBytes <= 0 {
		return gatewayFailoverPolicyDefaultScanBytes
	}
	if maxScanBytes < gatewayFailoverPolicyMinScanBytes {
		return gatewayFailoverPolicyMinScanBytes
	}
	if maxScanBytes > gatewayFailoverPolicyMaxScanBytes {
		return gatewayFailoverPolicyMaxScanBytes
	}
	return maxScanBytes
}

func (d *openAI200ContentBlockerDetector) ObservePayload(payload []byte) *openAI200ContentRuleMatch {
	if d == nil || d.done || len(d.rules) == 0 || d.maxScanBytes <= 0 {
		return nil
	}
	d.observeBody(payload)
	d.observeText(openAI200ContentBlockerExtractText(payload))
	if d.bodyBuffer.Len() == 0 && d.textBuffer.Len() == 0 {
		return nil
	}
	return d.match()
}

func (d *openAI200ContentBlockerDetector) observeBody(payload []byte) {
	if d == nil || len(payload) == 0 || d.scannedBodyBytes >= d.maxScanBytes {
		return
	}
	remaining := d.maxScanBytes - d.scannedBodyBytes
	if len(payload) > remaining {
		payload = payload[:remaining]
	}
	d.scannedBodyBytes += len(payload)
	d.bodyBuffer.Write(payload)
}

func (d *openAI200ContentBlockerDetector) observeText(text string) {
	if d == nil || text == "" || d.scannedTextBytes >= d.maxScanBytes {
		return
	}
	remaining := d.maxScanBytes - d.scannedTextBytes
	if len(text) > remaining {
		text = text[:remaining]
	}
	d.scannedTextBytes += len(text)
	d.textBuffer.WriteString(text)
}

func (d *openAI200ContentBlockerDetector) match() *openAI200ContentRuleMatch {
	if d == nil || d.done {
		return nil
	}
	text := d.textBuffer.String()
	body := d.bodyBuffer.String()
	for _, rule := range d.rules {
		maxScanBytes := openAI200ContentRuleMaxScanBytes(rule)
		event := openAIFailoverRuleEvent{
			Event:           GatewayFailoverRuleEventHTTPResponse,
			StatusCode:      http.StatusOK,
			Headers:         d.headers,
			UpstreamMessage: openAI200ContentScanPrefix(text, maxScanBytes),
			Body:            []byte(openAI200ContentScanPrefix(body, maxScanBytes)),
		}
		if matchesOpenAIFailoverRule(rule, event) {
			d.done = true
			return &openAI200ContentRuleMatch{
				decision: openAIFailoverRuleDecision{
					Rule:     rule,
					Failover: rule.Action.Failover,
				},
				event: event,
			}
		}
	}
	if d.scannedBodyBytes >= d.maxScanBytes && d.scannedTextBytes >= d.maxScanBytes {
		d.done = true
	}
	return nil
}

func openAI200ContentScanPrefix(raw string, maxBytes int) string {
	if maxBytes <= 0 || len(raw) <= maxBytes {
		return raw
	}
	return string([]byte(raw)[:maxBytes])
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

func (s *OpenAIGatewayService) checkOpenAI200ContentBlocker(ctx context.Context, c *gin.Context, account *Account, headers http.Header, upstreamRequestID string, payload []byte) *UpstreamFailoverError {
	detector := s.newOpenAI200ContentBlockerDetector(ctx, headers)
	match := detector.ObservePayload(payload)
	if match == nil || !match.decision.Failover {
		return nil
	}
	return s.newOpenAI200ContentBlockerFailoverError(ctx, c, account, upstreamRequestID, match)
}

func (s *OpenAIGatewayService) newOpenAI200ContentBlockerFailoverError(ctx context.Context, c *gin.Context, account *Account, upstreamRequestID string, match *openAI200ContentRuleMatch) *UpstreamFailoverError {
	if match == nil || !match.decision.Failover {
		return nil
	}
	event := match.event
	event.Account = account
	if s != nil {
		s.applyOpenAIFailoverRuleSideEffects(ctx, account, event, match.decision.Rule)
	}

	message := openAI200ContentBlockerFailoverMessage
	if ruleID := strings.TrimSpace(match.decision.Rule.ID); ruleID != "" {
		message = fmt.Sprintf("%s: %s", message, ruleID)
	}
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
