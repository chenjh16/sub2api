-- 为已保存的自动故障转移策略补充 request_too_large/tier limit 默认规则。
-- 没有保存过 gateway_failover_policy_settings 的实例会继续使用代码内置默认规则。
DO $$
DECLARE
    policy_settings jsonb;
    request_rule jsonb;
    migrated_rules jsonb := '[]'::jsonb;
    rule_item jsonb;
    rule_exists boolean := false;
BEGIN
    request_rule := '{
      "id": "openai_request_too_large_tier_limit",
      "name": "请求体超过上游套餐限制",
      "description": "匹配 OpenAI 上游 413 request_too_large 且包含 limit_bytes 的套餐体积限制响应",
      "enabled": true,
      "priority": 120,
      "event": "http_response",
      "match": {
        "status_codes": [413],
        "json_condition_group": {
          "logic": "all",
          "conditions": [
            {"paths": ["error.code", "code"], "op": "equals", "value": "request_too_large"},
            {"path": "error.limit_bytes", "op": "exists"}
          ]
        }
      },
      "action": {
        "failover": true,
        "cooldown_scope": "runtime",
        "cooldown_seconds": 600,
        "jitter_percent": 20,
        "reason": "request_too_large_tier_limit",
        "clear_session_binding": true
      }
    }'::jsonb;

    BEGIN
        SELECT value::jsonb
        INTO policy_settings
        FROM settings
        WHERE key = 'gateway_failover_policy_settings';
    EXCEPTION
        WHEN others THEN
            policy_settings := NULL;
    END;

    IF policy_settings IS NULL
       OR jsonb_typeof(policy_settings->'rules') <> 'array'
       OR jsonb_array_length(policy_settings->'rules') = 0 THEN
        RETURN;
    END IF;

    FOR rule_item IN SELECT value FROM jsonb_array_elements(policy_settings->'rules')
    LOOP
        IF rule_item->>'id' = 'openai_request_too_large_tier_limit' THEN
            rule_exists := true;
        END IF;
        migrated_rules := migrated_rules || jsonb_build_array(rule_item);
    END LOOP;

    IF rule_exists THEN
        RETURN;
    END IF;

    migrated_rules := migrated_rules || jsonb_build_array(request_rule);
    policy_settings := jsonb_set(policy_settings, '{rules}', migrated_rules, true);

    UPDATE settings
    SET value = policy_settings::text,
        updated_at = NOW()
    WHERE key = 'gateway_failover_policy_settings';
EXCEPTION
    WHEN others THEN
        RAISE NOTICE 'skip request_too_large failover rule migration: %', SQLERRM;
END $$;
