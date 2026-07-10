-- Add the get_channel_failed overload rule to already-saved failover policies.
-- Empty policies still use runtime defaults and must remain empty.
DO $$
DECLARE
    policy_settings jsonb;
    policy_rules jsonb;
    rule_exists boolean;
    overload_rule jsonb := '{
      "id": "openai_get_channel_failed_overloaded",
      "name": "上游模型负载已满",
      "description": "匹配 New API get_channel_failed 且提示模型负载达到上限的响应，立即 failover 并冷却 1 小时",
      "enabled": true,
      "priority": 130,
      "event": "http_response",
      "match": {
        "status_ranges": [{"min": 400, "max": 599}],
        "json_condition_group": {
          "logic": "all",
          "conditions": [
            {"paths": ["error.code", "code"], "op": "equals", "value": "get_channel_failed"},
            {"paths": ["error.type", "type"], "op": "equals", "value": "new_api_error"},
            {"paths": ["error.message", "message"], "op": "contains", "value": "负载已经达到上限"}
          ]
        }
      },
      "action": {
        "failover": true,
        "cooldown_scope": "runtime",
        "cooldown_seconds": 3600,
        "jitter_percent": 0,
        "reason": "get_channel_failed_overloaded",
        "clear_session_binding": true
      }
    }'::jsonb;
BEGIN
    SELECT value::jsonb
    INTO policy_settings
    FROM settings
    WHERE key = 'gateway_failover_policy_settings';

    IF policy_settings IS NULL OR jsonb_typeof(policy_settings->'rules') <> 'array' THEN
        RETURN;
    END IF;

    policy_rules := policy_settings->'rules';
    IF jsonb_array_length(policy_rules) = 0 THEN
        RETURN;
    END IF;

    SELECT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(policy_rules) AS rule_item
        WHERE rule_item->>'id' = 'openai_get_channel_failed_overloaded'
    ) INTO rule_exists;

    IF rule_exists THEN
        RETURN;
    END IF;

    UPDATE settings
    SET value = jsonb_set(
            policy_settings,
            '{rules}',
            policy_rules || jsonb_build_array(overload_rule),
            true
        )::text,
        updated_at = NOW()
    WHERE key = 'gateway_failover_policy_settings';
EXCEPTION
    WHEN others THEN
        RAISE NOTICE 'skip get_channel_failed failover rule migration: %', SQLERRM;
END $$;
