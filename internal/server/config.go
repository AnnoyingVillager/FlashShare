package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	DataDir       string
	ListenAddr    string
	PublicBaseURL string
	MasterKey     []byte
	CookieSecure  bool
	TrustProxy    bool
}

func LoadConfig() (Config, error) {
	dataDir := strings.TrimSpace(os.Getenv("FLASHSHARE_DATA_DIR"))
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "blobs"), 0o700); err != nil {
		return Config{}, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "tmp"), 0o700); err != nil {
		return Config{}, fmt.Errorf("create temporary directory: %w", err)
	}

	masterKey, err := loadMasterKey(dataDir)
	if err != nil {
		return Config{}, err
	}
	listen := strings.TrimSpace(os.Getenv("FLASHSHARE_LISTEN_ADDR"))
	if listen == "" {
		listen = ":8080"
	}
	publicURL := strings.TrimRight(strings.TrimSpace(os.Getenv("FLASHSHARE_PUBLIC_BASE_URL")), "/")
	cookieSecure := envBool("FLASHSHARE_COOKIE_SECURE", true)
	trustProxy := envBool("FLASHSHARE_TRUST_PROXY", true)
	return Config{
		DataDir:       dataDir,
		ListenAddr:    listen,
		PublicBaseURL: publicURL,
		MasterKey:     masterKey,
		CookieSecure:  cookieSecure,
		TrustProxy:    trustProxy,
	}, nil
}

func loadMasterKey(dataDir string) ([]byte, error) {
	value := strings.TrimSpace(os.Getenv("FLASHSHARE_MASTER_KEY"))
	if value != "" {
		key, err := decodeKey(value)
		if err != nil {
			return nil, fmt.Errorf("FLASHSHARE_MASTER_KEY: %w", err)
		}
		return key, nil
	}
	path := filepath.Join(dataDir, "master.key")
	b, err := os.ReadFile(path)
	if err == nil {
		return decodeKey(strings.TrimSpace(string(b)))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	encoded := hex.EncodeToString(key)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	return key, nil
}

func decodeKey(value string) ([]byte, error) {
	if len(value) == 64 {
		if key, err := hex.DecodeString(value); err == nil {
			return key, nil
		}
	}
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	return nil, errors.New("must be 32 bytes encoded as 64 hexadecimal characters")
}

func envBool(name string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
