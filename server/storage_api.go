package server

// 存储中心 API:桶 CRUD / 测活 / 默认桶 / 数据类别总览(契约 STORAGE_CONTRACTS.md §wire)。
//
// 路由(逐字对齐契约):
//
//	GET    /api/storage/buckets?protocol=&status=&q=&limit=&offset= → {items,total,counts,status_counts}
//	POST   /api/storage/buckets                → {id}
//	PUT    /api/storage/buckets/{id}           → {updated}(secret_key 留空 = 不改旧密钥)
//	DELETE /api/storage/buckets/{id}           → {deleted}(409: 默认桶或已有上桶对象)
//	POST   /api/storage/buckets/{id}/check     → {ok,error?,latency_ms}
//	POST   /api/storage/buckets/{id}/activate  → {active:true}(同步测活,通过才切默认)
//	POST   /api/storage/deactivate             → {active:false}
//	GET    /api/storage/overview               → {categories:[...],active_bucket_id}
//
// 凭据 secretbox 加密 base64 落库,任何响应永不回显(has_secret 布尔代替)。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/storagecenter"
)

// storageDecodeBody 读取并解析 JSON 请求体(1MB 上限)。
func storageDecodeBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

// storageNowMillis 当前毫秒时间戳(测活耗时展示)。
func storageNowMillis() int64 { return time.Now().UnixMilli() }

// terminalTaskSet 终态任务 id 集合(盘点 task-workdirs 时只统计终态;查询失败
// 返回 nil = 不做终态过滤,宁可数字偏大也不能让总览页面挂掉)。
func terminalTaskSet(pg *pgdb.DB) map[int64]bool {
	ids, err := pg.ListTerminalTaskWorkdirIDs()
	if err != nil {
		return nil
	}
	return ids
}

func (s *Server) registerStorageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/storage/buckets", s.storageListBuckets)
	mux.HandleFunc("POST /api/storage/buckets", s.storageCreateBucket)
	mux.HandleFunc("PUT /api/storage/buckets/{id}", s.storageUpdateBucket)
	mux.HandleFunc("DELETE /api/storage/buckets/{id}", s.storageDeleteBucket)
	mux.HandleFunc("POST /api/storage/buckets/{id}/check", s.storageCheckBucket)
	mux.HandleFunc("POST /api/storage/buckets/{id}/activate", s.storageActivateBucket)
	mux.HandleFunc("POST /api/storage/deactivate", s.storageDeactivate)
	mux.HandleFunc("GET /api/storage/overview", s.storageOverview)
	// 迁移与本地清理(实现在 storage_migrate.go;server.go 只挂本函数一处)
	mux.HandleFunc("POST /api/storage/migrate", s.storageStartMigration)
	mux.HandleFunc("GET /api/storage/migrate/status", s.storageMigrateStatus)
	mux.HandleFunc("POST /api/storage/migrate/cancel", s.storageMigrateCancel)
	mux.HandleFunc("POST /api/storage/purge-local", s.storagePurgeLocal)
}

// storageBucketDTO 桶的对外形状(凭据永不回显)。
type storageBucketDTO struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	Endpoint       string `json:"endpoint"`
	Bucket         string `json:"bucket"`
	Region         string `json:"region"`
	PathPrefix     string `json:"path_prefix"`
	Active         bool   `json:"active"`
	Status         string `json:"status"`
	HasSecret      bool   `json:"has_secret"`
	LastCheckAt    int64  `json:"last_check_at"`
	LastCheckError string `json:"last_check_error"`
}

func storageBucketToDTO(b *pgdb.StorageBucket) storageBucketDTO {
	return storageBucketDTO{
		ID: b.ID, Name: b.Name, Protocol: b.Protocol, Endpoint: b.Endpoint,
		Bucket: b.Bucket, Region: b.Region, PathPrefix: b.PathPrefix,
		Active: b.Active, Status: b.Status, HasSecret: b.HasSecret,
		LastCheckAt: b.LastCheckAtMs, LastCheckError: b.LastCheckError,
	}
}

// storagePathID 解析路径里的桶 id。
func storagePathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("bad bucket id")
	}
	return id, nil
}

func (s *Server) storageListBuckets(w http.ResponseWriter, r *http.Request) {
	items, total, counts, statusCounts, err := s.m.pg.ListStorageBuckets(pgdb.StorageBucketFilter{
		Protocol: r.URL.Query().Get("protocol"),
		Status:   r.URL.Query().Get("status"),
		Q:        r.URL.Query().Get("q"),
		Limit:    atoiDefault(r.URL.Query().Get("limit"), 20),
		Offset:   atoiDefault(r.URL.Query().Get("offset"), 0),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询存储桶失败: "+err.Error())
		return
	}
	out := []storageBucketDTO{}
	for _, b := range items {
		out = append(out, storageBucketToDTO(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out, "total": total, "counts": counts, "status_counts": statusCounts,
	})
}

// storageBucketPayload 建/改桶请求体。secret_key 明文提交,服务端加密落库。
type storageBucketPayload struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	Endpoint   string `json:"endpoint"`
	Bucket     string `json:"bucket"`
	Region     string `json:"region"`
	PathPrefix string `json:"path_prefix"`
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	Activate   bool   `json:"activate"`
}

// normalizeStorageBucket 校验并把 payload 归一成 db 行(secret 密文由调用方另取)。
// requireSecret:创建恒真;编辑时 false(secret_key 留空 = 保留旧密钥)。
func normalizeStorageBucket(p *storageBucketPayload, requireSecret bool) (*pgdb.StorageBucket, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Protocol = strings.TrimSpace(p.Protocol)
	p.Bucket = strings.Trim(strings.TrimSpace(p.Bucket), "/")
	p.Region = strings.TrimSpace(p.Region)
	p.PathPrefix = strings.Trim(strings.TrimSpace(p.PathPrefix), "/")
	if p.Name == "" {
		return nil, errors.New("名称必填")
	}
	if p.Protocol != "s3" && p.Protocol != "webdav" {
		return nil, fmt.Errorf("无效协议: %q(仅 s3/webdav)", p.Protocol)
	}
	endpoint, err := storagecenter.NormalizeEndpoint(p.Endpoint)
	if err != nil {
		return nil, err
	}
	if p.Bucket == "" {
		return nil, errors.New("webdav 模式填远端目录,s3 模式填桶名;不能为空")
	}
	if requireSecret && strings.TrimSpace(p.SecretKey) == "" {
		return nil, errors.New("secret_key 必填(webdav 匿名可留空由管理员把握,此处从严)")
	}
	return &pgdb.StorageBucket{
		Name: p.Name, Protocol: p.Protocol, Endpoint: endpoint, Bucket: p.Bucket,
		Region: p.Region, PathPrefix: p.PathPrefix, AccessKey: strings.TrimSpace(p.AccessKey),
	}, nil
}

// storageTestBackend 用桶行装配可用的 Backend(凭据解密 + 全局出口注入)。
func (s *Server) storageTestBackend(b *pgdb.StorageBucket) (storagecenter.Backend, error) {
	secret := ""
	if b.SecretEnc != "" {
		raw, err := base64.StdEncoding.DecodeString(b.SecretEnc)
		if err != nil {
			return nil, fmt.Errorf("密文解码失败: %w", err)
		}
		if s.apiSecret == nil {
			return nil, errors.New("凭据加密盒未初始化")
		}
		plain, err := s.apiSecret.open(raw)
		if err != nil {
			return nil, fmt.Errorf("密文解封失败: %w", err)
		}
		secret = string(plain)
	}
	return storagecenter.NewBackend(storagecenter.Config{
		Protocol: b.Protocol, Endpoint: b.Endpoint, Bucket: b.Bucket, Region: b.Region,
		PathPrefix: b.PathPrefix, AccessKey: b.AccessKey, Secret: secret,
		ProxyAddr: s.m.ProxyAddr(), CAFile: s.m.ProxyCACert(),
	})
}

func (s *Server) storageCreateBucket(w http.ResponseWriter, r *http.Request) {
	var p storageBucketPayload
	if err := storageDecodeBody(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := normalizeStorageBucket(&p, true)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := s.apiSecret.seal([]byte(strings.TrimSpace(p.SecretKey)))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "密钥加密失败: "+err.Error())
		return
	}
	row.SecretEnc = storageB64(raw)
	created, err := s.m.pg.CreateStorageBucket(row)
	if err != nil {
		if strings.Contains(err.Error(), "uq_storage_buckets_name") || strings.Contains(err.Error(), "23505") {
			writeErr(w, http.StatusConflict, "同名存储桶已存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "创建存储桶失败: "+err.Error())
		return
	}
	// activate=true:先实测连通,通过才设默认;失败只返回 id 不设默认(警告带回)
	activateErr := ""
	if p.Activate {
		if err := s.storageDoActivate(created.ID); err != nil {
			activateErr = err.Error()
		}
	}
	created, _ = s.m.pg.GetStorageBucket(created.ID)
	resp := map[string]any{"id": created.ID}
	if activateErr != "" {
		resp["warning"] = "已创建,但设为默认失败: " + activateErr
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) storageUpdateBucket(w http.ResponseWriter, r *http.Request) {
	id, err := storagePathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var p storageBucketPayload
	if err := storageDecodeBody(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := normalizeStorageBucket(&p, false)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	secretEnc := ""
	if sk := strings.TrimSpace(p.SecretKey); sk != "" {
		raw, err := s.apiSecret.seal([]byte(sk))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "密钥加密失败: "+err.Error())
			return
		}
		secretEnc = storageB64(raw)
	}
	if err := s.m.pg.UpdateStorageBucket(id, row, secretEnc); err != nil {
		writeErr(w, http.StatusInternalServerError, "更新存储桶失败: "+err.Error())
		return
	}
	// 配置变了,旧测活结论作废
	_ = s.m.pg.SetStorageBucketStatus(id, "unchecked", "")
	writeJSON(w, http.StatusOK, map[string]any{"updated": true})
}

func (s *Server) storageDeleteBucket(w http.ResponseWriter, r *http.Request) {
	id, err := storagePathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := s.m.pg.GetStorageBucket(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "存储桶不存在")
		return
	}
	if b.Active {
		writeErr(w, http.StatusConflict, "默认桶不可删除,先取消默认(或切到其他桶)")
		return
	}
	if n, _ := s.m.pg.CountStorageObjectsForBucket(id); n > 0 {
		writeErr(w, http.StatusConflict, fmt.Sprintf("该桶已有 %d 个上桶对象登记,先清空对象或另行处理", n))
		return
	}
	ok, err := s.m.pg.DeleteStorageBucket(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "存储桶不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// storageCheckBucket 同步测活:s3 = HEAD bucket,webdav = PROPFIND depth 0。
func (s *Server) storageCheckBucket(w http.ResponseWriter, r *http.Request) {
	id, err := storagePathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := s.m.pg.GetStorageBucket(id)
	if err != nil || b == nil {
		writeErr(w, http.StatusNotFound, "存储桶不存在")
		return
	}
	start := storageNowMillis()
	backend, err := s.storageTestBackend(b)
	if err == nil {
		err = backend.Test(r.Context())
	}
	latency := storageNowMillis() - start
	if err != nil {
		_ = s.m.pg.SetStorageBucketCheck(id, false, err.Error())
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "latency_ms": latency})
		return
	}
	_ = s.m.pg.SetStorageBucketCheck(id, true, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "latency_ms": latency})
}

// storageDoActivate 同步测活通过后设默认桶(激活即真验证,不留 stale 状态)。
func (s *Server) storageDoActivate(id int64) error {
	b, err := s.m.pg.GetStorageBucket(id)
	if err != nil || b == nil {
		return errors.New("存储桶不存在")
	}
	backend, err := s.storageTestBackend(b)
	if err != nil {
		_ = s.m.pg.SetStorageBucketCheck(id, false, err.Error())
		return err
	}
	if err := backend.Test(context.Background()); err != nil {
		_ = s.m.pg.SetStorageBucketCheck(id, false, err.Error())
		return fmt.Errorf("连通性测试未通过: %w", err)
	}
	if err := s.m.pg.ActivateStorageBucket(id); err != nil {
		return err
	}
	return s.m.pg.SetStorageBucketCheck(id, true, "")
}

func (s *Server) storageActivateBucket(w http.ResponseWriter, r *http.Request) {
	id, err := storagePathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.storageDoActivate(id); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": true})
}

func (s *Server) storageDeactivate(w http.ResponseWriter, r *http.Request) {
	if err := s.m.pg.DeactivateStorageBucket(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[storage] 已取消默认存储桶,平台回落纯本地存储")
	writeJSON(w, http.StatusOK, map[string]any{"active": false})
}

// storageOverview 各数据类别的本地量(文件数/字节)与已上桶登记数。
// 本地量走 60s 缓存的全量盘点(41G 级数据目录全 walk 很贵,别每次请求都算)。
func (s *Server) storageOverview(w http.ResponseWriter, r *http.Request) {
	uploaded, err := s.m.pg.CountStorageObjectsByCategory()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	locals := storageCategoryStats(s.m.dir, terminalTaskSet(s.m.pg))
	out := []map[string]any{}
	for _, cat := range storageCategories {
		nBytes, nFiles := locals[cat]
		out = append(out, map[string]any{
			"category":       cat,
			"files":          nFiles,
			"bytes":          nBytes,
			"uploaded_files": uploaded[cat],
		})
	}
	activeID := int64(0)
	if b, err := s.m.pg.GetActiveStorageBucket(); err == nil && b != nil {
		activeID = b.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"categories": out, "active_bucket_id": activeID,
	})
}
