package client

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// scram implements the client side of SCRAM-SHA-256 (RFC 5802, RFC 7677)
// without channel binding, as used by PostgreSQL 10 and later.
type scram struct {
	password    string
	clientNonce string
	clientFirst string // client-first-message-bare
	authMessage string
	salted      []byte
}

func newSCRAM(password string) (*scram, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	s := &scram{password: password, clientNonce: base64.RawStdEncoding.EncodeToString(b)}
	// PostgreSQL takes the user name from the startup message.
	s.clientFirst = "n=,r=" + s.clientNonce
	return s, nil
}

// first returns the client-first-message.
func (s *scram) first() string { return "n,," + s.clientFirst }

// final answers the server-first-message with the client-final-message.
func (s *scram) final(serverFirst string) (string, error) {
	attrs := scramAttrs(serverFirst)
	nonce, salt64, iters := attrs["r"], attrs["s"], attrs["i"]
	if !strings.HasPrefix(nonce, s.clientNonce) || len(nonce) == len(s.clientNonce) {
		return "", fmt.Errorf("scram: server nonce does not extend ours")
	}
	salt, err := base64.StdEncoding.DecodeString(salt64)
	if err != nil {
		return "", fmt.Errorf("scram: bad salt: %w", err)
	}
	n, err := strconv.Atoi(iters)
	if err != nil || n < 1 {
		return "", fmt.Errorf("scram: bad iteration count %q", iters)
	}
	s.salted = pbkdf2SHA256([]byte(s.password), salt, n)
	withoutProof := "c=biws,r=" + nonce // biws = base64("n,,")
	s.authMessage = s.clientFirst + "," + serverFirst + "," + withoutProof
	clientKey := hmacSHA256(s.salted, "Client Key")
	stored := sha256.Sum256(clientKey)
	sig := hmacSHA256(stored[:], s.authMessage)
	proof := make([]byte, len(clientKey))
	for i := range clientKey {
		proof[i] = clientKey[i] ^ sig[i]
	}
	return withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof), nil
}

// verify checks the server-final-message, proving the server knew the
// password too.
func (s *scram) verify(serverFinal string) error {
	attrs := scramAttrs(serverFinal)
	if e, ok := attrs["e"]; ok {
		return fmt.Errorf("scram: server error %s", e)
	}
	got, err := base64.StdEncoding.DecodeString(attrs["v"])
	if err != nil {
		return fmt.Errorf("scram: bad server signature: %w", err)
	}
	want := hmacSHA256(hmacSHA256(s.salted, "Server Key"), s.authMessage)
	if !hmac.Equal(got, want) {
		return fmt.Errorf("scram: server signature mismatch")
	}
	return nil
}

func scramAttrs(msg string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(msg, ",") {
		if k, v, ok := strings.Cut(part, "="); ok && len(k) == 1 {
			out[k] = v
		}
	}
	return out
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

// pbkdf2SHA256 is PBKDF2 (RFC 8018) with HMAC-SHA-256 and a 32-byte key,
// which is all SCRAM-SHA-256 needs.
func pbkdf2SHA256(password, salt []byte, iter int) []byte {
	prf := hmac.New(sha256.New, password)
	prf.Write(salt)
	prf.Write([]byte{0, 0, 0, 1})
	u := prf.Sum(nil)
	out := append([]byte(nil), u...)
	for i := 1; i < iter; i++ {
		prf.Reset()
		prf.Write(u)
		u = prf.Sum(u[:0])
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out
}
