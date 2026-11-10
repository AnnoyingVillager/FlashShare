package server

import (
	"mime"
	"net/http"
	"sort"
	"strings"
)

func (a *App) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	s, err := a.getSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取设置")
		return
	}
	s.TurnstileSecretKey = ""
	s.TurnstileClearSecret = false
	writeJSON(w, http.StatusOK, map[string]any{"settings": s})
}

func (a *App) handleUserConfig(w http.ResponseWriter, r *http.Request) {
	if _, err := a.requireUser(r); err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	s, err := a.getSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取配置")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": map[string]any{
		"maxFileBytes":                 s.MaxFileBytes,
		"maxItemsPerShare":             s.MaxItemsPerShare,
		"maxSessionTimeoutSeconds":     s.MaxSessionTimeout,
		"maxLinkValiditySeconds":       s.MaxLinkValidity,
		"defaultSessionTimeoutSeconds": s.DefaultSessionTimeout,
		"defaultLinkValiditySeconds":   s.DefaultLinkValidity,
		"maxTextBytes":                 s.MaxTextBytes,
		"maxColumns":                   s.MaxColumns,
		"allowedExtensions":            s.AllowedExtensions,
	}})
}

func (a *App) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var req Settings
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	current, err := a.getSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取当前设置")
		return
	}
	req.TurnstileSiteKey = strings.TrimSpace(req.TurnstileSiteKey)
	req.TurnstileSecretKey = strings.TrimSpace(req.TurnstileSecretKey)
	if req.TurnstileSecretKey == "" && !req.TurnstileClearSecret {
		req.TurnstileSecretKey = current.TurnstileSecretKey
	}
	if req.TurnstileClearSecret && req.TurnstileSecretKey != "" {
		writeError(w, http.StatusBadRequest, "不能同时设置并清除 Turnstile 私密密钥")
		return
	}
	if err := validateSettings(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.putSettings(req); err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存设置")
		return
	}
	req.TurnstileSecretConfigured = req.TurnstileSecretKey != "" && !req.TurnstileClearSecret
	req.TurnstileSecretKey = ""
	req.TurnstileClearSecret = false
	writeJSON(w, http.StatusOK, map[string]any{"settings": req})
}

func validateSettings(s *Settings) error {
	if s.MaxFileBytes < 1<<20 || s.MaxFileBytes > 1<<30 {
		return errText("单文件大小必须在 1 MiB 到 1 GiB 之间")
	}
	if s.MaxTotalStorageBytes < 10<<20 || s.MaxTotalStorageBytes < s.MaxFileBytes {
		return errText("总存储配额必须至少为 10 MiB 且不小于单文件上限")
	}
	if s.MaxActiveSharesPerUser < 1 || s.MaxActiveSharesPerUser > 100 {
		return errText("每用户活跃分享数必须在 1 到 100 之间")
	}
	if s.MaxItemsPerShare < 1 || s.MaxItemsPerShare > 1000 {
		return errText("每分享内容数必须在 1 到 1000 之间")
	}
	if s.MaxSessionTimeout < 30 || s.MaxSessionTimeout > 30*24*3600 {
		return errText("最长会话时间必须在 30 秒到 30 天之间")
	}
	if s.MaxLinkValidity < 60 || s.MaxLinkValidity > 365*24*3600 {
		return errText("最长链接有效期必须在 1 分钟到 365 天之间")
	}
	if s.DefaultSessionTimeout < 30 || s.DefaultSessionTimeout > s.MaxSessionTimeout {
		return errText("默认会话时间必须小于等于上限")
	}
	if s.DefaultLinkValidity < 60 || s.DefaultLinkValidity > s.MaxLinkValidity {
		return errText("默认链接有效期必须小于等于上限")
	}
	if s.MaxTextBytes < 1024 || s.MaxTextBytes > 2<<20 {
		return errText("单份文本上限必须在 1 KiB 到 2 MiB 之间")
	}
	if s.MaxColumns < 1 || s.MaxColumns > 100 {
		return errText("列数必须在 1 到 100 之间")
	}
	seen := make(map[string]bool)
	clean := make([]string, 0, len(s.AllowedExtensions))
	for _, ext := range s.AllowedExtensions {
		ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
		if ext == "" || len(ext) > 16 || !validExtension(ext) {
			return errText("文件扩展名格式无效: " + ext)
		}
		if !seen[ext] {
			seen[ext] = true
			clean = append(clean, ext)
		}
	}
	if len(clean) == 0 {
		return errText("至少允许一种文件扩展名")
	}
	sort.Strings(clean)
	s.AllowedExtensions = clean
	if s.MIMEMap == nil {
		s.MIMEMap = make(map[string]string)
	}
	for _, ext := range clean {
		m := strings.TrimSpace(s.MIMEMap[ext])
		if m == "" {
			m = mime.TypeByExtension("." + ext)
		}
		if m == "" {
			m = "application/octet-stream"
		}
		if _, _, err := mime.ParseMediaType(m); err != nil {
			return errText("MIME 类型格式无效: " + m)
		}
		s.MIMEMap[ext] = m
	}
	s.TurnstileSiteKey = strings.TrimSpace(s.TurnstileSiteKey)
	s.TurnstileSecretKey = strings.TrimSpace(s.TurnstileSecretKey)
	if len(s.TurnstileSiteKey) > 256 {
		return errText("Turnstile 站点密钥过长")
	}
	if len(s.TurnstileSecretKey) > 512 {
		return errText("Turnstile 私密密钥过长")
	}
	if s.TurnstileEnabled && (s.TurnstileSiteKey == "" || s.TurnstileSecretKey == "") {
		return errText("启用 Turnstile 时必须配置站点密钥和私密密钥")
	}
	return nil
}

func validExtension(v string) bool {
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

type textError string

func (e textError) Error() string { return string(e) }
func errText(v string) error      { return textError(v) }
