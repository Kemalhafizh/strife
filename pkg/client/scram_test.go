package client

import (
	"encoding/hex"
	"testing"
)

// RFC 7677 section 3 test vector (user "user", password "pencil").
func TestSCRAMRFC7677(t *testing.T) {
	s := &scram{password: "pencil", clientNonce: "rOprNGfwEbeRWgbNEkqO", clientFirst: "n=user,r=rOprNGfwEbeRWgbNEkqO"}
	final, err := s.final("r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096")
	if err != nil {
		t.Fatal(err)
	}
	want := "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
	if final != want {
		t.Fatalf("client-final\n got %s\nwant %s", final, want)
	}
	if err := s.verify("v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="); err != nil {
		t.Fatal(err)
	}
	if err := s.verify("v=AAAATRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="); err == nil {
		t.Fatal("a forged server signature must be rejected")
	}
	if _, err := s.final("r=someoneelse,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"); err == nil {
		t.Fatal("a server nonce that does not extend ours must be rejected")
	}
}

// RFC 7914 section 11 PBKDF2-HMAC-SHA256 vector (first 32 bytes).
func TestPBKDF2(t *testing.T) {
	got := hex.EncodeToString(pbkdf2SHA256([]byte("passwd"), []byte("salt"), 1))
	if got != "55ac046e56e3089fec1691c22544b605f94185216dde0465e68b9d57c20dacbc" {
		t.Fatal(got)
	}
}
