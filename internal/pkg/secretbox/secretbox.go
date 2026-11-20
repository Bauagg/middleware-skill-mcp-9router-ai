// Package secretbox mengenkripsi teks rahasia (mis. token OAuth) sebelum
// disimpan di database, memakai AES-256-GCM. Bila database bocor, token tidak
// bisa dipakai tanpa kunci yang ada di .env.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Box mengenkripsi dan mendekripsi dengan satu kunci.
type Box struct {
	aead cipher.AEAD
}

// New membuat Box dari kunci 32 byte yang ditulis sebagai 64 karakter hex
// (buat dengan: openssl rand -hex 32).
func New(keyHex string) (*Box, error) {
	key, err := hex.DecodeString(strings.TrimSpace(keyHex))
	if err != nil || len(key) != 32 {
		return nil, errors.New("kunci harus 64 karakter hex (32 byte), buat dengan: openssl rand -hex 32")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal mengenkripsi teks; hasilnya base64 (nonce acak + ciphertext). Teks
// kosong tetap kosong, supaya kolom kosong mudah dibedakan.
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open mendekripsi hasil Seal. Gagal bila kunci berbeda atau data diubah.
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("data terenkripsi rusak: %w", err)
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("data terenkripsi terlalu pendek")
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("gagal mendekripsi (kunci MCP_TOKEN_KEY berbeda?)")
	}
	return string(plain), nil
}
