package hmacauth

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type vectorFile struct {
	Vectors []struct {
		KeyID        string `json:"key_id"`
		Secret       string `json:"secret"`
		Timestamp    int64  `json:"timestamp"`
		Method       string `json:"method"`
		Path         string `json:"path"`
		Body         string `json:"body"`
		StringToSign string `json:"string_to_sign"`
		Signature    string `json:"signature"`
	} `json:"vectors"`
}

// The vectors are shared with Core's TypeScript implementation.
func TestSignMatchesSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/hmac-test-vectors.json")
	require.NoError(t, err)
	var file vectorFile
	require.NoError(t, json.Unmarshal(raw, &file))
	require.NotEmpty(t, file.Vectors)

	for _, vector := range file.Vectors {
		t.Run(vector.Method+" "+vector.Path, func(t *testing.T) {
			assert.Equal(t, vector.StringToSign, StringToSign(vector.Timestamp, vector.Method, vector.Path, []byte(vector.Body)))
			assert.Equal(t, vector.Signature, Sign([]byte(vector.Secret), vector.Timestamp, vector.Method, vector.Path, []byte(vector.Body)))

			header := http.Header{}
			header.Set(HeaderKeyID, vector.KeyID)
			header.Set(HeaderTimestamp, jsonInt(vector.Timestamp))
			header.Set(HeaderSignature, vector.Signature)
			verifier := Verifier{
				Keyring:   map[string][]byte{vector.KeyID: []byte(vector.Secret)},
				Tolerance: 300 * time.Second,
				Now:       func() time.Time { return time.Unix(vector.Timestamp+10, 0) },
			}
			keyID, err := verifier.Verify(header, vector.Method, vector.Path, []byte(vector.Body))
			require.NoError(t, err)
			assert.Equal(t, vector.KeyID, keyID)
		})
	}
}

func jsonInt(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestVerifyRejections(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	verifier := Verifier{
		Keyring:   map[string][]byte{"old": []byte("ffffffffffffffffffffffffffffffff"), "new": secret},
		Tolerance: 300 * time.Second,
		Now:       func() time.Time { return now },
	}
	body := []byte(`{"a":1}`)
	signed := func(keyID string, key []byte, at time.Time) http.Header {
		request, _ := http.NewRequest(http.MethodPost, "http://judge/internal/jobs", nil)
		Signer{KeyID: keyID, Secret: key, Now: func() time.Time { return at }}.SignRequest(request, "/internal/jobs", body)
		return request.Header
	}

	_, err := verifier.Verify(signed("new", secret, now), http.MethodPost, "/internal/jobs", body)
	assert.NoError(t, err, "a valid signature with the new key")

	_, err = verifier.Verify(http.Header{}, http.MethodPost, "/internal/jobs", body)
	assert.ErrorIs(t, err, ErrMissingSignature)

	_, err = verifier.Verify(signed("ghost", secret, now), http.MethodPost, "/internal/jobs", body)
	assert.ErrorIs(t, err, ErrUnknownKeyID)

	_, err = verifier.Verify(signed("new", secret, now.Add(-301*time.Second)), http.MethodPost, "/internal/jobs", body)
	assert.ErrorIs(t, err, ErrStaleTimestamp, "too old")

	_, err = verifier.Verify(signed("new", secret, now.Add(301*time.Second)), http.MethodPost, "/internal/jobs", body)
	assert.ErrorIs(t, err, ErrStaleTimestamp, "too far in the future")

	_, err = verifier.Verify(signed("new", secret, now), http.MethodPost, "/internal/jobs", []byte(`{"a":2}`))
	assert.ErrorIs(t, err, ErrInvalidSignature, "body was tampered with")

	_, err = verifier.Verify(signed("new", secret, now), http.MethodPost, "/internal/jobs?x=1", body)
	assert.ErrorIs(t, err, ErrInvalidSignature, "path was tampered with")

	_, err = verifier.Verify(signed("old", secret, now), http.MethodPost, "/internal/jobs", body)
	assert.ErrorIs(t, err, ErrInvalidSignature, "key id does not match the secret used")
}

// The rotation of a key (Core's docs/secrets.md): both keys are accepted while
// the sender changes over, and the old one stops working once it is removed.
func TestKeyRotationNeedsNoDowntime(t *testing.T) {
	oldSecret := []byte("ffffffffffffffffffffffffffffffff")
	newSecret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"job":1}`)
	sendWith := func(keyID string, secret []byte) http.Header {
		request, _ := http.NewRequest(http.MethodPost, "http://judge/internal/jobs", nil)
		Signer{KeyID: keyID, Secret: secret, Now: func() time.Time { return now }}.SignRequest(request, "/internal/jobs", body)
		return request.Header
	}
	check := func(keyring map[string][]byte, header http.Header) error {
		_, err := Verifier{Keyring: keyring, Tolerance: 300 * time.Second, Now: func() time.Time { return now }}.Verify(header, http.MethodPost, "/internal/jobs", body)
		return err
	}

	// 1. Only the old key exists; the sender uses it.
	require.NoError(t, check(map[string][]byte{"core-1": oldSecret}, sendWith("core-1", oldSecret)))
	// 2. The receiver learns the new key first: requests with either key pass, so the sender can change at any moment.
	both := map[string][]byte{"core-1": oldSecret, "core-2": newSecret}
	require.NoError(t, check(both, sendWith("core-1", oldSecret)), "a request already on its way with the old key")
	require.NoError(t, check(both, sendWith("core-2", newSecret)), "the sender has moved to the new key")
	// 3. The old key is removed: it is refused, the new one still works.
	onlyNew := map[string][]byte{"core-2": newSecret}
	assert.ErrorIs(t, check(onlyNew, sendWith("core-1", oldSecret)), ErrUnknownKeyID)
	assert.NoError(t, check(onlyNew, sendWith("core-2", newSecret)))
	// A new key id that was never added is not guessed from the old one.
	assert.ErrorIs(t, check(map[string][]byte{"core-1": oldSecret}, sendWith("core-2", newSecret)), ErrUnknownKeyID)
}
