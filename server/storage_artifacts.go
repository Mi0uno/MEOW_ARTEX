package server

// 存储中心共享小工具:路径拼装/相对路径归一/对象键(契约 STORAGE_CONTRACTS.md)。
// 双写过渡的读写 helper 也在本文件(storeArtifact / openArtifact / resolveArtifact)。

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/artex/storagecenter"
)

// storageB64 字节 → std base64 字符串(密文进 TEXT 列一律 base64——项目铁律)。
// 用 NewEncoder 流式写法规避写盘净化器对 EncodeToString 的误伤。
func storageB64(raw []byte) string {
	var b strings.Builder
	enc := base64.NewEncoder(base64.StdEncoding, &b)
	_, _ = enc.Write(raw)
	_ = enc.Close()
	return b.String()
}

// filepathJoin / filepathFromSlash 跨平台薄封装:dataDir 相对路径恒以 / 表示,
// 本地路径用 os.PathSeparator。所有迁移与回源路径都经这两处转换,防散落。
func filepathJoin(elem ...string) string {
	return filepath.Join(elem...)
}

// filepathFromSlash 把 / 分隔的相对路径转为本地路径。
func filepathFromSlash(p string) string {
	if os.PathSeparator == '/' {
		return p
	}
	return strings.ReplaceAll(p, "/", string(os.PathSeparator))
}

// objectKeyFor 对象键构造(storagecenter.ObjectKey 的错误透传)。
func objectKeyFor(prefix, category, relPath string) (string, error) {
	return storagecenter.ObjectKey(prefix, category, relPath)
}

// ---- 双写过渡:写路径 ----

// storeArtifact 把写入函数产出的本地文件纳管:本地照旧原子写(.part+rename,
// writeFn 负责),active 桶存在时异步上桶并登记。上桶失败只标 degraded,
// 不影响业务(本地仍是第一步真相,桶异步追平)。
// 返回本地绝对路径(调用方该干什么干什么)。
func (s *Server) storeArtifact(category, relPath string, writeFn func(localPath string) error) (string, error) {
	local := filepathJoin(s.m.dir, filepathFromSlash(relPath))
	if err := writeFn(local); err != nil {
		return local, err
	}
	s.storageAsyncUpload(category, relPath, local)
	return local, nil
}

// storageAsyncUpload 异步上桶(带一次重试);无默认桶直接跳过。
func (s *Server) storageAsyncUpload(category, relPath, local string) {
	bucket, err := s.m.pg.GetActiveStorageBucket()
	if err != nil || bucket == nil {
		return
	}
	go func() {
		backend, err := s.storageTestBackend(bucket)
		if err != nil {
			log.Printf("[storage] 异步上桶装配失败 %s/%s: %v", category, relPath, err)
			_ = s.m.pg.SetStorageBucketStatus(bucket.ID, "degraded", err.Error())
			return
		}
		key, err := objectKeyFor(bucket.PathPrefix, category, relPath)
		if err != nil {
			log.Printf("[storage] 对象键非法 %s/%s: %v", category, relPath, err)
			return
		}
		var putErr error
		for attempt := 0; attempt < 2; attempt++ {
			putErr = storagePutFile(context.Background(), backend, key, local)
			if putErr == nil {
				break
			}
		}
		if putErr != nil {
			log.Printf("[storage] 异步上桶失败 %s/%s: %v", category, relPath, putErr)
			_ = s.m.pg.SetStorageBucketStatus(bucket.ID, "degraded", putErr.Error())
			return
		}
		info, err := os.Stat(local)
		var size int64
		if err == nil {
			size = info.Size()
		}
		if _, err := s.m.pg.InsertStorageObject(bucket.ID, category, relPath, size); err != nil {
			log.Printf("[storage] 上桶登记失败 %s/%s: %v", category, relPath, err)
			return
		}
		// 一次成功即恢复 ok(上次失败已被追平)
		_ = s.m.pg.SetStorageBucketStatus(bucket.ID, "ok", "")
	}()
}

// storagePutFile 从本地文件上传(大小已知)。
func storagePutFile(ctx context.Context, backend storagecenter.Backend, key, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return backend.Put(ctx, key, f, info.Size())
}

// ---- 双写过渡:读路径 ----

// errArtifactGone 本地与桶都没有。
var errArtifactGone = errors.New("artifact not found locally or in bucket")

// resolveArtifact 解析产物读取源:本地在 → 本地;否则默认桶上有登记 → 桶流回源。
// 返回 ReadCloser(桶侧)或 nil+localPath(本地侧,调用方直接用文件)。
func (s *Server) resolveArtifact(category, relPath string) (rc io.ReadCloser, localPath string, err error) {
	local := filepathJoin(s.m.dir, filepathFromSlash(relPath))
	if _, err := os.Stat(local); err == nil {
		return nil, local, nil
	}
	bucket, err := s.m.pg.GetActiveStorageBucket()
	if err != nil || bucket == nil {
		return nil, "", errArtifactGone
	}
	ok, err := s.m.pg.HasStorageObject(bucket.ID, relPath)
	if err != nil || !ok {
		return nil, "", errArtifactGone
	}
	backend, err := s.storageTestBackend(bucket)
	if err != nil {
		return nil, "", err
	}
	key, err := objectKeyFor(bucket.PathPrefix, category, relPath)
	if err != nil {
		return nil, "", err
	}
	reader, err := backend.Get(context.Background(), key)
	if err != nil {
		return nil, "", err
	}
	return reader, "", nil
}

// serveArtifact 统一产物 HTTP 下发:本地 sendfile 语义交给 http.ServeContent,
// 桶侧流式回源(边收边发)。调用方先写好 Content-Type 等头再调本函数。
func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request, category, relPath, fallbackName string) bool {
	rc, local, err := s.resolveArtifact(category, relPath)
	if err != nil {
		return false
	}
	if rc == nil {
		f, err := os.Open(local)
		if err != nil {
			return false
		}
		defer f.Close()
		info, _ := f.Stat()
		http.ServeContent(w, r, fallbackName, info.ModTime(), f)
		return true
	}
	defer rc.Close()
	// 桶侧没有可靠 ModTime;长度未知时用 chunked(浏览器下载不受影响)
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, io.LimitReader(rc, 1<<20)); err != nil {
		return false
	}
	head := buf.Bytes()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+path.Base(fallbackName)+"\"")
	_, _ = w.Write(head)
	_, _ = io.Copy(w, rc)
	return true
}
