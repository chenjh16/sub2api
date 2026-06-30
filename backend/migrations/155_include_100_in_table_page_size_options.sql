-- 将旧默认分页选项 [10,20,50] 升级为 [10,20,50,100]。
-- 只修改仍保持旧默认值的实例，不覆盖管理员已经自定义过的分页选项。
UPDATE settings
SET value = '[10,20,50,100]',
    updated_at = NOW()
WHERE key = 'table_page_size_options'
  AND regexp_replace(value, '\s+', '', 'g') = '[10,20,50]';
