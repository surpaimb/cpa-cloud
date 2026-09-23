package service

// Independently authored from CPA Cloud's outbound proxy contract. Proxy
// authentication has its own AEAD purpose and object binding; it is never an
// upstream model credential or an employee credential.
import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const outboundProxyCredentialVersion = 1

var errOutboundProxyCredential = errors.New("invalid outbound proxy credential")

// These fields are private to the service. Only the explicit encrypted payload
// below is serialized. Formatting the value cannot disclose the credentials.
type outboundProxyCredential struct {
	username string
	password string
}

func (outboundProxyCredential) String() string   { return "[redacted proxy credential]" }
func (outboundProxyCredential) GoString() string { return "[redacted proxy credential]" }

func (outboundProxyCredential) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[redacted proxy credential]")
}

func validOutboundProxyCredential(value outboundProxyCredential) bool {
	if value.username == "" || len(value.username) > 1024 || len(value.password) > 4096 ||
		!utf8.ValidString(value.username) || !utf8.ValidString(value.password) || strings.Contains(value.username, ":") {
		return false
	}
	for _, field := range []string{value.username, value.password} {
		for _, r := range field {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}

func outboundProxyAAD(proxyID string) []byte {
	return []byte("cpacloud/outbound-proxy-credential/v1\x00" + proxyID)
}

func (s *secrets) encryptOutboundProxyCredential(proxyID string, value outboundProxyCredential) ([]byte, error) {
	if s == nil || s.aead == nil || !validIdentifier(proxyID, 128) || !validOutboundProxyCredential(value) {
		return nil, errOutboundProxyCredential
	}
	payload, err := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{value.username, value.password})
	if err != nil {
		return nil, errOutboundProxyCredential
	}
	defer clear(payload)
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, errOutboundProxyCredential
	}
	return s.aead.Seal(nonce, nonce, payload, outboundProxyAAD(proxyID)), nil
}

func (s *secrets) decryptOutboundProxyCredential(proxyID string, version int, encoded []byte) (outboundProxyCredential, error) {
	if s == nil || s.aead == nil || version != outboundProxyCredentialVersion || !validIdentifier(proxyID, 128) ||
		len(encoded) < s.aead.NonceSize()+s.aead.Overhead() || len(encoded) > 32<<10 {
		return outboundProxyCredential{}, errOutboundProxyCredential
	}
	payload, err := s.aead.Open(nil, encoded[:s.aead.NonceSize()], encoded[s.aead.NonceSize():], outboundProxyAAD(proxyID))
	if err != nil {
		return outboundProxyCredential{}, errOutboundProxyCredential
	}
	defer clear(payload)
	var decoded struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return outboundProxyCredential{}, errOutboundProxyCredential
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return outboundProxyCredential{}, errOutboundProxyCredential
	}
	value := outboundProxyCredential{username: decoded.Username, password: decoded.Password}
	if !validOutboundProxyCredential(value) {
		return outboundProxyCredential{}, errOutboundProxyCredential
	}
	return value, nil
}
