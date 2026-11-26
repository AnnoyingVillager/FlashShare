package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"
)

func TestClaimLifecycleDeletesWholeShare(t *testing.T) {
	app, server, client := newTestApp(t)
	defer server.Close()
	defer app.Close()

	response := doJSON(t, client, http.MethodPost, server.URL+"/api/setup", map[string]any{
		"username": "admin", "password": "a-very-long-password",
	}, "")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("setup status = %d", response.StatusCode)
	}
	response.Body.Close()

	created := doJSON(t, client, http.MethodPost, server.URL+"/api/shares", map[string]any{
		"title":                 "test drop",
		"sessionTimeoutSeconds": 300,
		"linkValiditySeconds":   3600,
		"ipAllow":               []string{},
		"ipDeny":                []string{},
		"defaultAlign":          "center",
		"items": []map[string]any{
			{"blocks": []map[string]string{{"type": "markdown", "text": "**first**", "align": "center"}}},
			{"blocks": []map[string]string{{"type": "markdown", "text": "second", "align": "left"}}},
		},
	}, "")
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		t.Fatalf("create share status = %d: %s", created.StatusCode, body)
	}
	var createBody struct {
		Share Share `json:"share"`
	}
	decodeResponse(t, created, &createBody)
	parsed, err := url.Parse(createBody.Share.PublicLink)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimPrefix(parsed.Fragment, "k=")
	if secret == "" {
		t.Fatal("public link has no secret fragment")
	}

	claimOne := claimShare(t, client, server.URL, createBody.Share.PublicID, secret)
	firstContent := getClaim(t, client, server.URL, claimOne.ClaimID, claimOne.Token)
	if len(firstContent.Items) != 1 || firstContent.Items[0].Blocks[0].Text != "**first**" {
		t.Fatalf("unexpected first content: %+v", firstContent)
	}
	started := startClaim(t, client, server.URL, claimOne.ClaimID, claimOne.Token)
	if started.ExpiresAt <= nowUnix() {
		t.Fatal("claim did not receive an expiry")
	}
	// A reload request must retrieve the same claim rather than allocate another item.
	again := getClaim(t, client, server.URL, claimOne.ClaimID, claimOne.Token)
	if again.Items[0].Blocks[0].Text != "**first**" {
		t.Fatal("refresh changed the assigned content")
	}

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/public/claims/" + claimOne.ClaimID + "/ws"
	dialer := websocket.Dialer{Subprotocols: []string{"flashshare.v1", claimOne.Token}}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	conn.Close()
	time.Sleep(50 * time.Millisecond)

	_, err = app.db.Exec(`UPDATE claims SET lease_expires_at=? WHERE id=?`, nowUnix()-1, claimOne.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.cleanupExpired(); err != nil {
		t.Fatal(err)
	}
	expired := getRaw(t, client, server.URL, "/api/public/claims/"+claimOne.ClaimID, claimOne.Token)
	if expired.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(expired.Body)
		t.Fatalf("destroyed claim status = %d: %s", expired.StatusCode, body)
	}
	expired.Body.Close()

	assertShareDeleted(t, app, createBody.Share.ID)
	againClaim := doJSON(t, client, http.MethodPost,
		server.URL+"/api/public/shares/"+createBody.Share.PublicID+"/claim",
		map[string]string{"secret": secret}, "")
	againClaim.Body.Close()
	if againClaim.StatusCode != http.StatusNotFound {
		t.Fatalf("claim after session destruction status = %d", againClaim.StatusCode)
	}
}

func TestListSharesAfterCreateDoesNotDeadlock(t *testing.T) {
	app, server, client := newTestApp(t)
	defer server.Close()
	defer app.Close()

	response := doJSON(t, client, http.MethodPost, server.URL+"/api/setup", map[string]any{
		"username": "admin", "password": "list-shares-password",
	}, "")
	response.Body.Close()

	created := doJSON(t, client, http.MethodPost, server.URL+"/api/shares", map[string]any{
		"title": "visible immediately", "sessionTimeoutSeconds": 300, "linkValiditySeconds": 3600,
		"ipAllow": []string{}, "ipDeny": []string{}, "defaultAlign": "center",
		"items": []map[string]any{{"blocks": []map[string]string{{"type": "markdown", "text": "hello", "align": "center"}}}},
	}, "")
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		created.Body.Close()
		t.Fatalf("create share status = %d: %s", created.StatusCode, body)
	}
	created.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/shares", nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := client.Do(req)
	if err != nil {
		t.Fatalf("list shares blocked after create: %v", err)
	}
	defer listed.Body.Close()
	if listed.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(listed.Body)
		t.Fatalf("list shares status = %d: %s", listed.StatusCode, body)
	}
	var body struct {
		Shares []Share `json:"shares"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Shares) != 1 || body.Shares[0].PublicLink == "" {
		t.Fatalf("unexpected shares response: %+v", body.Shares)
	}
}

func TestExpiredShareIsDeletedWithoutAuditRecord(t *testing.T) {
	app, server, client := newTestApp(t)
	defer server.Close()
	defer app.Close()

	response := doJSON(t, client, http.MethodPost, server.URL+"/api/setup", map[string]any{
		"username": "admin", "password": "expiry-password",
	}, "")
	response.Body.Close()
	created := doJSON(t, client, http.MethodPost, server.URL+"/api/shares", map[string]any{
		"title": "expires", "sessionTimeoutSeconds": 300, "linkValiditySeconds": 3600,
		"ipAllow": []string{}, "ipDeny": []string{}, "defaultAlign": "center",
		"items": []map[string]any{{"blocks": []map[string]string{{"type": "markdown", "text": "secret", "align": "center"}}}},
	}, "")
	var body struct {
		Share Share `json:"share"`
	}
	decodeResponse(t, created, &body)
	if _, err := app.db.Exec(`UPDATE shares SET expires_at=? WHERE id=?`, nowUnix()-1, body.Share.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.cleanupExpired(); err != nil {
		t.Fatal(err)
	}
	assertShareDeleted(t, app, body.Share.ID)
}

func TestManualShareDeletionRemovesDatabaseRecord(t *testing.T) {
	app, server, client := newTestApp(t)
	defer server.Close()
	defer app.Close()

	response := doJSON(t, client, http.MethodPost, server.URL+"/api/setup", map[string]any{
		"username": "admin", "password": "delete-password",
	}, "")
	response.Body.Close()
	created := doJSON(t, client, http.MethodPost, server.URL+"/api/shares", map[string]any{
		"title": "delete me", "sessionTimeoutSeconds": 300, "linkValiditySeconds": 3600,
		"ipAllow": []string{}, "ipDeny": []string{}, "defaultAlign": "center",
		"items": []map[string]any{{"blocks": []map[string]string{{"type": "markdown", "text": "secret", "align": "center"}}}},
	}, "")
	var body struct {
		Share Share `json:"share"`
	}
	decodeResponse(t, created, &body)
	deleted := doJSON(t, client, http.MethodDelete, server.URL+"/api/shares/"+body.Share.ID, map[string]any{}, "")
	if deleted.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(deleted.Body)
		deleted.Body.Close()
		t.Fatalf("delete share status = %d: %s", deleted.StatusCode, payload)
	}
	deleted.Body.Close()
	assertShareDeleted(t, app, body.Share.ID)
}

func TestSubscriptionLimitKeepsRecordAndCanReactivate(t *testing.T) {
	app, server, client := newTestApp(t)
	defer server.Close()
	defer app.Close()

	response := doJSON(t, client, http.MethodPost, server.URL+"/api/setup", map[string]any{
		"username": "admin", "password": "subscription-password",
	}, "")
	response.Body.Close()
	content := "mixed-port: 7890\nproxies: []\n"
	created := doJSON(t, client, http.MethodPost, server.URL+"/api/subscriptions", map[string]any{
		"title": "clash config", "content": content, "contentType": "text/yaml",
		"base64Encoded": false, "linkValiditySeconds": 3600, "maxRequests": 1,
	}, "")
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		created.Body.Close()
		t.Fatalf("create subscription status = %d: %s", created.StatusCode, body)
	}
	var body struct {
		Subscription Subscription `json:"subscription"`
	}
	decodeResponse(t, created, &body)

	first, err := client.Get(body.Subscription.PublicLink)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK || string(got) != content {
		t.Fatalf("subscription response status=%d body=%q", first.StatusCode, got)
	}
	second, err := client.Get(body.Subscription.PublicLink)
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusNotFound {
		t.Fatalf("exhausted subscription status = %d", second.StatusCode)
	}
	var successful int64
	if err := app.db.QueryRow(`SELECT successful_requests FROM subscriptions WHERE id=?`, body.Subscription.ID).Scan(&successful); err != nil {
		t.Fatalf("exhausted subscription record missing: %v", err)
	}
	if successful != 1 {
		t.Fatalf("successful requests = %d", successful)
	}
	if err := app.cleanupExpired(); err != nil {
		t.Fatal(err)
	}

	reactivated := doJSON(t, client, http.MethodPatch, server.URL+"/api/subscriptions/"+body.Subscription.ID, map[string]any{
		"linkValiditySeconds": 7200, "maxRequests": 2,
	}, "")
	if reactivated.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(reactivated.Body)
		reactivated.Body.Close()
		t.Fatalf("reactivate status = %d: %s", reactivated.StatusCode, payload)
	}
	reactivated.Body.Close()
	again, err := client.Get(body.Subscription.PublicLink)
	if err != nil {
		t.Fatal(err)
	}
	againBody, _ := io.ReadAll(again.Body)
	again.Body.Close()
	if again.StatusCode != http.StatusOK || string(againBody) != content {
		t.Fatalf("reactivated subscription status=%d body=%q", again.StatusCode, againBody)
	}

	deleted := doJSON(t, client, http.MethodDelete, server.URL+"/api/subscriptions/"+body.Subscription.ID, map[string]any{}, "")
	if deleted.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(deleted.Body)
		deleted.Body.Close()
		t.Fatalf("delete subscription status = %d: %s", deleted.StatusCode, payload)
	}
	deleted.Body.Close()
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE id=?`, body.Subscription.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("subscription record remains after manual deletion: %d", count)
	}
}

func TestSubscriptionBase64UnlimitedAndExpiryRetainsRecord(t *testing.T) {
	app, server, client := newTestApp(t)
	defer server.Close()
	defer app.Close()

	response := doJSON(t, client, http.MethodPost, server.URL+"/api/setup", map[string]any{
		"username": "admin", "password": "subscription-base64-password",
	}, "")
	response.Body.Close()
	content := "vmess://example\n"
	created := doJSON(t, client, http.MethodPost, server.URL+"/api/subscriptions", map[string]any{
		"title": "base64 config", "content": content, "contentType": "text/plain",
		"base64Encoded": true, "linkValiditySeconds": 3600, "maxRequests": 0,
	}, "")
	var body struct {
		Subscription Subscription `json:"subscription"`
	}
	decodeResponse(t, created, &body)
	for i := 0; i < 2; i++ {
		res, err := client.Get(body.Subscription.PublicLink)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("base64 subscription status = %d", res.StatusCode)
		}
		decoded, err := base64.StdEncoding.DecodeString(string(encoded))
		if err != nil || string(decoded) != content {
			t.Fatalf("base64 subscription mismatch: %q, %v", decoded, err)
		}
	}
	var successful int64
	if err := app.db.QueryRow(`SELECT successful_requests FROM subscriptions WHERE id=?`, body.Subscription.ID).Scan(&successful); err != nil {
		t.Fatal(err)
	}
	if successful != 2 {
		t.Fatalf("successful requests = %d", successful)
	}
	if _, err := app.db.Exec(`UPDATE subscriptions SET expires_at=? WHERE id=?`, nowUnix()-1, body.Subscription.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := client.Get(body.Subscription.PublicLink)
	if err != nil {
		t.Fatal(err)
	}
	expired.Body.Close()
	if expired.StatusCode != http.StatusNotFound {
		t.Fatalf("expired subscription status = %d", expired.StatusCode)
	}
	if err := app.cleanupExpired(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE id=?`, body.Subscription.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expired subscription record was removed: %d", count)
	}
}

func TestSettingsAndCIDRValidation(t *testing.T) {
	s := defaultSettings()
	if err := validateSettings(&s); err != nil {
		t.Fatalf("default settings invalid: %v", err)
	}
	values, err := normalizeCIDRs([]string{"10.0.0.5", "2001:db8::/32", "10.0.0.5/32"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0] != "10.0.0.5/32" {
		t.Fatalf("unexpected CIDRs: %#v", values)
	}
	if _, err := normalizeCIDRs([]string{"not-an-ip"}); err == nil {
		t.Fatal("invalid CIDR was accepted")
	}
}

func TestAssetUploadAndClaimDownload(t *testing.T) {
	app, server, client := newTestApp(t)
	defer server.Close()
	defer app.Close()

	response := doJSON(t, client, http.MethodPost, server.URL+"/api/setup", map[string]any{
		"username": "admin", "password": "another-long-password",
	}, "")
	response.Body.Close()

	payload := []byte("temporary-password=correct-horse\n")
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "credentials.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(payload)
	_ = writer.Close()
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/assets", &upload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("upload status = %d: %s", response.StatusCode, body)
	}
	var uploadBody struct {
		Asset Asset `json:"asset"`
	}
	decodeResponse(t, response, &uploadBody)
	if uploadBody.Asset.Size != int64(len(payload)) {
		t.Fatalf("asset size = %d", uploadBody.Asset.Size)
	}
	var storageKey string
	if err := app.db.QueryRow(`SELECT storage_key FROM assets WHERE id=?`, uploadBody.Asset.ID).Scan(&storageKey); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(app.cfg.DataDir, "blobs", storageKey)

	created := doJSON(t, client, http.MethodPost, server.URL+"/api/shares", map[string]any{
		"title": "attachment drop", "sessionTimeoutSeconds": 300, "linkValiditySeconds": 3600,
		"ipAllow": []string{}, "ipDeny": []string{}, "defaultAlign": "center",
		"items": []map[string]any{{"blocks": []map[string]string{{
			"type": "file", "assetId": uploadBody.Asset.ID,
		}}}},
	}, "")
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		t.Fatalf("create asset share status = %d: %s", created.StatusCode, body)
	}
	var createBody struct {
		Share Share `json:"share"`
	}
	decodeResponse(t, created, &createBody)
	parsed, _ := url.Parse(createBody.Share.PublicLink)
	secret := strings.TrimPrefix(parsed.Fragment, "k=")
	claim := claimShare(t, client, server.URL, createBody.Share.PublicID, secret)
	_ = startClaim(t, client, server.URL, claim.ClaimID, claim.Token)
	assetResponse := getRaw(t, client, server.URL,
		fmt.Sprintf("/api/public/claims/%s/assets/%s", claim.ClaimID, uploadBody.Asset.ID), claim.Token)
	if assetResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(assetResponse.Body)
		t.Fatalf("asset response status = %d: %s", assetResponse.StatusCode, body)
	}
	got, _ := io.ReadAll(assetResponse.Body)
	assetResponse.Body.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("asset content mismatch: %q", got)
	}
	if err := app.destroyClaim(claim.ClaimID, "test"); err != nil {
		t.Fatal(err)
	}
	after := getRaw(t, client, server.URL,
		fmt.Sprintf("/api/public/claims/%s/assets/%s", claim.ClaimID, uploadBody.Asset.ID), claim.Token)
	if after.StatusCode != http.StatusGone && after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("destroyed asset status = %d", after.StatusCode)
	}
	after.Body.Close()
	if _, err := os.Stat(assetPath); !os.IsNotExist(err) {
		t.Fatalf("asset file still exists after share deletion: %v", err)
	}
	var assets int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM assets WHERE id=?`, uploadBody.Asset.ID).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if assets != 0 {
		t.Fatalf("asset record count = %d", assets)
	}
	assertShareDeleted(t, app, createBody.Share.ID)
}

func TestTurnstileClaimRequiresServerVerification(t *testing.T) {
	app, server, client := newTestApp(t)
	defer server.Close()
	defer app.Close()

	response := doJSON(t, client, http.MethodPost, server.URL+"/api/setup", map[string]any{
		"username": "admin", "password": "turnstile-test-password",
	}, "")
	response.Body.Close()
	created := doJSON(t, client, http.MethodPost, server.URL+"/api/shares", map[string]any{
		"title": "protected drop", "sessionTimeoutSeconds": 300, "linkValiditySeconds": 3600,
		"ipAllow": []string{}, "ipDeny": []string{}, "defaultAlign": "center",
		"items": []map[string]any{{"blocks": []map[string]string{{"type": "markdown", "text": "protected", "align": "center"}}}},
	}, "")
	var createBody struct {
		Share Share `json:"share"`
	}
	decodeResponse(t, created, &createBody)
	parsed, _ := url.Parse(createBody.Share.PublicLink)
	secret := strings.TrimPrefix(parsed.Fragment, "k=")

	verifierCalls := 0
	verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifierCalls++
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse verifier form: %v", err)
		}
		if r.FormValue("secret") != "turnstile-secret" || r.FormValue("response") != "valid-token" {
			writeJSON(w, http.StatusOK, map[string]any{"success": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "action": turnstileAction, "cdata": createBody.Share.PublicID,
		})
	}))
	defer verifier.Close()
	app.turnstileEndpoint = verifier.URL

	settings, err := app.getSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.TurnstileEnabled = true
	settings.TurnstileSiteKey = "test-site-key"
	settings.TurnstileSecretKey = "turnstile-secret"
	if err := app.putSettings(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := app.getSettings()
	if err != nil || loaded.TurnstileSecretKey != "turnstile-secret" || !loaded.TurnstileSecretConfigured {
		t.Fatalf("secret round trip failed: %+v, %v", loaded, err)
	}
	var globalValue, secretValue string
	_ = app.db.QueryRow(`SELECT value FROM settings WHERE key='global'`).Scan(&globalValue)
	_ = app.db.QueryRow(`SELECT value FROM settings WHERE key='turnstile_secret'`).Scan(&secretValue)
	if strings.Contains(globalValue, "turnstile-secret") || secretValue == "" || strings.Contains(secretValue, "turnstile-secret") {
		t.Fatal("Turnstile secret was stored in plaintext")
	}

	blocked := doJSON(t, client, http.MethodPost,
		server.URL+"/api/public/shares/"+createBody.Share.PublicID+"/claim",
		map[string]string{"secret": secret}, "")
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusForbidden || verifierCalls != 0 {
		t.Fatalf("claim without token status=%d calls=%d", blocked.StatusCode, verifierCalls)
	}
	allowed := doJSON(t, client, http.MethodPost,
		server.URL+"/api/public/shares/"+createBody.Share.PublicID+"/claim",
		map[string]string{"secret": secret, "turnstileToken": "valid-token"}, "")
	if allowed.StatusCode != http.StatusCreated || verifierCalls != 1 {
		body, _ := io.ReadAll(allowed.Body)
		t.Fatalf("verified claim status=%d calls=%d: %s", allowed.StatusCode, verifierCalls, body)
	}
	allowed.Body.Close()

	adminSettings := getRaw(t, client, server.URL, "/api/admin/settings", "")
	adminBody, _ := io.ReadAll(adminSettings.Body)
	adminSettings.Body.Close()
	if strings.Contains(string(adminBody), "turnstile-secret") {
		t.Fatal("admin settings response exposed the Turnstile secret")
	}
}

func TestEncryptedFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{7}, 32)
	app := &App{cfg: Config{MasterKey: key}}
	path := filepath.Join(dir, "blob")
	payload := []byte("temporary account: hunter2\n")
	size, digest, err := app.encryptFile(bytes.NewReader(payload), path, "asset-test")
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(payload)) {
		t.Fatalf("size = %d", size)
	}
	if err := app.verifyFile(path, "asset-test", digest); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := app.decryptFileTo(path, "asset-test", &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), payload) {
		t.Fatalf("round trip mismatch: %q", output.Bytes())
	}
}

type claimResult struct {
	ClaimID string `json:"claimId"`
	Token   string `json:"claimToken"`
}

func newTestApp(t *testing.T) (*App, *httptest.Server, *http.Client) {
	t.Helper()
	key := bytes.Repeat([]byte{9}, 32)
	cfg := Config{DataDir: t.TempDir(), MasterKey: key, CookieSecure: false, TrustProxy: false}
	for _, dir := range []string{"blobs", "tmp"} {
		if err := ensureTestDir(filepath.Join(cfg.DataDir, dir)); err != nil {
			t.Fatal(err)
		}
	}
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>test</title>")}}
	app, err := New(cfg, "test", fs.FS(static))
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	return app, httptest.NewServer(app.Handler()), client
}

func ensureTestDir(path string) error {
	return os.MkdirAll(path, 0o700)
}

func doJSON(t *testing.T, client *http.Client, method, target string, body any, token string) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(method, target, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, dst any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(dst); err != nil {
		t.Fatal(err)
	}
}

func assertShareDeleted(t *testing.T, app *App, shareID string) {
	t.Helper()
	checks := []struct {
		name  string
		query string
	}{
		{"share", `SELECT COUNT(*) FROM shares WHERE id=?`},
		{"items", `SELECT COUNT(*) FROM items WHERE share_id=?`},
		{"claims", `SELECT COUNT(*) FROM claims WHERE share_id=?`},
	}
	for _, check := range checks {
		var count int
		if err := app.db.QueryRow(check.query, shareID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("%s records remain after deletion: %d", check.name, count)
		}
	}
}

func claimShare(t *testing.T, client *http.Client, base, publicID, secret string) claimResult {
	t.Helper()
	response := doJSON(t, client, http.MethodPost, base+"/api/public/shares/"+publicID+"/claim", map[string]string{"secret": secret}, "")
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("claim status = %d: %s", response.StatusCode, body)
	}
	var result claimResult
	decodeResponse(t, response, &result)
	return result
}

func getClaim(t *testing.T, client *http.Client, base, claimID, token string) ClaimContent {
	t.Helper()
	response := getRaw(t, client, base, "/api/public/claims/"+claimID, token)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("claim content status = %d: %s", response.StatusCode, body)
	}
	var result ClaimContent
	decodeResponse(t, response, &result)
	return result
}

func getRaw(t *testing.T, client *http.Client, base, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func startClaim(t *testing.T, client *http.Client, base, claimID, token string) struct {
	ExpiresAt int64 `json:"expiresAt"`
} {
	t.Helper()
	response := doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/public/claims/%s/start", base, claimID), map[string]any{}, token)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("start claim status = %d: %s", response.StatusCode, body)
	}
	var result struct {
		ExpiresAt int64 `json:"expiresAt"`
	}
	decodeResponse(t, response, &result)
	return result
}
