package fleet

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

type Signed struct {
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
}

func GenerateKey(path string) (string, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(path); err == nil {
		return "", fmt.Errorf("refusing to replace existing signing key")
	}
	if err = WritePrivate(path, []byte(hex.EncodeToString(key.Seed()))); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}
func Sign(v any, keyHex string) (Signed, error) {
	seed, err := hex.DecodeString(keyHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		return Signed{}, fmt.Errorf("invalid signing seed")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return Signed{}, err
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), b)
	return Signed{b, base64.StdEncoding.EncodeToString(sig)}, nil
}
func (s Signed) Verify(public string, v any) error {
	key, err := base64.StdEncoding.DecodeString(public)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid pinned public key")
	}
	var compact bytes.Buffer
	if err = json.Compact(&compact, s.Payload); err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(s.Signature)
	if err != nil || !ed25519.Verify(key, compact.Bytes(), sig) {
		return fmt.Errorf("fleet signature verification failed")
	}
	return json.Unmarshal(s.Payload, v)
}
