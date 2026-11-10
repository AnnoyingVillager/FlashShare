package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const turnstileAction = "flashshare_claim"

var errTurnstileRejected = errors.New("turnstile verification rejected")
var errTurnstileUnavailable = errors.New("turnstile verification unavailable")

func (a *App) handleTurnstileConfig(w http.ResponseWriter, r *http.Request) {
	share, err := a.shareByPublicID(r, r.PathValue("publicId"))
	if err != nil || share.RevokedAt != 0 || share.ExpiresAt <= nowUnix() {
		writeError(w, http.StatusNotFound, "分享链接不存在或已失效")
		return
	}
	if !clientIPAllowed(r, a.cfg.TrustProxy, share.IPAllow, share.IPDeny) {
		writeError(w, http.StatusForbidden, "当前网络不允许访问此分享")
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取安全校验设置")
		return
	}
	if settings.TurnstileEnabled && (settings.TurnstileSiteKey == "" || settings.TurnstileSecretKey == "") {
		writeError(w, http.StatusServiceUnavailable, "人机校验尚未正确配置")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": settings.TurnstileEnabled,
		"siteKey": settings.TurnstileSiteKey,
		"action":  turnstileAction,
		"shareId": share.PublicID,
	})
}

func (a *App) verifyTurnstile(ctx context.Context, secret, token, remoteIP, publicID string) error {
	if secret == "" {
		return errTurnstileUnavailable
	}
	if token == "" {
		return errTurnstileRejected
	}
	if len(token) > 2048 {
		return errTurnstileRejected
	}
	form := url.Values{
		"secret":   {secret},
		"response": {token},
	}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.turnstileEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return errTurnstileUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 7 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errTurnstileUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errTurnstileUnavailable
	}
	var result struct {
		Success    bool     `json:"success"`
		Action     string   `json:"action"`
		CData      string   `json:"cdata"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return errTurnstileUnavailable
	}
	if !result.Success || result.Action != turnstileAction || result.CData != publicID {
		return errTurnstileRejected
	}
	return nil
}
