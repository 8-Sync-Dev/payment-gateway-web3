package okx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"time"
)

func restTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func wsTimestamp() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}

// sign returns base64(HMAC-SHA256(timestamp+method+requestPath+body, secretKey)).
func sign(timestamp, method, requestPath, body, secretKey string) string {
	preHash := timestamp + method + requestPath + body
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(preHash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
