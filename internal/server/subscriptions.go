package server

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type subscriptionRecord struct {
	Subscription
	TokenHash     string
	TokenCipher   []byte
	TokenNonce    []byte
	ContentCipher []byte
	ContentNonce  []byte
}

func (a *App) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req CreateSubscriptionRequest
	if err := decodeJSONLimit(r, &req, 4<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取设置")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" || len([]rune(req.Title)) > 100 {
		writeError(w, http.StatusBadRequest, "标题必须为 1 到 100 个字符")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "订阅内容不能为空")
		return
	}
	if len([]byte(req.Content)) > settings.MaxTextBytes {
		writeError(w, http.StatusBadRequest, "订阅内容超过管理员配置的文本上限")
		return
	}
	switch req.ContentType {
	case "text/plain", "text/yaml", "application/octet-stream":
	default:
		writeError(w, http.StatusBadRequest, "订阅内容类型无效")
		return
	}
	if req.MaxRequests < 0 || req.MaxRequests > 1_000_000_000 {
		writeError(w, http.StatusBadRequest, "请求次数必须为 0（无限）或 1 到 1000000000")
		return
	}
	created := nowUnix()
	expiresAt := int64(0)
	if req.LinkValidity != 0 {
		if req.LinkValidity < 60 || req.LinkValidity > int64(settings.MaxLinkValidity) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("订阅有效期必须在 1 分钟到 %d 秒之间，或设置为永不过期", settings.MaxLinkValidity))
			return
		}
		expiresAt = created + req.LinkValidity
	}

	id, _ := randomID(16)
	publicID, _ := randomID(24)
	token, _ := randomToken()
	tokenCipher, tokenNonce, err := a.encryptText("subscription-token:"+id, []byte(token))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法生成订阅密钥")
		return
	}
	contentCipher, contentNonce, err := a.encryptText("subscription-content:"+id, []byte(req.Content))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法加密订阅内容")
		return
	}

	tx, err := a.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建订阅")
		return
	}
	defer tx.Rollback()
	var active int
	err = tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM shares WHERE owner_id=? AND revoked_at=0 AND expires_at>?) +
		(SELECT COUNT(*) FROM subscriptions WHERE owner_id=? AND (expires_at=0 OR expires_at>?) AND (max_requests=0 OR successful_requests<max_requests))`,
		auth.User.ID, created, auth.User.ID, created).Scan(&active)
	if err == nil && active >= settings.MaxActiveSharesPerUser {
		err = errText(fmt.Sprintf("最多同时开启 %d 条分享或订阅链接", settings.MaxActiveSharesPerUser))
	}
	if err == nil {
		_, err = tx.Exec(`INSERT INTO subscriptions(
			id,owner_id,public_id,token_hash,token_cipher,token_nonce,title,content_type,base64_encoded,
			content_cipher,content_nonce,expires_at,max_requests,successful_requests,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
			id, auth.User.ID, publicID, tokenHash(token), tokenCipher, tokenNonce, req.Title,
			req.ContentType, boolInt(req.Base64Encoded), contentCipher, contentNonce,
			expiresAt, req.MaxRequests, created)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存订阅")
		return
	}

	base := a.cfg.PublicBaseURL
	if base == "" {
		base = requestBaseURL(r)
	}
	subscription := Subscription{
		ID: id, OwnerID: auth.User.ID, PublicID: publicID, Title: req.Title,
		ContentType: req.ContentType, Base64Encoded: req.Base64Encoded,
		ExpiresAt: expiresAt, MaxRequests: req.MaxRequests, CreatedAt: created,
		PublicLink: strings.TrimRight(base, "/") + "/sub/" + publicID + "/" + token,
	}
	writeJSON(w, http.StatusCreated, map[string]any{"subscription": subscription})
}

func (a *App) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	query := `SELECT s.id,s.owner_id,u.username,s.public_id,s.title,s.content_type,s.base64_encoded,
		s.expires_at,s.max_requests,s.successful_requests,s.created_at,s.token_cipher,s.token_nonce
		FROM subscriptions s JOIN users u ON u.id=s.owner_id`
	args := []any{}
	if auth.User.Role != "admin" {
		query += ` WHERE s.owner_id=?`
		args = append(args, auth.User.ID)
	}
	query += ` ORDER BY s.created_at DESC`
	rows, err := a.db.Query(query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取订阅")
		return
	}
	defer rows.Close()
	subscriptions := make([]Subscription, 0)
	base := a.cfg.PublicBaseURL
	if base == "" {
		base = requestBaseURL(r)
	}
	for rows.Next() {
		subscription, tokenCipher, tokenNonce, err := scanSubscriptionRow(rows)
		if err != nil {
			continue
		}
		plain, decryptErr := a.decryptText("subscription-token:"+subscription.ID, tokenCipher, tokenNonce)
		if decryptErr == nil {
			subscription.PublicLink = strings.TrimRight(base, "/") + "/sub/" + subscription.PublicID + "/" + string(plain)
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取订阅")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subscriptions})
}

func (a *App) handleDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var ownerID string
	err = a.db.QueryRow(`SELECT owner_id FROM subscriptions WHERE id=?`, r.PathValue("id")).Scan(&ownerID)
	if err != nil || ownerID != auth.User.ID && auth.User.Role != "admin" {
		writeError(w, http.StatusNotFound, "订阅不存在")
		return
	}
	if err := a.deleteSubscription(r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除订阅")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleReactivateSubscription(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req struct {
		LinkValidity int64 `json:"linkValiditySeconds"`
		MaxRequests  int64 `json:"maxRequests"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取设置")
		return
	}
	expiresAt, err := subscriptionExpiry(req.LinkValidity, settings.MaxLinkValidity)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.MaxRequests < 0 || req.MaxRequests > 1_000_000_000 {
		writeError(w, http.StatusBadRequest, "请求次数必须为 0（无限）或 1 到 1000000000")
		return
	}
	var ownerID string
	err = a.db.QueryRow(`SELECT owner_id FROM subscriptions WHERE id=?`, r.PathValue("id")).Scan(&ownerID)
	if err != nil || ownerID != auth.User.ID && auth.User.Role != "admin" {
		writeError(w, http.StatusNotFound, "订阅不存在")
		return
	}
	now := nowUnix()
	tx, err := a.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法重新启用订阅")
		return
	}
	defer tx.Rollback()
	var active int
	err = tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM shares WHERE owner_id=? AND revoked_at=0 AND expires_at>?) +
		(SELECT COUNT(*) FROM subscriptions WHERE owner_id=? AND id!=? AND (expires_at=0 OR expires_at>?) AND (max_requests=0 OR successful_requests<max_requests))`,
		ownerID, now, ownerID, r.PathValue("id"), now).Scan(&active)
	if err == nil && active >= settings.MaxActiveSharesPerUser {
		err = errText(fmt.Sprintf("最多同时开启 %d 条分享或订阅链接", settings.MaxActiveSharesPerUser))
	}
	if err == nil {
		_, err = tx.Exec(`UPDATE subscriptions SET expires_at=?,max_requests=?,successful_requests=0 WHERE id=?`,
			expiresAt, req.MaxRequests, r.PathValue("id"))
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法重新启用订阅")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleSubscriptionContent(w http.ResponseWriter, r *http.Request) {
	subscription, err := a.subscriptionByPublicID(r.PathValue("publicId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "订阅不存在")
		return
	}
	now := nowUnix()
	if subscription.ExpiresAt > 0 && subscription.ExpiresAt <= now {
		writeError(w, http.StatusNotFound, "订阅不存在或已过期")
		return
	}
	if !constantEqual(subscription.TokenHash, tokenHash(r.PathValue("token"))) {
		writeError(w, http.StatusNotFound, "订阅不存在")
		return
	}
	plain, err := a.decryptText("subscription-content:"+subscription.ID, subscription.ContentCipher, subscription.ContentNonce)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取订阅内容")
		return
	}
	result, err := a.db.Exec(`UPDATE subscriptions SET successful_requests=successful_requests+1
		WHERE id=? AND (expires_at=0 OR expires_at>?) AND (max_requests=0 OR successful_requests<max_requests)`,
		subscription.ID, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法记录订阅请求")
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		writeError(w, http.StatusNotFound, "订阅请求次数已用完")
		return
	}

	payload := plain
	contentType := subscription.ContentType
	if subscription.Base64Encoded {
		payload = []byte(base64.StdEncoding.EncodeToString(plain))
		contentType = "text/plain; charset=utf-8"
	} else if strings.HasPrefix(contentType, "text/") {
		contentType += "; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) subscriptionByPublicID(publicID string) (subscriptionRecord, error) {
	row := a.db.QueryRow(`SELECT s.id,s.owner_id,u.username,s.public_id,s.title,s.content_type,s.base64_encoded,
		s.expires_at,s.max_requests,s.successful_requests,s.created_at,s.token_cipher,s.token_nonce,
		s.token_hash,s.content_cipher,s.content_nonce
		FROM subscriptions s JOIN users u ON u.id=s.owner_id WHERE s.public_id=?`, publicID)
	return scanSubscriptionRecord(row)
}

func (a *App) deleteSubscription(id string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE subscriptions SET
		token_cipher=zeroblob(1),token_nonce=zeroblob(1),
		content_cipher=zeroblob(1),content_nonce=zeroblob(1)
		WHERE id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM subscriptions WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func subscriptionExpiry(validity int64, maxValidity int) (int64, error) {
	if validity == 0 {
		return 0, nil
	}
	if validity < 60 || validity > int64(maxValidity) {
		return 0, errText(fmt.Sprintf("订阅有效期必须在 1 分钟到 %d 秒之间，或设置为永不过期", maxValidity))
	}
	return nowUnix() + validity, nil
}

type subscriptionScanner interface {
	Scan(...any) error
}

func scanSubscriptionRow(scanner subscriptionScanner) (Subscription, []byte, []byte, error) {
	var s Subscription
	var base64Encoded int
	var tokenCipher, tokenNonce []byte
	err := scanner.Scan(&s.ID, &s.OwnerID, &s.OwnerName, &s.PublicID, &s.Title, &s.ContentType,
		&base64Encoded, &s.ExpiresAt, &s.MaxRequests, &s.SuccessfulRequests, &s.CreatedAt,
		&tokenCipher, &tokenNonce)
	s.Base64Encoded = base64Encoded != 0
	return s, tokenCipher, tokenNonce, err
}

func scanSubscriptionRecord(scanner subscriptionScanner) (subscriptionRecord, error) {
	var s subscriptionRecord
	var base64Encoded int
	err := scanner.Scan(&s.ID, &s.OwnerID, &s.OwnerName, &s.PublicID, &s.Title, &s.ContentType,
		&base64Encoded, &s.ExpiresAt, &s.MaxRequests, &s.SuccessfulRequests, &s.CreatedAt,
		&s.TokenCipher, &s.TokenNonce, &s.TokenHash, &s.ContentCipher, &s.ContentNonce)
	s.Base64Encoded = base64Encoded != 0
	return s, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
