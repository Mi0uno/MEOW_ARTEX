package server

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey    = "auth.password_hash"
	// authJWTVerKey 是令牌版本号：改密时 +1，携带旧版本的 token 立即失效。
	// 升级前签发的无版本 token 与未改过密（版本 0）的实例互相兼容，平滑过渡。
	authJWTVerKey = "auth.jwt_ver"
	jwtTTL        = 7 * 24 * time.Hour
	keyChars      = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// jwtVer 读取当前令牌版本。
func jwtVer(pg *db.DB) int64 {
	v, ok, _ := pg.GetSetting(authJWTVerKey)
	if !ok || v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// loadOrCreateJWTKey reads the 32-byte signing key from keyDir/jwt.key. keyDir is
// the project base dir (next to the executable), NOT the browsable workspace root
// (dataDir) — the signing key must never be listable/downloadable via the file
// manager. Legacy installs kept it at dataDir/jwt.key; if present there and not yet
// at the new location, it is migrated (key preserved, so sessions stay valid) and
// the old file removed so it disappears from the workspace. On first run a random
// key is generated and persisted.
func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)
	// one-time migration out of the old in-workspace location.
	if legacy := filepath.Join(dataDir, jwtKeyFilename); legacy != path {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if data, rerr := os.ReadFile(legacy); rerr == nil {
				if werr := os.WriteFile(path, data, 0o600); werr == nil {
					_ = os.Remove(legacy)
					log.Printf("[auth] JWT key 已从 %s 迁移到 %s（移出可浏览工作区）", legacy, path)
				}
			}
		}
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	buf := make([]byte, 32)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(keyChars))))
		if err != nil {
			return nil, fmt.Errorf("generate jwt key: %w", err)
		}
		buf[i] = keyChars[n.Int64()]
	}
	if err := os.WriteFile(path, buf, 0600); err != nil {
		return nil, fmt.Errorf("write jwt key: %w", err)
	}
	log.Printf("[auth] 新 JWT key 已写入 %s", path)
	return buf, nil
}

// signJWT issues a 7-day HS256 token for user ARTEX. ver 省略时为 0（测试与
// 升级过渡场景），生产登录/初始化必须显式传当前版本。
func signJWT(key []byte, ver ...int64) (string, error) {
	v := int64(0)
	if len(ver) > 0 {
		v = ver[0]
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "ARTEX",
		"ver": v,
		"exp": time.Now().Add(jwtTTL).Unix(),
		"iat": time.Now().Unix(),
	}).SignedString(key)
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token whose
// 版本号与当前一致（改密吊销旧会话）。
func verifyJWT(tokenStr string, key []byte, ver int64) bool {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	if err != nil || !t.Valid {
		return false
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return true // 签名已把关；非 MapClaims 的 token 不是我们签的形态
	}
	tv, _ := claims["ver"].(float64)
	return int64(tv) == ver
}

// extractToken reads the JWT from Authorization: Bearer header,
// artex_token cookie, or ?token= query param (for SSE connections).
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("artex_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// requireAuth wraps h with JWT validation.
// /api/auth/* and /api/health are exempt.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/auth/") || p == "/api/health" {
			h.ServeHTTP(w, r)
			return
		}
		tok := extractToken(r)
		if tok == "" {
			writeErr(w, 401, "未授权")
			return
		}
		ver := int64(0)
		if s.m != nil && s.m.pg != nil {
			ver = jwtVer(s.m.pg)
		}
		if !verifyJWT(tok, s.jwtKey, ver) {
			writeErr(w, 401, "token 无效或已过期")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// GET /api/auth/status — reports whether the admin password has been initialised.
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash, _, _ := pg.GetSetting(authPassKey)
	writeJSON(w, 200, map[string]any{"initialized": hash != ""})
}

// POST /api/auth/init — sets the password for the first time; rejected if already set.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil || req.Password == "" {
		writeErr(w, 400, "密码不能为空")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "密码加密失败")
		return
	}
	// 原子占位：真正的"已初始化"判定在写入这一刻仲裁，并发的两个 init 只有
	// 一个成功——原来的先读后写存在竞态，且配合宽松 CORS 可被跨站抢注。
	claimed, err := pg.ClaimSetting(authPassKey, string(hash))
	if err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	if !claimed {
		writeErr(w, 403, "密码已设置")
		return
	}
	_ = pg.SetSetting(authJWTVerKey, "1")
	tok, err := signJWT(s.jwtKey, 1)
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token (this route is under /api/auth/* which requireAuth exempts, so the token
// is validated here) AND the current password.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if !verifyJWT(extractToken(r), s.jwtKey, jwtVer(pg)) {
		writeErr(w, 401, "未授权")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if req.NewPassword == "" {
		writeErr(w, 400, "新密码不能为空")
		return
	}
	hash, ok, _ := pg.GetSetting(authPassKey)
	if !ok || hash == "" {
		writeErr(w, 403, "密码未初始化，请先设置密码")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		writeErr(w, 401, "当前密码错误")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "密码加密失败")
		return
	}
	if err := pg.SetSetting(authPassKey, string(newHash)); err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	// 版本号 +1：所有已签发 token 立即失效，改密/疑似泄露后不再有 7 天裸奔窗口。
	next := jwtVer(pg) + 1
	_ = pg.SetSetting(authJWTVerKey, strconv.FormatInt(next, 10))
	writeJSON(w, 200, map[string]any{"ok": true, "relogin": true})
}

// --- 登录限速：按源 IP 记失败次数，达到阈值锁定窗口，成功登录即清除。---
// 只用 RemoteAddr（不信任 X-Forwarded-For，可伪造）。表有界，超限时先清理
// 过期条目，防伪造源 IP 撑爆内存。

const (
	loginFailLimit = 10
	loginLockout   = 5 * time.Minute
)

var (
	loginFailMu  sync.Mutex
	loginFailMap = map[string]*loginFailState{}
)

type loginFailState struct {
	fails int
	until time.Time
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func loginLocked(ip string) (bool, time.Duration) {
	loginFailMu.Lock()
	defer loginFailMu.Unlock()
	st, ok := loginFailMap[ip]
	if !ok || st.fails < loginFailLimit {
		return false, 0
	}
	if time.Now().After(st.until) {
		delete(loginFailMap, ip)
		return false, 0
	}
	return true, time.Until(st.until)
}

func loginRecordFail(ip string) {
	loginFailMu.Lock()
	defer loginFailMu.Unlock()
	if len(loginFailMap) > 4096 {
		now := time.Now()
		for k, v := range loginFailMap {
			if v.fails < loginFailLimit || now.After(v.until) {
				delete(loginFailMap, k)
			}
		}
	}
	st := loginFailMap[ip]
	if st == nil {
		st = &loginFailState{}
		loginFailMap[ip] = st
	}
	st.fails++
	if st.fails >= loginFailLimit {
		st.until = time.Now().Add(loginLockout)
	}
}

func loginRecordSuccess(ip string) {
	loginFailMu.Lock()
	delete(loginFailMap, ip)
	loginFailMu.Unlock()
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	ip := clientIP(r)
	if locked, wait := loginLocked(ip); locked {
		writeErr(w, 429, fmt.Sprintf("失败次数过多，请 %v 后重试", wait.Round(time.Second)))
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if req.Username != "ARTEX" {
		loginRecordFail(ip)
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	hash, ok, _ := pg.GetSetting(authPassKey)
	if !ok || hash == "" {
		writeErr(w, 403, "密码未初始化，请先设置密码")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		loginRecordFail(ip)
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	loginRecordSuccess(ip)
	tok, err := signJWT(s.jwtKey, jwtVer(pg))
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}
