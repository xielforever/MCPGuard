package metadata

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

type Vault struct {
	aead    cipher.AEAD
	planKey []byte
}

func NewVault(key, planKey []byte) (*Vault, error) {
	if len(key) != 32 || len(planKey) != 32 || hmac.Equal(key, planKey) {
		return nil, errors.New("distinct 32-byte encryption and plan keys required")
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	return &Vault{aead: a, planKey: append([]byte(nil), planKey...)}, nil
}
func (v *Vault) Seal(data []byte, binding string) []byte {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return v.aead.Seal(nonce, nonce, data, []byte(binding))
}
func (v *Vault) Open(data []byte, binding string) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("invalid ciphertext")
	}
	return v.aead.Open(nil, data[:n], data[n:], []byte(binding))
}
func (v *Vault) Hash(data []byte) string {
	h := hmac.New(sha256.New, v.planKey)
	h.Write(data)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
