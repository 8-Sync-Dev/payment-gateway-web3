package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"time"
)

// OKXConfig holds the credentials for the OKX API V5.
type OKXConfig struct {
	APIKey     string
	SecretKey  string
	Passphrase string
	IsDemo     bool
}

// GenerateOKXTimestamp returns the current time in ISO 8601 UTC format with millisecond precision.
// Required by OKX REST API (e.g., 2020-12-08T09:08:57.715Z).
func GenerateOKXTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// GenerateOKXWSTimestamp returns the current Unix timestamp in seconds as a string.
// Required by OKX WebSocket API login (e.g., "1538054050").
func GenerateOKXWSTimestamp() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}

// GenerateOKXSignature creates a base64-encoded HMAC-SHA256 signature required by OKX API V5.
// The pre-hash string MUST be concatenated exactly in this order: timestamp + method + requestPath + body.
// Note: For GET requests or requests with no body, the body parameter should be an empty string ("").
func GenerateOKXSignature(timestamp, method, requestPath, body, secretKey string) string {
	// Create the pre-hash string as specified by OKX V5 docs
	preHash := timestamp + method + requestPath + body

	// Create a new HMAC by defining the hash type and the key (as byte array)
	mac := hmac.New(sha256.New, []byte(secretKey))

	// Write the pre-hash string to the HMAC
	mac.Write([]byte(preHash))

	// Encode the result to Base64
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
