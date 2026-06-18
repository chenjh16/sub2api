-- 结构化 OpenAI 上游限流/套餐限制规则要求固定冷却 10 分钟。
-- 早期 spec 默认把这几条规则的 jitter_percent 设置为 20，可能导致实际冷却
-- 落在 8-12 分钟之间。这里仅修正仍保持旧默认值的内置规则，不覆盖管理员
-- 已经自定义为其他抖动比例的规则。
DO $$
DECLARE
    policy_settings jsonb;
    migrated_rules jsonb := '[]'::jsonb;
    rule_item jsonb;
    changed boolean := false;
    target_rule_ids text[] := ARRAY[
        'openai_structured_400_cooldown',
        'openai_structured_400_rpm',
        'openai_request_too_large_tier_limit'
    ];
BEGIN
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
        IF rule_item->>'id' = ANY(target_rule_ids)
           AND COALESCE(rule_item#>>'{action,jitter_percent}', '') = '20' THEN
            rule_item := jsonb_set(rule_item, '{action,jitter_percent}', '0'::jsonb, true);
            changed := true;
        END IF;
        migrated_rules := migrated_rules || jsonb_build_array(rule_item);
    END LOOP;

    IF NOT changed THEN
        RETURN;
    END IF;

    policy_settings := jsonb_set(policy_settings, '{rules}', migrated_rules, true);
    UPDATE settings
    SET value = policy_settings::text,
        updated_at = NOW()
    WHERE key = 'gateway_failover_policy_settings';
EXCEPTION
    WHEN others THEN
        RAISE NOTICE 'skip structured openai failover jitter fix: %', SQLERRM;
END $$;
