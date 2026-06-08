ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS openai_default_service_tier varchar(20) NOT NULL DEFAULT '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'groups_openai_default_service_tier_check'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_openai_default_service_tier_check
            CHECK (openai_default_service_tier IN ('', 'priority', 'flex', 'auto', 'default', 'scale'));
    END IF;
END $$;
