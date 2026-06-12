package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"golang.org/x/sync/singleflight"
)

// cachedVersionBounds 缓存 Claude Code 版本号上下限（进程内缓存，60s TTL）
type cachedVersionBounds struct {
	min       string // 空字符串 = 不检查
	max       string // 空字符串 = 不检查
	expiresAt int64  // unix nano
}

// versionBoundsCache 版本号上下限进程内缓存
var versionBoundsCache atomic.Value // *cachedVersionBounds

// versionBoundsSF 防止缓存过期时 thundering herd
var versionBoundsSF singleflight.Group

// versionBoundsCacheTTL 缓存有效期
const versionBoundsCacheTTL = 60 * time.Second

// versionBoundsErrorTTL DB 错误时的短缓存，快速重试
const versionBoundsErrorTTL = 5 * time.Second

// versionBoundsDBTimeout singleflight 内 DB 查询超时，独立于请求 context
const versionBoundsDBTimeout = 5 * time.Second

// cachedBackendMode Backend Mode cache (in-process, 60s TTL)
type cachedBackendMode struct {
	value     bool
	expiresAt int64 // unix nano
}

var backendModeCache atomic.Value // *cachedBackendMode
var backendModeSF singleflight.Group

const backendModeCacheTTL = 60 * time.Second
const backendModeErrorTTL = 5 * time.Second
const backendModeDBTimeout = 5 * time.Second

// cachedGatewayForwardingSettings 缓存网关转发行为设置（进程内缓存，60s TTL）
type cachedGatewayForwardingSettings struct {
	fingerprintUnification           bool
	metadataPassthrough              bool
	cchSigning                       bool
	claudeOAuthSystemPromptInjection bool
	claudeOAuthSystemPrompt          string
	claudeOAuthSystemPromptBlocks    string
	anthropicCacheTTL1hInjection     bool
	rewriteMessageCacheControl       bool
	clientDatelineNormalization      bool
	expiresAt                        int64 // unix nano
}

var gatewayForwardingCache atomic.Value // *cachedGatewayForwardingSettings
var gatewayForwardingSF singleflight.Group

const gatewayForwardingCacheTTL = 60 * time.Second
const gatewayForwardingErrorTTL = 5 * time.Second
const gatewayForwardingDBTimeout = 5 * time.Second

// cachedAntigravityUserAgentVersion 缓存 Antigravity UA 版本号（进程内缓存，60s TTL）
type cachedAntigravityUserAgentVersion struct {
	version   string
	expiresAt int64 // unix nano
}

const antigravityUserAgentVersionCacheTTL = 60 * time.Second
const antigravityUserAgentVersionErrorTTL = 5 * time.Second
const antigravityUserAgentVersionDBTimeout = 5 * time.Second

// DefaultOpenAICodexUserAgent OpenAI Codex 默认 User-Agent（用于规避 Cloudflare 对浏览器 UA 的质询）
const DefaultOpenAICodexUserAgent = "codex-tui/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.144.1)"

// cachedOpenAICodexUserAgent 缓存 OpenAI Codex UA（进程内缓存，60s TTL）
type cachedOpenAICodexUserAgent struct {
	value     string
	expiresAt int64 // unix nano
}

type cachedOpenAIQuotaAutoPauseSettings struct {
	settings  OpsOpenAIAccountQuotaAutoPauseSettings
	expiresAt int64
}

type cachedGatewayFailoverPolicySettings struct {
	settings  *GatewayFailoverPolicySettings
	expiresAt int64 // unix nano
}

type cachedGatewayContentBlockerSettings struct {
	settings  *GatewayContentBlockerSettings
	expiresAt int64 // unix nano
}

const openAICodexUserAgentCacheTTL = 60 * time.Second
const openAICodexUserAgentErrorTTL = 5 * time.Second
const openAICodexUserAgentDBTimeout = 5 * time.Second

const codexRestrictionPolicyCacheTTL = 60 * time.Second
const codexRestrictionPolicyDBTimeout = 5 * time.Second

// cachedCodexRestrictionPolicy codex_cli_only 全局加固策略缓存（进程内，60s TTL）。
// GetCodexRestrictionPolicy 在每个 codex_cli_only 账号的网关请求热路径上被调用，避免每次访问 DB。
type cachedCodexRestrictionPolicy struct {
	value     CodexRestrictionPolicy
	expiresAt int64 // unix nano
}

// cachedCyberSessionBlockRuntime cyber 会话屏蔽开关+TTL 进程内缓存（60s TTL）。
// GetCyberSessionBlockRuntime 在网关请求热路径上被调用，避免每次访问 DB。
type cachedCyberSessionBlockRuntime struct {
	enabled   bool
	ttl       time.Duration
	expiresAt int64 // unix nano
}

const cyberSessionBlockRuntimeCacheTTL = 60 * time.Second
const cyberSessionBlockRuntimeErrorTTL = 5 * time.Second
const cyberSessionBlockRuntimeDBTimeout = 5 * time.Second

const gatewayFailoverPolicySettingsCacheTTL = 60 * time.Second
const gatewayFailoverPolicySettingsErrorTTL = 5 * time.Second
const gatewayFailoverPolicySettingsDBTimeout = 5 * time.Second

const gatewayContentBlockerSettingsCacheTTL = 60 * time.Second
const gatewayContentBlockerSettingsErrorTTL = 5 * time.Second
const gatewayContentBlockerSettingsDBTimeout = 5 * time.Second

const openAIQuotaAutoPauseSettingsCacheTTL = 60 * time.Second
const openAIQuotaAutoPauseSettingsErrorTTL = 5 * time.Second
const openAIQuotaAutoPauseSettingsDBTimeout = 5 * time.Second

const openAIQuotaAutoPauseSettingsRefreshKey = "openai_quota_auto_pause_settings"

const (
	gatewayFailoverPolicyMinStructured400CooldownMinutes = 1
	gatewayFailoverPolicyMaxStructured400CooldownMinutes = 720
	gatewayFailoverPolicyMinJitterPercent                = 0
	gatewayFailoverPolicyMaxJitterPercent                = 100
	gatewayFailoverPolicyMinThreshold                    = 1
	gatewayFailoverPolicyMaxThreshold                    = 20
	gatewayFailoverPolicyMinWindowSeconds                = 1
	gatewayFailoverPolicyMaxWindowSeconds                = 3600
	gatewayFailoverPolicyMinCooldownSeconds              = 1
	gatewayFailoverPolicyMaxCooldownSeconds              = 7200
	gatewayFailoverPolicyMaxRules                        = 50
	gatewayFailoverPolicyMaxConditions                   = 50
	gatewayFailoverPolicyMaxConditionGroupDepth          = 8
	gatewayFailoverPolicyMinScanBytes                    = 1024
	gatewayFailoverPolicyMaxScanBytes                    = 1024 * 1024
	gatewayFailoverPolicyDefaultScanBytes                = 65536
	gatewayFailoverPolicyMaxStringBytes                  = 512
	gatewayFailoverPolicyDefaultJitterPercent            = 20
	gatewayFailoverPolicyDefaultHTTP5xxThreshold         = 3
	gatewayFailoverPolicyDefaultHTTP5xxWindowSeconds     = 30
	gatewayFailoverPolicyDefaultHTTP5xxCooldownSecond    = 120
	gatewayFailoverPolicyDefaultTransportThreshold       = 3
	gatewayFailoverPolicyDefaultTransportWindowSecond    = 30
	gatewayFailoverPolicyDefaultTransportCooldownSec     = 120
)

func gatewayFailoverBoolPtr(v bool) *bool {
	return &v
}

func defaultGatewayFailoverRulesFromLegacy(settings *GatewayFailoverPolicySettings) []GatewayFailoverRule {
	if settings == nil {
		settings = &GatewayFailoverPolicySettings{
			Structured400Enabled:         true,
			Structured400CooldownMinutes: 10,
			FailureCooldownJitterPercent: 20,
			HTTP5xxCooldownEnabled:       true,
			HTTP5xxThreshold:             3,
			HTTP5xxWindowSeconds:         30,
			HTTP5xxCooldownSeconds:       120,
			TransportCooldownEnabled:     true,
			TransportThreshold:           3,
			TransportWindowSeconds:       30,
			TransportCooldownSeconds:     120,
		}
	}
	structuredCooldownSeconds := settings.Structured400CooldownMinutes * 60
	if structuredCooldownSeconds <= 0 {
		structuredCooldownSeconds = 600
	}
	http5xxCooldownSeconds := settings.HTTP5xxCooldownSeconds
	if http5xxCooldownSeconds <= 0 {
		http5xxCooldownSeconds = 120
	}
	transportCooldownSeconds := settings.TransportCooldownSeconds
	if transportCooldownSeconds <= 0 {
		transportCooldownSeconds = 120
	}
	jitter := settings.FailureCooldownJitterPercent

	return []GatewayFailoverRule{
		{
			ID:          "openai_structured_400_cooldown",
			Name:        "结构化 400 冷却",
			Description: "匹配 rate_limit_cooldown 或 limit_type=cooldown 的结构化 400 响应",
			Enabled:     settings.Structured400Enabled,
			Priority:    100,
			Event:       GatewayFailoverRuleEventHTTPResponse,
			Match: GatewayFailoverRuleMatch{
				StatusCodes: []int{http.StatusBadRequest},
				JSONLogic:   GatewayFailoverRuleLogicAny,
				JSONConditions: []GatewayFailoverJSONCondition{
					{Path: "error.code", Op: GatewayFailoverRuleOpEquals, Value: "rate_limit_cooldown"},
					{Path: "code", Op: GatewayFailoverRuleOpEquals, Value: "rate_limit_cooldown"},
					{Path: "limit_type", Op: GatewayFailoverRuleOpEquals, Value: "cooldown"},
				},
			},
			Action: GatewayFailoverRuleAction{
				Failover:        true,
				CooldownScope:   GatewayFailoverCooldownScopeRuntime,
				CooldownSeconds: structuredCooldownSeconds,
				JitterPercent:   jitter,
				Reason:          "rate_limit_cooldown",
			},
		},
		{
			ID:          "openai_structured_400_rpm",
			Name:        "结构化 400 RPM 限流",
			Description: "匹配 rate_limit_exceeded 且 limit_type=rpm 的结构化 400 响应",
			Enabled:     settings.Structured400Enabled,
			Priority:    110,
			Event:       GatewayFailoverRuleEventHTTPResponse,
			Match: GatewayFailoverRuleMatch{
				StatusCodes: []int{http.StatusBadRequest},
				JSONLogic:   GatewayFailoverRuleLogicAll,
				JSONConditions: []GatewayFailoverJSONCondition{
					{Paths: []string{"error.code", "code"}, Op: GatewayFailoverRuleOpEquals, Value: "rate_limit_exceeded"},
					{Paths: []string{"limit_type", "error.limit_type"}, Op: GatewayFailoverRuleOpEquals, Value: "rpm"},
				},
			},
			Action: GatewayFailoverRuleAction{
				Failover:        true,
				CooldownScope:   GatewayFailoverCooldownScopeRuntime,
				CooldownSeconds: structuredCooldownSeconds,
				JitterPercent:   jitter,
				Reason:          "rate_limit_exceeded_rpm",
			},
		},
		{
			ID:          "openai_http_5xx_threshold",
			Name:        "连续 HTTP 5xx",
			Description: "OpenAI 上游连续 5xx 时自动 failover，并在达到阈值后短冷却",
			Enabled:     settings.HTTP5xxCooldownEnabled,
			Priority:    200,
			Event:       GatewayFailoverRuleEventHTTPResponse,
			Match: GatewayFailoverRuleMatch{
				StatusRanges:       []GatewayFailoverStatusRange{{Min: 500, Max: 599}},
				ExcludeStatusCodes: []int{529},
				Consecutive: &GatewayFailoverConsecutiveCondition{
					Enabled:       true,
					Threshold:     settings.HTTP5xxThreshold,
					WindowSeconds: settings.HTTP5xxWindowSeconds,
				},
			},
			Action: GatewayFailoverRuleAction{
				Failover:        true,
				CooldownScope:   GatewayFailoverCooldownScopeRuntime,
				CooldownSeconds: http5xxCooldownSeconds,
				JitterPercent:   jitter,
				Reason:          "http_5xx_threshold",
			},
		},
		{
			ID:          "openai_transport_threshold",
			Name:        "连续瞬时网络错误",
			Description: "OpenAI 上游连续超时、TLS 或网络错误时 failover，并在达到阈值后短冷却",
			Enabled:     settings.TransportCooldownEnabled,
			Priority:    300,
			Event:       GatewayFailoverRuleEventTransportError,
			Match: GatewayFailoverRuleMatch{
				TransportPersistent: gatewayFailoverBoolPtr(false),
				Consecutive: &GatewayFailoverConsecutiveCondition{
					Enabled:       true,
					Threshold:     settings.TransportThreshold,
					WindowSeconds: settings.TransportWindowSeconds,
				},
			},
			Action: GatewayFailoverRuleAction{
				Failover:        true,
				CooldownScope:   GatewayFailoverCooldownScopeRuntime,
				CooldownSeconds: transportCooldownSeconds,
				JitterPercent:   jitter,
				Reason:          "transport_threshold",
			},
		},
	}
}

func defaultGatewayFailoverRules() []GatewayFailoverRule {
	settings := &GatewayFailoverPolicySettings{
		Structured400Enabled:         true,
		Structured400CooldownMinutes: 10,
		FailureCooldownJitterPercent: 20,
		HTTP5xxCooldownEnabled:       true,
		HTTP5xxThreshold:             3,
		HTTP5xxWindowSeconds:         30,
		HTTP5xxCooldownSeconds:       120,
		TransportCooldownEnabled:     true,
		TransportThreshold:           3,
		TransportWindowSeconds:       30,
		TransportCooldownSeconds:     120,
	}
	rules := defaultGatewayFailoverRulesFromLegacy(settings)
	for i := range rules {
		match := &rules[i].Match
		if len(match.JSONConditions) > 0 {
			match.JSONConditionGroup = &GatewayFailoverJSONConditionGroup{
				Logic:      normalizeGatewayFailoverRuleLogic(match.JSONLogic),
				Conditions: append([]GatewayFailoverJSONCondition(nil), match.JSONConditions...),
			}
		}
		if len(match.HeaderConditions) > 0 {
			match.HeaderConditionGroup = &GatewayFailoverHeaderConditionGroup{
				Logic:      normalizeGatewayFailoverRuleLogic(match.HeaderLogic),
				Conditions: append([]GatewayFailoverHeaderCondition(nil), match.HeaderConditions...),
			}
		}
		if len(match.MessageConditions) > 0 {
			match.MessageConditionGroup = &GatewayFailoverValueConditionGroup{
				Logic:      GatewayFailoverRuleLogicAll,
				Conditions: cloneGatewayFailoverValueConditions(match.MessageConditions),
			}
		}
		if len(match.BodyConditions) > 0 {
			match.BodyConditionGroup = &GatewayFailoverValueConditionGroup{
				Logic:      GatewayFailoverRuleLogicAll,
				Conditions: cloneGatewayFailoverValueConditions(match.BodyConditions),
			}
		}
		if len(match.TransportConditions) > 0 {
			match.TransportConditionGroup = &GatewayFailoverValueConditionGroup{
				Logic:      GatewayFailoverRuleLogicAll,
				Conditions: cloneGatewayFailoverValueConditions(match.TransportConditions),
			}
		}
		clearGatewayFailoverFlatConditions(match)
	}
	rules = append(rules, GatewayFailoverRule{
		ID:          "openai_200_content_text",
		Name:        "200 内容公告文本",
		Description: "识别伪装成 200 成功响应的维护、繁忙或公告文本",
		Enabled:     false,
		Priority:    400,
		Event:       GatewayFailoverRuleEventHTTPResponse,
		Match: GatewayFailoverRuleMatch{
			StatusCodes:  []int{http.StatusOK},
			MaxScanBytes: gatewayFailoverPolicyDefaultScanBytes,
			MessageConditionGroup: &GatewayFailoverValueConditionGroup{
				Logic: GatewayFailoverRuleLogicAny,
				Conditions: []GatewayFailoverValueCondition{
					{Op: GatewayFailoverRuleOpContains, Value: "当前繁忙，休息十分钟"},
					{Op: GatewayFailoverRuleOpContains, Value: "公益服务器压力很大"},
					{Op: GatewayFailoverRuleOpContains, Value: "api.ranmeng.icu 提示：站点维护中"},
				},
			},
		},
		Action: GatewayFailoverRuleAction{
			Failover:        true,
			CooldownScope:   GatewayFailoverCooldownScopeRuntime,
			CooldownSeconds: int(openAIUpstreamCooldownFallback / time.Second),
			JitterPercent:   0,
			Reason:          "content_blocker",
		},
	})
	return rules
}

func clearGatewayFailoverFlatConditions(match *GatewayFailoverRuleMatch) {
	if match == nil {
		return
	}
	match.JSONLogic = ""
	match.JSONConditions = nil
	match.HeaderLogic = ""
	match.HeaderConditions = nil
	match.MessageConditions = nil
	match.BodyConditions = nil
	match.TransportConditions = nil
}

func cloneGatewayFailoverPolicySettings(settings *GatewayFailoverPolicySettings) *GatewayFailoverPolicySettings {
	if settings == nil {
		return DefaultGatewayFailoverPolicySettings()
	}
	cloned := *settings
	cloned.Rules = cloneGatewayFailoverRules(settings.Rules)
	return &cloned
}

func cloneGatewayFailoverRules(rules []GatewayFailoverRule) []GatewayFailoverRule {
	if rules == nil {
		return nil
	}
	cloned := make([]GatewayFailoverRule, len(rules))
	for i := range rules {
		cloned[i] = rules[i]
		cloned[i].Match.StatusCodes = append([]int(nil), rules[i].Match.StatusCodes...)
		cloned[i].Match.StatusRanges = append([]GatewayFailoverStatusRange(nil), rules[i].Match.StatusRanges...)
		cloned[i].Match.ExcludeStatusCodes = append([]int(nil), rules[i].Match.ExcludeStatusCodes...)
		cloned[i].Match.JSONConditions = append([]GatewayFailoverJSONCondition(nil), rules[i].Match.JSONConditions...)
		for j := range cloned[i].Match.JSONConditions {
			cloned[i].Match.JSONConditions[j].Paths = append([]string(nil), rules[i].Match.JSONConditions[j].Paths...)
			cloned[i].Match.JSONConditions[j].Values = append([]string(nil), rules[i].Match.JSONConditions[j].Values...)
		}
		cloned[i].Match.HeaderConditions = append([]GatewayFailoverHeaderCondition(nil), rules[i].Match.HeaderConditions...)
		for j := range cloned[i].Match.HeaderConditions {
			cloned[i].Match.HeaderConditions[j].Values = append([]string(nil), rules[i].Match.HeaderConditions[j].Values...)
		}
		cloned[i].Match.MessageConditions = cloneGatewayFailoverValueConditions(rules[i].Match.MessageConditions)
		cloned[i].Match.BodyConditions = cloneGatewayFailoverValueConditions(rules[i].Match.BodyConditions)
		cloned[i].Match.TransportConditions = cloneGatewayFailoverValueConditions(rules[i].Match.TransportConditions)
		cloned[i].Match.JSONConditionGroup = cloneGatewayFailoverJSONConditionGroup(rules[i].Match.JSONConditionGroup)
		cloned[i].Match.HeaderConditionGroup = cloneGatewayFailoverHeaderConditionGroup(rules[i].Match.HeaderConditionGroup)
		cloned[i].Match.MessageConditionGroup = cloneGatewayFailoverValueConditionGroup(rules[i].Match.MessageConditionGroup)
		cloned[i].Match.BodyConditionGroup = cloneGatewayFailoverValueConditionGroup(rules[i].Match.BodyConditionGroup)
		cloned[i].Match.TransportConditionGroup = cloneGatewayFailoverValueConditionGroup(rules[i].Match.TransportConditionGroup)
		if rules[i].Match.TransportPersistent != nil {
			v := *rules[i].Match.TransportPersistent
			cloned[i].Match.TransportPersistent = &v
		}
		if rules[i].Match.Consecutive != nil {
			v := *rules[i].Match.Consecutive
			cloned[i].Match.Consecutive = &v
		}
	}
	return cloned
}

func cloneGatewayFailoverValueConditions(conditions []GatewayFailoverValueCondition) []GatewayFailoverValueCondition {
	cloned := append([]GatewayFailoverValueCondition(nil), conditions...)
	for i := range cloned {
		cloned[i].Values = append([]string(nil), conditions[i].Values...)
	}
	return cloned
}

func cloneGatewayFailoverJSONConditionGroup(group *GatewayFailoverJSONConditionGroup) *GatewayFailoverJSONConditionGroup {
	if group == nil {
		return nil
	}
	cloned := &GatewayFailoverJSONConditionGroup{
		Logic:      group.Logic,
		Conditions: append([]GatewayFailoverJSONCondition(nil), group.Conditions...),
		Groups:     make([]GatewayFailoverJSONConditionGroup, len(group.Groups)),
	}
	for i := range cloned.Conditions {
		cloned.Conditions[i].Paths = append([]string(nil), group.Conditions[i].Paths...)
		cloned.Conditions[i].Values = append([]string(nil), group.Conditions[i].Values...)
	}
	for i := range group.Groups {
		nested := cloneGatewayFailoverJSONConditionGroup(&group.Groups[i])
		if nested != nil {
			cloned.Groups[i] = *nested
		}
	}
	return cloned
}

func cloneGatewayFailoverHeaderConditionGroup(group *GatewayFailoverHeaderConditionGroup) *GatewayFailoverHeaderConditionGroup {
	if group == nil {
		return nil
	}
	cloned := &GatewayFailoverHeaderConditionGroup{
		Logic:      group.Logic,
		Conditions: append([]GatewayFailoverHeaderCondition(nil), group.Conditions...),
		Groups:     make([]GatewayFailoverHeaderConditionGroup, len(group.Groups)),
	}
	for i := range cloned.Conditions {
		cloned.Conditions[i].Values = append([]string(nil), group.Conditions[i].Values...)
	}
	for i := range group.Groups {
		nested := cloneGatewayFailoverHeaderConditionGroup(&group.Groups[i])
		if nested != nil {
			cloned.Groups[i] = *nested
		}
	}
	return cloned
}

func cloneGatewayFailoverValueConditionGroup(group *GatewayFailoverValueConditionGroup) *GatewayFailoverValueConditionGroup {
	if group == nil {
		return nil
	}
	cloned := &GatewayFailoverValueConditionGroup{
		Logic:      group.Logic,
		Conditions: cloneGatewayFailoverValueConditions(group.Conditions),
		Groups:     make([]GatewayFailoverValueConditionGroup, len(group.Groups)),
	}
	for i := range group.Groups {
		nested := cloneGatewayFailoverValueConditionGroup(&group.Groups[i])
		if nested != nil {
			cloned.Groups[i] = *nested
		}
	}
	return cloned
}

func normalizeGatewayFailoverPolicyMatchMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "first":
		return "first"
	default:
		return "first"
	}
}

func normalizeGatewayFailoverRuleID(raw string, fallback string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	lastSep := false
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			_ = b.WriteByte(byte(r))
			lastSep = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !lastSep && b.Len() > 0 {
				_ = b.WriteByte('_')
				lastSep = true
			}
		}
	}
	id := strings.Trim(b.String(), "_")
	if id == "" {
		id = fallback
	}
	if id == "" {
		id = "rule"
	}
	return id
}

func trimGatewayFailoverString(raw string) string {
	raw = strings.TrimSpace(raw)
	if len([]byte(raw)) <= gatewayFailoverPolicyMaxStringBytes {
		return raw
	}
	return strings.TrimSpace(string([]byte(raw)[:gatewayFailoverPolicyMaxStringBytes]))
}

func normalizeGatewayFailoverRuleLogic(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), GatewayFailoverRuleLogicAny) {
		return GatewayFailoverRuleLogicAny
	}
	return GatewayFailoverRuleLogicAll
}

func normalizeGatewayFailoverRuleOp(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case GatewayFailoverRuleOpNotEquals:
		return GatewayFailoverRuleOpNotEquals
	case GatewayFailoverRuleOpContains:
		return GatewayFailoverRuleOpContains
	case GatewayFailoverRuleOpNotContains:
		return GatewayFailoverRuleOpNotContains
	case GatewayFailoverRuleOpExists:
		return GatewayFailoverRuleOpExists
	case GatewayFailoverRuleOpNotExists:
		return GatewayFailoverRuleOpNotExists
	case GatewayFailoverRuleOpIn:
		return GatewayFailoverRuleOpIn
	case GatewayFailoverRuleOpRegex:
		return GatewayFailoverRuleOpRegex
	default:
		return GatewayFailoverRuleOpEquals
	}
}

func normalizeGatewayFailoverCooldownScope(raw string, cooldownSeconds int) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case GatewayFailoverCooldownScopeRuntime:
		return GatewayFailoverCooldownScopeRuntime
	case GatewayFailoverCooldownScopeTempUnsched:
		return GatewayFailoverCooldownScopeTempUnsched
	case GatewayFailoverCooldownScopeNone:
		return GatewayFailoverCooldownScopeNone
	default:
		if cooldownSeconds > 0 {
			return GatewayFailoverCooldownScopeRuntime
		}
		return GatewayFailoverCooldownScopeNone
	}
}

func normalizeGatewayFailoverValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, raw := range values {
		if value := trimGatewayFailoverString(raw); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func normalizeGatewayFailoverValueCondition(condition GatewayFailoverValueCondition) GatewayFailoverValueCondition {
	condition.Op = normalizeGatewayFailoverRuleOp(condition.Op)
	condition.Value = trimGatewayFailoverString(condition.Value)
	condition.Values = normalizeGatewayFailoverValues(condition.Values)
	return condition
}

func normalizeGatewayFailoverJSONCondition(condition GatewayFailoverJSONCondition) GatewayFailoverJSONCondition {
	condition.Path = trimGatewayFailoverString(condition.Path)
	paths := make([]string, 0, len(condition.Paths))
	seen := map[string]struct{}{}
	if condition.Path != "" {
		seen[condition.Path] = struct{}{}
	}
	for _, raw := range condition.Paths {
		path := trimGatewayFailoverString(raw)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	condition.Paths = paths
	condition.Op = normalizeGatewayFailoverRuleOp(condition.Op)
	condition.Value = trimGatewayFailoverString(condition.Value)
	condition.Values = normalizeGatewayFailoverValues(condition.Values)
	return condition
}

func normalizeGatewayFailoverHeaderCondition(condition GatewayFailoverHeaderCondition) GatewayFailoverHeaderCondition {
	condition.Name = trimGatewayFailoverString(condition.Name)
	condition.Op = normalizeGatewayFailoverRuleOp(condition.Op)
	condition.Value = trimGatewayFailoverString(condition.Value)
	condition.Values = normalizeGatewayFailoverValues(condition.Values)
	return condition
}

func normalizeGatewayFailoverJSONConditionGroup(group *GatewayFailoverJSONConditionGroup, strict bool, ruleID string, depth int) (*GatewayFailoverJSONConditionGroup, error) {
	if group == nil {
		return nil, nil
	}
	if depth > gatewayFailoverPolicyMaxConditionGroupDepth {
		if strict {
			return nil, fmt.Errorf("rule %s json condition groups exceed max depth %d", ruleID, gatewayFailoverPolicyMaxConditionGroupDepth)
		}
		return nil, nil
	}
	normalized := &GatewayFailoverJSONConditionGroup{Logic: normalizeGatewayFailoverRuleLogic(group.Logic)}
	conditions := group.Conditions
	if len(conditions) > gatewayFailoverPolicyMaxConditions {
		if strict {
			return nil, fmt.Errorf("rule %s json conditions must contain at most %d items", ruleID, gatewayFailoverPolicyMaxConditions)
		}
		conditions = conditions[:gatewayFailoverPolicyMaxConditions]
	}
	for _, condition := range conditions {
		condition = normalizeGatewayFailoverJSONCondition(condition)
		if condition.Path != "" || len(condition.Paths) > 0 {
			normalized.Conditions = append(normalized.Conditions, condition)
		}
	}
	groups := group.Groups
	if len(groups) > gatewayFailoverPolicyMaxConditions {
		if strict {
			return nil, fmt.Errorf("rule %s json condition groups must contain at most %d items", ruleID, gatewayFailoverPolicyMaxConditions)
		}
		groups = groups[:gatewayFailoverPolicyMaxConditions]
	}
	for i := range groups {
		nested, err := normalizeGatewayFailoverJSONConditionGroup(&groups[i], strict, ruleID, depth+1)
		if err != nil {
			return nil, err
		}
		if nested != nil && (len(nested.Conditions) > 0 || len(nested.Groups) > 0) {
			normalized.Groups = append(normalized.Groups, *nested)
		}
	}
	if len(normalized.Conditions) == 0 && len(normalized.Groups) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func normalizeGatewayFailoverHeaderConditionGroup(group *GatewayFailoverHeaderConditionGroup, strict bool, ruleID string, depth int) (*GatewayFailoverHeaderConditionGroup, error) {
	if group == nil {
		return nil, nil
	}
	if depth > gatewayFailoverPolicyMaxConditionGroupDepth {
		if strict {
			return nil, fmt.Errorf("rule %s header condition groups exceed max depth %d", ruleID, gatewayFailoverPolicyMaxConditionGroupDepth)
		}
		return nil, nil
	}
	normalized := &GatewayFailoverHeaderConditionGroup{Logic: normalizeGatewayFailoverRuleLogic(group.Logic)}
	conditions := group.Conditions
	if len(conditions) > gatewayFailoverPolicyMaxConditions {
		if strict {
			return nil, fmt.Errorf("rule %s header conditions must contain at most %d items", ruleID, gatewayFailoverPolicyMaxConditions)
		}
		conditions = conditions[:gatewayFailoverPolicyMaxConditions]
	}
	for _, condition := range conditions {
		condition = normalizeGatewayFailoverHeaderCondition(condition)
		if condition.Name != "" {
			normalized.Conditions = append(normalized.Conditions, condition)
		}
	}
	for i := range group.Groups {
		nested, err := normalizeGatewayFailoverHeaderConditionGroup(&group.Groups[i], strict, ruleID, depth+1)
		if err != nil {
			return nil, err
		}
		if nested != nil && (len(nested.Conditions) > 0 || len(nested.Groups) > 0) {
			normalized.Groups = append(normalized.Groups, *nested)
		}
	}
	if len(normalized.Conditions) == 0 && len(normalized.Groups) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func normalizeGatewayFailoverValueConditionGroup(group *GatewayFailoverValueConditionGroup, strict bool, ruleID, field string, depth int) (*GatewayFailoverValueConditionGroup, error) {
	if group == nil {
		return nil, nil
	}
	if depth > gatewayFailoverPolicyMaxConditionGroupDepth {
		if strict {
			return nil, fmt.Errorf("rule %s %s condition groups exceed max depth %d", ruleID, field, gatewayFailoverPolicyMaxConditionGroupDepth)
		}
		return nil, nil
	}
	normalized := &GatewayFailoverValueConditionGroup{Logic: normalizeGatewayFailoverRuleLogic(group.Logic)}
	conditions := group.Conditions
	if len(conditions) > gatewayFailoverPolicyMaxConditions {
		if strict {
			return nil, fmt.Errorf("rule %s %s conditions must contain at most %d items", ruleID, field, gatewayFailoverPolicyMaxConditions)
		}
		conditions = conditions[:gatewayFailoverPolicyMaxConditions]
	}
	for _, condition := range conditions {
		condition = normalizeGatewayFailoverValueCondition(condition)
		if condition.Op == GatewayFailoverRuleOpExists || condition.Op == GatewayFailoverRuleOpNotExists || condition.Value != "" || len(condition.Values) > 0 {
			normalized.Conditions = append(normalized.Conditions, condition)
		}
	}
	for i := range group.Groups {
		nested, err := normalizeGatewayFailoverValueConditionGroup(&group.Groups[i], strict, ruleID, field, depth+1)
		if err != nil {
			return nil, err
		}
		if nested != nil && (len(nested.Conditions) > 0 || len(nested.Groups) > 0) {
			normalized.Groups = append(normalized.Groups, *nested)
		}
	}
	if len(normalized.Conditions) == 0 && len(normalized.Groups) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func hasGatewayFailoverHTTPMatch(match GatewayFailoverRuleMatch) bool {
	return len(match.StatusCodes) > 0 || len(match.StatusRanges) > 0 ||
		len(match.JSONConditions) > 0 || len(match.HeaderConditions) > 0 ||
		len(match.MessageConditions) > 0 || len(match.BodyConditions) > 0 ||
		match.JSONConditionGroup != nil || match.HeaderConditionGroup != nil ||
		match.MessageConditionGroup != nil || match.BodyConditionGroup != nil
}

func normalizeGatewayFailoverRules(rules []GatewayFailoverRule, strict bool) ([]GatewayFailoverRule, error) {
	if len(rules) > gatewayFailoverPolicyMaxRules {
		if strict {
			return nil, fmt.Errorf("rules must contain at most %d items", gatewayFailoverPolicyMaxRules)
		}
		rules = rules[:gatewayFailoverPolicyMaxRules]
	}

	normalized := make([]GatewayFailoverRule, 0, len(rules))
	seenIDs := map[string]int{}
	for i, raw := range rules {
		rule := raw
		rule.ID = normalizeGatewayFailoverRuleID(rule.ID, fmt.Sprintf("rule_%d", i+1))
		if count := seenIDs[rule.ID]; count > 0 {
			base := rule.ID
			for {
				count++
				candidate := fmt.Sprintf("%s_%d", base, count)
				if _, exists := seenIDs[candidate]; !exists {
					rule.ID = candidate
					break
				}
			}
		}
		seenIDs[rule.ID]++
		rule.Name = trimGatewayFailoverString(rule.Name)
		if rule.Name == "" {
			rule.Name = rule.ID
		}
		rule.Description = trimGatewayFailoverString(rule.Description)
		if strings.EqualFold(strings.TrimSpace(rule.Event), GatewayFailoverRuleEventTransportError) {
			rule.Event = GatewayFailoverRuleEventTransportError
		} else {
			rule.Event = GatewayFailoverRuleEventHTTPResponse
		}

		match := rule.Match
		match.JSONLogic = normalizeGatewayFailoverRuleLogic(match.JSONLogic)
		match.HeaderLogic = normalizeGatewayFailoverRuleLogic(match.HeaderLogic)
		statusCodes := make([]int, 0, len(match.StatusCodes))
		for _, code := range match.StatusCodes {
			if code >= 100 && code <= 599 {
				statusCodes = append(statusCodes, code)
			}
		}
		match.StatusCodes = statusCodes
		excludeCodes := make([]int, 0, len(match.ExcludeStatusCodes))
		for _, code := range match.ExcludeStatusCodes {
			if code >= 100 && code <= 599 {
				excludeCodes = append(excludeCodes, code)
			}
		}
		match.ExcludeStatusCodes = excludeCodes
		ranges := make([]GatewayFailoverStatusRange, 0, len(match.StatusRanges))
		for _, item := range match.StatusRanges {
			if item.Min >= 100 && item.Max <= 599 && item.Min <= item.Max {
				ranges = append(ranges, item)
			}
		}
		match.StatusRanges = ranges

		if len(match.JSONConditions) > gatewayFailoverPolicyMaxConditions {
			if strict {
				return nil, fmt.Errorf("rule %s json_conditions must contain at most %d items", rule.ID, gatewayFailoverPolicyMaxConditions)
			}
			match.JSONConditions = match.JSONConditions[:gatewayFailoverPolicyMaxConditions]
		}
		jsonConditions := make([]GatewayFailoverJSONCondition, 0, len(match.JSONConditions))
		for _, condition := range match.JSONConditions {
			condition = normalizeGatewayFailoverJSONCondition(condition)
			if condition.Path != "" || len(condition.Paths) > 0 {
				jsonConditions = append(jsonConditions, condition)
			}
		}
		match.JSONConditions = jsonConditions

		if len(match.HeaderConditions) > gatewayFailoverPolicyMaxConditions {
			if strict {
				return nil, fmt.Errorf("rule %s header_conditions must contain at most %d items", rule.ID, gatewayFailoverPolicyMaxConditions)
			}
			match.HeaderConditions = match.HeaderConditions[:gatewayFailoverPolicyMaxConditions]
		}
		headerConditions := make([]GatewayFailoverHeaderCondition, 0, len(match.HeaderConditions))
		for _, condition := range match.HeaderConditions {
			condition = normalizeGatewayFailoverHeaderCondition(condition)
			if condition.Name != "" {
				headerConditions = append(headerConditions, condition)
			}
		}
		match.HeaderConditions = headerConditions

		normalizeValueConditions := func(name string, conditions []GatewayFailoverValueCondition) ([]GatewayFailoverValueCondition, error) {
			if len(conditions) > gatewayFailoverPolicyMaxConditions {
				if strict {
					return nil, fmt.Errorf("rule %s %s must contain at most %d items", rule.ID, name, gatewayFailoverPolicyMaxConditions)
				}
				conditions = conditions[:gatewayFailoverPolicyMaxConditions]
			}
			out := make([]GatewayFailoverValueCondition, 0, len(conditions))
			for _, condition := range conditions {
				condition = normalizeGatewayFailoverValueCondition(condition)
				if condition.Op == GatewayFailoverRuleOpExists || condition.Op == GatewayFailoverRuleOpNotExists || condition.Value != "" || len(condition.Values) > 0 {
					out = append(out, condition)
				}
			}
			return out, nil
		}
		var err error
		if match.MessageConditions, err = normalizeValueConditions("message_conditions", match.MessageConditions); err != nil {
			return nil, err
		}
		if match.BodyConditions, err = normalizeValueConditions("body_conditions", match.BodyConditions); err != nil {
			return nil, err
		}
		if match.TransportConditions, err = normalizeValueConditions("transport_conditions", match.TransportConditions); err != nil {
			return nil, err
		}
		if match.JSONConditionGroup == nil && len(match.JSONConditions) > 0 {
			match.JSONConditionGroup = &GatewayFailoverJSONConditionGroup{Logic: match.JSONLogic, Conditions: match.JSONConditions}
		}
		if match.HeaderConditionGroup == nil && len(match.HeaderConditions) > 0 {
			match.HeaderConditionGroup = &GatewayFailoverHeaderConditionGroup{Logic: match.HeaderLogic, Conditions: match.HeaderConditions}
		}
		if match.MessageConditionGroup == nil && len(match.MessageConditions) > 0 {
			match.MessageConditionGroup = &GatewayFailoverValueConditionGroup{Logic: GatewayFailoverRuleLogicAll, Conditions: match.MessageConditions}
		}
		if match.BodyConditionGroup == nil && len(match.BodyConditions) > 0 {
			match.BodyConditionGroup = &GatewayFailoverValueConditionGroup{Logic: GatewayFailoverRuleLogicAll, Conditions: match.BodyConditions}
		}
		if match.TransportConditionGroup == nil && len(match.TransportConditions) > 0 {
			match.TransportConditionGroup = &GatewayFailoverValueConditionGroup{Logic: GatewayFailoverRuleLogicAll, Conditions: match.TransportConditions}
		}
		if match.JSONConditionGroup, err = normalizeGatewayFailoverJSONConditionGroup(match.JSONConditionGroup, strict, rule.ID, 1); err != nil {
			return nil, err
		}
		if match.HeaderConditionGroup, err = normalizeGatewayFailoverHeaderConditionGroup(match.HeaderConditionGroup, strict, rule.ID, 1); err != nil {
			return nil, err
		}
		if match.MessageConditionGroup, err = normalizeGatewayFailoverValueConditionGroup(match.MessageConditionGroup, strict, rule.ID, "message", 1); err != nil {
			return nil, err
		}
		if match.BodyConditionGroup, err = normalizeGatewayFailoverValueConditionGroup(match.BodyConditionGroup, strict, rule.ID, "body", 1); err != nil {
			return nil, err
		}
		if match.TransportConditionGroup, err = normalizeGatewayFailoverValueConditionGroup(match.TransportConditionGroup, strict, rule.ID, "transport", 1); err != nil {
			return nil, err
		}
		clearGatewayFailoverFlatConditions(&match)
		if match.MaxScanBytes < 0 || match.MaxScanBytes > gatewayFailoverPolicyMaxScanBytes || (match.MaxScanBytes > 0 && match.MaxScanBytes < gatewayFailoverPolicyMinScanBytes) {
			if strict && rule.Enabled {
				return nil, fmt.Errorf("rule %s match.max_scan_bytes must be between %d-%d", rule.ID, gatewayFailoverPolicyMinScanBytes, gatewayFailoverPolicyMaxScanBytes)
			}
			match.MaxScanBytes = gatewayFailoverPolicyDefaultScanBytes
		}
		if match.Consecutive != nil {
			if match.Consecutive.Threshold < gatewayFailoverPolicyMinThreshold || match.Consecutive.Threshold > gatewayFailoverPolicyMaxThreshold {
				if strict && rule.Enabled && match.Consecutive.Enabled {
					return nil, fmt.Errorf("rule %s consecutive.threshold must be between %d-%d", rule.ID, gatewayFailoverPolicyMinThreshold, gatewayFailoverPolicyMaxThreshold)
				}
				match.Consecutive.Threshold = 3
			}
			if match.Consecutive.WindowSeconds < gatewayFailoverPolicyMinWindowSeconds || match.Consecutive.WindowSeconds > gatewayFailoverPolicyMaxWindowSeconds {
				if strict && rule.Enabled && match.Consecutive.Enabled {
					return nil, fmt.Errorf("rule %s consecutive.window_seconds must be between %d-%d", rule.ID, gatewayFailoverPolicyMinWindowSeconds, gatewayFailoverPolicyMaxWindowSeconds)
				}
				match.Consecutive.WindowSeconds = 30
			}
		}
		if strict && rule.Enabled && rule.Event == GatewayFailoverRuleEventHTTPResponse && !hasGatewayFailoverHTTPMatch(match) {
			return nil, fmt.Errorf("rule %s must define at least one HTTP match condition", rule.ID)
		}
		rule.Match = match

		rule.Action.CooldownScope = normalizeGatewayFailoverCooldownScope(rule.Action.CooldownScope, rule.Action.CooldownSeconds)
		if rule.Action.CooldownScope == GatewayFailoverCooldownScopeNone {
			rule.Action.CooldownSeconds = 0
		} else if rule.Action.CooldownSeconds < gatewayFailoverPolicyMinCooldownSeconds || rule.Action.CooldownSeconds > gatewayFailoverPolicyMaxCooldownSeconds {
			if strict && rule.Enabled {
				return nil, fmt.Errorf("rule %s action.cooldown_seconds must be between %d-%d", rule.ID, gatewayFailoverPolicyMinCooldownSeconds, gatewayFailoverPolicyMaxCooldownSeconds)
			}
			rule.Action.CooldownSeconds = 120
		}
		if rule.Action.JitterPercent < gatewayFailoverPolicyMinJitterPercent || rule.Action.JitterPercent > gatewayFailoverPolicyMaxJitterPercent {
			if strict && rule.Enabled {
				return nil, fmt.Errorf("rule %s action.jitter_percent must be between %d-%d", rule.ID, gatewayFailoverPolicyMinJitterPercent, gatewayFailoverPolicyMaxJitterPercent)
			}
			rule.Action.JitterPercent = 20
		}
		rule.Action.Reason = normalizeGatewayFailoverRuleID(rule.Action.Reason, rule.ID)
		normalized = append(normalized, rule)
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Priority == normalized[j].Priority {
			return normalized[i].ID < normalized[j].ID
		}
		return normalized[i].Priority < normalized[j].Priority
	})
	return normalized, nil
}

func normalizeGatewayFailoverPolicySettings(settings *GatewayFailoverPolicySettings, strict bool) (*GatewayFailoverPolicySettings, error) {
	if settings == nil {
		return nil, fmt.Errorf("settings cannot be nil")
	}

	normalized := cloneGatewayFailoverPolicySettings(settings)
	defaults := DefaultGatewayFailoverPolicySettings()
	normalized.MatchMode = normalizeGatewayFailoverPolicyMatchMode(normalized.MatchMode)

	if normalized.Structured400CooldownMinutes < gatewayFailoverPolicyMinStructured400CooldownMinutes ||
		normalized.Structured400CooldownMinutes > gatewayFailoverPolicyMaxStructured400CooldownMinutes {
		if strict && normalized.Structured400Enabled {
			return nil, fmt.Errorf("structured_400_cooldown_minutes must be between %d-%d",
				gatewayFailoverPolicyMinStructured400CooldownMinutes,
				gatewayFailoverPolicyMaxStructured400CooldownMinutes)
		}
		normalized.Structured400CooldownMinutes = defaults.Structured400CooldownMinutes
	}
	if normalized.FailureCooldownJitterPercent < gatewayFailoverPolicyMinJitterPercent ||
		normalized.FailureCooldownJitterPercent > gatewayFailoverPolicyMaxJitterPercent {
		if strict {
			return nil, fmt.Errorf("failure_cooldown_jitter_percent must be between %d-%d",
				gatewayFailoverPolicyMinJitterPercent,
				gatewayFailoverPolicyMaxJitterPercent)
		}
		normalized.FailureCooldownJitterPercent = defaults.FailureCooldownJitterPercent
	}

	if normalized.HTTP5xxThreshold < gatewayFailoverPolicyMinThreshold ||
		normalized.HTTP5xxThreshold > gatewayFailoverPolicyMaxThreshold {
		if strict && normalized.HTTP5xxCooldownEnabled {
			return nil, fmt.Errorf("http_5xx_threshold must be between %d-%d",
				gatewayFailoverPolicyMinThreshold,
				gatewayFailoverPolicyMaxThreshold)
		}
		normalized.HTTP5xxThreshold = defaults.HTTP5xxThreshold
	}
	if normalized.HTTP5xxWindowSeconds < gatewayFailoverPolicyMinWindowSeconds ||
		normalized.HTTP5xxWindowSeconds > gatewayFailoverPolicyMaxWindowSeconds {
		if strict && normalized.HTTP5xxCooldownEnabled {
			return nil, fmt.Errorf("http_5xx_window_seconds must be between %d-%d",
				gatewayFailoverPolicyMinWindowSeconds,
				gatewayFailoverPolicyMaxWindowSeconds)
		}
		normalized.HTTP5xxWindowSeconds = defaults.HTTP5xxWindowSeconds
	}
	if normalized.HTTP5xxCooldownSeconds < gatewayFailoverPolicyMinCooldownSeconds ||
		normalized.HTTP5xxCooldownSeconds > gatewayFailoverPolicyMaxCooldownSeconds {
		if strict && normalized.HTTP5xxCooldownEnabled {
			return nil, fmt.Errorf("http_5xx_cooldown_seconds must be between %d-%d",
				gatewayFailoverPolicyMinCooldownSeconds,
				gatewayFailoverPolicyMaxCooldownSeconds)
		}
		normalized.HTTP5xxCooldownSeconds = defaults.HTTP5xxCooldownSeconds
	}

	if normalized.TransportThreshold < gatewayFailoverPolicyMinThreshold ||
		normalized.TransportThreshold > gatewayFailoverPolicyMaxThreshold {
		if strict && normalized.TransportCooldownEnabled {
			return nil, fmt.Errorf("transport_threshold must be between %d-%d",
				gatewayFailoverPolicyMinThreshold,
				gatewayFailoverPolicyMaxThreshold)
		}
		normalized.TransportThreshold = defaults.TransportThreshold
	}
	if normalized.TransportWindowSeconds < gatewayFailoverPolicyMinWindowSeconds ||
		normalized.TransportWindowSeconds > gatewayFailoverPolicyMaxWindowSeconds {
		if strict && normalized.TransportCooldownEnabled {
			return nil, fmt.Errorf("transport_window_seconds must be between %d-%d",
				gatewayFailoverPolicyMinWindowSeconds,
				gatewayFailoverPolicyMaxWindowSeconds)
		}
		normalized.TransportWindowSeconds = defaults.TransportWindowSeconds
	}
	if normalized.TransportCooldownSeconds < gatewayFailoverPolicyMinCooldownSeconds ||
		normalized.TransportCooldownSeconds > gatewayFailoverPolicyMaxCooldownSeconds {
		if strict && normalized.TransportCooldownEnabled {
			return nil, fmt.Errorf("transport_cooldown_seconds must be between %d-%d",
				gatewayFailoverPolicyMinCooldownSeconds,
				gatewayFailoverPolicyMaxCooldownSeconds)
		}
		normalized.TransportCooldownSeconds = defaults.TransportCooldownSeconds
	}

	if len(normalized.Rules) == 0 {
		normalized.Rules = defaultGatewayFailoverRules()
		return normalized, nil
	}
	rules, err := normalizeGatewayFailoverRules(normalized.Rules, strict)
	if err != nil {
		return nil, err
	}
	normalized.Rules = rules
	return normalized, nil
}

// GetGatewayFailoverPolicySettings 获取网关故障转移增强策略配置。
func (s *SettingService) GetGatewayFailoverPolicySettings(ctx context.Context) (*GatewayFailoverPolicySettings, error) {
	if s == nil || s.settingRepo == nil {
		return DefaultGatewayFailoverPolicySettings(), nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyGatewayFailoverPolicySettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultGatewayFailoverPolicySettings(), nil
		}
		return nil, fmt.Errorf("get gateway failover policy settings: %w", err)
	}
	if value == "" {
		return DefaultGatewayFailoverPolicySettings(), nil
	}

	var settings GatewayFailoverPolicySettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultGatewayFailoverPolicySettings(), nil
	}
	normalized, err := normalizeGatewayFailoverPolicySettings(&settings, false)
	if err != nil {
		return DefaultGatewayFailoverPolicySettings(), nil
	}
	return normalized, nil
}

// GetGatewayFailoverPolicySettingsCached 返回缓存后的网关故障转移增强策略配置。
func (s *SettingService) GetGatewayFailoverPolicySettingsCached(ctx context.Context) *GatewayFailoverPolicySettings {
	if s == nil || s.settingRepo == nil {
		return DefaultGatewayFailoverPolicySettings()
	}
	now := time.Now()
	if cached, ok := s.gatewayFailoverPolicyCache.Load().(*cachedGatewayFailoverPolicySettings); ok && cached != nil {
		if now.UnixNano() < cached.expiresAt {
			return cloneGatewayFailoverPolicySettings(cached.settings)
		}
	}

	val, _, _ := s.gatewayFailoverPolicySF.Do("gateway_failover_policy_settings", func() (any, error) {
		if cached, ok := s.gatewayFailoverPolicyCache.Load().(*cachedGatewayFailoverPolicySettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cloneGatewayFailoverPolicySettings(cached.settings), nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayFailoverPolicySettingsDBTimeout)
		defer cancel()
		settings, err := s.GetGatewayFailoverPolicySettings(dbCtx)
		ttl := gatewayFailoverPolicySettingsCacheTTL
		if err != nil {
			slog.Warn("failed to get gateway failover policy settings", "error", err)
			settings = DefaultGatewayFailoverPolicySettings()
			ttl = gatewayFailoverPolicySettingsErrorTTL
		}
		s.gatewayFailoverPolicyCache.Store(&cachedGatewayFailoverPolicySettings{
			settings:  cloneGatewayFailoverPolicySettings(settings),
			expiresAt: time.Now().Add(ttl).UnixNano(),
		})
		return cloneGatewayFailoverPolicySettings(settings), nil
	})
	if settings, ok := val.(*GatewayFailoverPolicySettings); ok && settings != nil {
		return settings
	}
	return DefaultGatewayFailoverPolicySettings()
}

// SetGatewayFailoverPolicySettings 设置网关故障转移增强策略配置。
func (s *SettingService) SetGatewayFailoverPolicySettings(ctx context.Context, settings *GatewayFailoverPolicySettings) error {
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting service is not initialized")
	}
	normalized, err := normalizeGatewayFailoverPolicySettings(settings, true)
	if err != nil {
		return err
	}

	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal gateway failover policy settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyGatewayFailoverPolicySettings, string(data)); err != nil {
		return err
	}
	s.gatewayFailoverPolicyCache.Store(&cachedGatewayFailoverPolicySettings{
		settings:  cloneGatewayFailoverPolicySettings(normalized),
		expiresAt: time.Now().Add(gatewayFailoverPolicySettingsCacheTTL).UnixNano(),
	})
	return nil
}

const (
	gatewayContentBlockerMinCooldownMinutes = 1
	gatewayContentBlockerMaxCooldownMinutes = 720
	gatewayContentBlockerMinScanBytes       = 1024
	gatewayContentBlockerMaxScanBytes       = 1024 * 1024
	gatewayContentBlockerMaxKeywords        = 100
	gatewayContentBlockerMaxKeywordBytes    = 512
)

func cloneGatewayContentBlockerSettings(settings *GatewayContentBlockerSettings) *GatewayContentBlockerSettings {
	if settings == nil {
		return DefaultGatewayContentBlockerSettings()
	}
	cloned := *settings
	if settings.Keywords == nil {
		cloned.Keywords = []string{}
	} else {
		cloned.Keywords = append([]string(nil), settings.Keywords...)
	}
	return &cloned
}

func normalizeGatewayContentBlockerSettings(settings *GatewayContentBlockerSettings, strict bool) (*GatewayContentBlockerSettings, error) {
	if settings == nil {
		return nil, fmt.Errorf("settings cannot be nil")
	}

	normalized := cloneGatewayContentBlockerSettings(settings)
	if normalized.CooldownMinutes < gatewayContentBlockerMinCooldownMinutes || normalized.CooldownMinutes > gatewayContentBlockerMaxCooldownMinutes {
		if strict && normalized.Enabled {
			return nil, fmt.Errorf("cooldown_minutes must be between %d-%d", gatewayContentBlockerMinCooldownMinutes, gatewayContentBlockerMaxCooldownMinutes)
		}
		normalized.CooldownMinutes = DefaultGatewayContentBlockerSettings().CooldownMinutes
	}
	if normalized.MaxScanBytes < gatewayContentBlockerMinScanBytes || normalized.MaxScanBytes > gatewayContentBlockerMaxScanBytes {
		if strict && normalized.Enabled {
			return nil, fmt.Errorf("max_scan_bytes must be between %d-%d", gatewayContentBlockerMinScanBytes, gatewayContentBlockerMaxScanBytes)
		}
		normalized.MaxScanBytes = DefaultGatewayContentBlockerSettings().MaxScanBytes
	}

	keywords := make([]string, 0, len(normalized.Keywords))
	seen := make(map[string]struct{}, len(normalized.Keywords))
	for _, raw := range normalized.Keywords {
		keyword := strings.TrimSpace(raw)
		if keyword == "" {
			continue
		}
		if len([]byte(keyword)) > gatewayContentBlockerMaxKeywordBytes {
			if strict {
				return nil, fmt.Errorf("keyword must be at most %d bytes", gatewayContentBlockerMaxKeywordBytes)
			}
			keyword = string([]byte(keyword)[:gatewayContentBlockerMaxKeywordBytes])
			keyword = strings.TrimSpace(keyword)
			if keyword == "" {
				continue
			}
		}
		dedupeKey := strings.ToLower(keyword)
		if _, ok := seen[dedupeKey]; ok {
			continue
		}
		seen[dedupeKey] = struct{}{}
		keywords = append(keywords, keyword)
		if len(keywords) > gatewayContentBlockerMaxKeywords {
			if strict {
				return nil, fmt.Errorf("keywords must contain at most %d items", gatewayContentBlockerMaxKeywords)
			}
			keywords = keywords[:gatewayContentBlockerMaxKeywords]
			break
		}
	}
	normalized.Keywords = keywords

	return normalized, nil
}

// GetGatewayContentBlockerSettings 获取 200 OK 响应内容关键词拦截配置。
func (s *SettingService) GetGatewayContentBlockerSettings(ctx context.Context) (*GatewayContentBlockerSettings, error) {
	if s == nil || s.settingRepo == nil {
		return DefaultGatewayContentBlockerSettings(), nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyGatewayContentBlockerSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultGatewayContentBlockerSettings(), nil
		}
		return nil, fmt.Errorf("get gateway content blocker settings: %w", err)
	}
	if value == "" {
		return DefaultGatewayContentBlockerSettings(), nil
	}

	var settings GatewayContentBlockerSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultGatewayContentBlockerSettings(), nil
	}
	normalized, err := normalizeGatewayContentBlockerSettings(&settings, false)
	if err != nil {
		return DefaultGatewayContentBlockerSettings(), nil
	}
	return normalized, nil
}

// GetGatewayContentBlockerSettingsCached 返回缓存后的 200 OK 响应内容关键词拦截配置。
func (s *SettingService) GetGatewayContentBlockerSettingsCached(ctx context.Context) *GatewayContentBlockerSettings {
	if s == nil || s.settingRepo == nil {
		return DefaultGatewayContentBlockerSettings()
	}
	now := time.Now()
	if cached, ok := s.gatewayContentBlockerCache.Load().(*cachedGatewayContentBlockerSettings); ok && cached != nil {
		if now.UnixNano() < cached.expiresAt {
			return cloneGatewayContentBlockerSettings(cached.settings)
		}
	}

	val, _, _ := s.gatewayContentBlockerSF.Do("gateway_content_blocker_settings", func() (any, error) {
		if cached, ok := s.gatewayContentBlockerCache.Load().(*cachedGatewayContentBlockerSettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cloneGatewayContentBlockerSettings(cached.settings), nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayContentBlockerSettingsDBTimeout)
		defer cancel()
		settings, err := s.GetGatewayContentBlockerSettings(dbCtx)
		ttl := gatewayContentBlockerSettingsCacheTTL
		if err != nil {
			slog.Warn("failed to get gateway content blocker settings", "error", err)
			settings = DefaultGatewayContentBlockerSettings()
			ttl = gatewayContentBlockerSettingsErrorTTL
		}
		s.gatewayContentBlockerCache.Store(&cachedGatewayContentBlockerSettings{
			settings:  cloneGatewayContentBlockerSettings(settings),
			expiresAt: time.Now().Add(ttl).UnixNano(),
		})
		return cloneGatewayContentBlockerSettings(settings), nil
	})
	if settings, ok := val.(*GatewayContentBlockerSettings); ok && settings != nil {
		return settings
	}
	return DefaultGatewayContentBlockerSettings()
}

// SetGatewayContentBlockerSettings 设置 200 OK 响应内容关键词拦截配置。
func (s *SettingService) SetGatewayContentBlockerSettings(ctx context.Context, settings *GatewayContentBlockerSettings) error {
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting service is not initialized")
	}
	normalized, err := normalizeGatewayContentBlockerSettings(settings, true)
	if err != nil {
		return err
	}

	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal gateway content blocker settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyGatewayContentBlockerSettings, string(data)); err != nil {
		return err
	}
	s.gatewayContentBlockerCache.Store(&cachedGatewayContentBlockerSettings{
		settings:  cloneGatewayContentBlockerSettings(normalized),
		expiresAt: time.Now().Add(gatewayContentBlockerSettingsCacheTTL).UnixNano(),
	})
	return nil
}

// GetCyberSessionBlockRuntime 返回 (开关, TTL)，进程内缓存 ~60s，
// 供网关热路径读取时避免 DB 往返。
// 两个 setting key 在单次 singleflight 里一起读取，减少 DB 往返。
// 默认值：开关 false，TTL 1h（与粘性会话对齐）。
func (s *SettingService) GetCyberSessionBlockRuntime(ctx context.Context) (bool, time.Duration) {
	if cached, ok := s.cyberSessionBlockRuntimeCache.Load().(*cachedCyberSessionBlockRuntime); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled, cached.ttl
		}
	}
	result, _, _ := s.cyberSessionBlockRuntimeSF.Do("cyber_session_block_runtime", func() (any, error) {
		if cached, ok := s.cyberSessionBlockRuntimeCache.Load().(*cachedCyberSessionBlockRuntime); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cyberSessionBlockRuntimeDBTimeout)
		defer cancel()

		enabledVal, enabledErr := s.settingRepo.GetValue(dbCtx, SettingKeyCyberSessionBlockEnabled)
		ttlVal, ttlErr := s.settingRepo.GetValue(dbCtx, SettingKeyCyberSessionBlockTTLSeconds)

		if enabledErr != nil && !errors.Is(enabledErr, ErrSettingNotFound) {
			slog.Warn("failed to get cyber_session_block_enabled setting", "error", enabledErr)
			entry := &cachedCyberSessionBlockRuntime{
				enabled:   false,
				ttl:       time.Hour,
				expiresAt: time.Now().Add(cyberSessionBlockRuntimeErrorTTL).UnixNano(),
			}
			s.cyberSessionBlockRuntimeCache.Store(entry)
			return entry, nil
		}

		enabled := enabledErr == nil && strings.TrimSpace(enabledVal) == "true"

		ttl := time.Hour
		if ttlErr == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(ttlVal)); perr == nil && n > 0 {
				ttl = time.Duration(n) * time.Second
			}
		}

		entry := &cachedCyberSessionBlockRuntime{
			enabled:   enabled,
			ttl:       ttl,
			expiresAt: time.Now().Add(cyberSessionBlockRuntimeCacheTTL).UnixNano(),
		}
		s.cyberSessionBlockRuntimeCache.Store(entry)
		return entry, nil
	})
	if entry, ok := result.(*cachedCyberSessionBlockRuntime); ok && entry != nil {
		return entry.enabled, entry.ttl
	}
	return false, time.Hour
}

// GetAntigravityUserAgentVersion 返回 Antigravity 上游请求使用的版本号。
// 后台设置优先；为空、缺失或非法时回退到 ANTIGRAVITY_USER_AGENT_VERSION / 内置默认值。
func (s *SettingService) GetAntigravityUserAgentVersion(ctx context.Context) string {
	fallback := antigravity.GetDefaultUserAgentVersion()
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	if cached, ok := s.antigravityUAVersionCache.Load().(*cachedAntigravityUserAgentVersion); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.version
		}
	}

	result, _, _ := s.antigravityUAVersionSF.Do("antigravity_user_agent_version", func() (any, error) {
		if cached, ok := s.antigravityUAVersionCache.Load().(*cachedAntigravityUserAgentVersion); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.version, nil
			}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), antigravityUserAgentVersionDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyAntigravityUserAgentVersion)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			slog.Warn("failed to get antigravity user agent version setting", "error", err)
			s.antigravityUAVersionCache.Store(&cachedAntigravityUserAgentVersion{
				version:   fallback,
				expiresAt: time.Now().Add(antigravityUserAgentVersionErrorTTL).UnixNano(),
			})
			return fallback, nil
		}
		version := antigravity.NormalizeUserAgentVersion(value)
		if version == "" {
			version = fallback
		}
		s.antigravityUAVersionCache.Store(&cachedAntigravityUserAgentVersion{
			version:   version,
			expiresAt: time.Now().Add(antigravityUserAgentVersionCacheTTL).UnixNano(),
		})
		return version, nil
	})
	if version, ok := result.(string); ok && version != "" {
		return version
	}
	return fallback
}

// GetOpenAICodexUserAgent 返回 OpenAI Codex 上游请求使用的 User-Agent。
// 后台设置优先；为空时回退到内置默认值。
func (s *SettingService) GetOpenAICodexUserAgent(ctx context.Context) string {
	fallback := DefaultOpenAICodexUserAgent
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	if cached, ok := s.openAICodexUACache.Load().(*cachedOpenAICodexUserAgent); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.value
		}
	}

	result, _, _ := s.openAICodexUASF.Do("openai_codex_user_agent", func() (any, error) {
		if cached, ok := s.openAICodexUACache.Load().(*cachedOpenAICodexUserAgent); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.value, nil
			}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAICodexUserAgentDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAICodexUserAgent)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			slog.Warn("failed to get openai codex user agent setting", "error", err)
			s.openAICodexUACache.Store(&cachedOpenAICodexUserAgent{
				value:     fallback,
				expiresAt: time.Now().Add(openAICodexUserAgentErrorTTL).UnixNano(),
			})
			return fallback, nil
		}
		ua := strings.TrimSpace(value)
		if ua == "" {
			ua = fallback
		}
		s.openAICodexUACache.Store(&cachedOpenAICodexUserAgent{
			value:     ua,
			expiresAt: time.Now().Add(openAICodexUserAgentCacheTTL).UnixNano(),
		})
		return ua, nil
	})
	if ua, ok := result.(string); ok && ua != "" {
		return ua
	}
	return fallback
}

var legacyClaudeCodeCodexWhitelistEntry = openai.AllowedClientEntry{
	Originator: "Claude Code",
	UAContains: []string{"Claude Code/"},
}

// MigrateOpenAIAllowClaudeCodeCodexPluginSetting folds the deprecated global Claude Code
// plugin allow switch into codex_cli_only_whitelist. The app-server identity model is the
// same originator + UA marker pair, so runtime checks no longer need a separate flag.
func (s *SettingService) MigrateOpenAIAllowClaudeCodeCodexPluginSetting(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexRestrictionPolicyDBTimeout)
	defer cancel()

	legacyValue, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAIAllowClaudeCodeCodexPlugin)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return nil
		}
		return fmt.Errorf("get deprecated %s setting: %w", SettingKeyOpenAIAllowClaudeCodeCodexPlugin, err)
	}
	if strings.TrimSpace(legacyValue) != "true" {
		return nil
	}

	rawWhitelist, err := s.settingRepo.GetValue(dbCtx, SettingKeyCodexCLIOnlyWhitelist)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return fmt.Errorf("get %s setting: %w", SettingKeyCodexCLIOnlyWhitelist, err)
	}

	var entries []openai.AllowedClientEntry
	if strings.TrimSpace(rawWhitelist) != "" {
		if err := json.Unmarshal([]byte(rawWhitelist), &entries); err != nil {
			return fmt.Errorf("parse %s setting: %w", SettingKeyCodexCLIOnlyWhitelist, err)
		}
	}
	if codexClientEntriesContain(entries, legacyClaudeCodeCodexWhitelistEntry) {
		return nil
	}

	entries = append(entries, legacyClaudeCodeCodexWhitelistEntry)
	encoded, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal %s setting: %w", SettingKeyCodexCLIOnlyWhitelist, err)
	}
	if err := s.settingRepo.Set(dbCtx, SettingKeyCodexCLIOnlyWhitelist, string(encoded)); err != nil {
		return fmt.Errorf("set %s setting: %w", SettingKeyCodexCLIOnlyWhitelist, err)
	}
	s.codexRestrictionPolicySF.Forget("codex_restriction_policy")
	s.codexRestrictionPolicyCache.Store(&cachedCodexRestrictionPolicy{expiresAt: 0})
	return nil
}

// MigrateCodexBodyFingerprintToSignals 把已废弃的 codex_cli_only_allow_body_engine_fingerprint
// 开关并入引擎指纹信号列表。幂等:信号键已存在(非空)则不动;缺失时写默认种子,
// 并把 body 路径行的 Required 设为旧 body 开关的值(旧 true ⇒ 勾上 body 行)。
func (s *SettingService) MigrateCodexBodyFingerprintToSignals(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexRestrictionPolicyDBTimeout)
	defer cancel()

	if v, err := s.settingRepo.GetValue(dbCtx, SettingKeyCodexCLIOnlyEngineFingerprintSignals); err == nil && strings.TrimSpace(v) != "" {
		return nil // 已配置/已迁移
	} else if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return fmt.Errorf("get %s setting: %w", SettingKeyCodexCLIOnlyEngineFingerprintSignals, err)
	}

	bodyOn := false
	if v, err := s.settingRepo.GetValue(dbCtx, SettingKeyCodexCLIOnlyAllowBodyEngineFingerprint); err == nil {
		bodyOn = strings.TrimSpace(v) == "true"
	} else if !errors.Is(err, ErrSettingNotFound) {
		return fmt.Errorf("get deprecated %s setting: %w", SettingKeyCodexCLIOnlyAllowBodyEngineFingerprint, err)
	}

	seed := make([]openai.EngineFingerprintSignal, len(openai.DefaultEngineFingerprintSignals))
	copy(seed, openai.DefaultEngineFingerprintSignals)
	if bodyOn {
		for i := range seed {
			if seed[i].Type == openai.FingerprintSignalBodyPath {
				seed[i].Required = true
			}
		}
	}
	encoded, err := json.Marshal(seed)
	if err != nil {
		return fmt.Errorf("marshal %s setting: %w", SettingKeyCodexCLIOnlyEngineFingerprintSignals, err)
	}
	if err := s.settingRepo.Set(dbCtx, SettingKeyCodexCLIOnlyEngineFingerprintSignals, string(encoded)); err != nil {
		return fmt.Errorf("set %s setting: %w", SettingKeyCodexCLIOnlyEngineFingerprintSignals, err)
	}
	s.codexRestrictionPolicySF.Forget("codex_restriction_policy")
	s.codexRestrictionPolicyCache.Store(&cachedCodexRestrictionPolicy{expiresAt: 0})
	return nil
}

func codexClientEntriesContain(entries []openai.AllowedClientEntry, want openai.AllowedClientEntry) bool {
	wantOriginator := strings.TrimSpace(want.Originator)
	if wantOriginator == "" {
		return false
	}
	wantMarkers := normalizedCodexClientMarkers(want.UAContains)
	if len(wantMarkers) == 0 {
		return false
	}
	for _, entry := range entries {
		if !strings.EqualFold(strings.TrimSpace(entry.Originator), wantOriginator) {
			continue
		}
		gotMarkers := normalizedCodexClientMarkers(entry.UAContains)
		if len(gotMarkers) != len(wantMarkers) {
			continue
		}
		matched := true
		for marker := range wantMarkers {
			if _, ok := gotMarkers[marker]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func normalizedCodexClientMarkers(markers []string) map[string]struct{} {
	normalized := make(map[string]struct{}, len(markers))
	for _, marker := range markers {
		marker = strings.TrimSpace(marker)
		if marker == "" {
			continue
		}
		normalized[strings.ToLower(marker)] = struct{}{}
	}
	return normalized
}

// GetCodexRestrictionPolicy 读取 codex_cli_only 全局加固策略（黑/白名单、最低版本、引擎指纹门）。
// 仅在调用方已确认账号 codex_cli_only 开启时读取；进程内 atomic.Value 缓存（60s TTL）避免热路径访问 DB。
// 任意键缺失/解析失败 → 安全默认：空名单、空版本、默认种子指纹信号。
func (s *SettingService) GetCodexRestrictionPolicy(ctx context.Context) CodexRestrictionPolicy {
	if cached, ok := s.codexRestrictionPolicyCache.Load().(*cachedCodexRestrictionPolicy); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.value
		}
	}
	result, _, _ := s.codexRestrictionPolicySF.Do("codex_restriction_policy", func() (any, error) {
		if cached, ok := s.codexRestrictionPolicyCache.Load().(*cachedCodexRestrictionPolicy); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.value, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexRestrictionPolicyDBTimeout)
		defer cancel()

		pol := CodexRestrictionPolicy{EngineFingerprintSignals: openai.DefaultEngineFingerprintSignals} // 安全默认：默认种子指纹信号
		if v, err := s.settingRepo.GetValue(dbCtx, SettingKeyMinCodexVersion); err == nil {
			pol.MinCodexVersion = strings.TrimSpace(v)
		}
		if v, err := s.settingRepo.GetValue(dbCtx, SettingKeyMaxCodexVersion); err == nil {
			pol.MaxCodexVersion = strings.TrimSpace(v)
		}
		if v, err := s.settingRepo.GetValue(dbCtx, SettingKeyCodexCLIOnlyAllowAppServerClients); err == nil {
			pol.AllowAppServerClients = strings.TrimSpace(v) == "true" // 仅显式 "true" 开启
		}
		pol.EngineFingerprintSignals = s.loadEngineFingerprintSignals(dbCtx)
		pol.Whitelist = s.loadCodexClientEntries(dbCtx, SettingKeyCodexCLIOnlyWhitelist)
		pol.Blacklist = s.loadCodexClientEntries(dbCtx, SettingKeyCodexCLIOnlyBlacklist)

		s.codexRestrictionPolicyCache.Store(&cachedCodexRestrictionPolicy{
			value:     pol,
			expiresAt: time.Now().Add(codexRestrictionPolicyCacheTTL).UnixNano(),
		})
		return pol, nil
	})
	if pol, ok := result.(CodexRestrictionPolicy); ok {
		return pol
	}
	return CodexRestrictionPolicy{EngineFingerprintSignals: openai.DefaultEngineFingerprintSignals}
}

// loadCodexClientEntries 读取并解析 []openai.AllowedClientEntry JSON 设置；缺失/空/非法 → nil（安全忽略）。
func (s *SettingService) loadCodexClientEntries(ctx context.Context, key string) []openai.AllowedClientEntry {
	v, err := s.settingRepo.GetValue(ctx, key)
	if err != nil || strings.TrimSpace(v) == "" {
		return nil
	}
	var entries []openai.AllowedClientEntry
	if json.Unmarshal([]byte(v), &entries) != nil {
		return nil
	}
	return entries
}

// loadEngineFingerprintSignals 读取引擎指纹信号列表;缺失/空/非法 → 默认种子。
func (s *SettingService) loadEngineFingerprintSignals(ctx context.Context) []openai.EngineFingerprintSignal {
	v, err := s.settingRepo.GetValue(ctx, SettingKeyCodexCLIOnlyEngineFingerprintSignals)
	if err != nil || strings.TrimSpace(v) == "" {
		return openai.DefaultEngineFingerprintSignals
	}
	sigs, ok := openai.ParseEngineFingerprintSignals(v)
	if !ok {
		return openai.DefaultEngineFingerprintSignals
	}
	return sigs
}

// ValidateCodexClientEntriesJSON 校验 codex_cli_only 名单 JSON 配置（黑名单语义）：
// 空=合法（禁用）；非空须为 []AllowedClientEntry 的 JSON 数组。黑名单是 OR 宽 deny，
// 允许 originator-only 条目，故不校验 ua_contains。白名单请用 ValidateCodexWhitelistEntriesJSON。
func ValidateCodexClientEntriesJSON(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var entries []openai.AllowedClientEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return fmt.Errorf("must be empty or a valid JSON array of {originator, ua_contains}")
	}
	return nil
}

// ValidateCodexWhitelistEntriesJSON 在 ValidateCodexClientEntriesJSON 的数组结构校验之上，额外要求
// 每条白名单条目「有可能命中」（openai.AllowedClientEntry.IsWhitelistable）。白名单是双因子 AND：
// originator-only、空或含空白 ua_contains 的条目会在运行时静默失效——这里让管理员在写入时即收到反馈，
// 而非存入永不命中的死规则。黑名单（OR 宽 deny）仍用 ValidateCodexClientEntriesJSON。
func ValidateCodexWhitelistEntriesJSON(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var entries []openai.AllowedClientEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return fmt.Errorf("must be empty or a valid JSON array of {originator, ua_contains}")
	}
	for i, e := range entries {
		if !e.IsWhitelistable() {
			return fmt.Errorf("entry %d: whitelist requires a non-empty originator and at least one non-empty ua_contains (double-factor AND; otherwise the rule never matches)", i)
		}
	}
	return nil
}

// ValidateEngineFingerprintSignalsJSON 服务层包装,复用 openai 校验逻辑。
func ValidateEngineFingerprintSignalsJSON(raw string) error {
	return openai.ValidateEngineFingerprintSignalsJSON(raw)
}

// IsBackendModeEnabled checks if backend mode is enabled
// Uses in-process atomic.Value cache with 60s TTL, zero-lock hot path
func (s *SettingService) IsBackendModeEnabled(ctx context.Context) bool {
	if cached, ok := backendModeCache.Load().(*cachedBackendMode); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.value
		}
	}
	result, _, _ := backendModeSF.Do("backend_mode", func() (any, error) {
		if cached, ok := backendModeCache.Load().(*cachedBackendMode); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.value, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backendModeDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyBackendModeEnabled)
		if err != nil {
			if errors.Is(err, ErrSettingNotFound) {
				// Setting not yet created (fresh install) - default to disabled with full TTL
				backendModeCache.Store(&cachedBackendMode{
					value:     false,
					expiresAt: time.Now().Add(backendModeCacheTTL).UnixNano(),
				})
				return false, nil
			}
			slog.Warn("failed to get backend_mode_enabled setting", "error", err)
			backendModeCache.Store(&cachedBackendMode{
				value:     false,
				expiresAt: time.Now().Add(backendModeErrorTTL).UnixNano(),
			})
			return false, nil
		}
		enabled := value == "true"
		backendModeCache.Store(&cachedBackendMode{
			value:     enabled,
			expiresAt: time.Now().Add(backendModeCacheTTL).UnixNano(),
		})
		return enabled, nil
	})
	if val, ok := result.(bool); ok {
		return val
	}
	return false
}

type gatewayForwardingSettingsResult struct {
	fp, mp, cch, claudeOAuthSystemPromptInjection, cacheTTL1h, rewriteMessageCacheControl bool
	clientDatelineNormalization                                                           bool
	claudeOAuthSystemPrompt, claudeOAuthSystemPromptBlocks                                string
}

func (s *SettingService) getGatewayForwardingSettingsCached(ctx context.Context) gatewayForwardingSettingsResult {
	if cached, ok := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return gatewayForwardingSettingsResult{
				fp:                               cached.fingerprintUnification,
				mp:                               cached.metadataPassthrough,
				cch:                              cached.cchSigning,
				claudeOAuthSystemPromptInjection: cached.claudeOAuthSystemPromptInjection,
				claudeOAuthSystemPrompt:          cached.claudeOAuthSystemPrompt,
				claudeOAuthSystemPromptBlocks:    cached.claudeOAuthSystemPromptBlocks,
				cacheTTL1h:                       cached.anthropicCacheTTL1hInjection,
				rewriteMessageCacheControl:       cached.rewriteMessageCacheControl,
				clientDatelineNormalization:      cached.clientDatelineNormalization,
			}
		}
	}
	val, _, _ := gatewayForwardingSF.Do("gateway_forwarding", func() (any, error) {
		if cached, ok := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return gatewayForwardingSettingsResult{
					fp:                               cached.fingerprintUnification,
					mp:                               cached.metadataPassthrough,
					cch:                              cached.cchSigning,
					claudeOAuthSystemPromptInjection: cached.claudeOAuthSystemPromptInjection,
					claudeOAuthSystemPrompt:          cached.claudeOAuthSystemPrompt,
					claudeOAuthSystemPromptBlocks:    cached.claudeOAuthSystemPromptBlocks,
					cacheTTL1h:                       cached.anthropicCacheTTL1hInjection,
					rewriteMessageCacheControl:       cached.rewriteMessageCacheControl,
					clientDatelineNormalization:      cached.clientDatelineNormalization,
				}, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayForwardingDBTimeout)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{
			SettingKeyEnableFingerprintUnification,
			SettingKeyEnableMetadataPassthrough,
			SettingKeyEnableCCHSigning,
			SettingKeyEnableClaudeOAuthSystemPromptInjection,
			SettingKeyClaudeOAuthSystemPrompt,
			SettingKeyClaudeOAuthSystemPromptBlocks,
			SettingKeyEnableAnthropicCacheTTL1hInjection,
			SettingKeyRewriteMessageCacheControl,
			SettingKeyEnableClientDatelineNormalization,
		})
		if err != nil {
			slog.Warn("failed to get gateway forwarding settings", "error", err)
			gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
				fingerprintUnification:           true,
				metadataPassthrough:              false,
				cchSigning:                       false,
				claudeOAuthSystemPromptInjection: true,
				anthropicCacheTTL1hInjection:     false,
				rewriteMessageCacheControl:       s.defaultRewriteMessageCacheControl(),
				clientDatelineNormalization:      true,
				expiresAt:                        time.Now().Add(gatewayForwardingErrorTTL).UnixNano(),
			})
			return gatewayForwardingSettingsResult{fp: true, claudeOAuthSystemPromptInjection: true, rewriteMessageCacheControl: s.defaultRewriteMessageCacheControl(), clientDatelineNormalization: true}, nil
		}
		fp := true
		if v, ok := values[SettingKeyEnableFingerprintUnification]; ok && v != "" {
			fp = v == "true"
		}
		mp := values[SettingKeyEnableMetadataPassthrough] == "true"
		cch := values[SettingKeyEnableCCHSigning] == "true"
		systemPromptInjection := true
		if v, ok := values[SettingKeyEnableClaudeOAuthSystemPromptInjection]; ok && v != "" {
			systemPromptInjection = v == "true"
		}
		systemPrompt := values[SettingKeyClaudeOAuthSystemPrompt]
		systemPromptBlocks := values[SettingKeyClaudeOAuthSystemPromptBlocks]
		cacheTTL1h := values[SettingKeyEnableAnthropicCacheTTL1hInjection] == "true"
		rewriteMessageCacheControl := s.defaultRewriteMessageCacheControl()
		if v, ok := values[SettingKeyRewriteMessageCacheControl]; ok && v != "" {
			rewriteMessageCacheControl = v == "true"
		}
		clientDatelineNormalization := true
		if v, ok := values[SettingKeyEnableClientDatelineNormalization]; ok && v != "" {
			clientDatelineNormalization = v == "true"
		}
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
			fingerprintUnification:           fp,
			metadataPassthrough:              mp,
			cchSigning:                       cch,
			claudeOAuthSystemPromptInjection: systemPromptInjection,
			claudeOAuthSystemPrompt:          systemPrompt,
			claudeOAuthSystemPromptBlocks:    systemPromptBlocks,
			anthropicCacheTTL1hInjection:     cacheTTL1h,
			rewriteMessageCacheControl:       rewriteMessageCacheControl,
			clientDatelineNormalization:      clientDatelineNormalization,
			expiresAt:                        time.Now().Add(gatewayForwardingCacheTTL).UnixNano(),
		})
		return gatewayForwardingSettingsResult{
			fp:                               fp,
			mp:                               mp,
			cch:                              cch,
			claudeOAuthSystemPromptInjection: systemPromptInjection,
			claudeOAuthSystemPrompt:          systemPrompt,
			claudeOAuthSystemPromptBlocks:    systemPromptBlocks,
			cacheTTL1h:                       cacheTTL1h,
			rewriteMessageCacheControl:       rewriteMessageCacheControl,
			clientDatelineNormalization:      clientDatelineNormalization,
		}, nil
	})
	if r, ok := val.(gatewayForwardingSettingsResult); ok {
		return r
	}
	return gatewayForwardingSettingsResult{fp: true, claudeOAuthSystemPromptInjection: true, clientDatelineNormalization: true}
}

// GetGatewayForwardingSettings returns cached gateway forwarding settings.
// Uses in-process atomic.Value cache with 60s TTL, zero-lock hot path.
// Returns (fingerprintUnification, metadataPassthrough, cchSigning).
func (s *SettingService) GetGatewayForwardingSettings(ctx context.Context) (fingerprintUnification, metadataPassthrough, cchSigning bool) {
	result := s.getGatewayForwardingSettingsCached(ctx)
	return result.fp, result.mp, result.cch
}

// IsAnthropicCacheTTL1hInjectionEnabled 检查是否对 Anthropic OAuth/SetupToken 请求体注入 1h cache_control ttl。
func (s *SettingService) IsAnthropicCacheTTL1hInjectionEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).cacheTTL1h
}

// IsRewriteMessageCacheControlEnabled 检查是否启用 messages cache_control 改写。
func (s *SettingService) IsRewriteMessageCacheControlEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).rewriteMessageCacheControl
}

// IsClientDatelineNormalizationEnabled 检查是否启用 Anthropic OAuth/SetupToken 请求体
// 的客户端 dateline 归一化。默认开启。
func (s *SettingService) IsClientDatelineNormalizationEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).clientDatelineNormalization
}

// GetClaudeOAuthSystemPromptInjectionSettings returns the Claude OAuth mimic
// system block switch, legacy custom expansion prompt, and configurable blocks JSON.
// Empty values mean use the built-in Claude Code default blocks.
func (s *SettingService) GetClaudeOAuthSystemPromptInjectionSettings(ctx context.Context) (enabled bool, prompt string, blocks string) {
	result := s.getGatewayForwardingSettingsCached(ctx)
	return result.claudeOAuthSystemPromptInjection, result.claudeOAuthSystemPrompt, result.claudeOAuthSystemPromptBlocks
}

// GetClaudeCodeVersionBounds 获取 Claude Code 版本号上下限要求
// 使用进程内 atomic.Value 缓存，60 秒 TTL，热路径零锁开销
// singleflight 防止缓存过期时 thundering herd
// 返回空字符串表示不做对应方向的版本检查
func (s *SettingService) GetClaudeCodeVersionBounds(ctx context.Context) (min, max string) {
	if cached, ok := versionBoundsCache.Load().(*cachedVersionBounds); ok {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.min, cached.max
		}
	}
	// singleflight: 同一时刻只有一个 goroutine 查询 DB，其余复用结果
	type bounds struct{ min, max string }
	result, err, _ := versionBoundsSF.Do("version_bounds", func() (any, error) {
		// 二次检查，避免排队的 goroutine 重复查询
		if cached, ok := versionBoundsCache.Load().(*cachedVersionBounds); ok {
			if time.Now().UnixNano() < cached.expiresAt {
				return bounds{cached.min, cached.max}, nil
			}
		}
		// 使用独立 context：断开请求取消链，避免客户端断连导致空值被长期缓存
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), versionBoundsDBTimeout)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{
			SettingKeyMinClaudeCodeVersion,
			SettingKeyMaxClaudeCodeVersion,
		})
		if err != nil {
			// fail-open: DB 错误时不阻塞请求，但记录日志并使用短 TTL 快速重试
			slog.Warn("failed to get claude code version bounds setting, skipping version check", "error", err)
			versionBoundsCache.Store(&cachedVersionBounds{
				min:       "",
				max:       "",
				expiresAt: time.Now().Add(versionBoundsErrorTTL).UnixNano(),
			})
			return bounds{"", ""}, nil
		}
		b := bounds{
			min: values[SettingKeyMinClaudeCodeVersion],
			max: values[SettingKeyMaxClaudeCodeVersion],
		}
		versionBoundsCache.Store(&cachedVersionBounds{
			min:       b.min,
			max:       b.max,
			expiresAt: time.Now().Add(versionBoundsCacheTTL).UnixNano(),
		})
		return b, nil
	})
	if err != nil {
		return "", ""
	}
	b, ok := result.(bounds)
	if !ok {
		return "", ""
	}
	return b.min, b.max
}

// GetOpenAIQuotaAutoPauseSettings returns the current global default quota auto-pause
// settings. It is invoked on the OpenAI scheduling hot path (once per request) and is
// therefore designed to never block on the DB:
//
//   - Fresh cached value → returned immediately.
//   - Stale or empty cache → the last known value is returned, and a background
//     goroutine refreshes the cache via singleflight (stale-while-revalidate).
//   - First call with no cache yet → zero defaults are returned and the same async
//     refresh is kicked off; the next call gets the freshly populated value.
//
// Callers that need the freshly persisted value synchronously (tests, post-update
// confirmation, optional startup warm-up) should call WarmOpenAIQuotaAutoPauseSettings.
func (s *SettingService) GetOpenAIQuotaAutoPauseSettings(ctx context.Context) OpsOpenAIAccountQuotaAutoPauseSettings {
	if s == nil {
		return OpsOpenAIAccountQuotaAutoPauseSettings{}
	}
	cached, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings)
	now := time.Now().UnixNano()
	if cached != nil && now < cached.expiresAt {
		return cached.settings
	}
	// Stale or unset: trigger background refresh without blocking this request.
	// singleflight.DoChan dedupes concurrent refreshes; we deliberately ignore the
	// returned channel — the result is observable via the atomic cache.
	s.openAIQuotaAutoPauseSettingsSF.DoChan(openAIQuotaAutoPauseSettingsRefreshKey, func() (any, error) {
		s.refreshOpenAIQuotaAutoPauseSettings(context.Background())
		return nil, nil
	})
	if cached != nil {
		return cached.settings // serve stale value while revalidating
	}
	return OpsOpenAIAccountQuotaAutoPauseSettings{}
}

// WarmOpenAIQuotaAutoPauseSettings synchronously loads the quota auto-pause settings
// into the in-memory cache. Useful for application startup (so the first request hits
// a warm cache) and for tests that need deterministic reads immediately after
// constructing the service.
func (s *SettingService) WarmOpenAIQuotaAutoPauseSettings(ctx context.Context) OpsOpenAIAccountQuotaAutoPauseSettings {
	if s == nil {
		return OpsOpenAIAccountQuotaAutoPauseSettings{}
	}
	s.refreshOpenAIQuotaAutoPauseSettings(ctx)
	cached, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings)
	if cached == nil {
		return OpsOpenAIAccountQuotaAutoPauseSettings{}
	}
	return cached.settings
}

// refreshOpenAIQuotaAutoPauseSettings reads the latest settings from the DB and stores
// them into the in-memory cache. On error it stores the prior value (or zero defaults
// if nothing is cached yet) with the shorter error TTL so the next refresh comes
// sooner. Always uses its own timeout-bounded context to keep refresh latency
// predictable regardless of the caller.
func (s *SettingService) refreshOpenAIQuotaAutoPauseSettings(ctx context.Context) {
	if s == nil || s.settingRepo == nil {
		return
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIQuotaAutoPauseSettingsDBTimeout)
	defer cancel()

	settings := OpsOpenAIAccountQuotaAutoPauseSettings{}
	ttl := openAIQuotaAutoPauseSettingsCacheTTL
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpsAdvancedSettings)
	if err == nil {
		cfg := defaultOpsAdvancedSettings()
		if strings.TrimSpace(raw) != "" {
			if jsonErr := json.Unmarshal([]byte(raw), cfg); jsonErr == nil {
				normalizeOpsAdvancedSettings(cfg)
			}
		}
		settings = cfg.OpenAIAccountQuotaAutoPause
	} else if !errors.Is(err, ErrSettingNotFound) {
		// Real error: keep serving prior value but refresh sooner.
		if prior, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings); prior != nil {
			settings = prior.settings
		}
		ttl = openAIQuotaAutoPauseSettingsErrorTTL
	}

	s.openAIQuotaAutoPauseSettingsCache.Store(&cachedOpenAIQuotaAutoPauseSettings{
		settings:  settings,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}

// SetOpenAIQuotaAutoPauseSettings writes the given settings directly into the in-memory
// cache. Called from settings-write code paths so that the next read reflects the new
// value immediately, without waiting for the background refresh.
func (s *SettingService) SetOpenAIQuotaAutoPauseSettings(settings OpsOpenAIAccountQuotaAutoPauseSettings) {
	if s == nil {
		return
	}
	s.openAIQuotaAutoPauseSettingsCache.Store(&cachedOpenAIQuotaAutoPauseSettings{
		settings:  settings,
		expiresAt: time.Now().Add(openAIQuotaAutoPauseSettingsCacheTTL).UnixNano(),
	})
}
