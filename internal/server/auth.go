package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const sessionCookie = "flashshare_session"

func hashPassword(password string) (string, error) {
	if len(password) < 10 || len(password) > 256 {
		return "", errors.New("密码长度必须为 10 到 256 个字符")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const memory = 64 * 1024
	const iterations = 3
	const parallelism = 2
	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (a *App) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取初始化状态")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needsSetup": count == 0})
}

func (a *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !validUsername(req.Username) {
		writeError(w, http.StatusBadRequest, "用户名需为 3 到 64 个字母、数字、下划线或短横线")
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.db.Exec(`BEGIN IMMEDIATE`)
	var count int
	err = a.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	if err == nil && count > 0 {
		err = errors.New("系统已经初始化")
	}
	if err != nil {
		a.db.Exec(`ROLLBACK`)
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	id, _ := randomID(16)
	_, err = a.db.Exec(`INSERT INTO users(id,username,password_hash,role,disabled,created_at) VALUES(?,?,?,'admin',0,?)`,
		id, req.Username, hash, nowUnix())
	if err == nil {
		_, err = a.db.Exec(`COMMIT`)
	} else {
		a.db.Exec(`ROLLBACK`)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建管理员")
		return
	}
	a.issueSession(w, r, id)
	writeJSON(w, http.StatusCreated, map[string]any{"user": User{ID: id, Username: req.Username, Role: "admin", CreatedAt: nowUnix()}})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	key := remoteIP(r, a.cfg.TrustProxy)
	if !a.loginRate.allow(key) {
		writeError(w, http.StatusTooManyRequests, "登录尝试过多，请稍后再试")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var id, hash string
	var disabled int
	err := a.db.QueryRow(`SELECT id,password_hash,disabled FROM users WHERE username=?`, strings.TrimSpace(req.Username)).Scan(&id, &hash, &disabled)
	if err != nil || disabled != 0 || !verifyPassword(hash, req.Password) {
		a.loginRate.fail(key)
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	a.loginRate.reset(key)
	a.issueSession(w, r, id)
	u, err := a.userByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取用户")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (a *App) issueSession(w http.ResponseWriter, r *http.Request, userID string) {
	token, err := randomToken()
	if err != nil {
		return
	}
	expires := time.Now().Add(12 * time.Hour)
	_, _ = a.db.Exec(`INSERT INTO auth_sessions(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`,
		tokenHash(token), userID, expires.Unix(), nowUnix())
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		MaxAge: int((12 * time.Hour).Seconds()), HttpOnly: true,
		Secure: a.cfg.CookieSecure || requestIsHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, _ = a.db.Exec(`DELETE FROM auth_sessions WHERE token_hash=?`, tokenHash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.cfg.CookieSecure || requestIsHTTPS(r), SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) requireUser(r *http.Request) (AuthContext, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return AuthContext{}, errors.New("请先登录")
	}
	hash := tokenHash(c.Value)
	var userID string
	var expires int64
	err = a.db.QueryRow(`SELECT user_id,expires_at FROM auth_sessions WHERE token_hash=?`, hash).Scan(&userID, &expires)
	if err != nil || expires <= nowUnix() {
		return AuthContext{}, errors.New("登录已过期")
	}
	u, err := a.userByID(r.Context(), userID)
	if err != nil || u.Disabled {
		return AuthContext{}, errors.New("账号不可用")
	}
	return AuthContext{User: u, TokenHash: hash}, nil
}

func (a *App) requireAdmin(w http.ResponseWriter, r *http.Request) (AuthContext, bool) {
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return AuthContext{}, false
	}
	if auth.User.Role != "admin" {
		writeError(w, http.StatusForbidden, "需要管理员权限")
		return AuthContext{}, false
	}
	return auth, true
}

func (a *App) userByID(ctx context.Context, id string) (User, error) {
	return userFromRow(a.db.QueryRowContext(ctx, `SELECT id,username,role,disabled,created_at FROM users WHERE id=?`, id))
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": auth.User})
}

func (a *App) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	rows, err := a.db.Query(`SELECT id,username,role,disabled,created_at FROM users ORDER BY created_at`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取用户")
		return
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		u, err := userFromRow(rows)
		if err == nil {
			users = append(users, u)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (a *App) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !validUsername(req.Username) {
		writeError(w, http.StatusBadRequest, "用户名格式无效")
		return
	}
	if req.Role != "user" && req.Role != "admin" {
		req.Role = "user"
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := randomID(16)
	created := nowUnix()
	_, err = a.db.Exec(`INSERT INTO users(id,username,password_hash,role,disabled,created_at) VALUES(?,?,?,?,0,?)`, id, req.Username, hash, req.Role, created)
	if err != nil {
		writeError(w, http.StatusConflict, "用户名已存在")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": User{ID: id, Username: req.Username, Role: req.Role, CreatedAt: created}})
}

func (a *App) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	auth, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
		Role     *string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if id == auth.User.ID && req.Disabled != nil && *req.Disabled {
		writeError(w, http.StatusBadRequest, "不能禁用当前管理员")
		return
	}
	if req.Disabled != nil {
		v := 0
		if *req.Disabled {
			v = 1
		}
		if _, err := a.db.Exec(`UPDATE users SET disabled=? WHERE id=?`, v, id); err != nil {
			writeError(w, http.StatusInternalServerError, "无法更新用户")
			return
		}
		if v == 1 {
			_, _ = a.db.Exec(`DELETE FROM auth_sessions WHERE user_id=?`, id)
		}
	}
	if req.Role != nil {
		if *req.Role != "user" && *req.Role != "admin" {
			writeError(w, http.StatusBadRequest, "角色无效")
			return
		}
		if *req.Role == "user" {
			if id == auth.User.ID {
				writeError(w, http.StatusBadRequest, "不能降低当前账号的管理员权限")
				return
			}
			var currentRole string
			var disabled int
			if err := a.db.QueryRow(`SELECT role,disabled FROM users WHERE id=?`, id).Scan(&currentRole, &disabled); err != nil {
				writeError(w, http.StatusNotFound, "用户不存在")
				return
			}
			if currentRole == "admin" && disabled == 0 {
				var admins int
				_ = a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&admins)
				if admins <= 1 {
					writeError(w, http.StatusBadRequest, "不能移除最后一个可用管理员")
					return
				}
			}
		}
		_, _ = a.db.Exec(`UPDATE users SET role=? WHERE id=?`, *req.Role, id)
	}
	if req.Password != nil {
		hash, err := hashPassword(*req.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_, _ = a.db.Exec(`UPDATE users SET password_hash=? WHERE id=?`, hash, id)
		_, _ = a.db.Exec(`DELETE FROM auth_sessions WHERE user_id=?`, id)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	auth, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == auth.User.ID {
		writeError(w, http.StatusBadRequest, "不能删除当前管理员")
		return
	}
	var role string
	if err := a.db.QueryRow(`SELECT role FROM users WHERE id=?`, id).Scan(&role); err != nil {
		writeError(w, http.StatusNotFound, "用户不存在")
		return
	}
	if role == "admin" {
		var admins int
		_ = a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&admins)
		if admins <= 1 {
			writeError(w, http.StatusBadRequest, "不能删除最后一个管理员")
			return
		}
	}
	rows, _ := a.db.Query(`SELECT id FROM shares WHERE owner_id=?`, id)
	if rows != nil {
		var shareIDs []string
		for rows.Next() {
			var sid string
			if rows.Scan(&sid) == nil {
				shareIDs = append(shareIDs, sid)
			}
		}
		rows.Close()
		for _, sid := range shareIDs {
			_ = a.destroyShareItems(sid)
		}
	}
	if _, err := a.db.Exec(`DELETE FROM users WHERE id=?`, id); err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除用户")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type rateLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string][]time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, failures: make(map[string][]time.Time)}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	values := l.failures[key]
	kept := values[:0]
	for _, t := range values {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	l.failures[key] = kept
	return len(kept) < l.max
}

func (l *rateLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[key] = append(l.failures[key], time.Now())
}

func (l *rateLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.failures, key)
	l.mu.Unlock()
}

func validUsername(v string) bool {
	if len(v) < 3 || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func remoteIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func clientIPAllowed(r *http.Request, trustProxy bool, allow, deny []string) bool {
	ip := net.ParseIP(remoteIP(r, trustProxy))
	if ip == nil {
		return false
	}
	for _, cidr := range deny {
		if _, block, err := net.ParseCIDR(cidr); err == nil && block.Contains(ip) {
			return false
		}
	}
	if len(allow) == 0 {
		return true
	}
	for _, cidr := range allow {
		if _, block, err := net.ParseCIDR(cidr); err == nil && block.Contains(ip) {
			return true
		}
	}
	return false
}

func decodeJSON(r *http.Request, dst any) error {
	return decodeJSONLimit(r, dst, 2<<20)
}

func decodeJSONLimit(r *http.Request, dst any, limit int64) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func parseID(prefix string) string {
	id, _ := randomID(16)
	return prefix + id
}

func parseIntParam(v string, fallback int) int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
