-- ARTEX PostgreSQL schema (单一数据源)
-- 幂等：可重复执行（IF NOT EXISTS / OR REPLACE / DROP TRIGGER IF EXISTS）。

-- =====================================================================
-- 0. 通用：updated_at 触发器
-- =====================================================================
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = now(); RETURN NEW; END;
$$ LANGUAGE plpgsql;

-- 安全的 text→inet 转换：非法值返回 NULL 而不是抛 22P02。assets.ip 是自由文本
-- (Agent / 资产 API 可能写进主机名)，裸转 a.ip::inet 会让单独一行脏数据把整条
-- 企业归属重算语句打挂。调用方用 try_inet(...) IS NULL 找出这些行并告警。
-- 不用 pg_input_is_valid 是因为那要 PG16+，这里要兼容更老的存量库。
CREATE OR REPLACE FUNCTION try_inet(value text) RETURNS inet AS $$
BEGIN
    RETURN value::inet;
EXCEPTION WHEN others THEN
    RETURN NULL;
END;
$$ LANGUAGE plpgsql IMMUTABLE STRICT;

-- =====================================================================
-- A. 资产层：companies / assets / company_scope
-- =====================================================================

CREATE TABLE IF NOT EXISTS companies (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    nkey       TEXT NOT NULL UNIQUE,
    logo       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_companies_nkey ON companies(nkey);
DROP TRIGGER IF EXISTS trg_companies_upd ON companies;
CREATE TRIGGER trg_companies_upd BEFORE UPDATE ON companies
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS assets (
    id              BIGSERIAL PRIMARY KEY,
    type            TEXT NOT NULL CHECK (type IN (
                        'root_domain','ip','subdomain','app','service','endpoint'
                    )),
    company_id      BIGINT REFERENCES companies(id) ON DELETE SET NULL,
    -- explicit: caller/user selected the company; scope: derived from company_scope.
    -- Existing installations are conservatively migrated as explicit so a scope
    -- rebuild can never erase a historical manual association.
    company_source  TEXT NOT NULL DEFAULT 'explicit'
                    CHECK (company_source IN ('explicit','scope')),
    task_ids        BIGINT[] NOT NULL DEFAULT '{}',
    domain          TEXT,
    root_domain     TEXT,
    ip              TEXT,
    c_segment       CIDR,
    port            INTEGER CHECK (port BETWEEN 1 AND 65535),
    icp             TEXT,
    bound_domains   TEXT[]  NOT NULL DEFAULT '{}',
    open_ports      JSONB[] NOT NULL DEFAULT '{}',
    record_type     TEXT,
    record_value    TEXT[],
    bundle_id       TEXT,
    app_name        TEXT,
    category        TEXT,
    app_description TEXT,
    app_icp         TEXT,
    url             TEXT,
    service_type    TEXT CHECK (service_type IN ('http','other')),
    service_name    TEXT,
    favicon_mmh3    TEXT,
    status_code     INTEGER,
    content_length  BIGINT,
    page_title      TEXT,
    technologies    TEXT[]  NOT NULL DEFAULT '{}',
    auth            JSONB[] NOT NULL DEFAULT '{}',
    method          TEXT,
    params          JSONB[] NOT NULL DEFAULT '{}',
    extra           JSONB   NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_root_domain  ON assets(domain) WHERE type = 'root_domain';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_ip           ON assets(ip)     WHERE type = 'ip';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_subdomain    ON assets(domain, COALESCE(record_type,'')) WHERE type = 'subdomain';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_app_bundle   ON assets(bundle_id) WHERE type = 'app' AND bundle_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_app_name     ON assets(app_name)  WHERE type = 'app' AND bundle_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_service_http ON assets(url) WHERE type = 'service' AND service_type = 'http';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_service_other
    ON assets(COALESCE(domain,''), COALESCE(ip,''), port, service_name) WHERE type = 'service' AND service_type = 'other';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_endpoint     ON assets(url, method) WHERE type = 'endpoint';
CREATE INDEX IF NOT EXISTS idx_av2_company      ON assets(company_id)       WHERE company_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_company_type ON assets(company_id, type) WHERE company_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_task_ids     ON assets USING GIN(task_ids);
CREATE INDEX IF NOT EXISTS idx_av2_domain       ON assets(domain)      WHERE domain IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_root_domain  ON assets(root_domain) WHERE root_domain IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_ip           ON assets(ip)          WHERE ip IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_c_segment    ON assets USING GIST(c_segment inet_ops) WHERE c_segment IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_technologies ON assets USING GIN(technologies) WHERE type = 'service';
CREATE INDEX IF NOT EXISTS idx_av2_bound_domains ON assets USING GIN(bound_domains) WHERE type = 'ip';
CREATE INDEX IF NOT EXISTS idx_av2_open_ports   ON assets USING GIN(open_ports)    WHERE type = 'ip';
CREATE INDEX IF NOT EXISTS idx_av2_last_seen    ON assets(last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_av2_type_seen    ON assets(type, last_seen DESC);
ALTER TABLE assets ADD COLUMN IF NOT EXISTS company_source TEXT;
UPDATE assets SET company_source = 'explicit' WHERE company_source IS NULL;
ALTER TABLE assets ALTER COLUMN company_source SET DEFAULT 'explicit';
ALTER TABLE assets ALTER COLUMN company_source SET NOT NULL;
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_company_source_check;
ALTER TABLE assets ADD CONSTRAINT assets_company_source_check
    CHECK (company_source IN ('explicit','scope'));
DROP TRIGGER IF EXISTS trg_av2_upd ON assets;
CREATE TRIGGER trg_av2_upd BEFORE UPDATE ON assets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS company_scope (
    id         BIGSERIAL PRIMARY KEY,
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('domain','ip','cidr','icp','keyword')),
    domain     TEXT,
    net        CIDR,
    value      TEXT,
    raw        TEXT NOT NULL,
    reason     TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_sv2_domain UNIQUE (company_id, domain),
    CONSTRAINT uq_sv2_net    UNIQUE (company_id, net),
    CONSTRAINT ck_company_scope_payload CHECK (
        (kind = 'domain' AND domain IS NOT NULL AND net IS NULL AND value IS NULL)
        OR (kind IN ('ip','cidr') AND domain IS NULL AND net IS NOT NULL AND value IS NULL)
        OR (kind IN ('icp','keyword') AND domain IS NULL AND net IS NULL AND value IS NOT NULL)
    )
);
-- Existing installations need the new text payload and expanded kind check.
ALTER TABLE company_scope ADD COLUMN IF NOT EXISTS value TEXT;
ALTER TABLE company_scope DROP CONSTRAINT IF EXISTS company_scope_kind_check;
ALTER TABLE company_scope ADD CONSTRAINT company_scope_kind_check
    CHECK (kind IN ('domain','ip','cidr','icp','keyword'));
ALTER TABLE company_scope DROP CONSTRAINT IF EXISTS ck_company_scope_payload;
ALTER TABLE company_scope ADD CONSTRAINT ck_company_scope_payload CHECK (
    (kind = 'domain' AND domain IS NOT NULL AND net IS NULL AND value IS NULL)
    OR (kind IN ('ip','cidr') AND domain IS NULL AND net IS NOT NULL AND value IS NULL)
    OR (kind IN ('icp','keyword') AND domain IS NULL AND net IS NULL AND value IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_sv2_domain  ON company_scope(domain)   WHERE kind = 'domain';
CREATE INDEX IF NOT EXISTS idx_sv2_net     ON company_scope USING GIST(net inet_ops) WHERE kind IN ('ip','cidr');
CREATE UNIQUE INDEX IF NOT EXISTS uq_sv2_value ON company_scope(company_id, kind, value) WHERE kind IN ('icp','keyword');
CREATE INDEX IF NOT EXISTS idx_sv2_icp ON company_scope(value) WHERE kind = 'icp';
CREATE INDEX IF NOT EXISTS idx_sv2_company ON company_scope(company_id);

-- =====================================================================
-- B. 推理探索层
-- =====================================================================
CREATE TABLE IF NOT EXISTS explorations (
    id          BIGSERIAL PRIMARY KEY,
    description TEXT,
    goal        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open'
                  CHECK (status IN ('open','achieved','failed')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- cold-digest (§2.3): per-task planner round counter — bumped once each time the
-- planner wakes and processes a round. Drives the ≥R cold-node debounce (measured in
-- this exploration's own rounds, not global node ids or wall-clock).
ALTER TABLE explorations ADD COLUMN IF NOT EXISTS round_no BIGINT NOT NULL DEFAULT 0;
DROP TRIGGER IF EXISTS trg_exp_upd ON explorations;
CREATE TRIGGER trg_exp_upd BEFORE UPDATE ON explorations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS exploration_nodes (
    id             BIGSERIAL PRIMARY KEY,
    exploration_id BIGINT NOT NULL REFERENCES explorations(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL,
    payload        JSONB NOT NULL DEFAULT '{}',
    priority       INT  NOT NULL DEFAULT 0,
    state          TEXT NOT NULL DEFAULT 'open',
    origin         TEXT,
    owner          TEXT,
    blocked_reason TEXT,
    delete_reason  TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at   TIMESTAMPTZ,
    CONSTRAINT ck_node_kind CHECK (kind IN ('begin','goal','intent','fact','finding','hint','digest','recon_step','recon_finding','recon_summary')),
    CONSTRAINT ck_node_state CHECK (
        (kind='begin'   AND state IN ('open')) OR
        (kind='intent'  AND state IN ('open','running','paused','done','blocked','exhausted','stopped','deleted')) OR
        (kind='goal'    AND state IN ('open','met','abandoned')) OR
        (kind='fact'    AND state IN ('confirmed','dismissed','origin')) OR
        (kind='finding' AND state IN ('confirmed','dismissed')) OR
        (kind='hint'    AND state IN ('active','consumed')) OR
        (kind='digest'  AND state IN ('active','superseded')) OR
        (kind='recon_step'    AND state IN ('open','running','done','failed','skipped')) OR
        (kind='recon_finding' AND state IN ('confirmed','dismissed')) OR
        (kind='recon_summary' AND state IN ('active','superseded'))
    )
);
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS blocked_reason TEXT;
-- 意图假删除(soft delete):state='deleted' 时,delete_reason 记用户填写的删除原因。
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS delete_reason TEXT;
-- cold-digest (§2.3/§5.3): content_version bumps on any change that could alter a
-- digest body (summary/state/confidence); cold_since_round stamps the planner round
-- a node most recently went from "has a live downstream branch" to none (NULL = hot).
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS content_version  INT    NOT NULL DEFAULT 0;
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS cold_since_round BIGINT;
-- ck_node_kind: existing installs predate the 'digest' kind — recreate to allow it.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='exploration_nodes'::regclass
          AND conname='ck_node_kind'
          AND (pg_get_constraintdef(oid) NOT LIKE '%digest%'
               OR pg_get_constraintdef(oid) NOT LIKE '%recon_step%')
    ) THEN
        ALTER TABLE exploration_nodes DROP CONSTRAINT ck_node_kind;
        ALTER TABLE exploration_nodes ADD CONSTRAINT ck_node_kind
            CHECK (kind IN ('begin','goal','intent','fact','finding','hint','digest','recon_step','recon_finding','recon_summary'));
    END IF;
END $$;
-- ck_node_state: recreate when it lacks the 'paused' (older), 'superseded' (digest rev),
-- or 'deleted' (intent soft-delete rev) branches.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='exploration_nodes'::regclass
          AND conname='ck_node_state'
          AND (pg_get_constraintdef(oid) NOT LIKE '%paused%'
               OR pg_get_constraintdef(oid) NOT LIKE '%superseded%'
               OR pg_get_constraintdef(oid) NOT LIKE '%deleted%'
               OR pg_get_constraintdef(oid) NOT LIKE '%recon_step%')
    ) THEN
        ALTER TABLE exploration_nodes DROP CONSTRAINT ck_node_state;
        ALTER TABLE exploration_nodes ADD CONSTRAINT ck_node_state CHECK (
            (kind='begin'   AND state IN ('open')) OR
            (kind='intent'  AND state IN ('open','running','paused','done','blocked','exhausted','stopped','deleted')) OR
            (kind='goal'    AND state IN ('open','met','abandoned')) OR
            (kind='fact'    AND state IN ('confirmed','dismissed','origin')) OR
            (kind='finding' AND state IN ('confirmed','dismissed')) OR
            (kind='hint'    AND state IN ('active','consumed')) OR
            (kind='digest'  AND state IN ('active','superseded')) OR
            (kind='recon_step'    AND state IN ('open','running','done','failed','skipped')) OR
            (kind='recon_finding' AND state IN ('confirmed','dismissed')) OR
            (kind='recon_summary' AND state IN ('active','superseded'))
        );
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_expnodes_part     ON exploration_nodes(exploration_id, kind);
CREATE INDEX IF NOT EXISTS idx_expnodes_frontier ON exploration_nodes(exploration_id, priority DESC)
    WHERE kind='intent' AND state='open';
DROP TRIGGER IF EXISTS trg_expnodes_upd ON exploration_nodes;
CREATE TRIGGER trg_expnodes_upd BEFORE UPDATE ON exploration_nodes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS exploration_edges (
    exploration_id BIGINT NOT NULL REFERENCES explorations(id) ON DELETE CASCADE,
    src_id         BIGINT NOT NULL REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    dst_id         BIGINT NOT NULL REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    rel            TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (exploration_id, src_id, rel, dst_id),
    CONSTRAINT ck_edge_noself CHECK (src_id <> dst_id),
    CONSTRAINT ck_edge_rel CHECK (rel IN ('spawns','derived_from','yields','proves','covers'))
);
CREATE INDEX IF NOT EXISTS idx_expedges_src ON exploration_edges(src_id, rel);
CREATE INDEX IF NOT EXISTS idx_expedges_dst ON exploration_edges(dst_id, rel);
-- cold-digest (§1): the 'covers' relation (digest→member) postdates shipped installs,
-- whose rel CHECK is an inline auto-named constraint. Find and recreate it as ck_edge_rel.
DO $$
DECLARE cname text;
BEGIN
    SELECT conname INTO cname FROM pg_constraint
     WHERE conrelid='exploration_edges'::regclass AND contype='c'
       AND pg_get_constraintdef(oid) LIKE '%rel%'
       AND pg_get_constraintdef(oid) NOT LIKE '%covers%';
    IF cname IS NOT NULL THEN
        EXECUTE 'ALTER TABLE exploration_edges DROP CONSTRAINT '||quote_ident(cname);
        ALTER TABLE exploration_edges ADD CONSTRAINT ck_edge_rel
            CHECK (rel IN ('spawns','derived_from','yields','proves','covers'));
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS exploration_anchors (
    node_id   BIGINT NOT NULL REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    asset_id  BIGINT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    PRIMARY KEY (node_id, asset_id)
);
CREATE INDEX IF NOT EXISTS idx_anchor_asset ON exploration_anchors(asset_id);

-- task_constraints: operator-authored operation constraints (allow/deny) for a task.
-- Extracted by the goals decomposer at round 0 (from goal/description), editable at
-- runtime by the main agent + 总览「约束管理」. Injected into the planner/worker system
-- prompt each round (config-gated) to keep exploration within the operator's boundary.
CREATE TABLE IF NOT EXISTS task_constraints (
    id             BIGSERIAL PRIMARY KEY,
    exploration_id BIGINT NOT NULL REFERENCES explorations(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('allow','deny')),
    text           TEXT NOT NULL,
    origin         TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_task_constraints_exp ON task_constraints(exploration_id);

CREATE TABLE IF NOT EXISTS activity (
    id                 BIGSERIAL PRIMARY KEY,
    exploration_id     BIGINT NOT NULL REFERENCES explorations(id) ON DELETE CASCADE,
    node_id            BIGINT REFERENCES exploration_nodes(id) ON DELETE SET NULL,
    worker             TEXT,
    kind               TEXT,
    tool               TEXT,
    tool_use_id        TEXT,
    is_error           BOOLEAN NOT NULL DEFAULT false,
    summary            TEXT,
    detail             TEXT,
    metadata           JSONB NOT NULL DEFAULT '{}',
    input_tokens       INTEGER,
    output_tokens      INTEGER,
    cache_read_tokens  INTEGER,
    cache_write_tokens INTEGER,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE activity ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}';
-- main_seg segments the main-agent session into resettable conversations: a new
-- main session bumps the segment so its transcript + activity start clean while the
-- task's graph/assets/goal are untouched. NULL == legacy rows == segment 0 (the
-- original session). Only worker='mainagent' rows carry it.
ALTER TABLE activity ADD COLUMN IF NOT EXISTS main_seg INTEGER;
CREATE INDEX IF NOT EXISTS idx_act_node  ON activity(exploration_id, node_id, id);
CREATE INDEX IF NOT EXISTS idx_act_since ON activity(exploration_id, id);
CREATE INDEX IF NOT EXISTS idx_act_tool_call ON activity(exploration_id, tool_use_id, id)
  WHERE kind IN ('tool_use', 'tool_result');
-- Main/Plan history pages filter by worker (both carry NULL node_id, so idx_act_node
-- can't distinguish them); this covers reverse pagination of those sessions.
CREATE INDEX IF NOT EXISTS idx_act_worker ON activity(exploration_id, worker, id);
-- Main-session pages filter by segment on top of worker='mainagent'; this partial
-- index covers reverse pagination within one segment.
CREATE INDEX IF NOT EXISTS idx_act_main_seg ON activity(exploration_id, main_seg, id)
    WHERE worker='mainagent';
-- Task-list polls aggregate result usage and find the latest event repeatedly.
-- Cover the token columns for index-only aggregation and the timestamp order for
-- per-exploration latest-activity lookups.
CREATE INDEX IF NOT EXISTS idx_act_result_usage ON activity(exploration_id)
    INCLUDE (input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
    WHERE kind='result';
CREATE INDEX IF NOT EXISTS idx_act_latest ON activity(exploration_id, created_at DESC);

-- main_sessions records the resettable main-agent conversation segments of a task.
-- Segment 0 (the original session) is implicit and never stored; this table holds
-- only the extra segments created by "新建会话" (seq >= 1). The current segment is
-- MAX(seq) or 0. Each segment gets its own transcript file + activity slice; the
-- task's exploration graph/assets/goal are shared and never reset.
CREATE TABLE IF NOT EXISTS main_sessions (
    exploration_id BIGINT NOT NULL REFERENCES explorations(id) ON DELETE CASCADE,
    seq            INTEGER NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (exploration_id, seq)
);

-- =====================================================================
-- C. LLM profiles
-- =====================================================================
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS llm_profiles (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    format           TEXT NOT NULL CHECK (format IN ('openai','anthropic','openai-responses')),
    base_url         TEXT,
    proxy            TEXT,
    model            TEXT NOT NULL,
    api_key          TEXT,
    api_key_hint     TEXT,
    rate_per_second  DOUBLE PRECISION NOT NULL DEFAULT 0,
    rate_per_minute  DOUBLE PRECISION NOT NULL DEFAULT 0,
    context_window_k INTEGER NOT NULL DEFAULT 0,
    -- 思考参数拆成两个独立字段：thinking_type=思考开关(''/disabled/enabled)，
    -- reasoning_effort=思考强度(''/low/medium/high/xhigh/max)，互不牵连。
    reasoning_effort TEXT NOT NULL DEFAULT '',
    thinking_type    TEXT NOT NULL DEFAULT '',
    is_default       BOOLEAN NOT NULL DEFAULT false,
    -- 轮询(故障转移)参数，见 docs/LLM轮询设计.md：
    --   priority     顺位，越大越先被选中；激活配置(is_default)永远排链首，与本值无关。
    --   pool_exclude true=不作为故障转移目标(仍可被 agent/任务显式绑定使用)。
    priority         INTEGER NOT NULL DEFAULT 0,
    pool_exclude     BOOLEAN NOT NULL DEFAULT false,
    -- streaming=true(默认)走流式 SSE；false 走真·非流式(stream:false，一次性 JSON)。
    streaming        BOOLEAN NOT NULL DEFAULT true,
    -- 单次回复的输出上限(token)。0=不发送该字段，由服务端默认值决定——保持既有行为。
    -- 与 context_window_k(模型总容量，仅本地用于压缩阈值)是两回事：本值会随请求发出。
    max_tokens       INTEGER NOT NULL DEFAULT 0,
    -- 输出上限用哪个请求字段名，仅对 format='openai' 生效：
    --   ''                      = max_tokens(默认，兼容绝大多数网关)
    --   'max_completion_tokens' = 新字段；OpenAI 推理模型(o 系列/GPT-5)只认它，
    --                             发 max_tokens 会被 unsupported_parameter 拒绝。
    -- anthropic(max_tokens 必填)与 openai-responses(max_output_tokens)自带字段名，不受此值影响。
    max_tokens_field TEXT NOT NULL DEFAULT '',
    -- 自定义会话头：非空时每次请求带一个该名字的 HTTP 头，头值=当前运行的 session id
    -- (chat 会话/worker 意图)。用于某些按 session-id 头做提示缓存/粘性路由的网关。''=不发送。
    session_header_key TEXT NOT NULL DEFAULT '',
    -- 重试覆盖：次数 0=用全局默认/-1=关闭/>0=该值；间隔 0=用默认指数退避/>0=固定毫秒。
    -- 三组分别对应建连重试、空响应重试、同 provider 安全窗口重试，详见下方 ALTER 处注释。
    retry_connect_attempts    INTEGER NOT NULL DEFAULT 0,
    retry_connect_interval_ms INTEGER NOT NULL DEFAULT 0,
    retry_empty_attempts      INTEGER NOT NULL DEFAULT 0,
    retry_empty_interval_ms   INTEGER NOT NULL DEFAULT 0,
    retry_stream_attempts     INTEGER NOT NULL DEFAULT 0,
    retry_stream_interval_ms  INTEGER NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_one_default ON llm_profiles(is_default) WHERE is_default;
DROP TRIGGER IF EXISTS trg_llm_upd ON llm_profiles;
CREATE TRIGGER trg_llm_upd BEFORE UPDATE ON llm_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- 轮询顺位/排除标记；补旧库。默认 0 / false = 全部配置都参与轮询。
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS priority     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS pool_exclude BOOLEAN NOT NULL DEFAULT false;
-- 流式开关；补旧库。默认 true = 保持既有的流式行为，旧配置无感升级。
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS streaming    BOOLEAN NOT NULL DEFAULT true;
-- 放开 format 约束以容纳 openai-responses(OpenAI Responses API)；补旧库。
-- 每次启动执行,幂等:先删旧 CHECK 再建含三值的新 CHECK。
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_format_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_format_check
    CHECK (format IN ('openai','anthropic','openai-responses'));

-- 输出上限及其字段名；补旧库。默认 0 / '' = 不发送上限、沿用 max_tokens 字段名，
-- 旧配置行为完全不变。
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS max_tokens       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS max_tokens_field TEXT    NOT NULL DEFAULT '';
-- 同 format：先删再建，保证每次启动幂等。
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_max_tokens_field_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_max_tokens_field_check
    CHECK (max_tokens_field IN ('','max_completion_tokens'));
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_max_tokens_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_max_tokens_check
    CHECK (max_tokens >= 0);
-- 自定义会话头名；补旧库。默认 '' = 不发送，旧配置行为不变。
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS session_header_key TEXT NOT NULL DEFAULT '';

-- 单配置的重试覆盖（见 docs/LLM重试设计.md）。三组各自一对「次数 + 固定间隔」，
-- 语义统一：次数 0=沿用全局默认、-1=关闭该层重试、>0=用该值；间隔 0=沿用该层的
-- 默认指数退避、>0=改用这个固定毫秒数。全部默认 0，所以旧库/旧配置行为不变。
--   connect = 建连重试（SDK doStream：连接重置/超时/429/5xx，流开始前）
--   empty   = 空响应重试（SDK：完成但没有任何 content block，仅 openai 格式）
--   stream  = 同 provider 安全窗口重试（本项目 task_llm：未交付输出前的断流重放）
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_connect_attempts    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_connect_interval_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_empty_attempts      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_empty_interval_ms   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_stream_attempts     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_stream_interval_ms  INTEGER NOT NULL DEFAULT 0;
-- 同 format：先删再建，保证每次启动幂等。次数下限 -1(关闭)，间隔不能为负。
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_retry_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_retry_check CHECK (
    retry_connect_attempts >= -1 AND retry_empty_attempts >= -1 AND retry_stream_attempts >= -1
    AND retry_connect_interval_ms >= 0 AND retry_empty_interval_ms >= 0 AND retry_stream_interval_ms >= 0);

-- 思考开关字段 thinking_type，从旧的单一 reasoning_effort 语义一次性拆分而来。
-- schema.sql 每次启动都执行，故迁移必须只跑一次：仅当该列尚不存在时才回填，
-- 否则每次启动都会把用户后来手动设的组合覆盖回去。旧 reasoning_effort 语义：
--   'off'                    → 显式关闭  → thinking_type='disabled'，强度清空
--   'low/medium/high/max'    → 开启+强度 → thinking_type='enabled'，强度保留
--   ''                       → 不发送    → 两者皆空(默认)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'llm_profiles' AND column_name = 'thinking_type'
    ) THEN
        ALTER TABLE llm_profiles ADD COLUMN thinking_type TEXT NOT NULL DEFAULT '';
        UPDATE llm_profiles SET thinking_type = 'enabled'
            WHERE reasoning_effort IN ('low','medium','high','max');
        UPDATE llm_profiles SET thinking_type = 'disabled', reasoning_effort = ''
            WHERE reasoning_effort = 'off';
    END IF;
END $$;

-- LLM 轮询熔断状态：某个配置连续失败(余额不足/key 失效/限流)后进入冷却，冷却期内
-- 轮询直接跳过它。内存态为准，这里落库只为重启后不丢冷却窗口——加载时只取尚未
-- 到期的行(open_until > now)，已到期的自然回到"正常"，等下一次调用半开试探。
CREATE TABLE IF NOT EXISTS llm_profile_health (
    profile_id  BIGINT PRIMARY KEY REFERENCES llm_profiles(id) ON DELETE CASCADE,
    fails       INTEGER NOT NULL DEFAULT 0,  -- 当前连续失败次数(成功即清零)
    trips       INTEGER NOT NULL DEFAULT 0,  -- 累计熔断次数,用于冷却时间指数退避
    open_until  TIMESTAMPTZ,                 -- 冷却截止;NULL/过期 = 未熔断
    last_error  TEXT NOT NULL DEFAULT '',
    last_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- =====================================================================
-- D. 任务层
-- =====================================================================
-- Global task categories are intentionally independent from task templates.
-- Deleting a category only moves its tasks back to the uncategorized bucket.
CREATE TABLE IF NOT EXISTS task_categories (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    nkey       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_task_categories_name ON task_categories(name, id);
DROP TRIGGER IF EXISTS trg_task_categories_upd ON task_categories;
CREATE TRIGGER trg_task_categories_upd BEFORE UPDATE ON task_categories
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS tasks (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL DEFAULT '',
    category_id    BIGINT REFERENCES task_categories(id) ON DELETE SET NULL,
    description    TEXT NOT NULL,
    goal           TEXT NOT NULL,
    exploration_id BIGINT NOT NULL UNIQUE
                     REFERENCES explorations(id) ON DELETE RESTRICT,
    status         TEXT NOT NULL DEFAULT 'created'
                     CHECK (status IN ('created','running','paused','done','failed','timeout')),
    paused         BOOLEAN NOT NULL DEFAULT false,
    queued         BOOLEAN NOT NULL DEFAULT false,
    queued_at      TIMESTAMPTZ,
    queue_mode     TEXT NOT NULL DEFAULT '',
    llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL,
    active_llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL,
    llm_chain_revision BIGINT NOT NULL DEFAULT 0,
    company_id     BIGINT REFERENCES companies(id) ON DELETE SET NULL,
    parent_ref     TEXT,
    timeout_seconds INTEGER NOT NULL DEFAULT 0,
    plan_heartbeat_seconds INTEGER NOT NULL DEFAULT 300,
    coverage_enabled BOOLEAN NOT NULL DEFAULT true,
    pinned_at      TIMESTAMPTZ,
    first_run_at   TIMESTAMPTZ,
    deadline_at    TIMESTAMPTZ,
    archived_at    TIMESTAMPTZ,
    deleted_at     TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tasks_alive  ON tasks(created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)          WHERE deleted_at IS NULL;
DROP TRIGGER IF EXISTS trg_tasks_upd ON tasks;
CREATE TRIGGER trg_tasks_upd BEFORE UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- planner 心跳触发间隔(秒);补旧库。默认 300s(5min)。见 docs/planner-trigger-impl-plan.md
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS plan_heartbeat_seconds INTEGER NOT NULL DEFAULT 300;
-- 并发上限挂起态;补旧库。true=因并发上限排队、等待空位自动启动。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS queued BOOLEAN NOT NULL DEFAULT false;
-- 资产覆盖度功能开关;补旧库。true(默认)=计算/展示测试覆盖度、自动累积测试范围、
-- 给 agent 开放 add_task_scope/list_untested_assets;false=全部关闭(见 task_scope.go)。
-- 存量任务默认 true 保持原行为;company 关联(task_scope kind=company)不受此开关影响。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS coverage_enabled BOOLEAN NOT NULL DEFAULT true;
-- queued_at makes admission FIFO reflect the actual enqueue order rather than the
-- task creation order. queue_mode distinguishes first bootstrap from resuming an
-- exploration that already owns goals/history.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS queued_at TIMESTAMPTZ;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS queue_mode TEXT NOT NULL DEFAULT '';
-- 可选的任务名称;补旧库。空串=未命名,前端展示时回退到描述。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS category_id BIGINT REFERENCES task_categories(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS active_llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS llm_chain_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_tasks_category ON tasks(category_id, created_at DESC)
    WHERE deleted_at IS NULL AND category_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_pinned ON tasks(pinned_at DESC)
    WHERE deleted_at IS NULL AND pinned_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_archived ON tasks(archived_at DESC)
    WHERE archived_at IS NOT NULL;

-- Cold task archives retain only compact metadata in PostgreSQL. The complete
-- task payload lives in a versioned .tar.zst package under data/archives/tasks.
-- task_id stays unique so an operation can be retried safely after a restart.
CREATE TABLE IF NOT EXISTS task_archives (
    id                         BIGSERIAL PRIMARY KEY,
    task_id                    BIGINT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
    state                      TEXT NOT NULL DEFAULT 'archive_queued' CHECK (state IN (
                                   'archive_queued','archiving','archive_failed','ready',
                                   'restore_queued','restoring','restore_failed',
                                   'delete_queued','deleting','delete_failed'
                               )),
    phase                      TEXT NOT NULL DEFAULT 'queued',
    progress                   INTEGER NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    error                      TEXT NOT NULL DEFAULT '',
    warnings                   JSONB NOT NULL DEFAULT '[]',
    format_version             INTEGER NOT NULL DEFAULT 2,
    archive_path               TEXT NOT NULL DEFAULT '',
    sha256                     TEXT NOT NULL DEFAULT '',
    original_size              BIGINT NOT NULL DEFAULT 0,
    compressed_size            BIGINT NOT NULL DEFAULT 0,
    task_name                  TEXT NOT NULL DEFAULT '',
    task_description           TEXT NOT NULL DEFAULT '',
    task_goal                  TEXT NOT NULL DEFAULT '',
    original_status            TEXT NOT NULL DEFAULT '',
    category_id_snapshot       BIGINT,
    category_name_snapshot     TEXT NOT NULL DEFAULT '',
    source_task_ids            BIGINT[] NOT NULL DEFAULT '{}',
    remaining_timeout_seconds  BIGINT NOT NULL DEFAULT 0,
    data_counts                JSONB NOT NULL DEFAULT '{}',
    aggregate_stats            JSONB NOT NULL DEFAULT '{}',
    archived_at                TIMESTAMPTZ,
    requested_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE task_archives ALTER COLUMN format_version SET DEFAULT 2;
CREATE INDEX IF NOT EXISTS idx_task_archives_state ON task_archives(state, requested_at, id);
CREATE INDEX IF NOT EXISTS idx_task_archives_archived ON task_archives(archived_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_task_archives_sources ON task_archives USING GIN(source_task_ids);
DROP TRIGGER IF EXISTS trg_task_archives_upd ON task_archives;
CREATE TRIGGER trg_task_archives_upd BEFORE UPDATE ON task_archives
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Reusable task description/goal presets. nkey is the normalized, case-insensitive
-- identity used to reject visually equivalent duplicate names.
CREATE TABLE IF NOT EXISTS task_templates (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    nkey        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    goal        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_task_templates_updated ON task_templates(updated_at DESC, id DESC);
DROP TRIGGER IF EXISTS trg_task_templates_upd ON task_templates;
CREATE TRIGGER trg_task_templates_upd BEFORE UPDATE ON task_templates
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Direct, read-only task context inheritance. Relations are intentionally not
-- recursive: a task sees only the source tasks explicitly chosen at creation.
CREATE TABLE IF NOT EXISTS task_relations (
    task_id        BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    source_task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, source_task_id),
    CONSTRAINT ck_task_relation_not_self CHECK (task_id <> source_task_id)
);
CREATE INDEX IF NOT EXISTS idx_task_relations_source ON task_relations(source_task_id);

-- Task/asset provenance supplements the legacy assets.task_ids association. The
-- array remains the compatibility source for existing query and cleanup paths;
-- this relation records how each association was obtained for operator review.
CREATE TABLE IF NOT EXISTS task_asset_links (
    task_id        BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    asset_id       BIGINT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    source         TEXT NOT NULL DEFAULT 'system',
    source_summary TEXT NOT NULL DEFAULT '',
    source_node_id BIGINT REFERENCES exploration_nodes(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, asset_id)
);
CREATE INDEX IF NOT EXISTS idx_task_asset_links_asset ON task_asset_links(asset_id, task_id);
CREATE INDEX IF NOT EXISTS idx_task_asset_links_node ON task_asset_links(source_node_id)
    WHERE source_node_id IS NOT NULL;
DROP TRIGGER IF EXISTS trg_task_asset_links_upd ON task_asset_links;
CREATE TRIGGER trg_task_asset_links_upd BEFORE UPDATE ON task_asset_links
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Keep provenance rows synchronized when existing asset upsert paths append or
-- remove task ids. Detailed callers overwrite the generic source after upsert.
CREATE OR REPLACE FUNCTION sync_task_asset_links() RETURNS trigger AS $$
BEGIN
    INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
    SELECT task.id, NEW.id, 'system', '任务执行期间自动关联'
    FROM unnest(NEW.task_ids) AS requested(task_id)
    JOIN tasks task ON task.id=requested.task_id AND task.deleted_at IS NULL
    ON CONFLICT (task_id, asset_id) DO NOTHING;

    DELETE FROM task_asset_links link
    WHERE link.asset_id=NEW.id
      AND NOT (link.task_id=ANY(NEW.task_ids));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_assets_task_links ON assets;
CREATE TRIGGER trg_assets_task_links AFTER INSERT OR UPDATE OF task_ids ON assets
    FOR EACH ROW EXECUTE FUNCTION sync_task_asset_links();

-- Existing installations receive an auditable legacy source without rewriting
-- task_ids. Ignore stale array ids that no longer resolve to a live task.
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
SELECT task.id, asset.id, 'legacy', '由历史任务资产关联迁移'
FROM assets asset
CROSS JOIN LATERAL unnest(asset.task_ids) AS requested(task_id)
JOIN tasks task ON task.id=requested.task_id AND task.deleted_at IS NULL
ON CONFLICT (task_id, asset_id) DO NOTHING;

-- Ordered task-level LLM failover chain. A quota-exhausted entry is skipped
-- until the user saves/resets the chain, which clears all failure state.
CREATE TABLE IF NOT EXISTS task_llm_profiles (
    task_id          BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    profile_id       BIGINT NOT NULL REFERENCES llm_profiles(id) ON DELETE CASCADE,
    position         INTEGER NOT NULL CHECK (position >= 0),
    status           TEXT NOT NULL DEFAULT 'ready'
                       CHECK (status IN ('ready','quota_exhausted')),
    last_error       TEXT,
    exhausted_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, profile_id),
    UNIQUE (task_id, position)
);
CREATE INDEX IF NOT EXISTS idx_task_llm_profiles_order ON task_llm_profiles(task_id, position);
CREATE INDEX IF NOT EXISTS idx_task_llm_profiles_profile ON task_llm_profiles(profile_id, task_id);
CREATE INDEX IF NOT EXISTS idx_tasks_llm_profile ON tasks(llm_profile_id) WHERE llm_profile_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_active_llm_profile ON tasks(active_llm_profile_id) WHERE active_llm_profile_id IS NOT NULL;
DROP TRIGGER IF EXISTS trg_task_llm_profiles_upd ON task_llm_profiles;
CREATE TRIGGER trg_task_llm_profiles_upd BEFORE UPDATE ON task_llm_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One-time-compatible backfill: old pinned tasks become one-entry chains. A user
-- can still clear the chain later because the update path also clears the legacy
-- llm_profile_id column, preventing this block from re-adding it on restart.
INSERT INTO task_llm_profiles(task_id, profile_id, position)
SELECT t.id, t.llm_profile_id, 0
FROM tasks t
WHERE t.llm_profile_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM task_llm_profiles x WHERE x.task_id=t.id)
ON CONFLICT DO NOTHING;
UPDATE tasks t
SET active_llm_profile_id = t.llm_profile_id
WHERE t.active_llm_profile_id IS NULL
  AND t.llm_profile_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM task_llm_profiles x WHERE x.task_id=t.id AND x.profile_id=t.llm_profile_id);

-- 任务测试范围（资产覆盖度的分母 + 授权边界）。
--   自动填(source='auto')：insertAssets 顶层按 worker 显式插入的资产类型加保守范围
--     （root_domain→root_domain，subdomain/service/endpoint→subdomain(host)，ip→ip）；
--     side-effect 派生的资产不入范围（钩子在 handler 顶层，派生在 db 层内部）。
--   agent 填(source='agent')：add_task_scope 加 company/root_domain/subdomain/ip。
-- 覆盖度 = 匹配 active 行的 assets（分母）中，被 fact 节点锚定过的占比（分子）。
CREATE TABLE IF NOT EXISTS task_scope (
    id          BIGSERIAL PRIMARY KEY,
    task_id     BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('company','root_domain','subdomain','ip','cidr','icp','keyword')),
    company_id  BIGINT REFERENCES companies(id) ON DELETE CASCADE,  -- kind='company'
    domain      TEXT,          -- root_domain / subdomain
    net         CIDR,          -- ip / cidr
    value       TEXT,          -- icp / keyword
    source      TEXT NOT NULL DEFAULT 'auto' CHECK (source IN ('auto','agent','manual')),
    reason      TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 旧库升级：扩展任务范围，使其与企业范围的单文本框识别能力一致。
ALTER TABLE task_scope ADD COLUMN IF NOT EXISTS value TEXT;
ALTER TABLE task_scope DROP CONSTRAINT IF EXISTS task_scope_kind_check;
ALTER TABLE task_scope ADD CONSTRAINT task_scope_kind_check
    CHECK (kind IN ('company','root_domain','subdomain','ip','cidr','icp','keyword'));
-- 去重：同一 task 的同一条范围只存一次（自动填批量插入靠它幂等）。
DROP INDEX IF EXISTS uq_task_scope;
CREATE UNIQUE INDEX IF NOT EXISTS uq_task_scope_v2 ON task_scope(
    task_id, kind, COALESCE(domain,''), COALESCE(net::text,''), COALESCE(company_id,0), COALESCE(value,''));
CREATE INDEX IF NOT EXISTS idx_ts_domain  ON task_scope(domain) WHERE kind IN ('root_domain','subdomain');
CREATE INDEX IF NOT EXISTS idx_ts_net     ON task_scope USING GIST(net inet_ops) WHERE kind IN ('ip','cidr');
CREATE INDEX IF NOT EXISTS idx_ts_company ON task_scope(company_id) WHERE kind = 'company';

-- =====================================================================
-- E. Agents / 提示词模板 / 变量目录
-- =====================================================================
CREATE TABLE IF NOT EXISTS agents (
    id                BIGSERIAL PRIMARY KEY,
    key               TEXT NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9_]*$'),
    name              TEXT NOT NULL,
    description       TEXT,
    role              TEXT NOT NULL,
    stage             TEXT NOT NULL DEFAULT '',
    builtin           BOOLEAN NOT NULL DEFAULT true,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    llm_profile_id    BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL,
    current_prompt_id BIGINT,
    max_turns         INTEGER NOT NULL DEFAULT 0,
    run_seconds       INTEGER NOT NULL DEFAULT 1200,
    web_search        BOOLEAN NOT NULL DEFAULT false,
    interactive_shell BOOLEAN NOT NULL DEFAULT false,
    wrapup_prompt     TEXT NOT NULL DEFAULT '',
    wrapup_max_turns  INTEGER NOT NULL DEFAULT 0,
    task_timeout_wrapup_prompt    TEXT NOT NULL DEFAULT '',
    task_timeout_wrapup_max_turns INTEGER NOT NULL DEFAULT 0,
    trigger_run_mode     TEXT    NOT NULL DEFAULT 'serial'  CHECK (trigger_run_mode IN ('serial','parallel')),
    trigger_merge_mode   TEXT    NOT NULL DEFAULT 'all' CHECK (trigger_merge_mode IN ('by_task','all','none')),
    trigger_max_parallel INTEGER NOT NULL DEFAULT 5,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agents_role_ck CHECK (role IN ('goals','main','planner','worker','assistant'))
);
-- 加列迁移(已发版,旧库升级补列;新库 CREATE 已含。迁移不带 CHECK:旧库存量安全 + 后端写入白名单兜底)。
ALTER TABLE agents ADD COLUMN IF NOT EXISTS trigger_run_mode     TEXT    NOT NULL DEFAULT 'serial';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS trigger_merge_mode   TEXT    NOT NULL DEFAULT 'all';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS trigger_max_parallel INTEGER NOT NULL DEFAULT 5;
-- agent 归属阶段(''=通用,跨阶段;recon/exploit/post/report):三阶段分组与报告中心的
-- 「对话」入口按 ?stage= 只显示本阶段 agent 的会话,''=通用只在任务中心(全部)可见。与 tools.stage 同一套取值。
ALTER TABLE agents ADD COLUMN IF NOT EXISTS stage TEXT NOT NULL DEFAULT '';
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_stage_check;
ALTER TABLE agents ADD CONSTRAINT agents_stage_check CHECK (stage IN ('','recon','exploit','post','report'));
-- 回填内置 agent 的阶段归属:仅在仍为空时写入,不覆盖用户后续手改。
UPDATE agents SET stage='recon'   WHERE key IN ('recon_worker','recon_analyst') AND stage='';
UPDATE agents SET stage='exploit' WHERE key = 'pentest' AND stage='';
-- per-agent LLM 绑定(agent 级默认模型):列自初版即在上方 CREATE 中,此 ALTER 仅为极旧库兜底(幂等)。
ALTER TABLE agents ADD COLUMN IF NOT EXISTS llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL;
-- run_seconds 单次 run 墙钟默认 600→1200:只改列默认(影响将来新插入的行),不动旧库存量行。
ALTER TABLE agents ALTER COLUMN run_seconds SET DEFAULT 1200;
CREATE INDEX IF NOT EXISTS idx_agents_llm_profile ON agents(llm_profile_id) WHERE llm_profile_id IS NOT NULL;
DROP TRIGGER IF EXISTS trg_agents_upd ON agents;
CREATE TRIGGER trg_agents_upd BEFORE UPDATE ON agents
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS agent_prompts (
    id            BIGSERIAL PRIMARY KEY,
    agent_id      BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    version       INT NOT NULL,
    template_text TEXT NOT NULL,
    note          TEXT,
    updated_by    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (agent_id, version)
);
-- 循环外键：agents.current_prompt_id → agent_prompts.id（需在两表创建后加）
DO $$ BEGIN
    ALTER TABLE agents ADD CONSTRAINT fk_agents_curprompt
        FOREIGN KEY (current_prompt_id) REFERENCES agent_prompts(id) ON DELETE SET NULL;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS agent_prompt_vars (
    id          BIGSERIAL PRIMARY KEY,
    agent_id    BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    var_name    TEXT NOT NULL,
    description TEXT,
    example     TEXT,
    source      TEXT NOT NULL CHECK (source IN ('exploration','runtime','distilled')),
    UNIQUE (agent_id, var_name)
);

-- =====================================================================
-- F. MCP 服务
-- =====================================================================
CREATE TABLE IF NOT EXISTS mcp_servers (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    transport   TEXT NOT NULL CHECK (transport IN ('stdio','http','sse')),
    command     TEXT,
    args        JSONB NOT NULL DEFAULT '[]',
    env         JSONB NOT NULL DEFAULT '{}',
    url         TEXT,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    insecure    BOOLEAN NOT NULL DEFAULT false,  -- http: 跳过 TLS 证书校验(自签证书场景, issue #108)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Allow legacy MCP SSE servers on databases created before SSE support.
ALTER TABLE mcp_servers DROP CONSTRAINT IF EXISTS mcp_servers_transport_check;
ALTER TABLE mcp_servers ADD CONSTRAINT mcp_servers_transport_check
    CHECK (transport IN ('stdio','http','sse'));
-- 旧库补列(schema.sql 每次启动都会 Exec)。
ALTER TABLE mcp_servers ADD COLUMN IF NOT EXISTS insecure BOOLEAN NOT NULL DEFAULT false;
DROP TRIGGER IF EXISTS trg_mcp_upd ON mcp_servers;
CREATE TRIGGER trg_mcp_upd BEFORE UPDATE ON mcp_servers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 默认数据源占位：ScopeSentry 资产同步 MCP（地址与认证均留空、未启用）。
-- 供「资产同步」页检测数据源是否已配置；用户在页面填入 url 与 X-API-Key 后再启用。
-- 仅在缺失时插入，绝不覆盖用户已配置/已启用的服务器（schema.sql 每次启动都会 Exec）。
INSERT INTO mcp_servers (name, transport, url, env, enabled)
VALUES ('ScopeSentry', 'http', NULL, '{"X-API-Key":""}', false)
ON CONFLICT (name) DO NOTHING;

CREATE TABLE IF NOT EXISTS mcp_tools_cache (
    id            BIGSERIAL PRIMARY KEY,
    server_id     BIGINT NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    tool_name     TEXT NOT NULL,
    description   TEXT,
    schema        JSONB,
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (server_id, tool_name)
);

-- =====================================================================
-- G. 可见性：agent × mcp / skill
-- =====================================================================
CREATE TABLE IF NOT EXISTS agent_visibility (
    agent_id      BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    resource_kind TEXT   NOT NULL CHECK (resource_kind IN ('mcp')),
    resource_id   BIGINT NOT NULL,
    mcp_tool_name TEXT   NOT NULL DEFAULT '',
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, resource_kind, resource_id, mcp_tool_name)
);
CREATE INDEX IF NOT EXISTS idx_vis_resource ON agent_visibility(resource_kind, resource_id);

CREATE TABLE IF NOT EXISTS agent_skill_visibility (
    agent_id   BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    skill_name TEXT   NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, skill_name)
);
CREATE INDEX IF NOT EXISTS idx_askv_skill ON agent_skill_visibility(skill_name);

-- Skill 调用账本（见 db/skill_usage.go）。一次 Skill() 调用一行，只记维度不记正文。
-- 刻意不设外键：任务/会话删除后统计仍要保留（与 llm_usage 同理），skill 本身也只是
-- 文件系统上的目录名，没有对应的表。
CREATE TABLE IF NOT EXISTS skill_usage (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    skill          TEXT NOT NULL,
    agent_key      TEXT,
    task_id        BIGINT,
    exploration_id BIGINT,
    intent_id      BIGINT,
    session_id     TEXT,
    args_len       INTEGER NOT NULL DEFAULT 0,
    -- false = 模型点名了一个不存在的 skill(未命中)。这类行同样保留：它反映"想用但没有"
    -- 的缺口，是补 skill 的依据。
    found          BOOLEAN NOT NULL DEFAULT true
);
CREATE INDEX IF NOT EXISTS idx_skill_usage_skill ON skill_usage(skill, ts DESC);
CREATE INDEX IF NOT EXISTS idx_skill_usage_task  ON skill_usage(task_id);

-- 工具调用账本（见 db/tool_usage.go）。一次实际 CoreTool.Call 一行，只记归属维度，
-- 不保存工具参数或返回内容。刻意不设外键，任务、会话或自定义工具删除后仍保留统计。
CREATE TABLE IF NOT EXISTS tool_usage (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    tool_key       TEXT NOT NULL,
    agent_key      TEXT,
    task_id        BIGINT,
    exploration_id BIGINT,
    intent_id      BIGINT,
    session_id     TEXT
);
CREATE INDEX IF NOT EXISTS idx_tool_usage_tool ON tool_usage(tool_key, ts DESC);
CREATE INDEX IF NOT EXISTS idx_tool_usage_task ON tool_usage(task_id);

-- =====================================================================
-- H. 内置工具目录
-- =====================================================================
CREATE TABLE IF NOT EXISTS tools (
    key         TEXT PRIMARY KEY,
    system      BOOLEAN NOT NULL DEFAULT true,
    description TEXT    NOT NULL DEFAULT '',
    schema      JSONB   NOT NULL DEFAULT '{}',
    agents      JSONB   NOT NULL DEFAULT '[]',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    kind        TEXT    NOT NULL DEFAULT 'builtin',
    exec        JSONB   NOT NULL DEFAULT '{}',
    deferred    BOOLEAN NOT NULL DEFAULT false,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
DROP TRIGGER IF EXISTS trg_tools_upd ON tools;
CREATE TRIGGER trg_tools_upd BEFORE UPDATE ON tools
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- =====================================================================
-- I. 会话（对话页）
-- =====================================================================
CREATE TABLE IF NOT EXISTS conversations (
    id             BIGSERIAL PRIMARY KEY,
    agent_key      TEXT NOT NULL,
    title          TEXT NOT NULL DEFAULT '',
    llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL,
    pinned_at      TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_conversations_llm_profile ON conversations(llm_profile_id) WHERE llm_profile_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_conversations_pinned ON conversations(pinned_at DESC) WHERE pinned_at IS NOT NULL;
DROP TRIGGER IF EXISTS trg_conversations_upd ON conversations;
CREATE TRIGGER trg_conversations_upd BEFORE UPDATE ON conversations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS conversation_activities (
    id                 BIGSERIAL PRIMARY KEY,
    conversation_id    BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    worker             TEXT,
    kind               TEXT,
    tool               TEXT,
    tool_use_id        TEXT,
    is_error           BOOLEAN NOT NULL DEFAULT false,
    summary            TEXT,
    detail             TEXT,
    input_tokens       INTEGER,
    output_tokens      INTEGER,
    cache_read_tokens  INTEGER,
    cache_write_tokens INTEGER,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_conv_act ON conversation_activities(conversation_id, id);
CREATE INDEX IF NOT EXISTS idx_conv_act_tool_call ON conversation_activities(conversation_id, tool_use_id, id)
  WHERE kind IN ('tool_use', 'tool_result');

-- =====================================================================
-- J. Agent 触发器
-- =====================================================================
CREATE TABLE IF NOT EXISTS agent_triggers (
    id                          BIGSERIAL PRIMARY KEY,
    agent_key                   TEXT NOT NULL,
    enabled                     BOOLEAN NOT NULL DEFAULT true,
    interval_sec                INTEGER NOT NULL DEFAULT 0,
    on_finding                  BOOLEAN NOT NULL DEFAULT false,
    on_goal_met                 BOOLEAN NOT NULL DEFAULT false,
    on_task_timeout             BOOLEAN NOT NULL DEFAULT false,
    on_tool_call                BOOLEAN NOT NULL DEFAULT false,
    on_task_create              BOOLEAN NOT NULL DEFAULT false,
    interval_message            TEXT NOT NULL DEFAULT '',
    finding_message             TEXT NOT NULL DEFAULT '',
    goal_message                TEXT NOT NULL DEFAULT '',
    task_timeout_message        TEXT NOT NULL DEFAULT '',
    tool_call_message           TEXT NOT NULL DEFAULT '',
    task_create_message         TEXT NOT NULL DEFAULT '',
    tool_names                  TEXT NOT NULL DEFAULT '',
    last_fire                   TIMESTAMPTZ,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_triggers_agent ON agent_triggers(agent_key);
-- 加列迁移(已发版,旧库升级补列;新库 CREATE 已含这些列,ALTER 为 no-op)。幂等,每次启动可重复执行。
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS on_tool_call        BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS tool_call_message   TEXT    NOT NULL DEFAULT '';
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS tool_names          TEXT    NOT NULL DEFAULT '';
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS on_task_create      BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS task_create_message TEXT    NOT NULL DEFAULT '';
DROP TRIGGER IF EXISTS trg_agent_triggers_upd ON agent_triggers;
CREATE TRIGGER trg_agent_triggers_upd BEFORE UPDATE ON agent_triggers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS scheduler_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- =====================================================================
-- K. 拦截规则
-- =====================================================================
CREATE TABLE IF NOT EXISTS intercept_rules (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    priority        INTEGER NOT NULL DEFAULT 0,
    match_target    TEXT NOT NULL CHECK (match_target IN ('tool_name', 'tool_input')),
    match_type      TEXT NOT NULL CHECK (match_type IN ('string', 'regex')),
    pattern         TEXT NOT NULL,
    action          TEXT NOT NULL CHECK (action IN ('allow', 'deny', 'ask')),
    message         TEXT NOT NULL DEFAULT '',
    timeout_enabled BOOLEAN NOT NULL DEFAULT true,
    timeout_seconds INTEGER NOT NULL DEFAULT 60,
    timeout_action  TEXT    NOT NULL DEFAULT 'deny',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
DROP TRIGGER IF EXISTS trg_intercept_rules_upd ON intercept_rules;
CREATE TRIGGER trg_intercept_rules_upd BEFORE UPDATE ON intercept_rules
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 监管动作扩到五档:allow(AI放通)/deny(正则拦截·AI拦截)/ask(AI转人工)/
-- flag(AI判疑——标记但放通:记一条 flagged 审计行,工具照常执行,不阻塞 worker)。
-- 内联 CHECK 在首次建表时就已存在,老库不会重建,故必须显式 DROP + ADD
-- (同 tasks_stage_check 的写法);不改内联定义是为了让新库与老库走同一条迁移路径。
ALTER TABLE intercept_rules DROP CONSTRAINT IF EXISTS intercept_rules_action_check;
ALTER TABLE intercept_rules ADD CONSTRAINT intercept_rules_action_check
    CHECK (action IN ('allow','deny','ask','flag'));

CREATE TABLE IF NOT EXISTS intercept_pending (
    id              BIGSERIAL PRIMARY KEY,
    rule_id         BIGINT REFERENCES intercept_rules(id) ON DELETE SET NULL,
    conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
    task_id         TEXT,
    agent_name      TEXT NOT NULL DEFAULT '',
    tool_name       TEXT NOT NULL,
    tool_input      JSONB NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'allowed', 'denied', 'timeout')),
    -- 判定理由:规则命中时为规则 message;LLM 兜底判定时为模型给的简短理由(前缀 [模型])。
    reason          TEXT NOT NULL DEFAULT '',
    decided_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_intercept_pending_status ON intercept_pending(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_intercept_pending_task   ON intercept_pending(task_id, created_at DESC);
-- 补旧库:reason 列(已发版,加列要带 IF NOT EXISTS)。
ALTER TABLE intercept_pending ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '';
-- Detail payloads are lazy-loaded; NULL preserves the meaning of legacy history.
ALTER TABLE intercept_pending ADD COLUMN IF NOT EXISTS audit JSONB;
ALTER TABLE intercept_pending ADD COLUMN IF NOT EXISTS decision_source TEXT NOT NULL DEFAULT '';
UPDATE intercept_pending SET decision_source=CASE WHEN rule_id IS NOT NULL THEN 'rule'
 WHEN reason LIKE '[模型]%' THEN 'model' ELSE 'unknown' END WHERE decision_source='';
-- 审计状态扩到五档:flagged = AI 判疑放通(标记但放行,供人事后复盘)。同 rules.action 的写法。
ALTER TABLE intercept_pending DROP CONSTRAINT IF EXISTS intercept_pending_status_check;
ALTER TABLE intercept_pending ADD CONSTRAINT intercept_pending_status_check
    CHECK (status IN ('pending','allowed','denied','timeout','flagged'));

-- =====================================================================
-- L. 漏洞发现持久化
-- =====================================================================
CREATE TABLE IF NOT EXISTS findings (
    id          BIGSERIAL PRIMARY KEY,
    task_id     BIGINT REFERENCES tasks(id) ON DELETE SET NULL,
    node_id     BIGINT REFERENCES exploration_nodes(id) ON DELETE SET NULL,
    vulnclass   TEXT NOT NULL DEFAULT '',
    -- 漏洞名称(可读标题)；为空时前端回退展示 vulnclass。severity 取值：
    -- critical 严重 / high 高 / medium 中 / low 低（不加 CHECK，与 status 一致由 server 白名单校验）。
    name        TEXT NOT NULL DEFAULT '',
    severity    TEXT NOT NULL DEFAULT '',
    summary     TEXT NOT NULL DEFAULT '',
    evidence    TEXT NOT NULL DEFAULT '',
    worker      TEXT NOT NULL DEFAULT '',
    asset_ids   JSONB NOT NULL DEFAULT '[]',
    -- 处置状态：pending 待处理 / in_progress 处理中 / confirmed 已确认 / resolved 已处理 / fixed 已修复 /
    -- false_positive 误报 / ignored 忽略 / duplicate 重复 / risk_accepted 风险接受。
    -- 取值不加 CHECK：旧库靠下面的 ALTER 补列,CHECK 无法回填,统一由 server 侧白名单校验。
    status      TEXT NOT NULL DEFAULT 'pending',
    -- 漏洞详细报告(Markdown)；默认空,仅详情页读取/展示,不进列表接口以免 payload 膨胀。
    report      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE findings ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE findings ADD COLUMN IF NOT EXISTS name   TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN IF NOT EXISTS report TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_findings_task ON findings(task_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_findings_time ON findings(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_findings_status ON findings(status, created_at DESC);
-- 「按资产」视图靠 asset_ids @> '[<id>]' 反查发现,没有这个 GIN 索引就是全表扫。
CREATE INDEX IF NOT EXISTS idx_findings_asset_ids ON findings USING GIN(asset_ids jsonb_path_ops);

-- 手动复测属于独立会话；结论与原漏洞处置状态分开保存。
CREATE TABLE IF NOT EXISTS finding_retests (
    id BIGSERIAL PRIMARY KEY,
    finding_id BIGINT NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    conversation_id BIGINT UNIQUE REFERENCES conversations(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed','stopped')),
    verdict TEXT NOT NULL DEFAULT '' CHECK (verdict IN ('','reproduced','fixed','inconclusive')),
    notes TEXT NOT NULL DEFAULT '',
    snapshot JSONB NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    evidence TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_finding_retests_history ON finding_retests(finding_id, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_finding_retests_active ON finding_retests(finding_id)
    WHERE status IN ('pending','running');

-- 删除会话保留复测记录，同时解除尚未结束的复测占用。
CREATE OR REPLACE FUNCTION stop_deleted_conversation_retest() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE finding_retests SET status='stopped', error='复测会话已删除', finished_at=now()
    WHERE conversation_id=OLD.id AND status IN ('pending','running');
    RETURN OLD;
END;
$$;
DROP TRIGGER IF EXISTS trg_conversation_retest_delete ON conversations;
CREATE TRIGGER trg_conversation_retest_delete BEFORE DELETE ON conversations
    FOR EACH ROW EXECUTE FUNCTION stop_deleted_conversation_retest();

ALTER TABLE findings ADD COLUMN IF NOT EXISTS evidence_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE findings ADD COLUMN IF NOT EXISTS report_evidence_version BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS traffic_evidence_snapshots (
    id TEXT PRIMARY KEY,
    source_traffic_id TEXT NOT NULL,
    captured_at BIGINT NOT NULL,
    url TEXT NOT NULL,
    method TEXT NOT NULL,
    status INTEGER NOT NULL,
    content_type TEXT NOT NULL DEFAULT '',
    req_head TEXT NOT NULL,
    resp_head TEXT NOT NULL,
    req_hash TEXT NOT NULL,
    resp_hash TEXT NOT NULL,
    req_len BIGINT NOT NULL,
    resp_len BIGINT NOT NULL,
    unreferenced_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS finding_traffic_bindings (
    id BIGSERIAL PRIMARY KEY,
    finding_id BIGINT NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    snapshot_id TEXT NOT NULL REFERENCES traffic_evidence_snapshots(id),
    role TEXT NOT NULL DEFAULT 'supporting',
    note TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(finding_id, snapshot_id)
);
CREATE INDEX IF NOT EXISTS idx_finding_traffic_order ON finding_traffic_bindings(finding_id, position, id);
CREATE INDEX IF NOT EXISTS idx_finding_traffic_snapshot ON finding_traffic_bindings(snapshot_id);

-- =====================================================================
-- M. 后端日志持久化
-- =====================================================================
CREATE TABLE IF NOT EXISTS server_logs (
    id         BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    level      TEXT NOT NULL DEFAULT 'info',
    tag        TEXT NOT NULL DEFAULT '',
    text       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_server_logs_id ON server_logs(id DESC);

-- Independent /btw history and the latest provider-ready main checkpoint.
CREATE TABLE IF NOT EXISTS side_question_sessions (
    session_key TEXT PRIMARY KEY,
    conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
    task_id BIGINT REFERENCES tasks(id) ON DELETE CASCADE,
    exploration_id BIGINT REFERENCES explorations(id) ON DELETE CASCADE,
    intent_id BIGINT REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    run_id BIGINT NOT NULL,
    version BIGINT NOT NULL,
    snapshot JSONB NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    CHECK ((conversation_id IS NOT NULL AND task_id IS NULL AND exploration_id IS NULL AND intent_id IS NULL)
        OR (conversation_id IS NULL AND task_id IS NOT NULL AND exploration_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_side_sessions_conv ON side_question_sessions(conversation_id);
CREATE INDEX IF NOT EXISTS idx_side_sessions_task ON side_question_sessions(task_id);
CREATE INDEX IF NOT EXISTS idx_side_sessions_exp ON side_question_sessions(exploration_id);
CREATE INDEX IF NOT EXISTS idx_side_sessions_intent ON side_question_sessions(intent_id);

CREATE TABLE IF NOT EXISTS side_question_requests (
    id TEXT PRIMARY KEY,
    ordinal BIGSERIAL UNIQUE,
    session_key TEXT NOT NULL REFERENCES side_question_sessions(session_key) ON DELETE CASCADE,
    generation BIGINT NOT NULL,
    client_id TEXT NOT NULL,
    question TEXT NOT NULL,
    answer TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK(status IN ('running','completed','failed','cancelled','interrupted')),
    error TEXT NOT NULL DEFAULT '',
    model JSONB NOT NULL,
    snapshot_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sequence BIGINT NOT NULL DEFAULT 0,
    usage JSONB NOT NULL DEFAULT '{}',
    UNIQUE(session_key,generation,client_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_side_request_running ON side_question_requests(session_key) WHERE status='running';
CREATE INDEX IF NOT EXISTS idx_side_requests_history ON side_question_requests(session_key,ordinal DESC);

-- Additive v3 archive fields; old archives restore these as empty objects.
ALTER TABLE side_question_sessions ADD COLUMN IF NOT EXISTS memory JSONB NOT NULL DEFAULT '{}';
ALTER TABLE side_question_requests ADD COLUMN IF NOT EXISTS context_info JSONB NOT NULL DEFAULT '{}';

-- ============================================================
-- 三阶段平台 M1：任务阶段(stage) + 信息搜集收集器流水线
-- recon/exploit/post 三阶段；recon 任务 kind=pipeline 走确定性收集器，
-- 其余任务 kind=agent 走现有 agent 引擎。存量任务默认 exploit/agent。
-- ============================================================
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS stage TEXT NOT NULL DEFAULT 'exploit';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'agent';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS pipeline JSONB;
-- 单步骤任务模型(信息搜集):每条 recon 任务对应一个细分步骤(收集器)。
-- step_key=收集器 key;step_config=该步骤配置。pipeline 字段保留兼容旧读法。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS step_key TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS step_config JSONB;
ALTER TABLE tasks DROP CONSTRAINT IF EXISTS tasks_stage_check;
-- 'report' = 报告中心的 AI 识别任务(AI 占位模板生成等),由任务行承载过程与状态。
ALTER TABLE tasks ADD CONSTRAINT tasks_stage_check CHECK (stage IN ('recon','exploit','post','report'));
ALTER TABLE tasks DROP CONSTRAINT IF EXISTS tasks_kind_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_kind_check CHECK (kind IN ('agent','pipeline'));
CREATE INDEX IF NOT EXISTS idx_tasks_stage ON tasks(stage, created_at DESC) WHERE deleted_at IS NULL;

-- 收集器执行记录：流水线里每个收集器一次执行的完整轨迹(状态/统计/错误)。
-- raw_evidence_hash 指向 evidence blob(内容寻址)，原始输出落盘留证。
CREATE TABLE IF NOT EXISTS task_collector_runs (
    id          BIGSERIAL PRIMARY KEY,
    task_id     BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    collector   TEXT NOT NULL,
    position    INTEGER NOT NULL DEFAULT 0,
    status      TEXT NOT NULL DEFAULT 'running'
                CHECK (status IN ('running','done','failed','skipped')),
    stats       JSONB NOT NULL DEFAULT '{}',
    error       TEXT NOT NULL DEFAULT '',
    raw_evidence_hash TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_collector_runs_task ON task_collector_runs(task_id, position, id);

-- 外部结果暂存区：收集器产出先落这里，人工采纳后才合并进正式资产图。
-- (task_id, collector, type, value, port) 唯一 —— 重跑幂等，重复产出只刷计数。
CREATE TABLE IF NOT EXISTS staged_assets (
    id          BIGSERIAL PRIMARY KEY,
    task_id     BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id      BIGINT REFERENCES task_collector_runs(id) ON DELETE SET NULL,
    company_id  BIGINT REFERENCES companies(id) ON DELETE CASCADE,
    collector   TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL DEFAULT 'domain'
                CHECK (type IN ('root_domain','domain','ip','service','app')),
    value       TEXT NOT NULL,
    port        INTEGER NOT NULL DEFAULT 0,
    extra       JSONB NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','accepted','ignored')),
    batch_id    BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_staged_assets_dedupe
    ON staged_assets(task_id, collector, type, value, port);
CREATE INDEX IF NOT EXISTS idx_staged_assets_status ON staged_assets(task_id, status);

-- ============================================================
-- M2：外部接口(FOFA/Hunter 等)配置表
-- credentials 用 AES-256-GCM 加密落库(密钥在 data/external_api_secret.key);
-- base_url 允许自建网关,但请求前必须通过公网 URL 校验。
-- ============================================================
CREATE TABLE IF NOT EXISTS external_apis (
    id          BIGSERIAL PRIMARY KEY,
    provider    TEXT NOT NULL,
    name        TEXT NOT NULL,
    base_url    TEXT NOT NULL DEFAULT '',
    credentials BYTEA NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    last_test_at TIMESTAMPTZ,
    last_test_ok BOOLEAN,
    quota_info  JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_external_apis_provider ON external_apis(provider, enabled);
DROP TRIGGER IF EXISTS trg_external_apis_upd ON external_apis;
CREATE TRIGGER trg_external_apis_upd BEFORE UPDATE ON external_apis
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ============================================================
-- M2：三阶段工具体系
-- tools.stage: 工具归属阶段(''=通用跨阶段),三阶段工具页各自管理本阶段工具,
-- 系统设置→工具 统一管理全部。
-- ============================================================
ALTER TABLE tools ADD COLUMN IF NOT EXISTS stage TEXT NOT NULL DEFAULT '';
-- stage 含 'report'(报告中心 2026-09-24 引入)。此前漏列导致 report_writer 四工具
-- 每次启动播种都被本约束 23514 拒收(2026-09-26 C2 集成时从启动日志发现,顺带修复)。
ALTER TABLE tools DROP CONSTRAINT IF EXISTS tools_stage_check;
ALTER TABLE tools ADD CONSTRAINT tools_stage_check CHECK (stage IN ('','recon','exploit','post','report'));
CREATE INDEX IF NOT EXISTS idx_tools_stage ON tools(stage) WHERE enabled;

-- 工具手动执行记录(阶段工具执行页的历史底账)
CREATE TABLE IF NOT EXISTS tool_executions (
    id          BIGSERIAL PRIMARY KEY,
    stage       TEXT NOT NULL,
    tool_key    TEXT NOT NULL,
    input       JSONB NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','done','failed')),
    summary     JSONB NOT NULL DEFAULT '{}',
    raw_evidence_hash TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_tool_executions_stage ON tool_executions(stage, id DESC);

-- ============================================================
-- M2：字典库(信息搜集的枚举弹药)
-- 文件本体落 dataDir/wordbooks/<id>.txt,表里只存元数据。
-- kind: subdomain=子域爆破 / path=目录路径 / password=密码 / username=用户名 / other
-- ============================================================
CREATE TABLE IF NOT EXISTS wordbooks (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL DEFAULT 'other' CHECK (kind IN ('subdomain','path','password','username','other')),
    line_count INTEGER NOT NULL DEFAULT 0,
    byte_size  BIGINT NOT NULL DEFAULT 0,
    file_path  TEXT NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- 报告中心：持久化的产出报告(任务总结/漏洞报告/自定义)
-- 产出报告此前只有按需渲染(GET /api/report)与 findings.report 字段,
-- 报告中心需要一个可检索的实体。归档走 upsert:同一来源重复归档覆盖正文。
-- ============================================================
CREATE TABLE IF NOT EXISTS reports (
    id          BIGSERIAL PRIMARY KEY,
    kind        TEXT NOT NULL DEFAULT 'task' CHECK (kind IN ('task','finding','custom')),
    title       TEXT NOT NULL,
    summary     TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL DEFAULT '',
    task_id     BIGINT,
    finding_id  BIGINT,
    source      TEXT NOT NULL DEFAULT 'manual',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_reports_kind_created ON reports (kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reports_created ON reports (created_at DESC);
-- 同一任务/同一发现的报告只保留一份,重复归档覆盖(部分唯一索引 + ON CONFLICT)。
CREATE UNIQUE INDEX IF NOT EXISTS uq_reports_task ON reports (task_id) WHERE kind='task' AND task_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_reports_finding ON reports (finding_id) WHERE kind='finding' AND finding_id IS NOT NULL;

-- ============================================================
-- 报告模板:上传 md/docx 模板,解析结构后用于按模板导出报告。
-- content 归一化为 markdown(docx 解析后落地);structure 存章节树等解析产物。
-- 模板中可使用内置占位符:{{title}} {{date}} {{summary}} {{content}}
--   {{findings_table}} {{findings_detail}} {{assets_summary}}
-- ============================================================
CREATE TABLE IF NOT EXISTS report_templates (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    format      TEXT NOT NULL DEFAULT 'markdown' CHECK (format IN ('markdown','docx','doc')),
    content     TEXT NOT NULL DEFAULT '',
    structure   JSONB NOT NULL DEFAULT '{}',
    source_note TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- 报告/模板事件流:记录报告中心产出的生成过程
-- (新建/归档/更新/导出/应用模板),供详情页「生成过程」时间线展示。
-- report_id 与 template_id 二选一;报告删除后事件保留,历史可溯。
-- ============================================================
CREATE TABLE IF NOT EXISTS report_events (
    id          BIGSERIAL PRIMARY KEY,
    report_id   BIGINT,
    template_id BIGINT,
    kind        TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT '',
    task_id     BIGINT,
    detail      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_report_events_report ON report_events(report_id, id);
CREATE INDEX IF NOT EXISTS idx_report_events_template ON report_events(template_id, id);
-- 报告模板原始 docx 存档路径:格式保真导出时以此文件为外壳只替换变量文本。
ALTER TABLE report_templates ADD COLUMN IF NOT EXISTS orig_path TEXT NOT NULL DEFAULT '';
-- 模板分类(自由文本,如:渗透测试报告/漏扫报告/通用模板),供分类筛选。
ALTER TABLE report_templates ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT '';
-- AI 占位模板识别(异步任务化):每次识别 = 一条 stage='report' 任务 + 一条
-- template_engineer 监看会话。analyze_status: ''=未识别 running/done/failed;
-- 草稿存 suggested_content,由用户在编辑器确认采用,不自动覆盖正文。
ALTER TABLE report_templates ADD COLUMN IF NOT EXISTS analyze_status     TEXT NOT NULL DEFAULT '';
ALTER TABLE report_templates ADD COLUMN IF NOT EXISTS suggested_content  TEXT NOT NULL DEFAULT '';
ALTER TABLE report_templates ADD COLUMN IF NOT EXISTS analyze_error      TEXT NOT NULL DEFAULT '';
ALTER TABLE report_templates ADD COLUMN IF NOT EXISTS analyze_task_id    BIGINT NOT NULL DEFAULT 0;
ALTER TABLE report_templates ADD COLUMN IF NOT EXISTS analyze_conv_id    BIGINT;
ALTER TABLE report_templates ADD COLUMN IF NOT EXISTS analyzed_at        TIMESTAMPTZ;
-- 模板导出写作指令:模板识别时由 AI 一并产出,与占位模板配套;AI 按模板导出时
-- 注入 LLM,描述每个章节应填什么数据、写作口吻、格式与脱敏要求。可在模板管理页编辑。
ALTER TABLE report_templates ADD COLUMN IF NOT EXISTS export_prompt      TEXT NOT NULL DEFAULT '';

-- 报告生成作业:报告中心所有「走 LLM 的报告产出」统一落这张表,前端轮询进度、
-- 下载产物、回看历史。kind:
--   export         AI 按模板导出(产物文件落 <data>/report-jobs/,路径不外泄,经下载接口取);
--   task_report_ai 任务总结报告 AI 撰写(产出直接覆盖写回 reports,无产物文件)。
-- monitor_task_id/conv_id 指向监看任务(stage='report',kind=pipeline)与会话,
-- 报告中心任务(/function/tasks?stage=report)与对话(/chat?stage=report)可见。
CREATE TABLE IF NOT EXISTS report_jobs (
    id              BIGSERIAL PRIMARY KEY,
    kind            TEXT NOT NULL CHECK (kind IN ('export','task_report_ai')),
    report_id       BIGINT NOT NULL DEFAULT 0,
    template_id     BIGINT NOT NULL DEFAULT 0,
    source_task_id  BIGINT NOT NULL DEFAULT 0,
    monitor_task_id BIGINT NOT NULL DEFAULT 0,
    conv_id         BIGINT,
    format          TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','done','failed')),
    error           TEXT NOT NULL DEFAULT '',
    result_path     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_report_jobs_report ON report_jobs(report_id, id DESC);

-- ============================================================
-- 漏洞库(vulndb_entries):沉淀漏洞情报与利用指引。
-- 三类:0day(未公开/自研发现) / nday(已公开无 CVE 编号或未收录) / cve(有 CVE 编号)。
-- 与 findings(某次任务的实测发现)分表:findings 是「打出来的」,vulndb 是「知识库」;
-- 实测成果经自动归类后入本表,由 SRC 成果页引用。
-- ============================================================
CREATE TABLE IF NOT EXISTS vulndb_entries (
    id           BIGSERIAL PRIMARY KEY,
    -- 分类:0day / nday / cve(不加 CHECK,由 server 白名单校验,便于将来扩展)
    category     TEXT NOT NULL DEFAULT 'nday',
    title        TEXT NOT NULL DEFAULT '',
    -- CVE 编号(仅 category=cve 时非空,如 CVE-2024-1234);唯一索引防重复入库。
    cve_id       TEXT NOT NULL DEFAULT '',
    vendor       TEXT NOT NULL DEFAULT '',
    product      TEXT NOT NULL DEFAULT '',
    affected     TEXT NOT NULL DEFAULT '',
    severity     TEXT NOT NULL DEFAULT '',
    -- 漏洞类别(与 findings.vulnclass 同语义:sql_injection/rce/ssrf/…)
    vuln_class   TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    -- 利用指引/复现要点(Markdown)
    exploitation TEXT NOT NULL DEFAULT '',
    remediation  TEXT NOT NULL DEFAULT '',
    -- 来源:manual(手工) / ai_finding(渗透任务成果自动入库) / cve_feed(定时搜集)
    source       TEXT NOT NULL DEFAULT 'manual',
    task_id      BIGINT,
    finding_id   BIGINT,
    -- 参考链接与标签(数组 JSON)
    refs         JSONB NOT NULL DEFAULT '[]',
    tags         JSONB NOT NULL DEFAULT '[]',
    -- 在野利用标记(来自 CISA KEV 已知被在野利用目录,由采集器维护;人工不可编辑)
    kev          BOOLEAN NOT NULL DEFAULT false,
    -- 已收录 PoC 数(由 vulndb_pocs 派生,冗余存储便于列表展示)
    poc_count    INT NOT NULL DEFAULT 0,
    -- 状态:active(有效) / archived(归档) / draft(草稿)
    status       TEXT NOT NULL DEFAULT 'active',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- CVE 编号唯一(非空时);同一 CVE 重复拉取只更新不新增。
-- 存量库补 kev 列(新建库由上方 CREATE TABLE 直接带出)。
-- ⚠️ 必须排在下方 idx_vulndb_kev 之前:存量库建 partial index 时 kev 列必须已存在。
ALTER TABLE vulndb_entries ADD COLUMN IF NOT EXISTS kev BOOLEAN NOT NULL DEFAULT false;
CREATE UNIQUE INDEX IF NOT EXISTS idx_vulndb_cve_unique
    ON vulndb_entries(cve_id) WHERE cve_id <> '';
CREATE INDEX IF NOT EXISTS idx_vulndb_category ON vulndb_entries(category, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_vulndb_severity ON vulndb_entries(severity, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_vulndb_time     ON vulndb_entries(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_vulndb_kev      ON vulndb_entries(kev, updated_at DESC) WHERE kev;
CREATE INDEX IF NOT EXISTS idx_vulndb_task     ON vulndb_entries(task_id) WHERE task_id IS NOT NULL;
-- 全文检索(标题/描述)走 pg_trgm,与流量正文检索同套路。
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS idx_vulndb_title_trgm ON vulndb_entries USING GIN(title gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_vulndb_desc_trgm  ON vulndb_entries USING GIN(description gin_trgm_ops);
DROP TRIGGER IF EXISTS trg_vulndb_upd ON vulndb_entries;
CREATE TRIGGER trg_vulndb_upd BEFORE UPDATE ON vulndb_entries
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ============================================================
-- 漏洞 PoC(vulndb_pocs):一条漏洞可挂多个 PoC(多来源/多语言)。
-- kind 区分 poc(验证) / exp(利用) / nuclei / script。
-- ============================================================
CREATE TABLE IF NOT EXISTS vulndb_pocs (
    id          BIGSERIAL PRIMARY KEY,
    entry_id    BIGINT NOT NULL REFERENCES vulndb_entries(id) ON DELETE CASCADE,
    name        TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL DEFAULT 'poc',
    language    TEXT NOT NULL DEFAULT '',
    -- PoC 正文(代码/脚本/请求报文);大文本只详情页读取
    content     TEXT NOT NULL DEFAULT '',
    -- 来源:manual / github(自动发现) / exploitdb / pocorexp(PocOrExp_in_Github) /
    --      nuclei(projectdiscovery 官方模板) / ai_generated
    source      TEXT NOT NULL DEFAULT 'manual',
    source_url  TEXT NOT NULL DEFAULT '',
    verified    BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_vulndb_pocs_entry ON vulndb_pocs(entry_id, id);
-- 同一来源 URL 不重复入库(自动发现时靠它去重)。
CREATE UNIQUE INDEX IF NOT EXISTS idx_vulndb_pocs_url_unique
    ON vulndb_pocs(entry_id, source_url) WHERE source_url <> '';
DROP TRIGGER IF EXISTS trg_vulndb_pocs_upd ON vulndb_pocs;
CREATE TRIGGER trg_vulndb_pocs_upd BEFORE UPDATE ON vulndb_pocs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ============================================================
-- 漏洞库采集状态:定时搜集器每轮运行记录(供页面展示「上次同步」)。
-- ============================================================
CREATE TABLE IF NOT EXISTS vulndb_sync_runs (
    id           BIGSERIAL PRIMARY KEY,
    source       TEXT NOT NULL DEFAULT '',       -- nvd / github_advisory / exploitdb / github_poc /
                                                    -- pocorexp_today / nuclei / kev / articles / nvd_backfill
    status       TEXT NOT NULL DEFAULT 'running', -- running / done / failed
    fetched      INT NOT NULL DEFAULT 0,
    inserted     INT NOT NULL DEFAULT 0,
    updated      INT NOT NULL DEFAULT 0,
    pocs_found   INT NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_vulndb_sync_time ON vulndb_sync_runs(started_at DESC);

-- ============================================================
-- SRC 模式:企业 SRC 规范(限制 AI 边界) + 成果产出。
-- ============================================================
-- 企业 SRC 规范:每个企业一套。规范文本作为最高优先级约束注入任务的
-- Planner/Worker 提示词,防止 AI 在 SRC 授权范围外动作(打到非授权域名、
-- 使用高危 payload、超出速率限制等)。
CREATE TABLE IF NOT EXISTS src_profiles (
    id            BIGSERIAL PRIMARY KEY,
    company_id    BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    platform      TEXT NOT NULL DEFAULT '',      -- SRC 平台名称(补天/漏洞盒子/教育SRC…)
    scope_note    TEXT NOT NULL DEFAULT '',      -- 授权范围说明(人可读)
    forbidden     TEXT NOT NULL DEFAULT '',      -- 禁止事项,每行一条,注入约束
    allowed_types TEXT NOT NULL DEFAULT '',      -- 允许的测试类型
    rate_limit    TEXT NOT NULL DEFAULT '',      -- 速率/强度限制说明
    rules         TEXT NOT NULL DEFAULT '',      -- 额外规范正文(Markdown)
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_src_profiles_company ON src_profiles(company_id);
DROP TRIGGER IF EXISTS trg_src_profiles_upd ON src_profiles;
CREATE TRIGGER trg_src_profiles_upd BEFORE UPDATE ON src_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- SRC 成果:任务在 SRC 模式下产出的可提交成果(漏洞 + 提交状态跟踪)。
CREATE TABLE IF NOT EXISTS src_rewards (
    id           BIGSERIAL PRIMARY KEY,
    company_id   BIGINT REFERENCES companies(id) ON DELETE SET NULL,
    task_id      BIGINT REFERENCES tasks(id) ON DELETE SET NULL,
    finding_id   BIGINT,
    entry_id     BIGINT REFERENCES vulndb_entries(id) ON DELETE SET NULL,
    title        TEXT NOT NULL DEFAULT '',
    severity     TEXT NOT NULL DEFAULT '',
    vuln_class   TEXT NOT NULL DEFAULT '',
    -- 提交状态: pending(待提交) / submitted(已提交) / accepted(已收录) / rejected(已驳回) / duplicate(重复)
    status       TEXT NOT NULL DEFAULT 'pending',
    platform_ref TEXT NOT NULL DEFAULT '',       -- 平台侧编号/链接
    feedback     TEXT NOT NULL DEFAULT '',       -- 平台反馈/赏金说明
    submitted_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_src_rewards_company ON src_rewards(company_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_src_rewards_task    ON src_rewards(task_id);
CREATE INDEX IF NOT EXISTS idx_src_rewards_status  ON src_rewards(status, created_at DESC);
DROP TRIGGER IF EXISTS trg_src_rewards_upd ON src_rewards;
CREATE TRIGGER trg_src_rewards_upd BEFORE UPDATE ON src_rewards
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 任务标记 SRC 模式 + 关联企业 SRC 规范。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS src_mode       BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS src_profile_id BIGINT REFERENCES src_profiles(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_src_mode ON tasks(src_mode, id DESC) WHERE src_mode;

-- ============================================================
-- 代理中心(proxy_groups / proxies / proxy_sources / proxy_requests / proxy_check_runs):
-- 统一管理出网代理,支撑「本地裸网 ⇄ 代理池」热切换。
--
-- 分层:
--   proxy_groups      代理组(调度策略/监听端口/测活参数)—— 调度的单位
--   proxies           组内单个代理(来源:手工 / 商业 API / 公益 API / 全网在野搜集)
--   proxy_sources     组上接入的代理池 API(商业 + 公益),定时拉取入库
--   proxy_requests    走代理池的请求流水(仪表盘数据源,记录「本次调度到哪条代理」)
--   proxy_check_runs  测活批次台账
--
-- 枚举取值一律不加 CHECK(旧库靠 ALTER 补列,CHECK 无法回填),由 server 侧白名单校验。
-- ============================================================
CREATE TABLE IF NOT EXISTS proxy_groups (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    -- 组类型:manual(纯手工) / api(接入代理池 API) / wild(全网在野搜集) / mixed(混合)
    kind          TEXT NOT NULL DEFAULT 'mixed',
    -- 调度策略:round_robin(轮询) / random(随机) / least_latency(最低延迟优先) / failover(故障转移)
    strategy      TEXT NOT NULL DEFAULT 'round_robin',
    -- 本地代理池监听端口:0 = 启用时自动分配(18080 起找空闲端口)。
    -- agent 侧把出网地址指向 http://127.0.0.1:<port>,由本进程按策略动态选代理。
    listen_port   INT NOT NULL DEFAULT 0,
    enabled       BOOLEAN NOT NULL DEFAULT true,
    -- 自动测活:定时对本组代理做连通性检测,连续失败超阈值标记 dead
    auto_check    BOOLEAN NOT NULL DEFAULT true,
    check_interval_minutes INT NOT NULL DEFAULT 10,
    -- 测活目标 URL(经代理请求它,判断是否真的能出网)
    test_url      TEXT NOT NULL DEFAULT 'http://httpbin.org/ip',
    timeout_ms    INT NOT NULL DEFAULT 10000,
    -- 连续失败次数达到该值即标记 dead(成功一次即清零)
    max_failures  INT NOT NULL DEFAULT 3,
    -- 单次测活/拉取的最大并发(避免把本机打满,也避免被源站限流)
    concurrency   INT NOT NULL DEFAULT 20,
    remark        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_proxy_groups_kind ON proxy_groups(kind, id DESC);

CREATE TABLE IF NOT EXISTS proxies (
    id            BIGSERIAL PRIMARY KEY,
    group_id      BIGINT NOT NULL REFERENCES proxy_groups(id) ON DELETE CASCADE,
    -- 完整代理地址:http://user:pass@host:port / https://... / socks5://...
    url           TEXT NOT NULL,
    -- 协议(由 url 解析冗余存储,便于筛选):http / https / socks5
    protocol      TEXT NOT NULL DEFAULT 'http',
    host          TEXT NOT NULL DEFAULT '',
    port          INT NOT NULL DEFAULT 0,
    label         TEXT NOT NULL DEFAULT '',
    -- 来源:manual(手工录入) / api(代理池 API) / public(公益池) / wild(全网在野搜集)
    source        TEXT NOT NULL DEFAULT 'manual',
    -- 来源标识:API 源 id 或在野搜集的站点 key,用于追溯与批量清理
    source_ref    TEXT NOT NULL DEFAULT '',
    -- 出口地区(源站给的,或测活时由响应推断)
    region        TEXT NOT NULL DEFAULT '',
    -- 匿名度:transparent(透明) / anonymous(匿名) / elite(高匿);测活时探测
    anonymity     TEXT NOT NULL DEFAULT '',
    -- 最近一次测活延迟(毫秒),0 = 未测
    latency_ms    INT NOT NULL DEFAULT 0,
    -- 状态:untested(未测) / alive(存活) / dead(失效) / checking(测活中)
    status        TEXT NOT NULL DEFAULT 'untested',
    fail_count    INT NOT NULL DEFAULT 0,
    success_count INT NOT NULL DEFAULT 0,
    last_checked_at TIMESTAMPTZ,
    last_ok_at    TIMESTAMPTZ,
    -- 测活时观测到的出口 IP(用于识别「一批代理其实是同一出口」)
    exit_ip       TEXT NOT NULL DEFAULT '',
    -- 累计被调度次数(仪表盘用)
    use_count     BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 同一组内同一地址只留一条(不同组可重复,便于跨组对比)。
CREATE UNIQUE INDEX IF NOT EXISTS idx_proxies_group_url ON proxies(group_id, url);
CREATE INDEX IF NOT EXISTS idx_proxies_status ON proxies(group_id, status, latency_ms);
CREATE INDEX IF NOT EXISTS idx_proxies_source ON proxies(group_id, source, source_ref);

CREATE TABLE IF NOT EXISTS proxy_sources (
    id            BIGSERIAL PRIMARY KEY,
    group_id      BIGINT NOT NULL REFERENCES proxy_groups(id) ON DELETE CASCADE,
    -- 内置源 key(见 proxycenter/providers.go):如 fofa_proxy / kuaidaili / proxyscrape …
    provider_key  TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL DEFAULT '',
    -- 自定义源的取用地址(内置源可留空,用 provider 默认地址)
    api_url       TEXT NOT NULL DEFAULT '',
    -- 凭据(API Key / 用户名 / 密码),加密存储,接口只回显是否已配置
    api_key       TEXT NOT NULL DEFAULT '',
    username      TEXT NOT NULL DEFAULT '',
    password      TEXT NOT NULL DEFAULT '',
    -- 额外参数(JSON,如地区/协议/数量上限)
    params        JSONB NOT NULL DEFAULT '{}',
    enabled       BOOLEAN NOT NULL DEFAULT true,
    -- 拉取间隔(分钟),0 = 只手动拉
    interval_minutes INT NOT NULL DEFAULT 30,
    last_sync_at  TIMESTAMPTZ,
    -- 最近一次拉取状态:idle / running / ok / failed
    last_status   TEXT NOT NULL DEFAULT 'idle',
    last_error    TEXT NOT NULL DEFAULT '',
    -- 最近一次拉取结果计数
    imported_count INT NOT NULL DEFAULT 0,
    total_count    INT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_proxy_sources_group ON proxy_sources(group_id, enabled, id);

CREATE TABLE IF NOT EXISTS proxy_requests (
    id            BIGSERIAL PRIMARY KEY,
    ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
    group_id      BIGINT,
    -- 本次动态调度命中的代理(代理被删后置空,流水本身保留)
    proxy_id      BIGINT,
    -- 冗余记录调度到的代理地址与来源:代理被删后流水仍可读
    proxy_url     TEXT NOT NULL DEFAULT '',
    method        TEXT NOT NULL DEFAULT '',
    host          TEXT NOT NULL DEFAULT '',
    url           TEXT NOT NULL DEFAULT '',
    status_code   INT NOT NULL DEFAULT 0,
    latency_ms    INT NOT NULL DEFAULT 0,
    bytes_in      BIGINT NOT NULL DEFAULT 0,
    bytes_out     BIGINT NOT NULL DEFAULT 0,
    ok            BOOLEAN NOT NULL DEFAULT true,
    error         TEXT NOT NULL DEFAULT '',
    -- 归属任务(经 agent 出网时带上,便于按任务回看)
    task_id       BIGINT,
    stage         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_proxy_requests_ts ON proxy_requests(ts DESC);
CREATE INDEX IF NOT EXISTS idx_proxy_requests_group ON proxy_requests(group_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_proxy_requests_proxy ON proxy_requests(proxy_id, ts DESC);

CREATE TABLE IF NOT EXISTS proxy_check_runs (
    id            BIGSERIAL PRIMARY KEY,
    group_id      BIGINT,
    -- 触发方式:manual(手动) / auto(定时)
    trigger       TEXT NOT NULL DEFAULT 'manual',
    -- 状态:running / done / failed
    status        TEXT NOT NULL DEFAULT 'running',
    checked       INT NOT NULL DEFAULT 0,
    alive         INT NOT NULL DEFAULT 0,
    dead          INT NOT NULL DEFAULT 0,
    error         TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_proxy_check_runs_group ON proxy_check_runs(group_id, started_at DESC);

DROP TRIGGER IF EXISTS trg_proxy_groups_upd ON proxy_groups;
CREATE TRIGGER trg_proxy_groups_upd BEFORE UPDATE ON proxy_groups
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
DROP TRIGGER IF EXISTS trg_proxies_upd ON proxies;
CREATE TRIGGER trg_proxies_upd BEFORE UPDATE ON proxies
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
DROP TRIGGER IF EXISTS trg_proxy_sources_upd ON proxy_sources;
CREATE TRIGGER trg_proxy_sources_upd BEFORE UPDATE ON proxy_sources
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 代理地区识别:代理的出口 IP → 国家/地区。
--   proxies 增补国家码/国家名两列(便于按国家筛选与展示);
--   proxy_geo_cache 按 IP 缓存解析结果——地区查询走第三方限速接口(ip-api 免费档
--   约 45 次/分钟),而在野代理清单里大量条目共用同一出口 IP,不缓存会很快被限流。
ALTER TABLE proxies ADD COLUMN IF NOT EXISTS country_code TEXT NOT NULL DEFAULT '';
ALTER TABLE proxies ADD COLUMN IF NOT EXISTS country      TEXT NOT NULL DEFAULT '';
-- 按国家筛选 + 按「已解析/未解析」挑回填对象。
CREATE INDEX IF NOT EXISTS idx_proxies_country ON proxies(group_id, country_code);
CREATE INDEX IF NOT EXISTS idx_proxies_geo_pending ON proxies(group_id, exit_ip)
    WHERE country_code = '' AND exit_ip <> '';

CREATE TABLE IF NOT EXISTS proxy_geo_cache (
    ip           TEXT PRIMARY KEY,
    country_code TEXT NOT NULL DEFAULT '',
    country      TEXT NOT NULL DEFAULT '',
    -- 地区细分(省/市),仅展示用
    region_name  TEXT NOT NULL DEFAULT '',
    city         TEXT NOT NULL DEFAULT '',
    -- 解析失败也要缓存:否则每个失败 IP 每轮都会重试,白白消耗限速配额
    ok           BOOLEAN NOT NULL DEFAULT true,
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
DROP TRIGGER IF EXISTS trg_proxy_geo_cache_upd ON proxy_geo_cache;
CREATE TRIGGER trg_proxy_geo_cache_upd BEFORE UPDATE ON proxy_geo_cache
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 任务的出网方式(创建任务时可选是否走代理池)。
--   proxy_mode:'' 或 'inherit' = 跟随全局出网模式(默认,保持既有行为)
--               'direct'        = 本任务强制直连(本地裸网),不受全局代理池影响
--               'pool'          = 本任务强制走代理池(需同时给 proxy_group_id)
--   proxy_group_id:'pool' 时指定用哪个代理组;0 = 用全局选中的那个组。
-- 语义是「任务级覆盖」:agent 与信息搜集流水线都按它取出口,空值不改变任何现有行为。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS proxy_mode     TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS proxy_group_id BIGINT REFERENCES proxy_groups(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_proxy_mode ON tasks(proxy_mode) WHERE proxy_mode <> '';

-- 任务的监管门限(任务级覆盖,与 proxy_mode 同一套语义)。
--   guard_mode:'' 或 'inherit' = 跟随全局监管配置(默认,保持既有行为)
--              'off'           = 本任务不跑监管(规则与 AI 审查都不介入)
--              'enforce'       = 本任务强制启用监管(即使全局关)
--   guard_rule_ids:本任务只用这些规则 id;空数组 = 用全部已启用规则。
-- 用途:内网靶场类任务可 off 掉监管避免误拦,SRC 类任务可 enforce 收紧。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS guard_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS guard_rule_ids JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE tasks DROP CONSTRAINT IF EXISTS tasks_guard_mode_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_guard_mode_check
    CHECK (guard_mode IN ('','inherit','off','enforce'));

-- ============================================================
-- SRC 平台选择:平台 → 目标 → 资产 三级结构。
--
-- 为什么重构:旧的 src_profiles 是「一个企业一条规范」,platform 只是个自由文本,
-- 所以「进入平台 → 添加目标 → 添加资产」这条链路在数据上根本不存在,平台批量页
-- 也只能靠前端 group-by 字符串聚合。新模型把平台提升为顶层实体。
--
-- 关键取舍:平台与目标是多对多(src_platform_targets)。同一家企业可能同时收录在
-- 补天和漏洞盒子,各平台下的授权范围/规范/奖励标准各不相同——把授权范围挂在
-- 「平台×目标」这一层而不是目标上,才能表达这种差异。
-- ============================================================

-- src_nkey 归一化平台/目标的自然键:小写 + 折叠空白。与 Go 侧 srcNKey 必须一致,
-- 否则迁移建的行和运行时建的行会因键不同而重复。
CREATE OR REPLACE FUNCTION src_nkey(value text) RETURNS text AS $$
    SELECT btrim(regexp_replace(lower(btrim(coalesce(value, ''))), '\s+', ' ', 'g'));
$$ LANGUAGE sql IMMUTABLE;

CREATE TABLE IF NOT EXISTS src_platforms (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT NOT NULL,
    nkey             TEXT NOT NULL UNIQUE,
    homepage         TEXT NOT NULL DEFAULT '',   -- 平台主页/提交入口
    description      TEXT NOT NULL DEFAULT '',
    -- 四类平台级文本规范。它们与附件解析出的正文一起,在创建 SRC 任务时
    -- 转成 task_constraints 注入 Planner/Worker 系统提示(防 AI 越界)。
    reward_standard  TEXT NOT NULL DEFAULT '',   -- 奖励标准
    scoring_standard TEXT NOT NULL DEFAULT '',   -- 评分标准
    code_of_conduct  TEXT NOT NULL DEFAULT '',   -- 守则
    rules            TEXT NOT NULL DEFAULT '',   -- 规范
    enabled          BOOLEAN NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
DROP TRIGGER IF EXISTS trg_src_platforms_upd ON src_platforms;
CREATE TRIGGER trg_src_platforms_upd BEFORE UPDATE ON src_platforms
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 平台规范附件。stored_name 只存文件名(不含路径),路径由 dataDir/src-platforms/<platform_id>/
-- 推导——与 report_templates.orig_path 同样的可移植性考虑:换机器/换 dataDir 后仍能找回。
-- content_text 是解析出的正文,合并进规范文本注入约束;pdf/xlsx 解析不了就留空并标 skipped。
CREATE TABLE IF NOT EXISTS src_platform_files (
    id           BIGSERIAL PRIMARY KEY,
    platform_id  BIGINT NOT NULL REFERENCES src_platforms(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('reward_standard','scoring_standard','code_of_conduct','rules')),
    filename     TEXT NOT NULL DEFAULT '',
    stored_name  TEXT NOT NULL DEFAULT '',
    byte_size    BIGINT NOT NULL DEFAULT 0,
    content_text TEXT NOT NULL DEFAULT '',
    -- parsed=已解析出正文并注入约束 / skipped=格式不支持,仅存档 / failed=解析报错
    parse_status TEXT NOT NULL DEFAULT 'skipped' CHECK (parse_status IN ('parsed','skipped','failed')),
    parse_note   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_src_platform_files_platform ON src_platform_files(platform_id, id);

-- 目标是「被测试的对象」(厂商/学校/单位)。独立表而非直接复用 companies:
-- 平台下的目标可以只是个待补全的名字(还没建企业、还没有资产归属),等资产进来
-- 再关联企业;company_id 可空正是为了这条路。
CREATE TABLE IF NOT EXISTS src_targets (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    nkey        TEXT NOT NULL UNIQUE,
    company_id  BIGINT REFERENCES companies(id) ON DELETE SET NULL,
    description TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_src_targets_company ON src_targets(company_id) WHERE company_id IS NOT NULL;
DROP TRIGGER IF EXISTS trg_src_targets_upd ON src_targets;
CREATE TRIGGER trg_src_targets_upd BEFORE UPDATE ON src_targets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 平台 × 目标 关联:授权范围与规范挂这一层(同一目标在不同平台下规范不同)。
CREATE TABLE IF NOT EXISTS src_platform_targets (
    id            BIGSERIAL PRIMARY KEY,
    platform_id   BIGINT NOT NULL REFERENCES src_platforms(id) ON DELETE CASCADE,
    target_id     BIGINT NOT NULL REFERENCES src_targets(id) ON DELETE CASCADE,
    scope_note    TEXT NOT NULL DEFAULT '',
    forbidden     TEXT NOT NULL DEFAULT '',   -- 禁止事项,每行一条,逐条注入 deny 约束
    allowed_types TEXT NOT NULL DEFAULT '',
    rate_limit    TEXT NOT NULL DEFAULT '',
    rules         TEXT NOT NULL DEFAULT '',
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_src_platform_targets UNIQUE (platform_id, target_id)
);
CREATE INDEX IF NOT EXISTS idx_src_platform_targets_target ON src_platform_targets(target_id);
DROP TRIGGER IF EXISTS trg_src_platform_targets_upd ON src_platform_targets;
CREATE TRIGGER trg_src_platform_targets_upd BEFORE UPDATE ON src_platform_targets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 目标资产:资产本体仍在 assets 表,这里只记「该平台该目标纳入了哪些资产」。
-- 用引用而非复制,资产详情/覆盖度等既有链路完全不受影响。
--   source: manual=手工录入 / recon=从信息搜集导入 / existing=关联已有资产
CREATE TABLE IF NOT EXISTS src_target_assets (
    id          BIGSERIAL PRIMARY KEY,
    platform_id BIGINT NOT NULL REFERENCES src_platforms(id) ON DELETE CASCADE,
    target_id   BIGINT NOT NULL REFERENCES src_targets(id) ON DELETE CASCADE,
    asset_id    BIGINT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    source      TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','recon','existing')),
    note        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_src_target_assets UNIQUE (platform_id, target_id, asset_id)
);
CREATE INDEX IF NOT EXISTS idx_src_target_assets_target ON src_target_assets(target_id, platform_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_src_target_assets_asset  ON src_target_assets(asset_id);

-- 任务归属到「平台×目标」,成果随之可平台维度聚合。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS src_platform_id BIGINT REFERENCES src_platforms(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS src_target_id   BIGINT REFERENCES src_targets(id)   ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_src_platform ON tasks(src_platform_id) WHERE src_platform_id IS NOT NULL;
ALTER TABLE src_rewards ADD COLUMN IF NOT EXISTS platform_id BIGINT REFERENCES src_platforms(id) ON DELETE SET NULL;
ALTER TABLE src_rewards ADD COLUMN IF NOT EXISTS target_id   BIGINT REFERENCES src_targets(id)   ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_src_rewards_platform ON src_rewards(platform_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_src_rewards_target   ON src_rewards(target_id, created_at DESC);

-- 存量数据迁移:旧 src_profiles(企业 + 平台文本) → 新三级结构。
-- 用 settings 里的标记保证只跑一次(建平台/目标都是 INSERT,重复跑会累积垃圾),
-- 跑完把标记写上;src_profiles 表本身保留不删,作为回滚源。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM settings WHERE key = 'src_platforms_migrated') THEN
        RETURN;
    END IF;

    -- 1. 平台:旧 platform 文本去重。空文本归入「未分类平台」而不是丢弃——
    --    丢了那些规范就再也找不回来了。
    INSERT INTO src_platforms (name, nkey, description)
    SELECT btrim(p.platform), src_nkey(p.platform), '由旧「企业模式」SRC 规范迁移'
      FROM src_profiles p
     WHERE btrim(p.platform) <> ''
     GROUP BY src_nkey(p.platform), btrim(p.platform)
    ON CONFLICT (nkey) DO NOTHING;

    INSERT INTO src_platforms (name, nkey, description)
    SELECT '未分类平台', src_nkey('未分类平台'), '旧规范中未填写平台名称的记录'
     WHERE EXISTS (SELECT 1 FROM src_profiles WHERE btrim(platform) = '')
    ON CONFLICT (nkey) DO NOTHING;

    -- 2. 目标:旧规范关联的企业升级为平台下的目标。
    INSERT INTO src_targets (name, nkey, company_id)
    SELECT c.name, c.nkey, c.id
      FROM src_profiles p
      JOIN companies c ON c.id = p.company_id
    ON CONFLICT (nkey) DO NOTHING;

    -- 3. 挂链:旧规范的全部字段原样搬到「平台×目标」这一层,一个都不丢。
    INSERT INTO src_platform_targets
        (platform_id, target_id, scope_note, forbidden, allowed_types, rate_limit, rules, enabled)
    SELECT pl.id, t.id, p.scope_note, p.forbidden, p.allowed_types, p.rate_limit, p.rules, p.enabled
      FROM src_profiles p
      JOIN companies c  ON c.id = p.company_id
      JOIN src_targets t ON t.company_id = c.id
      JOIN src_platforms pl
        ON pl.nkey = CASE WHEN btrim(p.platform) = '' THEN src_nkey('未分类平台') ELSE src_nkey(p.platform) END
    ON CONFLICT (platform_id, target_id) DO NOTHING;

    INSERT INTO settings (key, value) VALUES ('src_platforms_migrated', 'true')
    ON CONFLICT (key) DO NOTHING;
END $$;


-- ============================================================
-- SRC 增强(2026-09-25):规范附件统一上传 + 拦截规则 SRC 适用范围。
-- ============================================================

-- 平台规范附件放开「未分类」:统一上传入口先收文件,AI 识别填入时再把正文
-- 归类进四类规范文本;文件本身的类别可随时改派(不必在上传时就选对)。
ALTER TABLE src_platform_files DROP CONSTRAINT IF EXISTS src_platform_files_kind_check;
ALTER TABLE src_platform_files ADD CONSTRAINT src_platform_files_kind_check
    CHECK (kind IN ('reward_standard','scoring_standard','code_of_conduct','rules','other'));

-- 拦截规则适用范围:''=全部任务(既有行为不变);'src'=仅 SRC 模式任务。
-- 为什么放规则上而不是另建一套 SRC 执行系统:拦截管线是工具调用的唯一咽喉,
-- 两套执行真相会让「这条规则到底拦没拦」无法回答。SRC 维度只是匹配时的一个
-- 任务级过滤条件(任务 src_mode 与规则 scope 须同时满足)。
ALTER TABLE intercept_rules ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT '';
ALTER TABLE intercept_rules DROP CONSTRAINT IF EXISTS intercept_rules_scope_check;
ALTER TABLE intercept_rules ADD CONSTRAINT intercept_rules_scope_check
    CHECK (scope IN ('','src'));
CREATE INDEX IF NOT EXISTS idx_intercept_rules_scope ON intercept_rules(scope) WHERE scope <> '';

-- ============================================================
-- VPS 中心(2026-09-25):任务级执行环境。自有 VPS 手动录入,
-- agent 的命令类工具经 SSH 在指派机器上执行,Web 终端 / 工具集 / 流水审计。
-- ============================================================

-- 机器池:一台可 SSH 的 Linux 机器 = 一个执行环境。
-- 凭据(密码/私钥/私钥口令)以 secretbox AES-256-GCM 密文入库,明文只在 server 层短暂存在。
CREATE TABLE IF NOT EXISTS vps_machines (
    id                 BIGSERIAL PRIMARY KEY,
    name               TEXT NOT NULL,
    host               TEXT NOT NULL,
    port               INT NOT NULL DEFAULT 22,
    username           TEXT NOT NULL,
    auth_type          TEXT NOT NULL DEFAULT 'password',
    password_enc       BYTEA,
    private_key_enc    BYTEA,
    key_passphrase_enc BYTEA,
    -- online/offline/unknown/busy/maintenance
    status             TEXT NOT NULL DEFAULT 'unknown',
    -- 系统信息(录入测活时经 SSH 采集)
    os_name            TEXT NOT NULL DEFAULT '',
    os_version         TEXT NOT NULL DEFAULT '',
    kernel             TEXT NOT NULL DEFAULT '',
    arch               TEXT NOT NULL DEFAULT '',
    cpu_cores          INT NOT NULL DEFAULT 0,
    memory_mb          INT NOT NULL DEFAULT 0,
    disk_gb            INT NOT NULL DEFAULT 0,
    region             TEXT NOT NULL DEFAULT '',
    -- manual(手动录入)/ cloud(云 API 开通,二期)
    provider           TEXT NOT NULL DEFAULT 'manual',
    tags               JSONB NOT NULL DEFAULT '[]',
    -- 该机器上平台工作区的根目录(每任务一个子目录,SSH 执行固定 cd 进去)
    workspace_base     TEXT NOT NULL DEFAULT '/root/.artex-ws',
    -- 当前指派(冗余列,配合下面的部分唯一索引保证一台机器同一时刻最多被一个任务占用)
    assigned_task_id   BIGINT,
    last_error         TEXT NOT NULL DEFAULT '',
    last_check_at      TIMESTAMPTZ,
    last_online_at     TIMESTAMPTZ,
    notes              TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (auth_type IN ('password','key')),
    CHECK (status IN ('online','offline','unknown','busy','maintenance')),
    CHECK (provider IN ('manual','cloud'))
);
-- 一台机器同一时刻只能被一个任务占用。
CREATE UNIQUE INDEX IF NOT EXISTS uq_vps_machines_active_task
    ON vps_machines(assigned_task_id) WHERE assigned_task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_vps_machines_status ON vps_machines(status);
DROP TRIGGER IF EXISTS trg_vps_machines_upd ON vps_machines;
CREATE TRIGGER trg_vps_machines_upd BEFORE UPDATE ON vps_machines
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 指派历史:谁(哪个任务)在什么时间用了哪台机器、何时因何释放。
CREATE TABLE IF NOT EXISTS vps_assignments (
    id             BIGSERIAL PRIMARY KEY,
    machine_id     BIGINT NOT NULL,
    task_id        BIGINT NOT NULL,
    assigned_by    TEXT NOT NULL DEFAULT 'manual',
    assigned_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at    TIMESTAMPTZ,
    -- manual(手动解绑)/ task_end(任务终态自动释放)
    release_reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_vps_assignments_task ON vps_assignments(task_id);
CREATE INDEX IF NOT EXISTS idx_vps_assignments_machine ON vps_assignments(machine_id);

-- 执行流水:每一次经平台在该机器上跑过的命令。终端交互命令按整段会话记一条。
-- 完整输出落 <BaseDir>/vps/outputs/<id>.txt,表内只留预览与大小(命令输出可能很大)。
CREATE TABLE IF NOT EXISTS vps_executions (
    id           BIGSERIAL PRIMARY KEY,
    machine_id   BIGINT NOT NULL,
    task_id      BIGINT,
    -- agent(worker 工具执行)/ terminal(Web 终端整段会话)/ toolset(工具集脚本)/ manual(单命令试跑)/ detect(系统/工具探测)
    source       TEXT NOT NULL DEFAULT 'manual',
    toolset_id   BIGINT,
    command      TEXT NOT NULL,
    exit_code    INT,
    duration_ms  INT NOT NULL DEFAULT 0,
    output_size  INT NOT NULL DEFAULT 0,
    output_path  TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (source IN ('agent','terminal','toolset','manual','detect'))
);
CREATE INDEX IF NOT EXISTS idx_vps_exec_machine_time ON vps_executions(machine_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_vps_exec_task ON vps_executions(task_id) WHERE task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_vps_exec_created ON vps_executions(created_at DESC);

-- 工具集:初始化脚本模板(一键给机器装渗透工具)。builtin 种子在 Go 侧幂等播种
-- (同 promptcatalog/toolcatalog 惯例),不在这里 INSERT。
CREATE TABLE IF NOT EXISTS vps_toolsets (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    script      TEXT NOT NULL,
    builtin     BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
DROP TRIGGER IF EXISTS trg_vps_toolsets_upd ON vps_toolsets;
CREATE TRIGGER trg_vps_toolsets_upd BEFORE UPDATE ON vps_toolsets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 工具检测:每台机器 × 每个已知工具的最新探测结果(工具检测页的 ✓/✗/版本矩阵)。
CREATE TABLE IF NOT EXISTS vps_tool_detections (
    machine_id  BIGINT NOT NULL,
    tool_name   TEXT NOT NULL,
    installed   BOOLEAN NOT NULL DEFAULT false,
    version     TEXT NOT NULL DEFAULT '',
    detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (machine_id, tool_name)
);

-- ───────────────────────── C2 中心（自研轻量 beacon，契约见 C2_CONTRACTS.md）─────────────────────────
-- 监听器:beacon HTTP 回连入口。enc_key 是 secretbox 密文(32B 明文只活在 server 内存,
-- 接口永不回显);url_path 是信道路径随机段;callback 是写进载荷的回调 base URL。
CREATE TABLE IF NOT EXISTS c2_listeners (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    -- http(https 二期)
    proto       TEXT NOT NULL DEFAULT 'http',
    bind_host   TEXT NOT NULL DEFAULT '0.0.0.0',
    port        INT NOT NULL DEFAULT 0,          -- 0=启动时自动分配
    url_path    TEXT NOT NULL DEFAULT '',
    callback    TEXT NOT NULL DEFAULT '',        -- 如 http://1.2.3.4:8443,生成载荷必填
    enc_key     BYTEA NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    notes       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (proto IN ('http'))
);
CREATE INDEX IF NOT EXISTS idx_c2_listeners_enabled ON c2_listeners(enabled) WHERE enabled;
DROP TRIGGER IF EXISTS trg_c2_listeners_upd ON c2_listeners;
CREATE TRIGGER trg_c2_listeners_upd BEFORE UPDATE ON c2_listeners
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 会话:一台上线主机。sid 是 beacon 持有的会话令牌;listener_id+beacon_uid 幂等
-- (同一主机重连=同一会话复活);task_id 绑定任务(可空);sleep/jitter 由平台下发控制。
CREATE TABLE IF NOT EXISTS c2_sessions (
    id           BIGSERIAL PRIMARY KEY,
    listener_id  BIGINT NOT NULL,
    beacon_uid   TEXT NOT NULL DEFAULT '',
    sid          TEXT NOT NULL,
    hostname     TEXT NOT NULL DEFAULT '',
    username     TEXT NOT NULL DEFAULT '',
    elevated     BOOLEAN NOT NULL DEFAULT false,
    os           TEXT NOT NULL DEFAULT '',
    arch         TEXT NOT NULL DEFAULT '',
    local_ips    TEXT NOT NULL DEFAULT '',
    pid          INT NOT NULL DEFAULT 0,
    process      TEXT NOT NULL DEFAULT '',
    sleep_sec    INT NOT NULL DEFAULT 10,
    jitter_pct   INT NOT NULL DEFAULT 20,
    task_id      BIGINT,
    -- online(心跳正常)/offline(超时未归)/dead(平台终止)
    status       TEXT NOT NULL DEFAULT 'online',
    labels       TEXT NOT NULL DEFAULT '',
    meta         JSONB NOT NULL DEFAULT '{}',
    first_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status IN ('online','offline','dead'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_c2_sessions_sid ON c2_sessions(sid);
CREATE INDEX IF NOT EXISTS idx_c2_sessions_listener ON c2_sessions(listener_id);
CREATE INDEX IF NOT EXISTS idx_c2_sessions_status ON c2_sessions(status, last_seen);
CREATE INDEX IF NOT EXISTS idx_c2_sessions_task ON c2_sessions(task_id) WHERE task_id IS NOT NULL;
DROP TRIGGER IF EXISTS trg_c2_sessions_upd ON c2_sessions;
CREATE TRIGGER trg_c2_sessions_upd BEFORE UPDATE ON c2_sessions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 下发任务队列:平台写 queued,beacon 长轮询取走(→sent),结果回传置 done/error。
-- 大输出(>256KB)落 <BaseDir>/c2/outputs/,表内只留预览与大小(vps_executions 同款)。
CREATE TABLE IF NOT EXISTS c2_tasks (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT NOT NULL,
    -- shell(命令)/files_list(目录浏览)/upload/download(文件传输)/loot/info(主机采集)
    kind         TEXT NOT NULL DEFAULT 'shell',
    input        TEXT NOT NULL DEFAULT '',
    output       TEXT NOT NULL DEFAULT '',
    output_path  TEXT NOT NULL DEFAULT '',
    output_size  BIGINT NOT NULL DEFAULT 0,
    -- queued(待发)/sent(已发未归)/done/error
    status       TEXT NOT NULL DEFAULT 'queued',
    -- user(人在页面下发)/agent(AI 工具下发)
    actor        TEXT NOT NULL DEFAULT 'user',
    created_by   TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at      TIMESTAMPTZ,
    done_at      TIMESTAMPTZ,
    CHECK (kind IN ('shell','files_list','upload','download','loot','info')),
    CHECK (status IN ('queued','sent','done','error')),
    CHECK (actor IN ('user','agent'))
);
CREATE INDEX IF NOT EXISTS idx_c2_tasks_session ON c2_tasks(session_id, id);
CREATE INDEX IF NOT EXISTS idx_c2_tasks_status ON c2_tasks(status) WHERE status IN ('queued','sent');

-- 文件传输:up(平台→目标)/down(目标→平台)的分块传输记录。
-- up 的平台侧暂存 blob 与 down 的产物都落 <BaseDir>/c2/ 下,表内只存路径。
CREATE TABLE IF NOT EXISTS c2_transfers (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT NOT NULL,
    task_id      BIGINT,
    direction    TEXT NOT NULL DEFAULT 'down',
    remote_path  TEXT NOT NULL DEFAULT '',
    size         BIGINT NOT NULL DEFAULT 0,
    done_size    BIGINT NOT NULL DEFAULT 0,
    -- queued/running/done/error
    status       TEXT NOT NULL DEFAULT 'queued',
    local_path   TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    done_at      TIMESTAMPTZ,
    CHECK (direction IN ('up','down')),
    CHECK (status IN ('queued','running','done','error'))
);
CREATE INDEX IF NOT EXISTS idx_c2_transfers_session ON c2_transfers(session_id, id);

-- 战利品:凭据/哈希/文件/截图的归档。credential/hash 的 content 是 secretbox 密文;
-- 大文件落 <BaseDir>/c2/loot/,finding_id 二期联动发现。
CREATE TABLE IF NOT EXISTS c2_loot (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT NOT NULL,
    task_id      BIGINT,
    -- credential/hash/file/screenshot/other
    kind         TEXT NOT NULL DEFAULT 'other',
    name         TEXT NOT NULL DEFAULT '',
    content      TEXT NOT NULL DEFAULT '',
    blob_path    TEXT NOT NULL DEFAULT '',
    source_path  TEXT NOT NULL DEFAULT '',
    finding_id   BIGINT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (kind IN ('credential','hash','file','screenshot','other'))
);
CREATE INDEX IF NOT EXISTS idx_c2_loot_kind ON c2_loot(kind);
CREATE INDEX IF NOT EXISTS idx_c2_loot_session ON c2_loot(session_id);

-- 载荷:由监听器生成的 beacon 投递物。token 用于监听器免鉴权投递端点
-- /<url_path>/p/<token>(不可猜);文件落 <BaseDir>/c2/payloads/。
CREATE TABLE IF NOT EXISTS c2_payloads (
    id           BIGSERIAL PRIMARY KEY,
    listener_id  BIGINT NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    -- ps1/bash/py(解释型)/gosrc(Go 源码工程)
    format       TEXT NOT NULL DEFAULT 'ps1',
    -- windows/linux/macos/cross
    target       TEXT NOT NULL DEFAULT 'windows',
    token        TEXT NOT NULL,
    file_path    TEXT NOT NULL DEFAULT '',
    sha256       TEXT NOT NULL DEFAULT '',
    size         BIGINT NOT NULL DEFAULT 0,
    oneliner     TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (format IN ('ps1','bash','py','gosrc')),
    CHECK (target IN ('windows','linux','macos','cross'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_c2_payloads_token ON c2_payloads(token);
CREATE INDEX IF NOT EXISTS idx_c2_payloads_listener ON c2_payloads(listener_id);
DROP TRIGGER IF EXISTS trg_c2_payloads_upd ON c2_payloads;
CREATE TRIGGER trg_c2_payloads_upd BEFORE UPDATE ON c2_payloads
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ───────────────────────── C2 中心二期（契约 C2_CONTRACTS.md §11-§16）─────────────────────────
-- 监听器载体扩展:local=平台本机(一期行为);vps=以 VPS 机器为出入口,平台经 SSH 远程端口
-- 转发把 VPS公网:remote_port 的流量转回本机监听器(host_kind='vps' 时 vps_machine_id 必填)。
ALTER TABLE c2_listeners ADD COLUMN IF NOT EXISTS host_kind TEXT NOT NULL DEFAULT 'local';
ALTER TABLE c2_listeners ADD COLUMN IF NOT EXISTS vps_machine_id BIGINT;
ALTER TABLE c2_listeners ADD COLUMN IF NOT EXISTS remote_port INT NOT NULL DEFAULT 0;
ALTER TABLE c2_listeners DROP CONSTRAINT IF EXISTS c2_listeners_host_check;
ALTER TABLE c2_listeners ADD CONSTRAINT c2_listeners_host_check
    CHECK (host_kind IN ('local','vps') AND (host_kind <> 'vps' OR vps_machine_id IS NOT NULL));
CREATE INDEX IF NOT EXISTS idx_c2_listeners_vps ON c2_listeners(vps_machine_id) WHERE vps_machine_id IS NOT NULL;

-- 载荷二期列:变形来源档案 / 是否经 VPS 构建出二进制。
ALTER TABLE c2_payloads ADD COLUMN IF NOT EXISTS evasion_profile_id BIGINT;
ALTER TABLE c2_payloads ADD COLUMN IF NOT EXISTS built_on_vps BOOLEAN NOT NULL DEFAULT false;

-- 免杀档案:目标 AV 环境 + 技术配置(LLM 变形与静态自测的输入)。techniques 是自由 JSONB
-- (混淆层级/字符串编码/加载器形态等),语义由 server 侧管线解释。
CREATE TABLE IF NOT EXISTS c2_evasion_profiles (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL,
    -- defender/360/huorong/generic/custom
    target_av      TEXT NOT NULL DEFAULT 'generic',
    -- 适用载荷格式: ps1/bash/py/gosrc
    payload_format TEXT NOT NULL DEFAULT 'ps1',
    techniques     JSONB NOT NULL DEFAULT '{}',
    instructions   TEXT NOT NULL DEFAULT '',
    -- manual(手工)/llm(AI 生成)
    source         TEXT NOT NULL DEFAULT 'manual',
    notes          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (target_av IN ('defender','360','huorong','generic','custom')),
    CHECK (payload_format IN ('ps1','bash','py','gosrc')),
    CHECK (source IN ('manual','llm'))
);
DROP TRIGGER IF EXISTS trg_c2_evasion_profiles_upd ON c2_evasion_profiles;
CREATE TRIGGER trg_c2_evasion_profiles_upd BEFORE UPDATE ON c2_evasion_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- C2 异步作业:免杀变形(evasion)与 VPS 构建(build)共用。task_id 挂 stage=post 监看任务
-- (任务中心/对话自动收录,仿报告中心 AI 管线);result 存静态自测报告/构建日志摘要。
CREATE TABLE IF NOT EXISTS c2_jobs (
    id             BIGSERIAL PRIMARY KEY,
    -- evasion(载荷变形)/build(gosrc 经 VPS 编译)
    kind           TEXT NOT NULL,
    profile_id     BIGINT,
    payload_id     BIGINT,
    listener_id    BIGINT,
    vps_machine_id BIGINT,
    task_id        BIGINT,
    -- queued/running/done/error
    status         TEXT NOT NULL DEFAULT 'queued',
    input          JSONB NOT NULL DEFAULT '{}',
    result         JSONB NOT NULL DEFAULT '{}',
    error          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    done_at        TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (kind IN ('evasion','build')),
    CHECK (status IN ('queued','running','done','error'))
);
CREATE INDEX IF NOT EXISTS idx_c2_jobs_kind_status ON c2_jobs(kind, status, id DESC);
DROP TRIGGER IF EXISTS trg_c2_jobs_upd ON c2_jobs;
CREATE TRIGGER trg_c2_jobs_upd BEFORE UPDATE ON c2_jobs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ───────────────────────── C2 中心三期（契约 C2_CONTRACTS.md §17-§23）─────────────────────────
-- 监听器传输协议扩展:http|ws|tcp|kcp|oss(https 与 DNS 系四期)。
-- oss 载体的 endpoint/bucket/AK/SK 存 transport_config_enc(secretbox 密封 JSON,永不回显)。
ALTER TABLE c2_listeners DROP CONSTRAINT IF EXISTS c2_listeners_proto_check;
ALTER TABLE c2_listeners ADD CONSTRAINT c2_listeners_proto_check
    CHECK (proto IN ('http','ws','tcp','kcp','oss'));
ALTER TABLE c2_listeners ADD COLUMN IF NOT EXISTS transport_config_enc BYTEA;

-- 会话承载传输标注(http/ws/tcp/kcp/oss;一期数据回填 http)。
ALTER TABLE c2_sessions ADD COLUMN IF NOT EXISTS transport TEXT NOT NULL DEFAULT 'http';

-- 载荷产物维度:artifact 产物形态 / direction 连接方向 / staged 是否 stage 模式。
ALTER TABLE c2_payloads ADD COLUMN IF NOT EXISTS artifact TEXT NOT NULL DEFAULT 'script';
ALTER TABLE c2_payloads ADD COLUMN IF NOT EXISTS direction TEXT NOT NULL DEFAULT 'reverse';
ALTER TABLE c2_payloads ADD COLUMN IF NOT EXISTS staged BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE c2_payloads DROP CONSTRAINT IF EXISTS c2_payloads_artifact_check;
ALTER TABLE c2_payloads ADD CONSTRAINT c2_payloads_artifact_check
    CHECK (artifact IN ('script','exe','dll','shellcode','ebpf'));
ALTER TABLE c2_payloads DROP CONSTRAINT IF EXISTS c2_payloads_direction_check;
ALTER TABLE c2_payloads ADD CONSTRAINT c2_payloads_direction_check
    CHECK (direction IN ('reverse','bind'));

-- 下发任务 kind 扩 'plugin'(插件运行记录=任务流水,复用既有通道)。
ALTER TABLE c2_tasks DROP CONSTRAINT IF EXISTS c2_tasks_kind_check;
ALTER TABLE c2_tasks ADD CONSTRAINT c2_tasks_kind_check
    CHECK (kind IN ('shell','files_list','upload','download','loot','info','plugin'));

-- 免杀档案 source 扩 'builtin'(三期知识库内置档案)。
ALTER TABLE c2_evasion_profiles DROP CONSTRAINT IF EXISTS c2_evasion_profiles_source_check;
ALTER TABLE c2_evasion_profiles ADD CONSTRAINT c2_evasion_profiles_source_check
    CHECK (source IN ('manual','llm','builtin'));

-- C2 插件:可推送执行的能力包。script 直走命令通道(content+entry 渲染);binary 走
-- 文件通道上传后执行(blob 落 <BaseDir>/c2/plugins/)。params_schema 是参数 JSON Schema
-- (仅 {{param}} 占位注入,渲染前白名单校验防注入)。
CREATE TABLE IF NOT EXISTS c2_plugins (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT NOT NULL,
    -- windows/linux/macos/cross
    platform      TEXT NOT NULL DEFAULT 'cross',
    -- script(脚本,直走命令通道)/binary(二进制,走文件通道)
    kind          TEXT NOT NULL DEFAULT 'script',
    -- ps1/sh/py
    language      TEXT NOT NULL DEFAULT 'sh',
    entry         TEXT NOT NULL DEFAULT '',
    content       TEXT NOT NULL DEFAULT '',
    blob_path     TEXT NOT NULL DEFAULT '',
    params_schema JSONB NOT NULL DEFAULT '{}',
    description   TEXT NOT NULL DEFAULT '',
    builtin       BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (platform IN ('windows','linux','macos','cross')),
    CHECK (kind IN ('script','binary')),
    CHECK (language IN ('ps1','sh','py'))
);
CREATE INDEX IF NOT EXISTS idx_c2_plugins_platform ON c2_plugins(platform);
DROP TRIGGER IF EXISTS trg_c2_plugins_upd ON c2_plugins;
CREATE TRIGGER trg_c2_plugins_upd BEFORE UPDATE ON c2_plugins
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
