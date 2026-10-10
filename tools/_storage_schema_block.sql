-- ============ 存储中心(2026-10-10,契约 STORAGE_CONTRACTS.md) ============
-- 双写过渡:桶为真源+本地回退。协议 s3(SigV4 path-style)/webdav。
-- 凭据 secretbox 密文 base64 落库(密文进 TEXT 列一律 base64——项目铁律),API 永不回显。
CREATE TABLE IF NOT EXISTS storage_buckets (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    protocol     TEXT NOT NULL CHECK (protocol IN ('s3','webdav')),
    endpoint     TEXT NOT NULL,                 -- 形如 https://minio.example:9000(不带桶名)
    bucket       TEXT NOT NULL,
    region       TEXT NOT NULL DEFAULT 'us-east-1',
    path_prefix  TEXT NOT NULL DEFAULT '',      -- 对象 key 前缀,如 artex/
    access_key   TEXT NOT NULL DEFAULT '',      -- webdav = 用户名
    secret_enc   TEXT NOT NULL DEFAULT '',      -- secretbox+base64;webdav = 密码
    active       BOOLEAN NOT NULL DEFAULT false,
    status       TEXT NOT NULL DEFAULT 'unchecked' CHECK (status IN ('ok','degraded','unchecked')),
    last_check_at TIMESTAMPTZ,
    last_check_error TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_buckets_name ON storage_buckets (name);
-- 单默认桶:partial unique index,active 只允许一行 true。
CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_buckets_active ON storage_buckets (active) WHERE active;
DROP TRIGGER IF EXISTS trg_storage_buckets_upd ON storage_buckets;
CREATE TRIGGER trg_storage_buckets_upd BEFORE UPDATE ON storage_buckets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 已上桶对象登记表:key = path_prefix + category + '/' + rel_path(dataDir 相对路径)。
-- 读回退解析器与迁移幂等判重都查它;删除桶级联删登记行(桶上对象由用户自行清)。
CREATE TABLE IF NOT EXISTS storage_objects (
    id          BIGSERIAL PRIMARY KEY,
    bucket_id   BIGINT NOT NULL REFERENCES storage_buckets(id) ON DELETE CASCADE,
    category    TEXT NOT NULL,
    rel_path    TEXT NOT NULL,                 -- dataDir 相对路径,恒以 / 分隔
    size        BIGINT NOT NULL DEFAULT 0,
    uploaded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_objects ON storage_objects (bucket_id, rel_path);
CREATE INDEX IF NOT EXISTS idx_storage_objects_cat ON storage_objects (category);

-- 迁移作业:仿 report_jobs。cursor 存每类别最后完成 rel_path(settings 侧镜像
-- storage_migrate_cursor,供开机续跑;此处存 done 时的终态快照)。
CREATE TABLE IF NOT EXISTS storage_migrations (
    id           BIGSERIAL PRIMARY KEY,
    bucket_id    BIGINT NOT NULL REFERENCES storage_buckets(id) ON DELETE CASCADE,
    categories   TEXT[] NOT NULL DEFAULT '{}',
    status       TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','done','failed','canceled')),
    cursor       JSONB NOT NULL DEFAULT '{}'::jsonb,
    total_files  BIGINT NOT NULL DEFAULT 0,
    done_files   BIGINT NOT NULL DEFAULT 0,
    failed_files BIGINT NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ
);
