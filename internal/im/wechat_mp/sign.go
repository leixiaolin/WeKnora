package wechatmp

import (
	"crypto/hmac"
	"crypto/sha1"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const signatureTimestampSkew = 5 * time.Minute

func computeSignature(token, timestamp, nonce string) string {
	parts := []string{token, timestamp, nonce}
	sort.Strings(parts)

	h := sha1.New()
	_, _ = h.Write([]byte(strings.Join(parts, "")))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func verifySignatureAt(token, signature, timestamp, nonce string, now time.Time) bool {
	if strings.TrimSpace(token) == "" ||
		strings.TrimSpace(signature) == "" ||
		strings.TrimSpace(timestamp) == "" ||
		strings.TrimSpace(nonce) == "" {
		return false
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	requestTime := time.Unix(ts, 0)
	if requestTime.After(now.Add(signatureTimestampSkew)) ||
		requestTime.Before(now.Add(-signatureTimestampSkew)) {
		return false
	}

	expected := computeSignature(token, timestamp, nonce)
	return hmac.Equal([]byte(expected), []byte(signature))
}
