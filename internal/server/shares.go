package server

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
)

func (a *App) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req CreateShareRequest
	if err := decodeJSONLimit(r, &req, 16<<20); err != nil {
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
	if req.SessionTimeout == 0 {
		req.SessionTimeout = settings.DefaultSessionTimeout
	}
	if req.LinkValidity == 0 {
		req.LinkValidity = settings.DefaultLinkValidity
	}
	if req.SessionTimeout < 30 || req.SessionTimeout > settings.MaxSessionTimeout {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("会话时间必须在 30 到 %d 秒之间", settings.MaxSessionTimeout))
		return
	}
	if req.LinkValidity < 60 || req.LinkValidity > settings.MaxLinkValidity {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("链接有效期必须在 60 到 %d 秒之间", settings.MaxLinkValidity))
		return
	}
	if len(req.Items) < 1 || len(req.Items) > settings.MaxItemsPerShare {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("分享内容必须为 1 到 %d 份", settings.MaxItemsPerShare))
		return
	}
	allow, err := normalizeCIDRs(req.IPAllow)
	if err != nil {
		writeError(w, http.StatusBadRequest, "IP 白名单格式无效: "+err.Error())
		return
	}
	deny, err := normalizeCIDRs(req.IPDeny)
	if err != nil {
		writeError(w, http.StatusBadRequest, "IP 黑名单格式无效: "+err.Error())
		return
	}
	if req.DefaultAlign != "left" && req.DefaultAlign != "center" && req.DefaultAlign != "right" {
		req.DefaultAlign = "center"
	}
	if err := a.validateItemInputs(auth.User.ID, req.Items, settings); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	shareID, _ := randomID(16)
	publicID, _ := randomID(24)
	secret, _ := randomToken()
	secretCipher, secretNonce, err := a.encryptText("share-secret:"+shareID, []byte(secret))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法生成分享密钥")
		return
	}
	allowJSON, _ := json.Marshal(allow)
	denyJSON, _ := json.Marshal(deny)
	created := nowUnix()
	expires := created + int64(req.LinkValidity)
	a.db.Exec(`BEGIN IMMEDIATE`)
	var active int
	err = a.db.QueryRow(`SELECT COUNT(*) FROM shares WHERE owner_id=? AND revoked_at=0 AND expires_at>?`, auth.User.ID, created).Scan(&active)
	if err == nil && active >= settings.MaxActiveSharesPerUser {
		err = errText(fmt.Sprintf("最多同时开启 %d 条分享链接", settings.MaxActiveSharesPerUser))
	}
	if err == nil {
		_, err = a.db.Exec(`INSERT INTO shares(id,owner_id,public_id,secret_cipher,secret_nonce,title,session_timeout,expires_at,ip_allow,ip_deny,default_align,created_at,revoked_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,0)`, shareID, auth.User.ID, publicID, secretCipher, secretNonce, req.Title, req.SessionTimeout, expires, string(allowJSON), string(denyJSON), req.DefaultAlign, created)
	}
	itemIDs := make([]string, 0, len(req.Items))
	if err == nil {
		for i, item := range req.Items {
			itemID, _ := randomID(16)
			itemIDs = append(itemIDs, itemID)
			payload, _ := json.Marshal(ItemPayload{Blocks: item.Blocks})
			ciphertext, nonce, cryptoErr := a.encryptText("item:"+itemID, payload)
			if cryptoErr != nil {
				err = cryptoErr
				break
			}
			_, err = a.db.Exec(`INSERT INTO items(id,share_id,position,payload_cipher,payload_nonce,state,created_at) VALUES(?,?,?,?,?,'available',?)`,
				itemID, shareID, i, ciphertext, nonce, created)
			if err != nil {
				break
			}
			for _, block := range item.Blocks {
				if block.AssetID == "" {
					continue
				}
				result, updateErr := a.db.Exec(`UPDATE assets SET item_id=? WHERE id=? AND owner_id=? AND item_id IS NULL`, itemID, block.AssetID, auth.User.ID)
				if updateErr != nil {
					err = updateErr
					break
				}
				changed, _ := result.RowsAffected()
				if changed != 1 {
					err = errors.New("附件不存在或已被使用")
					break
				}
			}
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		_, err = a.db.Exec(`COMMIT`)
	} else {
		a.db.Exec(`ROLLBACK`)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	base := a.cfg.PublicBaseURL
	if base == "" {
		base = requestBaseURL(r)
	}
	share := Share{ID: shareID, OwnerID: auth.User.ID, PublicID: publicID, Title: req.Title,
		SessionTimeout: req.SessionTimeout, ExpiresAt: expires, IPAllow: allow, IPDeny: deny,
		DefaultAlign: req.DefaultAlign, CreatedAt: created, PublicLink: base + "/s/" + publicID + "#k=" + secret,
		ItemCount: len(itemIDs), AvailableCount: len(itemIDs)}
	writeJSON(w, http.StatusCreated, map[string]any{"share": share})
}

func (a *App) validateItemInputs(ownerID string, items []ItemInput, settings Settings) error {
	seenAssets := make(map[string]bool)
	for index, item := range items {
		if len(item.Blocks) < 1 || len(item.Blocks) > settings.MaxColumns {
			return errText(fmt.Sprintf("第 %d 份内容必须包含 1 到 %d 个块", index+1, settings.MaxColumns))
		}
		totalText := 0
		for _, block := range item.Blocks {
			switch block.Type {
			case "markdown":
				if block.Text == "" {
					return errText(fmt.Sprintf("第 %d 份内容包含空文本块", index+1))
				}
				totalText += len([]byte(block.Text))
				if block.Align != "" && block.Align != "left" && block.Align != "center" && block.Align != "right" {
					return errText("文本对齐方式无效")
				}
			case "image", "file":
				if block.AssetID == "" {
					return errText("图片或文件块缺少附件")
				}
				if seenAssets[block.AssetID] {
					return errText("同一个附件不能重复使用")
				}
				seenAssets[block.AssetID] = true
				var owner sql.NullString
				err := a.db.QueryRow(`SELECT owner_id FROM assets WHERE id=? AND item_id IS NULL`, block.AssetID).Scan(&owner)
				if err != nil || owner.String != ownerID {
					return errText("附件不存在、已使用或不属于当前用户")
				}
			default:
				return errText("内容块类型无效")
			}
		}
		if totalText > settings.MaxTextBytes {
			return errText(fmt.Sprintf("第 %d 份文本超过允许大小", index+1))
		}
	}
	return nil
}

func normalizeCIDRs(values []string) ([]string, error) {
	set := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			ip := net.ParseIP(raw)
			if ip == nil {
				return nil, fmt.Errorf("%s", raw)
			}
			if ip.To4() != nil {
				raw += "/32"
			} else {
				raw += "/128"
			}
		}
		_, block, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("%s", raw)
		}
		normalized := block.String()
		if !set[normalized] {
			set[normalized] = true
			result = append(result, normalized)
		}
	}
	sort.Strings(result)
	return result, nil
}

func (a *App) handleListShares(w http.ResponseWriter, r *http.Request) {
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	query := `SELECT s.id,s.owner_id,u.username,s.public_id,s.title,s.session_timeout,s.expires_at,s.ip_allow,s.ip_deny,s.default_align,s.created_at,s.revoked_at,
		COUNT(i.id), SUM(CASE WHEN i.state='available' THEN 1 ELSE 0 END), SUM(CASE WHEN i.state!='available' THEN 1 ELSE 0 END)
		FROM shares s JOIN users u ON u.id=s.owner_id LEFT JOIN items i ON i.share_id=s.id`
	args := []any{}
	if auth.User.Role != "admin" {
		query += ` WHERE s.owner_id=?`
		args = append(args, auth.User.ID)
	}
	query += ` GROUP BY s.id ORDER BY s.created_at DESC`
	rows, err := a.db.Query(query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取分享")
		return
	}
	defer rows.Close()
	shares := make([]Share, 0)
	for rows.Next() {
		s, err := a.scanShare(rows)
		if err == nil {
			if link, linkErr := a.shareLink(s, requestBaseURL(r)); linkErr == nil {
				s.PublicLink = link
			}
			shares = append(shares, s)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares})
}

func (a *App) handleGetShare(w http.ResponseWriter, r *http.Request) {
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	share, err := a.shareByID(r, r.PathValue("id"))
	if err != nil || (share.OwnerID != auth.User.ID && auth.User.Role != "admin") {
		writeError(w, http.StatusNotFound, "分享不存在")
		return
	}
	link, _ := a.shareLink(share, requestBaseURL(r))
	share.PublicLink = link
	rows, err := a.db.Query(`SELECT id,payload_cipher,payload_nonce,state FROM items WHERE share_id=? ORDER BY position`, share.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取分享内容")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, state string
		var cipher, nonce []byte
		if rows.Scan(&id, &cipher, &nonce, &state) != nil {
			continue
		}
		entry := map[string]any{"id": id, "state": state}
		if state != "destroyed" {
			plain, decryptErr := a.decryptText("item:"+id, cipher, nonce)
			if decryptErr == nil {
				var payload ItemPayload
				if json.Unmarshal(plain, &payload) == nil {
					entry["blocks"] = payload.Blocks
				}
			}
		}
		items = append(items, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": share, "items": items})
}

func (a *App) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	share, err := a.shareByID(r, r.PathValue("id"))
	if err != nil || (share.OwnerID != auth.User.ID && auth.User.Role != "admin") {
		writeError(w, http.StatusNotFound, "分享不存在")
		return
	}
	if err := a.destroyShareItems(share.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "无法销毁分享内容")
		return
	}
	_, _ = a.db.Exec(`UPDATE shares SET revoked_at=? WHERE id=?`, nowUnix(), share.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) shareByID(r *http.Request, id string) (Share, error) {
	row := a.db.QueryRow(`SELECT s.id,s.owner_id,u.username,s.public_id,s.title,s.session_timeout,s.expires_at,s.ip_allow,s.ip_deny,s.default_align,s.created_at,s.revoked_at,
		(SELECT COUNT(*) FROM items WHERE share_id=s.id),
		(SELECT COUNT(*) FROM items WHERE share_id=s.id AND state='available'),
		(SELECT COUNT(*) FROM items WHERE share_id=s.id AND state!='available')
		FROM shares s JOIN users u ON u.id=s.owner_id WHERE s.id=?`, id)
	return a.scanShare(row)
}

type rowScanner interface{ Scan(...any) error }

func (a *App) scanShare(scanner rowScanner) (Share, error) {
	var s Share
	var allowRaw, denyRaw string
	var revoked int64
	err := scanner.Scan(&s.ID, &s.OwnerID, &s.OwnerName, &s.PublicID, &s.Title, &s.SessionTimeout, &s.ExpiresAt, &allowRaw, &denyRaw, &s.DefaultAlign, &s.CreatedAt, &revoked, &s.ItemCount, &s.AvailableCount, &s.ConsumedCount)
	if err != nil {
		return Share{}, err
	}
	s.RevokedAt = revoked
	_ = json.Unmarshal([]byte(allowRaw), &s.IPAllow)
	_ = json.Unmarshal([]byte(denyRaw), &s.IPDeny)
	return s, nil
}

func (a *App) shareLink(share Share, fallbackBase string) (string, error) {
	var cipher, nonce []byte
	if err := a.db.QueryRow(`SELECT secret_cipher,secret_nonce FROM shares WHERE id=?`, share.ID).Scan(&cipher, &nonce); err != nil {
		return "", err
	}
	plain, err := a.decryptText("share-secret:"+share.ID, cipher, nonce)
	if err != nil {
		return "", err
	}
	base := a.cfg.PublicBaseURL
	if base == "" {
		base = strings.TrimRight(fallbackBase, "/")
	}
	return base + "/s/" + share.PublicID + "#k=" + string(plain), nil
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func constantEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (a *App) destroyShareItems(shareID string) error {
	rows, err := a.db.Query(`SELECT a.id,a.storage_key FROM assets a JOIN items i ON i.id=a.item_id WHERE i.share_id=?`, shareID)
	if err != nil {
		return err
	}
	type stored struct{ id, key string }
	var files []stored
	for rows.Next() {
		var v stored
		if rows.Scan(&v.id, &v.key) == nil {
			files = append(files, v)
		}
	}
	rows.Close()
	for _, f := range files {
		_ = removeFile(filepath.Join(a.cfg.DataDir, "blobs", f.key))
		_, _ = a.db.Exec(`DELETE FROM assets WHERE id=?`, f.id)
	}
	claims, _ := a.db.Query(`SELECT id FROM claims WHERE share_id=? AND state!='destroyed'`, shareID)
	if claims != nil {
		var ids []string
		for claims.Next() {
			var id string
			if claims.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		claims.Close()
		for _, id := range ids {
			a.hub.closeClaim(id)
		}
	}
	_, err = a.db.Exec(`UPDATE claims SET state='destroyed',destroyed_at=? WHERE share_id=? AND state!='destroyed'`, nowUnix(), shareID)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`UPDATE items SET state='destroyed',payload_cipher=zeroblob(1),payload_nonce=zeroblob(1) WHERE share_id=?`, shareID)
	return err
}

func (a *App) destroyClaim(claimID, reason string) error {
	var itemID string
	err := a.db.QueryRow(`SELECT item_id FROM claims WHERE id=? AND state!='destroyed'`, claimID).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := a.db.Query(`SELECT id,storage_key FROM assets WHERE item_id=?`, itemID)
	if err != nil {
		return err
	}
	type stored struct{ id, key string }
	var files []stored
	for rows.Next() {
		var v stored
		if rows.Scan(&v.id, &v.key) == nil {
			files = append(files, v)
		}
	}
	rows.Close()
	for _, f := range files {
		_ = removeFile(filepath.Join(a.cfg.DataDir, "blobs", f.key))
		_, _ = a.db.Exec(`DELETE FROM assets WHERE id=?`, f.id)
	}
	_, err = a.db.Exec(`UPDATE items SET state='destroyed',payload_cipher=zeroblob(1),payload_nonce=zeroblob(1) WHERE id=?`, itemID)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`UPDATE claims SET state='destroyed',destroyed_at=? WHERE id=?`, nowUnix(), claimID)
	if err == nil {
		a.hub.closeClaim(claimID)
	}
	_ = reason
	return err
}
