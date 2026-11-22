package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Config is what the app needs from outside. LoadConfig fills it from flags and environment variables.
type Config struct {
	DataDir  string // -data, KOLO_GROUP_DATA. Default: <user config dir>/kolo-group
	Headless bool   // -headless: run with no window
}

// LoadConfig reads the flags in args, with getenv for the defaults. flag.ErrHelp means the help was shown.
func LoadConfig(args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	var cfg Config
	fs := flag.NewFlagSet("kolo-group", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.DataDir, "data", getenv("KOLO_GROUP_DATA"), "folder for the app's data (default <user config dir>/kolo-group)")
	fs.BoolVar(&cfg.Headless, "headless", false, "run with no window (not built yet)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("kolo-group: unexpected argument %q", fs.Arg(0))
	}
	if cfg.DataDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return Config{}, errors.New("kolo-group: no -data given and no user config dir: " + err.Error())
		}
		cfg.DataDir = filepath.Join(dir, "kolo-group")
	}
	return cfg, nil
}
