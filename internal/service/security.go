package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const rootKeyFilename = "master.key"

type secrets struct {
	digestKey        []byte
	aead             cipher.AEAD
	responseWrapAEAD cipher.AEAD
}

func createRootKey(dataDir string) error {
	path := filepath.Join(dataDir, rootKeyFilename)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate root key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create root key: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(base64.RawStdEncoding.EncodeToString(key)); err != nil {
		return fmt.Errorf("write root key: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync root key: %w", err)
	}
	return nil
}

func loadSecrets(dataDir string) (*secrets, error) {
	path := filepath.Join(dataDir, rootKeyFilename)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read root key: %w", err)
	}
	root, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(root) != 32 {
		return nil, errors.New("root key is invalid")
	}
	digestKey := deriveKey(root, "cpacloud/employee-and-session-digests/v1")
	encryptionKey := deriveKey(root, "cpacloud/upstream-credentials/v1")
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("initialize encryption: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize encryption: %w", err)
	}
	responseWrapKey := deriveKey(root, "cpacloud/response-state-wrap/v1")
	responseWrapBlock, err := aes.NewCipher(responseWrapKey)
	if err != nil {
		return nil, fmt.Errorf("initialize response state encryption: %w", err)
	}
	responseWrapAEAD, err := cipher.NewGCM(responseWrapBlock)
	if err != nil {
		return nil, fmt.Errorf("initialize response state encryption: %w", err)
	}
	return &secrets{digestKey: digestKey, aead: aead, responseWrapAEAD: responseWrapAEAD}, nil
}

func deriveKey(root []byte, purpose string) []byte {
	h := hmac.New(sha256.New, root)
	h.Write([]byte(purpose))
	return h.Sum(nil)
}

func (s *secrets) digest(purpose, value string) []byte {
	h := hmac.New(sha256.New, s.digestKey)
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write([]byte(value))
	return h.Sum(nil)
}

func (s *secrets) equalDigest(purpose, value string, expected []byte) bool {
	actual := s.digest(purpose, value)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func (s *secrets) encryptCredential(accountID, plaintext string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	aad := []byte("cpacloud/upstream-api-key/v1\x00" + accountID)
	sealed := s.aead.Seal(nil, nonce, []byte(plaintext), aad)
	return append(nonce, sealed...), nil
}

func (s *secrets) decryptCredential(accountID string, encoded []byte) (string, error) {
	if len(encoded) < s.aead.NonceSize()+s.aead.Overhead() {
		return "", errors.New("credential ciphertext is invalid")
	}
	nonce := encoded[:s.aead.NonceSize()]
	ciphertext := encoded[s.aead.NonceSize():]
	aad := []byte("cpacloud/upstream-api-key/v1\x00" + accountID)
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", errors.New("credential decryption failed")
	}
	return string(plaintext), nil
}

func (s *secrets) encryptGeminiAPIKey(upstreamID, plaintext string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	aad := []byte("cpacloud/upstream-credential/v2\x00gemini-api-key\x00api-key\x00" + upstreamID)
	sealed := s.aead.Seal(nil, nonce, []byte(plaintext), aad)
	return append(nonce, sealed...), nil
}

func (s *secrets) decryptGeminiAPIKey(upstreamID string, encoded []byte) (string, error) {
	if len(encoded) < s.aead.NonceSize()+s.aead.Overhead() {
		return "", errors.New("credential ciphertext is invalid")
	}
	nonce := encoded[:s.aead.NonceSize()]
	ciphertext := encoded[s.aead.NonceSize():]
	aad := []byte("cpacloud/upstream-credential/v2\x00gemini-api-key\x00api-key\x00" + upstreamID)
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", errors.New("credential decryption failed")
	}
	return string(plaintext), nil
}

func (s *secrets) encryptCodexAuth(upstreamID string, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	aad := []byte("cpacloud/upstream-credential/v2\x00codex-membership\x00auth-json\x00" + upstreamID)
	sealed := s.aead.Seal(nil, nonce, plaintext, aad)
	return append(nonce, sealed...), nil
}

func (s *secrets) decryptCodexAuth(upstreamID string, encoded []byte) ([]byte, error) {
	if len(encoded) < s.aead.NonceSize()+s.aead.Overhead() {
		return nil, errors.New("credential ciphertext is invalid")
	}
	nonce := encoded[:s.aead.NonceSize()]
	ciphertext := encoded[s.aead.NonceSize():]
	aad := []byte("cpacloud/upstream-credential/v2\x00codex-membership\x00auth-json\x00" + upstreamID)
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("credential decryption failed")
	}
	return plaintext, nil
}

func (s *secrets) encryptCodexOAuthSession(sessionID string, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate OAuth session nonce: %w", err)
	}
	aad := []byte("cpacloud/codex-oauth-session/v1\x00" + sessionID)
	sealed := s.aead.Seal(nil, nonce, plaintext, aad)
	return append(nonce, sealed...), nil
}

func (s *secrets) decryptCodexOAuthSession(sessionID string, encoded []byte) ([]byte, error) {
	if len(encoded) < s.aead.NonceSize()+s.aead.Overhead() {
		return nil, errors.New("OAuth session ciphertext is invalid")
	}
	nonce := encoded[:s.aead.NonceSize()]
	plaintext, err := s.aead.Open(nil, nonce, encoded[s.aead.NonceSize():], []byte("cpacloud/codex-oauth-session/v1\x00"+sessionID))
	if err != nil {
		return nil, errors.New("OAuth session decryption failed")
	}
	return plaintext, nil
}

func randomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newID(prefix string) (string, error) {
	t, err := randomToken(18)
	if err != nil {
		return "", err
	}
	return prefix + "_" + t, nil
}
