package server

import "time"

type Settings struct {
	MaxFileBytes              int64             `json:"maxFileBytes"`
	MaxTotalStorageBytes      int64             `json:"maxTotalStorageBytes"`
	MaxActiveSharesPerUser    int               `json:"maxActiveSharesPerUser"`
	MaxItemsPerShare          int               `json:"maxItemsPerShare"`
	MaxSessionTimeout         int               `json:"maxSessionTimeoutSeconds"`
	MaxLinkValidity           int               `json:"maxLinkValiditySeconds"`
	DefaultSessionTimeout     int               `json:"defaultSessionTimeoutSeconds"`
	DefaultLinkValidity       int               `json:"defaultLinkValiditySeconds"`
	MaxTextBytes              int               `json:"maxTextBytes"`
	MaxColumns                int               `json:"maxColumns"`
	AllowedExtensions         []string          `json:"allowedExtensions"`
	MIMEMap                   map[string]string `json:"mimeMap"`
	TurnstileEnabled          bool              `json:"turnstileEnabled"`
	TurnstileSiteKey          string            `json:"turnstileSiteKey"`
	TurnstileSecretKey        string            `json:"-"`
	TurnstileSecretConfigured bool              `json:"turnstileSecretConfigured"`
	TurnstileClearSecret      bool              `json:"turnstileClearSecret,omitempty"`
}

func defaultSettings() Settings {
	return Settings{
		MaxFileBytes:           25 << 20,
		MaxTotalStorageBytes:   1 << 30,
		MaxActiveSharesPerUser: 10,
		MaxItemsPerShare:       100,
		MaxSessionTimeout:      3600,
		MaxLinkValidity:        7 * 24 * 3600,
		DefaultSessionTimeout:  900,
		DefaultLinkValidity:    24 * 3600,
		MaxTextBytes:           64 << 10,
		MaxColumns:             20,
		AllowedExtensions:      []string{"png", "jpg", "jpeg", "gif", "webp", "txt", "pdf", "zip", "csv"},
		MIMEMap: map[string]string{
			"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg",
			"gif": "image/gif", "webp": "image/webp", "txt": "text/plain",
			"pdf": "application/pdf", "zip": "application/zip", "csv": "text/csv",
		},
	}
}

type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	CreatedAt int64  `json:"createdAt"`
}

type AuthContext struct {
	User      User
	TokenHash string
}

type Block struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Align   string `json:"align,omitempty"`
	AssetID string `json:"assetId,omitempty"`
}

type ItemPayload struct {
	Blocks []Block `json:"blocks"`
}

type ItemInput struct {
	Blocks []Block `json:"blocks"`
}

type Share struct {
	ID             string   `json:"id"`
	OwnerID        string   `json:"ownerId"`
	OwnerName      string   `json:"ownerName,omitempty"`
	PublicID       string   `json:"publicId"`
	Title          string   `json:"title"`
	SessionTimeout int      `json:"sessionTimeoutSeconds"`
	ExpiresAt      int64    `json:"expiresAt"`
	IPAllow        []string `json:"ipAllow"`
	IPDeny         []string `json:"ipDeny"`
	DefaultAlign   string   `json:"defaultAlign"`
	CreatedAt      int64    `json:"createdAt"`
	RevokedAt      int64    `json:"revokedAt,omitempty"`
	PublicLink     string   `json:"publicLink,omitempty"`
	ItemCount      int      `json:"itemCount"`
	AvailableCount int      `json:"availableCount"`
	ConsumedCount  int      `json:"consumedCount"`
}

type CreateShareRequest struct {
	Title          string      `json:"title"`
	SessionTimeout int         `json:"sessionTimeoutSeconds"`
	LinkValidity   int         `json:"linkValiditySeconds"`
	IPAllow        []string    `json:"ipAllow"`
	IPDeny         []string    `json:"ipDeny"`
	DefaultAlign   string      `json:"defaultAlign"`
	Items          []ItemInput `json:"items"`
}

type Asset struct {
	ID           string `json:"id"`
	OriginalName string `json:"name"`
	MIMEType     string `json:"mimeType"`
	Size         int64  `json:"size"`
	Extension    string `json:"extension"`
	CreatedAt    int64  `json:"createdAt"`
	ItemID       string `json:"itemId,omitempty"`
}

type ClaimContent struct {
	ClaimID        string      `json:"claimId"`
	ShareTitle     string      `json:"shareTitle"`
	DefaultAlign   string      `json:"defaultAlign"`
	ExpiresAt      int64       `json:"expiresAt"`
	LeaseExpiresAt int64       `json:"leaseExpiresAt"`
	Items          []ClaimItem `json:"items"`
}

type ClaimItem struct {
	Blocks []ResolvedBlock `json:"blocks"`
}

type ResolvedBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Align     string `json:"align,omitempty"`
	AssetID   string `json:"assetId,omitempty"`
	AssetName string `json:"assetName,omitempty"`
	MIMEType  string `json:"mimeType,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

type WSMessage struct {
	Type string `json:"type"`
	At   int64  `json:"at,omitempty"`
}

func nowUnix() int64 { return time.Now().Unix() }
