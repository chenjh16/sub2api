-- 将已保存的自动故障转移规则从旧的 *_logic / *_conditions 形态迁移到
-- 新的 *_condition_group 形态。运行时不再兼容旧字段，因此这里做一次性数据转换。
DO $$
DECLARE
    raw_settings jsonb;
    migrated_settings jsonb;
    rule_item jsonb;
    match_json jsonb;
    new_match jsonb;
    new_rules jsonb := '[]'::jsonb;
    logic_text text;
BEGIN
    SELECT value::jsonb
    INTO raw_settings
    FROM settings
    WHERE key = 'gateway_failover_policy_settings';

    IF raw_settings IS NULL THEN
        RETURN;
    END IF;

    IF jsonb_typeof(raw_settings->'rules') <> 'array'
       OR jsonb_array_length(raw_settings->'rules') = 0 THEN
        DELETE FROM settings WHERE key = 'gateway_failover_policy_settings';
        RETURN;
    END IF;

    FOR rule_item IN SELECT value FROM jsonb_array_elements(raw_settings->'rules')
    LOOP
        match_json := COALESCE(rule_item->'match', '{}'::jsonb);
        new_match := match_json
            - 'json_logic'
            - 'json_conditions'
            - 'header_logic'
            - 'header_conditions'
            - 'message_conditions'
            - 'body_conditions'
            - 'transport_conditions';

        IF jsonb_typeof(match_json->'json_conditions') = 'array'
           AND jsonb_array_length(match_json->'json_conditions') > 0 THEN
            logic_text := COALESCE(NULLIF(match_json->>'json_logic', ''), 'all');
            IF logic_text NOT IN ('all', 'any') THEN
                logic_text := 'all';
            END IF;
            new_match := jsonb_set(
                new_match,
                '{json_condition_group}',
                jsonb_build_object(
                    'logic', logic_text,
                    'conditions', match_json->'json_conditions'
                ),
                true
            );
        END IF;

        IF jsonb_typeof(match_json->'header_conditions') = 'array'
           AND jsonb_array_length(match_json->'header_conditions') > 0 THEN
            logic_text := COALESCE(NULLIF(match_json->>'header_logic', ''), 'all');
            IF logic_text NOT IN ('all', 'any') THEN
                logic_text := 'all';
            END IF;
            new_match := jsonb_set(
                new_match,
                '{header_condition_group}',
                jsonb_build_object(
                    'logic', logic_text,
                    'conditions', match_json->'header_conditions'
                ),
                true
            );
        END IF;

        IF jsonb_typeof(match_json->'message_conditions') = 'array'
           AND jsonb_array_length(match_json->'message_conditions') > 0 THEN
            new_match := jsonb_set(
                new_match,
                '{message_condition_group}',
                jsonb_build_object(
                    'logic', 'all',
                    'conditions', match_json->'message_conditions'
                ),
                true
            );
        END IF;

        IF jsonb_typeof(match_json->'body_conditions') = 'array'
           AND jsonb_array_length(match_json->'body_conditions') > 0 THEN
            new_match := jsonb_set(
                new_match,
                '{body_condition_group}',
                jsonb_build_object(
                    'logic', 'all',
                    'conditions', match_json->'body_conditions'
                ),
                true
            );
        END IF;

        IF jsonb_typeof(match_json->'transport_conditions') = 'array'
           AND jsonb_array_length(match_json->'transport_conditions') > 0 THEN
            new_match := jsonb_set(
                new_match,
                '{transport_condition_group}',
                jsonb_build_object(
                    'logic', 'all',
                    'conditions', match_json->'transport_conditions'
                ),
                true
            );
        END IF;

        new_rules := new_rules || jsonb_build_array(jsonb_set(rule_item, '{match}', new_match, true));
    END LOOP;

    migrated_settings := raw_settings
        - 'structured_400_enabled'
        - 'structured_400_cooldown_minutes'
        - 'failure_cooldown_jitter_percent'
        - 'http_5xx_cooldown_enabled'
        - 'http_5xx_threshold'
        - 'http_5xx_window_seconds'
        - 'http_5xx_cooldown_seconds'
        - 'transport_cooldown_enabled'
        - 'transport_threshold'
        - 'transport_window_seconds'
        - 'transport_cooldown_seconds';
    migrated_settings := jsonb_set(migrated_settings, '{rules}', new_rules, true);

    UPDATE settings
    SET value = migrated_settings::text,
        updated_at = NOW()
    WHERE key = 'gateway_failover_policy_settings';
EXCEPTION
    WHEN others THEN
        RAISE NOTICE 'skip gateway_failover_policy_settings condition group migration: %', SQLERRM;
END $$;
