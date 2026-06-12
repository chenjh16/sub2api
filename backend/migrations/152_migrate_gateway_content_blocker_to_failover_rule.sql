-- 将旧的 200 OK 响应内容拦截配置并入自动故障转移策略。
-- 运行时不再读取 gateway_content_blocker_settings；旧配置会转换成
-- gateway_failover_policy_settings.rules 中的 openai_200_content_text 规则。
DO $$
DECLARE
    policy_settings jsonb;
    content_settings jsonb;
    default_rules jsonb;
    default_content_rule jsonb;
    content_rule jsonb;
    migrated_rules jsonb := '[]'::jsonb;
    rule_item jsonb;
    keyword_text text;
    keyword_conditions jsonb := '[]'::jsonb;
    content_enabled boolean := false;
    content_cooldown_minutes integer := 10;
    content_max_scan_bytes integer := 65536;
    content_rule_exists boolean := false;
    should_migrate_content boolean := false;
BEGIN
    default_content_rule := '{
      "id": "openai_200_content_text",
      "name": "200 内容公告文本",
      "description": "识别伪装成 200 成功响应的维护、繁忙或公告文本",
      "enabled": false,
      "priority": 400,
      "event": "http_response",
      "match": {
        "status_codes": [200],
        "max_scan_bytes": 65536,
        "message_condition_group": {
          "logic": "any",
          "conditions": [
            {"op": "contains", "value": "当前繁忙，休息十分钟"},
            {"op": "contains", "value": "公益服务器压力很大"},
            {"op": "contains", "value": "api.ranmeng.icu 提示：站点维护中"}
          ]
        }
      },
      "action": {
        "failover": true,
        "cooldown_scope": "runtime",
        "cooldown_seconds": 600,
        "jitter_percent": 0,
        "reason": "content_blocker"
      }
    }'::jsonb;

    default_rules := jsonb_build_array(
      '{
        "id": "openai_structured_400_cooldown",
        "name": "结构化 400 冷却",
        "description": "匹配 rate_limit_cooldown 或 limit_type=cooldown 的结构化 400 响应",
        "enabled": true,
        "priority": 100,
        "event": "http_response",
        "match": {
          "status_codes": [400],
          "json_condition_group": {
            "logic": "any",
            "conditions": [
              {"path": "error.code", "op": "equals", "value": "rate_limit_cooldown"},
              {"path": "code", "op": "equals", "value": "rate_limit_cooldown"},
              {"path": "limit_type", "op": "equals", "value": "cooldown"}
            ]
          }
        },
        "action": {
          "failover": true,
          "cooldown_scope": "runtime",
          "cooldown_seconds": 600,
          "jitter_percent": 20,
          "reason": "rate_limit_cooldown"
        }
      }'::jsonb,
      '{
        "id": "openai_structured_400_rpm",
        "name": "结构化 400 RPM 限流",
        "description": "匹配 rate_limit_exceeded 且 limit_type=rpm 的结构化 400 响应",
        "enabled": true,
        "priority": 110,
        "event": "http_response",
        "match": {
          "status_codes": [400],
          "json_condition_group": {
            "logic": "all",
            "conditions": [
              {"paths": ["error.code", "code"], "op": "equals", "value": "rate_limit_exceeded"},
              {"paths": ["limit_type", "error.limit_type"], "op": "equals", "value": "rpm"}
            ]
          }
        },
        "action": {
          "failover": true,
          "cooldown_scope": "runtime",
          "cooldown_seconds": 600,
          "jitter_percent": 20,
          "reason": "rate_limit_exceeded_rpm"
        }
      }'::jsonb,
      '{
        "id": "openai_http_5xx_threshold",
        "name": "连续 HTTP 5xx",
        "description": "OpenAI 上游连续 5xx 时自动 failover，并在达到阈值后短冷却",
        "enabled": true,
        "priority": 200,
        "event": "http_response",
        "match": {
          "status_ranges": [{"min": 500, "max": 599}],
          "exclude_status_codes": [529],
          "consecutive": {"enabled": true, "threshold": 3, "window_seconds": 30}
        },
        "action": {
          "failover": true,
          "cooldown_scope": "runtime",
          "cooldown_seconds": 120,
          "jitter_percent": 20,
          "reason": "http_5xx_threshold"
        }
      }'::jsonb,
      '{
        "id": "openai_transport_threshold",
        "name": "连续瞬时网络错误",
        "description": "OpenAI 上游连续超时、TLS 或网络错误时 failover，并在达到阈值后短冷却",
        "enabled": true,
        "priority": 300,
        "event": "transport_error",
        "match": {
          "transport_persistent": false,
          "consecutive": {"enabled": true, "threshold": 3, "window_seconds": 30}
        },
        "action": {
          "failover": true,
          "cooldown_scope": "runtime",
          "cooldown_seconds": 120,
          "jitter_percent": 20,
          "reason": "transport_threshold"
        }
      }'::jsonb,
      default_content_rule
    );

    BEGIN
        SELECT value::jsonb
        INTO content_settings
        FROM settings
        WHERE key = 'gateway_content_blocker_settings';
    EXCEPTION
        WHEN others THEN
            content_settings := NULL;
    END;

    IF content_settings IS NOT NULL THEN
        content_enabled := lower(COALESCE(content_settings->>'enabled', 'false')) = 'true';

        BEGIN
            content_cooldown_minutes := COALESCE((content_settings->>'cooldown_minutes')::integer, 10);
        EXCEPTION
            WHEN others THEN
                content_cooldown_minutes := 10;
        END;
        IF content_cooldown_minutes < 1 OR content_cooldown_minutes > 720 THEN
            content_cooldown_minutes := 10;
        END IF;

        BEGIN
            content_max_scan_bytes := COALESCE((content_settings->>'max_scan_bytes')::integer, 65536);
        EXCEPTION
            WHEN others THEN
                content_max_scan_bytes := 65536;
        END;
        IF content_max_scan_bytes < 1024 OR content_max_scan_bytes > 1048576 THEN
            content_max_scan_bytes := 65536;
        END IF;

        IF jsonb_typeof(content_settings->'keywords') = 'array' THEN
            FOR keyword_text IN
                SELECT btrim(value)
                FROM jsonb_array_elements_text(content_settings->'keywords') AS value
            LOOP
                IF keyword_text <> '' THEN
                    keyword_conditions := keyword_conditions || jsonb_build_array(
                        jsonb_build_object('op', 'contains', 'value', keyword_text)
                    );
                END IF;
            END LOOP;
        END IF;
    END IF;

    should_migrate_content := content_enabled AND jsonb_array_length(keyword_conditions) > 0;
    content_rule := default_content_rule;
    IF should_migrate_content THEN
        content_rule := jsonb_set(content_rule, '{enabled}', 'true'::jsonb, true);
        content_rule := jsonb_set(content_rule, '{match,max_scan_bytes}', to_jsonb(content_max_scan_bytes), true);
        content_rule := jsonb_set(
            content_rule,
            '{match,message_condition_group,conditions}',
            keyword_conditions,
            true
        );
        content_rule := jsonb_set(
            content_rule,
            '{action,cooldown_seconds}',
            to_jsonb(content_cooldown_minutes * 60),
            true
        );
    END IF;

    BEGIN
        SELECT value::jsonb
        INTO policy_settings
        FROM settings
        WHERE key = 'gateway_failover_policy_settings';
    EXCEPTION
        WHEN others THEN
            policy_settings := NULL;
    END;

    IF policy_settings IS NOT NULL
       AND jsonb_typeof(policy_settings->'rules') = 'array'
       AND jsonb_array_length(policy_settings->'rules') > 0 THEN
        FOR rule_item IN SELECT value FROM jsonb_array_elements(policy_settings->'rules')
        LOOP
            IF rule_item->>'id' = 'openai_200_content_text' THEN
                content_rule_exists := true;
                IF should_migrate_content THEN
                    rule_item := content_rule;
                END IF;
            END IF;
            migrated_rules := migrated_rules || jsonb_build_array(rule_item);
        END LOOP;

        IF NOT content_rule_exists THEN
            migrated_rules := migrated_rules || jsonb_build_array(content_rule);
        END IF;

        policy_settings := jsonb_set(policy_settings, '{rules}', migrated_rules, true);
        UPDATE settings
        SET value = policy_settings::text,
            updated_at = NOW()
        WHERE key = 'gateway_failover_policy_settings';
    ELSIF should_migrate_content THEN
        default_rules := jsonb_set(default_rules, '{4}', content_rule, false);
        INSERT INTO settings (key, value, updated_at)
        VALUES (
            'gateway_failover_policy_settings',
            jsonb_build_object('match_mode', 'first', 'rules', default_rules)::text,
            NOW()
        )
        ON CONFLICT (key) DO UPDATE
        SET value = EXCLUDED.value,
            updated_at = EXCLUDED.updated_at;
    END IF;

    DELETE FROM settings WHERE key = 'gateway_content_blocker_settings';
EXCEPTION
    WHEN others THEN
        RAISE NOTICE 'skip gateway content blocker migration: %', SQLERRM;
END $$;
