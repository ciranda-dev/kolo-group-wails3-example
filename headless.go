package main

import (
	"context"
	"errors"
)

// errHeadless is what -headless returns until the headless mode is built.
var errHeadless = errors.New("kolo-group: -headless is not built yet")

// runHeadless runs the app with no window.
func runHeadless(ctx context.Context, cfg Config) error { return errHeadless }
