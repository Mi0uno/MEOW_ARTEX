package server

// 存储中心一键迁移引擎(契约 STORAGE_CONTRACTS.md):
//   - vulndb_backfill 同款形态:mutex 单例 + ctx 取消 + settings 游标 + 开机续跑;
//   - 逐类别枚举本地文件 → 逐文件 Put → storage_objects 登记 → 推进游标;
//   - 幂等:已登记(storage_objects)与游标之后的重复重跑都安全;
//   - task-workdirs 只动终态任务目录,uploads 类别与运行中任务绝不触碰。
//
// 路由:
//
//	POST   /api/storage/migrate         → {migration_id}(202;进行中 409)
//	GET    /api/storage/migrate/status  → {running,migration?,last?}
//	POST   /api/storage/migrate/cancel  → {canceled}
//	POST   /api/storage/purge-local     → {purged,skipped}(仅 report-jobs/task-archives)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	pgdb "github.com/Autumn-27/artex/db"
)

// ---- 数据类别注册表 ----

// storageCategories 迁移/纳管的固定类别顺序(overview 与前端页签共用此序)。
var storageCategories = []string{
	"report-jobs", "report-templates", "uploads", "evidence", "task-archives",
	"c2", "vps", "wordbooks", "src-platform", "task-workdirs",
}

// storagePurgeableCategories 允许「清理本地已上桶文件」的类别(v1 只开两类:
// 产物生成后不再被写入路径依赖;uploads/c2 等有运行时回读诉求,暂不开放)。
var storagePurgeableCategories = []string{"report-jobs", "task-archives"}

// storageCategoryRoots 类别 → dataDir 下相对根目录(多根 = 联合)。
var storageCategoryRoots = map[string][]string{
	"report-jobs":      {"report-jobs"},
	"report-templates": {"report-templates"},
	"uploads":          {"tasks", "sessions", "drafts"}, // 根下逐层找 uploads 子目录
	"evidence":         {filepathJoin("evidence", "blobs")},
	"task-archives":    {"archives"},
	"c2":               {"c2"},
	"vps":              {filepathJoin("vps", "outputs")},
	"wordbooks":        {"wordbooks"},
	"src-platform":     {"src-platforms"},
	"task-workdirs":    {"tasks"},
}

// storageStatsEntry 本地盘点条目(类别 → 字节数,类别 → 文件数)。
type storageStats = map[string]int64

// storageCategoryStats 盘点各类别本地文件数与字节数。单类别文件数上限 20 万,
// 超出即停(防护异常目录;总览是展示量,不是账本)。task-workdirs 只统计终态任务。
func storageCategoryStats(dataDir string, terminal map[int64]bool) map[string]storageStats {
	out := map[string]storageStats{}
	for _, cat := range storageCategories {
		out[cat] = storageStats{"files": 0, "bytes": 0}
		for _, root := range storageCategoryRoots[cat] {
			storageWalkStats(dataDir, root, cat, terminal, out[cat])
		}
	}
	return out
}

func storageWalkStats(dataDir, root, cat string, terminal map[int64]bool, acc storageStats) {
	base := filepathJoin(dataDir, root)
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return
	}
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单点不可读不阻塞盘点
		}
		if d.IsDir() {
			switch cat {
			case "uploads":
				// 只进 uploads 目录本身及其后代
				if d.Name() != "uploads" && path != base && !storageUnderUploadsAncestor(dataDir, path) {
					return fs.SkipDir
				}
			case "task-workdirs":
				if d.Name() == "uploads" {
					return fs.SkipDir
				}
				if path != base && !storageTerminalTaskDir(dataDir, path, terminal) {
					return fs.SkipDir
				}
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".part") {
			return nil
		}
		if i, err := d.Info(); err == nil {
			acc["files"]++
			acc["bytes"] += i.Size()
		}
		if acc["files"] >= 200000 {
			return fs.SkipAll
		}
		return nil
	})
}

// storageTerminalTaskDir 路径是否位于终态任务目录之下(tasks/<id>/ 前缀判断)。
func storageTerminalTaskDir(dataDir, path string, terminal map[int64]bool) bool {
	rel, err := relToDataDir(dataDir, path)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) < 2 {
		return true // tasks/ 根层继续走
	}
	var id int64
	if _, err := fmt.Sscanf(parts[1], "%d", &id); err != nil {
		return false // 非数字目录名不属于任务工作区
	}
	return terminal[id]
}

// storageUnderUploadsAncestor 路径是否已在某个 uploads 目录之下(统计延续)。
func storageUnderUploadsAncestor(dataDir, path string) bool {
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		if filepath.Base(parent) == "uploads" {
			return true
		}
		if parent == dataDir {
			return false
		}
		path = parent
	}
}

func relToDataDir(dataDir, path string) (string, error) {
	return filepath.Rel(dataDir, path)
}

// ---- 迁移控制器 ----

// storageMigrateController 迁移单例:同一时刻至多一个迁移在跑。
type storageMigrateController struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	running  bool
	migID    int64
	serverMu sync.RWMutex
	srv      *Server
}

var storageMigrate = &storageMigrateController{}

// bindStorageServer 注入 Server(启动时序:Handler/init 最早调用处)。
func bindStorageServer(s *Server) {
	storageMigrate.serverMu.Lock()
	storageMigrate.srv = s
	storageMigrate.serverMu.Unlock()
}

func (c *storageMigrateController) server() *Server {
	c.serverMu.RLock()
	defer c.serverMu.RUnlock()
	return c.srv
}

// storageMigrateCursor 游标:每类别最后完成(dataDir 相对路径,不含类别前缀)。
type storageMigrateCursor map[string]string

const storageMigrateCursorKey = "storage_migrate_cursor"

func storageLoadCursor(s *Server) storageMigrateCursor {
	c := storageMigrateCursor{}
	if v, ok, _ := s.m.pg.GetSetting(storageMigrateCursorKey); ok && v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

func storageSaveCursor(s *Server, c storageMigrateCursor) {
	raw, _ := json.Marshal(c)
	_ = s.m.pg.SetSetting(storageMigrateCursorKey, string(raw))
}

// storageStartMigration 启动迁移(幂等防重入)。categories 空 = 全部类别。
func (s *Server) storageStartMigration(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Categories []string `json:"categories"`
		BucketID   int64    `json:"bucket_id"`
	}
	if err := storageDecodeBody(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cats := []string{}
	valid := map[string]bool{}
	for _, c := range storageCategories {
		valid[c] = true
	}
	for _, c := range p.Categories {
		if !valid[c] {
			writeErr(w, http.StatusBadRequest, "无效数据类别: "+c)
			return
		}
		cats = append(cats, c)
	}
	if len(cats) == 0 {
		cats = append([]string{}, storageCategories...)
	}
	active, err := s.m.pg.GetActiveStorageBucket()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if active == nil {
		writeErr(w, http.StatusBadRequest, "尚未启用任何存储桶,先在存储桶页签设为默认")
		return
	}
	if p.BucketID != 0 && p.BucketID != active.ID {
		writeErr(w, http.StatusBadRequest, "迁移目标桶必须是当前默认桶")
		return
	}
	mig, err := s.m.pg.CreateStorageMigration(active.ID, cats)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.storageLaunchMigration(mig); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"migration_id": mig.ID})
}

// storageLaunchMigration 装配并起 goroutine;单例占用时返回错误(调用方转 409)。
// 开机续跑与手动启动共用:running 作业行存在即续跑(游标在 settings,天然断点)。
func (s *Server) storageLaunchMigration(mig *pgdb.StorageMigration) error {
	storageMigrate.mu.Lock()
	defer storageMigrate.mu.Unlock()
	if storageMigrate.running {
		return errors.New("已有迁移在进行中")
	}
	ctx, cancel := context.WithCancel(context.Background())
	storageMigrate.running = true
	storageMigrate.cancel = cancel
	storageMigrate.migID = mig.ID
	go func() {
		defer func() {
			storageMigrate.mu.Lock()
			storageMigrate.running = false
			storageMigrate.cancel = nil
			storageMigrate.mu.Unlock()
		}()
		s.storageRunMigration(ctx, mig)
	}()
	return nil
}

// initStorageMigration 开机续跑入口(server.go 启动区调用一次):
// 上次崩溃/重启遗留的 running 作业行,按 settings 游标接着跑。
func initStorageMigration(s *Server) {
	bindStorageServer(s)
	mig, err := s.m.pg.GetRunningStorageMigration()
	if err != nil {
		log.Printf("[storage] 迁移续跑检查失败: %v", err)
		return
	}
	if mig == nil {
		return
	}
	log.Printf("[storage] 发现在途迁移作业 #%d,按游标续跑", mig.ID)
	if err := s.storageLaunchMigration(mig); err != nil {
		log.Printf("[storage] 迁移续跑启动失败: %v", err)
	}
}

// storageRunMigration 迁移主循环。错误策略:单文件失败计数后继续;类别开头
// 测活失败/凭据缺失 = 整作业 fail-fast(继续跑只会把失败刷满)。
func (s *Server) storageRunMigration(ctx context.Context, mig *pgdb.StorageMigration) {
	bucket, err := s.m.pg.GetStorageBucket(mig.BucketID)
	if err != nil || bucket == nil {
		s.storageFailMigration(mig, "目标存储桶不存在")
		return
	}
	backend, err := s.storageTestBackend(bucket)
	if err != nil {
		s.storageFailMigration(mig, "装配存储客户端失败: "+err.Error())
		return
	}
	if err := backend.Test(ctx); err != nil {
		s.storageFailMigration(mig, "存储桶连通性失败: "+err.Error())
		return
	}

	var done, failed int64
	cursor := storageLoadCursor(s)
	// 起点预盘点:total 一次性落库,进度条才有分母(逐文件累加的旧算法起点恒 0)
	preTotal := int64(0)
	terminal0 := map[int64]bool{}
	if migHasCategory(mig.Categories, "task-workdirs") {
		if terminal0, err = s.m.pg.ListTerminalTaskWorkdirIDs(); err != nil {
			terminal0 = nil
		}
	}
	for _, cat := range mig.Categories {
		preTotal += int64(len(storageEnumerate(s.m.dir, cat, terminal0)))
	}
	_ = s.m.pg.SetStorageMigrationProgress(mig.ID, 0, 0, preTotal)
	terminal := map[int64]bool{}
	if migHasCategory(mig.Categories, "task-workdirs") {
		if terminal, err = s.m.pg.ListTerminalTaskWorkdirIDs(); err != nil {
			s.storageFailMigration(mig, "查询终态任务失败: "+err.Error())
			return
		}
	}

	for _, cat := range mig.Categories {
		select {
		case <-ctx.Done():
			_ = s.m.pg.FinishStorageMigration(mig.ID, "canceled", "手动取消", cursorJSON(cursor))
			log.Printf("[storage] 迁移 #%d 已取消(done=%d failed=%d)", mig.ID, done, failed)
			return
		default:
		}
		files := storageEnumerate(s.m.dir, cat, terminal)
		sort.Strings(files)
		last := cursor[cat]
		for _, rel := range files {
			select {
			case <-ctx.Done():
				_ = s.m.pg.FinishStorageMigration(mig.ID, "canceled", "手动取消", cursorJSON(cursor))
				return
			default:
			}
			// 游标快进:上次已越过的不重跑(字典序,逐文件推进)
			if last != "" && rel <= last {
				continue
			}
			if err := s.storageMigrateOne(ctx, backend, bucket, cat, rel); err != nil {
				failed++
				log.Printf("[storage] 迁移失败 %s/%s: %v", cat, rel, err)
			} else {
				done++
				cursor[cat] = rel
			}
			_ = s.m.pg.SetStorageMigrationProgress(mig.ID, done, failed, preTotal)
			// 游标每 20 个文件落一次盘(重启最多重做 20 个,不逐文件打 settings)
			if done%20 == 0 {
				storageSaveCursor(s, cursor)
			}
		}
		storageSaveCursor(s, cursor)
	}
	status := "done"
	errText := ""
	if failed > 0 {
		status = "done" // 整体完成,失败数看 failed_files;逐文件失败不算作业失败
		errText = fmt.Sprintf("%d 个文件上桶失败,详见服务端日志", failed)
	}
	_ = s.m.pg.FinishStorageMigration(mig.ID, status, errText, cursorJSON(cursor))
	log.Printf("[storage] 迁移 #%d 完成: done=%d failed=%d", mig.ID, done, failed)
}

func (s *Server) storageFailMigration(mig *pgdb.StorageMigration, msg string) {
	_ = s.m.pg.FinishStorageMigration(mig.ID, "failed", msg, []byte("{}"))
	log.Printf("[storage] 迁移 #%d 失败: %s", mig.ID, msg)
}

func migHasCategory(cats []string, cat string) bool {
	for _, c := range cats {
		if c == cat {
			return true
		}
	}
	return false
}

func cursorJSON(c storageMigrateCursor) []byte {
	raw, _ := json.Marshal(c)
	return raw
}

// storageMigrateOne 单文件上桶:读本地 → Put → 登记(幂等)。
func (s *Server) storageMigrateOne(ctx context.Context, backend interface {
	Put(ctx context.Context, key string, r io.Reader, size int64) error
}, bucket *pgdb.StorageBucket, cat, rel string) error {
	local := filepathJoin(s.m.dir, filepathFromSlash(rel))
	f, err := os.Open(local)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 本地已不在(此前清理过):视为已完成
		}
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	key, err := objectKeyFor(bucket.PathPrefix, rel)
	if err != nil {
		return err
	}
	if err := backend.Put(ctx, key, f, info.Size()); err != nil {
		return err
	}
	_, err = s.m.pg.InsertStorageObject(bucket.ID, cat, rel, info.Size())
	return err
}

// storageEnumerate 枚举类别下的全部文件(dataDir 相对路径,/ 分隔,已过滤
// .part/.tmp 中间态;uploads 只收 */uploads/ 之下;task-workdirs 只收终态任务
// 且排除 uploads 子目录)。
func storageEnumerate(dataDir, cat string, terminal map[int64]bool) []string {
	out := []string{}
	for _, root := range storageCategoryRoots[cat] {
		base := filepathJoin(dataDir, root)
		info, err := os.Stat(base)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				switch cat {
				case "uploads":
					if name != "uploads" && path != base && !storageUnderUploadsAncestor(dataDir, path) {
						return fs.SkipDir
					}
				case "task-workdirs":
					if name == "uploads" {
						return fs.SkipDir
					}
					if path != base {
						// tasks/<id>/ 二层才进,且必须是终态任务
						rel, _ := relToDataDir(dataDir, path)
						parts := strings.Split(rel, string(os.PathSeparator))
						if len(parts) >= 2 {
							var id int64
							if _, err := fmt.Sscanf(parts[1], "%d", &id); err != nil || !terminal[id] {
								return fs.SkipDir
							}
						}
					}
				}
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".tmp") {
				return nil
			}
			rel, err := relToDataDir(dataDir, path)
			if err != nil {
				return nil
			}
			out = append(out, filepath.ToSlash(rel)) // 恒 / 分隔(此前写反成 OS 分隔符,冒烟实锤)
			return nil
		})
	}
	return out
}

// ---- 路由 ----

func (s *Server) storageMigrateStatus(w http.ResponseWriter, r *http.Request) {
	running, _ := s.m.pg.GetRunningStorageMigration()
	last, _ := s.m.pg.GetLatestStorageMigration()
	storageMigrate.mu.Lock()
	isRunning := storageMigrate.running
	storageMigrate.mu.Unlock()
	// running 行存在但控制器不在跑(理论上 initStorageMigration 会兜)→ 以控制器为准;
	// 控制器在跑但行没了 → 异常态也不谎报。
	if running != nil && !isRunning {
		running = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": isRunning, "migration": running, "last": last,
	})
}

func (s *Server) storageMigrateCancel(w http.ResponseWriter, r *http.Request) {
	storageMigrate.mu.Lock()
	cancel := storageMigrate.cancel
	storageMigrate.mu.Unlock()
	if cancel == nil {
		writeErr(w, http.StatusConflict, "当前没有进行中的迁移")
		return
	}
	cancel()
	writeJSON(w, http.StatusOK, map[string]any{"canceled": true})
}

// storagePurgeLocal 清理本地已上桶文件(v1 仅 report-jobs/task-archives)。
// 只删「storage_objects 已登记 + 本地仍存在」的文件;逐文件尽力删,错误计数跳过。
func (s *Server) storagePurgeLocal(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Categories []string `json:"categories"`
	}
	if err := storageDecodeBody(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	allowed := map[string]bool{}
	for _, c := range storagePurgeableCategories {
		allowed[c] = true
	}
	cats := []string{}
	for _, c := range p.Categories {
		if !allowed[c] {
			writeErr(w, http.StatusBadRequest, "类别不支持本地清理: "+c)
			return
		}
		cats = append(cats, c)
	}
	if len(cats) == 0 {
		cats = append([]string{}, storagePurgeableCategories...)
	}
	bucket, err := s.m.pg.GetActiveStorageBucket()
	if err != nil || bucket == nil {
		writeErr(w, http.StatusBadRequest, "尚未启用默认存储桶")
		return
	}
	purged, skipped := 0, 0
	for _, cat := range cats {
		paths, err := s.m.pg.ListUploadedRelPaths(bucket.ID, cat)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for rel := range paths {
			local := filepathJoin(s.m.dir, filepathFromSlash(rel))
			if err := os.Remove(local); err == nil {
				purged++
			} else if !os.IsNotExist(err) {
				skipped++
			}
		}
	}
	log.Printf("[storage] 本地清理完成: 删除 %d,跳过 %d", purged, skipped)
	writeJSON(w, http.StatusOK, map[string]any{"purged": purged, "skipped": skipped})
}
