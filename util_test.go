package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func hexKey(k []byte) string { return hex.EncodeToString(k) }

// sumOf is the name -welcome-file records a welcome under.
func sumOf(text string) string {
	s := sha256.Sum256([]byte(text))
	return hex.EncodeToString(s[:])
}

// promote makes a device an admin and waits until it knows.
func promote(t testing.TB, a *testApp, roomID, device string, isAdmin func() bool) {
	t.Helper()
	if err := a.svc.Promote(roomID, device); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the device to be an admin", isAdmin)
}
