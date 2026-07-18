package replay

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const MaxCiphertextBytes = 2 * 1024 * 1024

const (
	CompressionGZIP     = "gzip"
	EncryptionAES256GCM = "aes-256-gcm"
)

var (
	ErrCiphertextTooLarge = errors.New("Replay ciphertext exceeds attachment limit")
	ErrIntegrity          = errors.New("Replay payload integrity check failed")
	ErrKeyUnavailable     = errors.New("Replay encryption key is unavailable")
)

type WrappedDataKey struct {
	KeyID      string
	Ciphertext []byte
}

type KeyProvider interface {
	WrapDataKey(context.Context, []byte) (WrappedDataKey, error)
	UnwrapDataKey(context.Context, WrappedDataKey) ([]byte, error)
}

type SealedPayload struct {
	Class            Class
	SchemaVersion    int
	PlaintextSHA256  string
	Compression      string
	Encryption       string
	KeyID            string
	WrappedKey       []byte
	Nonce            []byte
	Ciphertext       []byte
	CiphertextSHA256 string
}

type Sealer struct {
	keys KeyProvider
}

func NewSealer(keys KeyProvider) (*Sealer, error) {
	if keys == nil {
		return nil, errors.New("Replay key provider is required")
	}
	return &Sealer{keys: keys}, nil
}

func (s *Sealer) Seal(ctx context.Context, payload PlainPayload) (SealedPayload, error) {
	if err := ctx.Err(); err != nil {
		return SealedPayload{}, err
	}
	validated, err := NewPlainPayload(payload.Class, payload.SchemaVersion, payload.Bytes)
	if err != nil {
		return SealedPayload{}, err
	}
	if payload.SHA256 != "" && payload.SHA256 != validated.SHA256 {
		return SealedPayload{}, ErrIntegrity
	}

	compressed := &boundedBuffer{max: MaxCiphertextBytes - 16}
	compressor := gzip.NewWriter(compressed)
	if _, err := compressor.Write(validated.Bytes); err != nil {
		_ = compressor.Close()
		return SealedPayload{}, normalizeCiphertextLimit(err)
	}
	if err := compressor.Close(); err != nil {
		return SealedPayload{}, normalizeCiphertextLimit(err)
	}

	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return SealedPayload{}, err
	}
	defer zeroBytes(dataKey)
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return SealedPayload{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return SealedPayload{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return SealedPayload{}, err
	}
	aad := replayAAD(validated.Class, validated.SchemaVersion, validated.SHA256)
	ciphertext := gcm.Seal(nil, nonce, compressed.Bytes(), aad)
	if len(ciphertext) > MaxCiphertextBytes {
		return SealedPayload{}, ErrCiphertextTooLarge
	}
	wrapped, err := s.keys.WrapDataKey(ctx, dataKey)
	if err != nil {
		return SealedPayload{}, fmt.Errorf("wrap Replay data key: %w", err)
	}
	if strings.TrimSpace(wrapped.KeyID) == "" || len(wrapped.Ciphertext) == 0 {
		return SealedPayload{}, ErrKeyUnavailable
	}
	ciphertextDigest := sha256.Sum256(ciphertext)
	return SealedPayload{
		Class: validated.Class, SchemaVersion: validated.SchemaVersion,
		PlaintextSHA256: validated.SHA256, Compression: CompressionGZIP,
		Encryption: EncryptionAES256GCM, KeyID: wrapped.KeyID,
		WrappedKey: append([]byte(nil), wrapped.Ciphertext...), Nonce: append([]byte(nil), nonce...),
		Ciphertext: append([]byte(nil), ciphertext...), CiphertextSHA256: hex.EncodeToString(ciphertextDigest[:]),
	}, nil
}

func (s *Sealer) Open(ctx context.Context, sealed SealedPayload) (PlainPayload, error) {
	if err := ctx.Err(); err != nil {
		return PlainPayload{}, err
	}
	if !sealed.Class.Valid() || sealed.SchemaVersion != 1 || sealed.Compression != CompressionGZIP ||
		sealed.Encryption != EncryptionAES256GCM || strings.TrimSpace(sealed.KeyID) == "" ||
		len(sealed.WrappedKey) == 0 || len(sealed.Nonce) == 0 || len(sealed.Ciphertext) == 0 ||
		len(sealed.Ciphertext) > MaxCiphertextBytes || len(sealed.CiphertextSHA256) != sha256.Size*2 ||
		len(sealed.PlaintextSHA256) != sha256.Size*2 {
		return PlainPayload{}, ErrIntegrity
	}
	ciphertextDigest := sha256.Sum256(sealed.Ciphertext)
	if !hmac.Equal([]byte(sealed.CiphertextSHA256), []byte(hex.EncodeToString(ciphertextDigest[:]))) {
		return PlainPayload{}, ErrIntegrity
	}
	dataKey, err := s.keys.UnwrapDataKey(ctx, WrappedDataKey{KeyID: sealed.KeyID, Ciphertext: sealed.WrappedKey})
	if err != nil {
		return PlainPayload{}, fmt.Errorf("unwrap Replay data key: %w", err)
	}
	defer zeroBytes(dataKey)
	if len(dataKey) != 32 {
		return PlainPayload{}, ErrKeyUnavailable
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return PlainPayload{}, ErrKeyUnavailable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(sealed.Nonce) != gcm.NonceSize() {
		return PlainPayload{}, ErrIntegrity
	}
	compressed, err := gcm.Open(nil, sealed.Nonce, sealed.Ciphertext, replayAAD(sealed.Class, sealed.SchemaVersion, sealed.PlaintextSHA256))
	if err != nil {
		return PlainPayload{}, ErrIntegrity
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return PlainPayload{}, ErrIntegrity
	}
	plaintext, readErr := io.ReadAll(io.LimitReader(reader, MaxPlaintextBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(plaintext) > MaxPlaintextBytes {
		return PlainPayload{}, ErrIntegrity
	}
	opened, err := NewPlainPayload(sealed.Class, sealed.SchemaVersion, plaintext)
	if err != nil || !hmac.Equal([]byte(opened.SHA256), []byte(sealed.PlaintextSHA256)) {
		return PlainPayload{}, ErrIntegrity
	}
	return opened, nil
}

type DevelopmentKeyProvider struct {
	keyID string
	key   []byte
}

func NewDevelopmentKeyProvider(keyID string, key []byte) (*DevelopmentKeyProvider, error) {
	if strings.TrimSpace(keyID) == "" || len(keyID) > 160 || len(key) != 32 {
		return nil, errors.New("development Replay key must have an ID and 32-byte KEK")
	}
	return &DevelopmentKeyProvider{keyID: keyID, key: append([]byte(nil), key...)}, nil
}

func (p *DevelopmentKeyProvider) WrapDataKey(ctx context.Context, dataKey []byte) (WrappedDataKey, error) {
	if err := ctx.Err(); err != nil {
		return WrappedDataKey{}, err
	}
	if len(dataKey) != 32 {
		return WrappedDataKey{}, ErrKeyUnavailable
	}
	gcm, err := p.gcm()
	if err != nil {
		return WrappedDataKey{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return WrappedDataKey{}, err
	}
	wrapped := append(nonce, gcm.Seal(nil, nonce, dataKey, []byte(p.keyID))...)
	return WrappedDataKey{KeyID: p.keyID, Ciphertext: wrapped}, nil
}

func (p *DevelopmentKeyProvider) UnwrapDataKey(ctx context.Context, wrapped WrappedDataKey) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if wrapped.KeyID != p.keyID {
		return nil, ErrKeyUnavailable
	}
	gcm, err := p.gcm()
	if err != nil || len(wrapped.Ciphertext) <= gcm.NonceSize() {
		return nil, ErrKeyUnavailable
	}
	nonce := wrapped.Ciphertext[:gcm.NonceSize()]
	dataKey, err := gcm.Open(nil, nonce, wrapped.Ciphertext[gcm.NonceSize():], []byte(p.keyID))
	if err != nil || len(dataKey) != 32 {
		return nil, ErrKeyUnavailable
	}
	return dataKey, nil
}

func (p *DevelopmentKeyProvider) gcm() (cipher.AEAD, error) {
	if p == nil || len(p.key) != 32 {
		return nil, ErrKeyUnavailable
	}
	block, err := aes.NewCipher(p.key)
	if err != nil {
		return nil, ErrKeyUnavailable
	}
	return cipher.NewGCM(block)
}

func replayAAD(class Class, schemaVersion int, plaintextSHA256 string) []byte {
	return []byte(fmt.Sprintf("nano-replay\nschema=%d\nclass=%s\nplaintext_sha256=%s", schemaVersion, class, plaintextSHA256))
}

type boundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(payload []byte) (int, error) {
	remaining := b.max - b.Len()
	if remaining <= 0 {
		return 0, ErrCiphertextTooLarge
	}
	if len(payload) > remaining {
		_, _ = b.Buffer.Write(payload[:remaining])
		return remaining, ErrCiphertextTooLarge
	}
	return b.Buffer.Write(payload)
}

func normalizeCiphertextLimit(err error) error {
	if errors.Is(err, ErrCiphertextTooLarge) {
		return ErrCiphertextTooLarge
	}
	return err
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
