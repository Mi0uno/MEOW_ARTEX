package storagecenter

// WebDAV 最小客户端(PUT/GET/PROPFIND/DELETE/MKCOL,纯 net/http,零依赖)。
// 覆盖坚果云/Alist/群晖/Nextcloud 等常见网关;集合自动创建(409 时逐级 MKCOL)。

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type webdavBackend struct {
	cfg    Config
	client *http.Client
}

func newWebDAVBackend(cfg Config, client *http.Client) (Backend, error) {
	if _, err := NormalizeEndpoint(strings.TrimRight(cfg.Endpoint, "/")); err != nil {
		return nil, err
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("webdav: 远端目录(桶名)不能为空")
	}
	return &webdavBackend{cfg: cfg, client: client}, nil
}

// objectURL 对象地址:endpoint/bucket/key(key 按段转义,保留 /)。
func (w *webdavBackend) objectURL(key string) string {
	seg := make([]string, 0, 3)
	for _, p := range []string{w.cfg.Endpoint, w.cfg.Bucket, key} {
		for _, s := range strings.Split(p, "/") {
			if s != "" {
				seg = append(seg, url.PathEscape(s))
			}
		}
	}
	return strings.Join(seg, "/")
}

func (w *webdavBackend) auth(req *http.Request) {
	if w.cfg.AccessKey != "" || w.cfg.Secret != "" {
		req.SetBasicAuth(w.cfg.AccessKey, w.cfg.Secret)
	}
}

func (w *webdavBackend) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	if err := w.ensureDirs(ctx, parentDirs(key)); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, w.objectURL(key), r)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	w.auth(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webdav put %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("webdav put %s: HTTP %d %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (w *webdavBackend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.objectURL(key), nil)
	if err != nil {
		return nil, err
	}
	w.auth(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webdav get %s: %w", key, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, fmt.Errorf("webdav get %s: HTTP %d %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

func (w *webdavBackend) Stat(ctx context.Context, key string) (int64, bool, error) {
	body, err := w.propfind(ctx, w.objectURL(key), "0")
	if err != nil {
		if nfe, ok := err.(*statusErr); ok && nfe.Status == http.StatusNotFound {
			return 0, false, nil
		}
		return 0, false, err
	}
	var resp struct {
		Responses []struct {
			Propstats []struct {
				Prop struct {
					GetContentLength int64 `xml:"getcontentlength"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &resp); err != nil {
		return 0, false, fmt.Errorf("webdav stat 解析 %s: %w", key, err)
	}
	for _, r := range resp.Responses {
		for _, ps := range r.Propstats {
			return ps.Prop.GetContentLength, true, nil
		}
	}
	return 0, false, nil
}

func (w *webdavBackend) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, w.objectURL(key), nil)
	if err != nil {
		return err
	}
	w.auth(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webdav delete %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode/100 != 2 {
		return fmt.Errorf("webdav delete %s: HTTP %d", key, resp.StatusCode)
	}
	return nil
}

func (w *webdavBackend) Test(ctx context.Context) error {
	_, err := w.propfind(ctx, w.objectURL(""), "0")
	if err != nil {
		return fmt.Errorf("webdav 探测: %w", err)
	}
	return nil
}

// ---- 内部 ----

type statusErr struct {
	Status int
	Body   string
}

func (e *statusErr) Error() string {
	return fmt.Sprintf("HTTP %d %s", e.Status, e.Body)
}

func (w *webdavBackend) propfind(ctx context.Context, rawURL, depth string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", rawURL,
		bytes.NewReader([]byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/></d:prop></d:propfind>`)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", "application/xml")
	w.auth(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, &statusErr{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return body, nil
}

// mkcol 逐级建集合;405 = 已存在(视为成功)。
func (w *webdavBackend) mkcol(ctx context.Context, dirKey string) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", w.objectURL(dirKey), nil)
	if err != nil {
		return err
	}
	w.auth(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webdav mkcol %s: %w", dirKey, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode/100 == 2 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return &statusErr{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
}

// ensureDirs 按 key 的父目录链逐级建集合(webdav 没有自动建目录)。
func (w *webdavBackend) ensureDirs(ctx context.Context, dirs []string) error {
	for _, d := range dirs {
		if err := w.mkcol(ctx, d); err != nil {
			// 已存在以外的失败才算错;并发迁移下 405 竞态也放行
			if se, ok := err.(*statusErr); ok && se.Status == http.StatusMethodNotAllowed {
				continue
			}
			return err
		}
	}
	return nil
}

// parentDirs key 的全部祖先集合键(不含文件本身,不含根)。
func parentDirs(key string) []string {
	parts := strings.Split(key, "/")
	out := []string{}
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}
