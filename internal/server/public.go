package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (a *App) handlePublicClaim(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	var req struct {
		Secret         string `json:"secret"`
		TurnstileToken string `json:"turnstileToken"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "领取请求无效")
		return
	}
	share, err := a.shareByPublicID(r, r.PathValue("publicId"))
	if err != nil || share.RevokedAt != 0 || share.ExpiresAt <= nowUnix() {
		writeError(w, http.StatusNotFound, "分享链接不存在或已失效")
		return
	}
	var cipher, nonce []byte
	if err := a.db.QueryRow(`SELECT secret_cipher,secret_nonce FROM shares WHERE id=?`, share.ID).Scan(&cipher, &nonce); err != nil {
		writeError(w, http.StatusNotFound, "分享链接不存在或已失效")
		return
	}
	plain, err := a.decryptText("share-secret:"+share.ID, cipher, nonce)
	if err != nil || !constantEqual(string(plain), req.Secret) {
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
	if settings.TurnstileEnabled {
		if err := a.verifyTurnstile(r.Context(), settings.TurnstileSecretKey, req.TurnstileToken,
			remoteIP(r, a.cfg.TrustProxy), share.PublicID); err != nil {
			if errors.Is(err, errTurnstileUnavailable) {
				writeError(w, http.StatusServiceUnavailable, "人机校验服务暂不可用，请稍后重试")
			} else {
				writeError(w, http.StatusForbidden, "人机校验失败，请重新完成验证")
			}
			return
		}
	}
	claimToken, _ := randomToken()
	claimID, _ := randomID(16)
	itemID, _ := randomID(16)
	now := nowUnix()
	a.db.Exec(`BEGIN IMMEDIATE`)
	var selected string
	err = a.db.QueryRow(`SELECT id FROM items WHERE share_id=? AND state='available' ORDER BY position LIMIT 1`, share.ID).Scan(&selected)
	if errors.Is(err, sql.ErrNoRows) {
		a.db.Exec(`ROLLBACK`)
		writeError(w, http.StatusGone, "分享内容已领取完")
		return
	}
	if err == nil {
		itemID = selected
		_, err = a.db.Exec(`UPDATE items SET state='allocated' WHERE id=? AND state='available'`, itemID)
	}
	if err == nil {
		_, err = a.db.Exec(`INSERT INTO claims(id,share_id,item_id,token_hash,state,allocated_at,lease_expires_at) VALUES(?,?,?,?,'allocated',?,?)`,
			claimID, share.ID, itemID, tokenHash(claimToken), now, now+15)
	}
	if err == nil {
		_, err = a.db.Exec(`COMMIT`)
	} else {
		a.db.Exec(`ROLLBACK`)
	}
	if err != nil {
		writeError(w, http.StatusConflict, "暂时无法领取内容，请稍后再试")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"claimId": claimID, "claimToken": claimToken,
		"shareTitle": share.Title, "defaultAlign": share.DefaultAlign,
	})
}

func (a *App) handleResumeClaim(w http.ResponseWriter, r *http.Request) {
	token := claimTokenFromRequest(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "缺少领取凭证")
		return
	}
	claim, err := a.claimByID(r.PathValue("claimId"))
	if err != nil || !constantEqual(claim.TokenHash, tokenHash(token)) {
		writeError(w, http.StatusNotFound, "领取记录不存在")
		return
	}
	if claim.State == "destroyed" || claim.ExpiresAt > 0 && claim.ExpiresAt <= nowUnix() {
		if claim.State != "destroyed" {
			_ = a.destroyClaim(claim.ID, "expired")
		}
		writeError(w, http.StatusGone, "内容已销毁")
		return
	}
	share, err := a.shareByID(r, claim.ShareID)
	if err != nil || share.RevokedAt != 0 || share.ExpiresAt <= nowUnix() {
		writeError(w, http.StatusGone, "分享已失效")
		return
	}
	content, err := a.resolveClaimContent(claim, share)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取分享内容")
		return
	}
	writeJSON(w, http.StatusOK, content)
}

func (a *App) handleStartClaim(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	token := claimTokenFromRequest(r)
	claim, err := a.claimByID(r.PathValue("claimId"))
	if err != nil || token == "" || !constantEqual(claim.TokenHash, tokenHash(token)) {
		writeError(w, http.StatusNotFound, "领取记录不存在")
		return
	}
	if claim.State == "destroyed" {
		writeError(w, http.StatusGone, "内容已销毁")
		return
	}
	share, err := a.shareByID(r, claim.ShareID)
	if err != nil || share.RevokedAt != 0 || share.ExpiresAt <= nowUnix() {
		_ = a.destroyClaim(claim.ID, "share-expired")
		writeError(w, http.StatusGone, "分享已失效")
		return
	}
	now := nowUnix()
	expires := claim.ExpiresAt
	if claim.State == "allocated" {
		expires = now + int64(share.SessionTimeout)
		if expires > share.ExpiresAt {
			expires = share.ExpiresAt
		}
		_, err = a.db.Exec(`UPDATE claims SET state='active',revealed_at=?,expires_at=?,lease_expires_at=? WHERE id=?`,
			now, expires, now+15, claim.ID)
		if err == nil {
			_, err = a.db.Exec(`UPDATE items SET state='active' WHERE id=?`, claim.ItemID)
		}
	} else {
		_, err = a.db.Exec(`UPDATE claims SET lease_expires_at=? WHERE id=?`, now+15, claim.ID)
	}
	if err != nil || expires <= now {
		writeError(w, http.StatusGone, "内容已失效")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"expiresAt": expires, "leaseExpiresAt": now + 15})
}

func (a *App) handleClaimAsset(w http.ResponseWriter, r *http.Request) {
	claim, ok := a.authorizedClaim(w, r)
	if !ok {
		return
	}
	asset, err := a.assetByID(r.PathValue("assetId"))
	if err != nil || !asset.ItemID.Valid || asset.ItemID.String != claim.ItemID {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	a.serveAsset(w, asset, r.URL.Query().Get("download") == "1")
}

type claimRecord struct {
	ID             string
	ShareID        string
	ItemID         string
	TokenHash      string
	State          string
	AllocatedAt    int64
	RevealedAt     int64
	ExpiresAt      int64
	LeaseExpiresAt int64
}

func (a *App) claimByID(id string) (claimRecord, error) {
	var c claimRecord
	err := a.db.QueryRow(`SELECT id,share_id,item_id,token_hash,state,allocated_at,revealed_at,expires_at,lease_expires_at FROM claims WHERE id=?`, id).
		Scan(&c.ID, &c.ShareID, &c.ItemID, &c.TokenHash, &c.State, &c.AllocatedAt, &c.RevealedAt, &c.ExpiresAt, &c.LeaseExpiresAt)
	return c, err
}

func (a *App) authorizedClaim(w http.ResponseWriter, r *http.Request) (claimRecord, bool) {
	token := claimTokenFromRequest(r)
	claim, err := a.claimByID(r.PathValue("claimId"))
	if err != nil || token == "" || !constantEqual(claim.TokenHash, tokenHash(token)) {
		writeError(w, http.StatusUnauthorized, "领取凭证无效")
		return claimRecord{}, false
	}
	if claim.State == "destroyed" || claim.ExpiresAt > 0 && claim.ExpiresAt <= nowUnix() {
		writeError(w, http.StatusGone, "内容已销毁")
		return claimRecord{}, false
	}
	return claim, true
}

func claimTokenFromRequest(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-Claim-Token"))
}

func (a *App) resolveClaimContent(claim claimRecord, share Share) (ClaimContent, error) {
	var cipher, nonce []byte
	if err := a.db.QueryRow(`SELECT payload_cipher,payload_nonce FROM items WHERE id=? AND state!='destroyed'`, claim.ItemID).Scan(&cipher, &nonce); err != nil {
		return ClaimContent{}, err
	}
	plain, err := a.decryptText("item:"+claim.ItemID, cipher, nonce)
	if err != nil {
		return ClaimContent{}, err
	}
	var payload ItemPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return ClaimContent{}, err
	}
	resolved := ClaimItem{Blocks: make([]ResolvedBlock, 0, len(payload.Blocks))}
	for _, b := range payload.Blocks {
		rb := ResolvedBlock{Type: b.Type, Text: b.Text, Align: b.Align, AssetID: b.AssetID}
		if rb.Align == "" {
			rb.Align = share.DefaultAlign
		}
		if b.AssetID != "" {
			asset, assetErr := a.assetByID(b.AssetID)
			if assetErr != nil || !asset.ItemID.Valid || asset.ItemID.String != claim.ItemID {
				return ClaimContent{}, errors.New("asset unavailable")
			}
			rb.AssetName = asset.OriginalName
			rb.MIMEType = asset.MIMEType
			rb.Size = asset.Size
		}
		resolved.Blocks = append(resolved.Blocks, rb)
	}
	return ClaimContent{ClaimID: claim.ID, ShareTitle: share.Title, DefaultAlign: share.DefaultAlign,
		ExpiresAt: claim.ExpiresAt, LeaseExpiresAt: claim.LeaseExpiresAt, Items: []ClaimItem{resolved}}, nil
}

func (a *App) shareByPublicID(r *http.Request, publicID string) (Share, error) {
	row := a.db.QueryRow(`SELECT s.id,s.owner_id,u.username,s.public_id,s.title,s.session_timeout,s.expires_at,s.ip_allow,s.ip_deny,s.default_align,s.created_at,s.revoked_at,
		(SELECT COUNT(*) FROM items WHERE share_id=s.id),
		(SELECT COUNT(*) FROM items WHERE share_id=s.id AND state='available'),
		(SELECT COUNT(*) FROM items WHERE share_id=s.id AND state!='available')
		FROM shares s JOIN users u ON u.id=s.owner_id WHERE s.public_id=?`, publicID)
	return a.scanShare(row)
}
