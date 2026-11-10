package server

import (
	"bytes"
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

func TestClaimLifecycleAndRefresh(t *testing.T) {
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
	if expired.StatusCode != http.StatusGone {
		body, _ := io.ReadAll(expired.Body)
		t.Fatalf("destroyed claim status = %d: %s", expired.StatusCode, body)
	}
	expired.Body.Close()

	claimTwo := claimShare(t, client, server.URL, createBody.Share.PublicID, secret)
	if claimTwo.ClaimID == claimOne.ClaimID {
		t.Fatal("new session reused the prior claim")
	}
	secondContent := getClaim(t, client, server.URL, claimTwo.ClaimID, claimTwo.Token)
	if secondContent.Items[0].Blocks[0].Text != "second" {
		t.Fatalf("new session got wrong item: %+v", secondContent)
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
