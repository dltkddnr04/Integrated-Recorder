package bootstrap

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const maxTrustedKeysJSONBytes = 8 << 10

var releaseKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type releaseTrustKey struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key_base64"`
}

// parseTrustedReleaseKeys reads only public Ed25519 trust roots. Production
// private signing keys never belong in Runtime Host configuration. An empty
// value intentionally disables remote updates and leaves the bundled release
// available, which is the safe behavior for development installs.
func parseTrustedReleaseKeys(value string) (map[string]ed25519.PublicKey, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > maxTrustedKeysJSONBytes {
		return nil, errors.New("IR_RELEASE_TRUSTED_KEYS_JSON exceeds the supported size")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	var entries []releaseTrustKey
	if err := decoder.Decode(&entries); err != nil || len(entries) == 0 || len(entries) > 16 {
		return nil, errors.New("IR_RELEASE_TRUSTED_KEYS_JSON is invalid")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("IR_RELEASE_TRUSTED_KEYS_JSON has trailing data")
	}
	keys := make(map[string]ed25519.PublicKey, len(entries))
	for _, entry := range entries {
		if !releaseKeyIDPattern.MatchString(entry.KeyID) {
			return nil, errors.New("IR_RELEASE_TRUSTED_KEYS_JSON contains an invalid key ID")
		}
		if _, exists := keys[entry.KeyID]; exists {
			return nil, errors.New("IR_RELEASE_TRUSTED_KEYS_JSON contains a duplicate key ID")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(entry.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, errors.New("IR_RELEASE_TRUSTED_KEYS_JSON contains an invalid public key")
		}
		keys[entry.KeyID] = append(ed25519.PublicKey(nil), decoded...)
	}
	return keys, nil
}
