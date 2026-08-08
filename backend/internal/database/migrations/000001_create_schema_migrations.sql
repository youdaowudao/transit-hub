-- rollback-safe: additive（仅新增表、列或索引，旧代码可忽略多出的结构）
-- 迁移系统自身的元数据表，记录已执行的迁移版本号
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT        PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
