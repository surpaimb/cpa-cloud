package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestOutboundProxyCredentialEncryptionAndIsolation(t *testing.T) {
	dir := t.TempDir()
	if err := createRootKey(dir); err != nil {
		t.Fatal(err)
	}
	sec, err := loadSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	credential := outboundProxyCredential{username: "synthetic-proxy-user", password: "synthetic-secret:密碼"}
	ciphertext, err := sec.encryptOutboundProxyCredential("proxy_test", credential)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sec.encryptOutboundProxyCredential("proxy_test", credential)
	if err != nil || bytes.Equal(ciphertext, second) {
		t.Fatal("encryption must use fresh nonces")
	}
	if bytes.Contains(ciphertext, []byte(credential.username)) || bytes.Contains(ciphertext, []byte(credential.password)) {
		t.Fatal("ciphertext contains plaintext")
	}
	reloaded, err := loadSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := reloaded.decryptOutboundProxyCredential("proxy_test", 1, ciphertext)
	if err != nil || decoded != credential {
		t.Fatal("encrypted credential did not survive reloading root key")
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 1
	apiKey, err := sec.encryptCredential("proxy_test", credential.password)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		id      string
		version int
		data    []byte
	}{
		{"wrong proxy", "proxy_other", 1, ciphertext},
		{"wrong version", "proxy_test", 2, ciphertext},
		{"tampering", "proxy_test", 1, tampered},
		{"truncated", "proxy_test", 1, ciphertext[:8]},
		{"API key purpose", "proxy_test", 1, apiKey},
		{"oversize", "proxy_test", 1, make([]byte, 33<<10)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := sec.decryptOutboundProxyCredential(test.id, test.version, test.data)
			if !errors.Is(err, errOutboundProxyCredential) || got != (outboundProxyCredential{}) {
				t.Fatal("invalid ciphertext did not fail closed")
			}
		})
	}
	if _, err := sec.decryptCredential("proxy_test", ciphertext); err == nil {
		t.Fatal("proxy credential accepted as upstream API key")
	}
	for _, formatted := range []string{fmt.Sprint(credential), fmt.Sprintf("%+v", credential), fmt.Sprintf("%#v", credential)} {
		if strings.Contains(formatted, credential.username) || strings.Contains(formatted, credential.password) {
			t.Fatal("formatting leaked credentials")
		}
	}
	encoded, err := json.Marshal(credential)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("default JSON serialization must not expose proxy credentials")
	}
}

func TestOutboundProxyCredentialValidation(t *testing.T) {
	for _, invalid := range []outboundProxyCredential{
		{}, {username: "colon:user", password: "secret"},
		{username: "user\r\n", password: "secret"}, {username: "user", password: "secret\x00"},
		{username: "user", password: string([]byte{0xff})},
		{username: strings.Repeat("u", 1025)}, {username: "user", password: strings.Repeat("p", 4097)},
	} {
		if validOutboundProxyCredential(invalid) {
			t.Fatal("invalid credential accepted")
		}
	}
	if !validOutboundProxyCredential(outboundProxyCredential{username: "user", password: ""}) {
		t.Fatal("Basic auth permits an empty password")
	}
}
