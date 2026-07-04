-- Model mapping automation settings for account model tools.

INSERT INTO settings (key, value)
VALUES
  ('model_mapping_auto_rules', '[]'),
  ('model_batch_test_concurrency', '3')
ON CONFLICT (key) DO NOTHING;
