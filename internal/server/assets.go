package server

import (
	"bufio"
	"database/sql"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

func (a *App) handleUploadAsset(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取上传设置")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, settings.MaxFileBytes+(2<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "上传文件超过允许大小")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "缺少上传文件")
		return
	}
	defer file.Close()
	name := sanitizeFilename(header.Filename)
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if ext == "" || !containsString(settings.AllowedExtensions, ext) {
		writeError(w, http.StatusBadRequest, "该文件类型未获允许")
		return
	}
	declared := strings.ToLower(strings.TrimSpace(header.Header.Get("Content-Type")))
	if declared == "" {
		declared = "application/octet-stream"
	}
	reader := bufio.NewReader(file)
	head, _ := reader.Peek(512)
	detected := http.DetectContentType(head)
	if extLooksImage(ext) && !strings.HasPrefix(detected, "image/") {
		writeError(w, http.StatusBadRequest, "文件内容与图片扩展名不符")
		return
	}
	if strings.HasPrefix(declared, "image/") && !strings.HasPrefix(detected, "image/") {
		writeError(w, http.StatusBadRequest, "文件内容与 MIME 类型不符")
		return
	}

	id, _ := randomID(16)
	storageKey, _ := randomID(24)
	path := filepath.Join(a.cfg.DataDir, "blobs", storageKey)
	limited := &io.LimitedReader{R: reader, N: settings.MaxFileBytes + 1}
	size, digest, err := a.encryptFile(limited, path, id)
	if err != nil {
		_ = removeFile(path)
		writeError(w, http.StatusInternalServerError, "无法保存上传文件")
		return
	}
	if size > settings.MaxFileBytes {
		_ = removeFile(path)
		writeError(w, http.StatusRequestEntityTooLarge, "上传文件超过允许大小")
		return
	}
	servedMIME := settings.MIMEMap[ext]
	if servedMIME == "" {
		servedMIME = "application/octet-stream"
	}

	a.db.Exec(`BEGIN IMMEDIATE`)
	var total sql.NullInt64
	err = a.db.QueryRow(`SELECT SUM(size) FROM assets`).Scan(&total)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.db.Exec(`ROLLBACK`)
		_ = removeFile(path)
		writeError(w, http.StatusInternalServerError, "无法检查存储配额")
		return
	}
	if total.Int64+size > settings.MaxTotalStorageBytes {
		a.db.Exec(`ROLLBACK`)
		_ = removeFile(path)
		writeError(w, http.StatusInsufficientStorage, "服务器存储配额不足")
		return
	}
	created := nowUnix()
	_, err = a.db.Exec(`INSERT INTO assets(id,owner_id,item_id,original_name,declared_mime,detected_mime,allowed_mime,extension,size,storage_key,mac,created_at)
		VALUES(?,?,NULL,?,?,?,?,?,?,?,?,?)`, id, auth.User.ID, name, declared, detected, servedMIME, ext, size, storageKey, digest, created)
	if err == nil {
		_, err = a.db.Exec(`COMMIT`)
	} else {
		a.db.Exec(`ROLLBACK`)
	}
	if err != nil {
		_ = removeFile(path)
		writeError(w, http.StatusInternalServerError, "无法保存文件记录")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"asset": Asset{
		ID: id, OriginalName: name, MIMEType: servedMIME, Size: size, Extension: ext, CreatedAt: created,
	}})
}

func (a *App) handleOwnerAssetPreview(w http.ResponseWriter, r *http.Request) {
	auth, err := a.requireUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	asset, err := a.assetByID(r.PathValue("id"))
	if err != nil || asset.OwnerID != auth.User.ID {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	a.serveAsset(w, asset, r.URL.Query().Get("download") == "1")
}

type assetRecord struct {
	Asset
	OwnerID      string
	DeclaredMIME string
	DetectedMIME string
	StorageKey   string
	MAC          string
	ItemID       sql.NullString
}

func (a *App) assetByID(id string) (assetRecord, error) {
	var v assetRecord
	err := a.db.QueryRow(`SELECT id,owner_id,item_id,original_name,declared_mime,detected_mime,allowed_mime,extension,size,storage_key,mac,created_at
		FROM assets WHERE id=?`, id).Scan(&v.ID, &v.OwnerID, &v.ItemID, &v.OriginalName, &v.DeclaredMIME, &v.DetectedMIME, &v.MIMEType, &v.Extension, &v.Size, &v.StorageKey, &v.MAC, &v.CreatedAt)
	return v, err
}

func (a *App) serveAsset(w http.ResponseWriter, asset assetRecord, download bool) {
	path := filepath.Join(a.cfg.DataDir, "blobs", asset.StorageKey)
	if err := a.verifyFile(path, asset.ID, asset.MAC); err != nil {
		writeError(w, http.StatusInternalServerError, "文件完整性校验失败")
		return
	}
	contentType := asset.MIMEType
	inline := isSafeImageMIME(contentType) && !download
	if !inline {
		contentType = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(asset.Size, 10))
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": asset.OriginalName}))
	if err := a.decryptFileTo(path, asset.ID, w); err != nil {
		return
	}
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	runes := []rune(name)
	if len(runes) > 180 {
		name = string(runes[len(runes)-180:])
	}
	if strings.TrimSpace(name) == "" || name == "." {
		return "file"
	}
	return name
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func extLooksImage(ext string) bool {
	return ext == "png" || ext == "jpg" || ext == "jpeg" || ext == "gif" || ext == "webp"
}

func isSafeImageMIME(v string) bool {
	switch strings.ToLower(strings.Split(v, ";")[0]) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
