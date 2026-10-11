// Package hmacauth implements the request signing scheme in
// contracts/HMAC.md. Core signs job requests (Judge verifies) and Judge
// signs result callbacks (Core verifies), each with its own keyring.
package hmacauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderKeyID     = "X-Verdale-Key-Id"
	HeaderTimestamp = "X-Verdale-Timestamp"
	HeaderSignature = "X-Verdale-Signature"

	signatureVersion = "v1"
)

var (
	ErrMissingSignature = errors.New("missing or malformed signature headers")
	ErrUnknownKeyID     = errors.New("unknown key id")
	ErrStaleTimestamp   = errors.New("timestamp outside the accepted window")
	ErrInvalidSignature = errors.New("signature does not match")

	keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// StringToSign builds the canonical string from HMAC.md.
func StringToSign(timestamp int64, method, path string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	return strings.Join([]string{
		signatureVersion,
		strconv.FormatInt(timestamp, 10),
		strings.ToUpper(method),
		path,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
}

// Sign returns the X-Verdale-Signature header value.
func Sign(secret []byte, timestamp int64, method, path string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(StringToSign(timestamp, method, path, body)))
	return signatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
}

// Signer signs outgoing requests with one active key.
type Signer struct {
	KeyID  string
	Secret []byte
	Now    func() time.Time
}

// SignRequest sets the three signature headers. path must be exactly what
// is sent on the wire (path plus query string).
func (s Signer) SignRequest(request *http.Request, path string, body []byte) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	timestamp := now().Unix()
	request.Header.Set(HeaderKeyID, s.KeyID)
	request.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	request.Header.Set(HeaderSignature, Sign(s.Secret, timestamp, request.Method, path, body))
}

// Verifier checks incoming requests against a keyring. Several keys may be
// active at once so keys can be rotated without downtime.
type Verifier struct {
	Keyring   map[string][]byte
	Tolerance time.Duration
	Now       func() time.Time
}

// Verify checks the headers against the raw body. It returns one of the
// Err* values so the caller can map it to an error code.
func (v Verifier) Verify(header http.Header, method, path string, body []byte) (keyID string, err error) {
	keyID = header.Get(HeaderKeyID)
	rawTimestamp := header.Get(HeaderTimestamp)
	signature := header.Get(HeaderSignature)
	if !keyIDPattern.MatchString(keyID) || rawTimestamp == "" || !strings.HasPrefix(signature, signatureVersion+"=") {
		return "", ErrMissingSignature
	}
	timestamp, parseErr := strconv.ParseInt(rawTimestamp, 10, 64)
	if parseErr != nil {
		return "", ErrMissingSignature
	}
	secret, known := v.Keyring[keyID]
	if !known {
		return keyID, ErrUnknownKeyID
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	skew := now().Unix() - timestamp
	if skew < 0 {
		skew = -skew
	}
	if time.Duration(skew)*time.Second > v.Tolerance {
		return keyID, ErrStaleTimestamp
	}
	expected := Sign(secret, timestamp, method, path, body)
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return keyID, ErrInvalidSignature
	}
	return keyID, nil
}
