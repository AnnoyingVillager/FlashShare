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
	if err := decodeJSONLimit+}���$z{-���jםxt/") {
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
