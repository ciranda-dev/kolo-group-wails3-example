package main

import (
	"testing"
	"time"

	"crypto/sha256"
	"encoding/hex"
)

func hexKey(k []byte) string { return hex.EncodeToString(k) }

// sumOf is the name -welcome-file records a welcome under.
func sumOf(text string) string {
	s := sha256.Sum256([]byte(text))
	return hex.EncodeToString(s[:])
}

// promote makes a device an admin. The room sends its offer once, so one sent before the device is in the mesh is lost and
// Promote waits for ever: stop the wait and offer again until the device is an admin.
func promote(t testing.TB, a *testApp, roomID, device string, isAdmin func() bool) {
	t.Helper()
	for range 8 {
		done := make(chan error, 1)
		go func() { done <- a.svc.Promote(roomID, device) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, wait, "the device to be an admin", isAdmin)
			return
		case <-time.After(10 * time.Second):
			a.svc.CancelAction()
			<-done
		}
	}
	t.Fatal("the device never answered the offer to be an admin")
}
