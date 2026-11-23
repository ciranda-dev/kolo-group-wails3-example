package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ciranda-dev/kolo-pin"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
)

const (
	loopbackListen        = "127.0.0.1:0" // the default when every entry node is on this computer
	publicListen          = "0.0.0.0:0"   // the default otherwise: the public network cannot be reached from loopback
	defaultLookupInterval = 5 * time.Second
	defaultPinTTL         = 30 * 24 * time.Hour
	maxConfigFile         = 64 << 10 // bytes, as in kolo-node
)

// ErrConfig matches every ConfigError, whatever setting it names.
var ErrConfig = errors.New("invalid configuration")

// Causes of a ConfigError.
var ErrMalformed = errors.New("is malformed")

// ConfigError names the setting that is wrong and why. errors.Is(err, ErrConfig) is true for every one.
type ConfigError struct {
	Setting string // the flag name, such as "pin"
	Err     error
}

func (e *ConfigError) Error() string        { return fmt.Sprintf("kolo-group: -%s %v", e.Setting, e.Err) }
func (e *ConfigError) Unwrap() error        { return e.Err }
func (e *ConfigError) Is(target error) bool { return target == ErrConfig }

// Config is what the app needs from outside. LoadConfig fills it from flags, a config file and environment variables.
type Config struct {
	DataDir        string        // -data, KOLO_GROUP_DATA. Default: <user config dir>/kolo-group
	Headless       bool          // -headless: run with no window
	ConfigFile     string        // -config: a JSON file with the settings below, under the names of their flags
	Bootstrap      string        // -bootstrap, KOLO_GROUP_BOOTSTRAP: host:port of the entry nodes of your own network, comma-separated, used alone. Default: the public network
	Listen         string        // -listen, KOLO_GROUP_LISTEN: local IP:PORT. Default 127.0.0.1:0 when every entry node is on loopback, else 0.0.0.0:0
	Pins           []string      // -pin, KOLO_GROUP_PIN: pins as IP:PORT@KEY, comma-separated, repeatable
	PinFiles       []string      // -pin-file, KOLO_GROUP_PIN_FILE: files of pins, one or more per line, "#" starts a comment; repeatable
	PinTTL         time.Duration // -pin-ttl, KOLO_GROUP_PIN_TTL: lifetime of the capabilities for the pins. Default 720h
	LookupInterval time.Duration // -lookup-interval, KOLO_GROUP_LOOKUP_INTERVAL: time between topic lookups. Default 5s
	Relays         []string      // -relay, KOLO_GROUP_RELAYS: connection relays as host:port@key, comma-separated, repeatable
	RelayFiles     []string      // -relay-file: files of relays, one or more per line, "#" starts a comment; repeatable
	WelcomeFile    string        // -welcome-file, KOLO_GROUP_WELCOME_FILE: with -headless, the file an admin's welcome is written to

	relaysEnv bool // Relays came from KOLO_GROUP_RELAYS
}

// fileConfig is the config file: the settings under the names of their flags, each one optional.
type fileConfig struct {
	Data           *string   `json:"data"`
	Bootstrap      *string   `json:"bootstrap"`
	Listen         *string   `json:"listen"`
	Pin            *[]string `json:"pin"`
	PinFile        *[]string `json:"pin-file"`
	PinTTL         *string   `json:"pin-ttl"`
	LookupInterval *string   `json:"lookup-interval"`
	Relay          *[]string `json:"relay"`
	RelayFile      *[]string `json:"relay-file"`
	WelcomeFile    *string   `json:"welcome-file"`
}

// LoadConfig reads the flags in args. A setting that no flag gives comes from the config file, else from its
// environment variable (read with getenv, such as os.Getenv), else from its default; for a list, a flag replaces the rest.
func LoadConfig(args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("kolo-group", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.DataDir, "data", "", "folder for the app's data (KOLO_GROUP_DATA, default <user config dir>/kolo-group)")
	fs.BoolVar(&c.Headless, "headless", false, "run with no window: an always-on device that holds the rooms it was added to")
	fs.StringVar(&c.ConfigFile, "config", "", "JSON file with the settings below under the names of their flags; a flag wins over it")
	fs.StringVar(&c.Bootstrap, "bootstrap", "", "host:port of the entry nodes of your own network, comma-separated, used alone (KOLO_GROUP_BOOTSTRAP, default: the public network)")
	fs.StringVar(&c.Listen, "listen", "", "local IP:PORT to listen on (KOLO_GROUP_LISTEN, default "+loopbackListen+" when every entry node is on loopback, else "+publicListen+")")
	fs.StringVar(&c.WelcomeFile, "welcome-file", "", "with -headless: the file an admin's welcome is written to; the device joins that room (KOLO_GROUP_WELCOME_FILE)")
	var pins, pinFiles, relays, relayFiles []string
	fs.Func("pin", "pins as IP:PORT@KEY, comma-separated, repeatable: they keep the admins' logs for people who are offline (KOLO_GROUP_PIN)",
		func(v string) error { pins = append(pins, splitList(v)...); return nil })
	fs.Func("pin-file", "file of pins, one or more IP:PORT@KEY per line, repeatable (KOLO_GROUP_PIN_FILE)",
		func(v string) error { pinFiles = append(pinFiles, v); return nil })
	fs.Func("relay", "connection relays as host:port@key, comma-separated, repeatable (KOLO_GROUP_RELAYS when no -relay or -relay-file is given)",
		func(v string) error { relays = append(relays, v); return nil })
	fs.Func("relay-file", "file of connection relays, one or more host:port@key per line, repeatable",
		func(v string) error { relayFiles = append(relayFiles, v); return nil })
	var ttl, lookup string
	fs.StringVar(&ttl, "pin-ttl", "", "lifetime of the capabilities for the pins (KOLO_GROUP_PIN_TTL, default 720h)")
	fs.StringVar(&lookup, "lookup-interval", "", "time between lookups of the room topics (KOLO_GROUP_LOOKUP_INTERVAL, default 5s)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("kolo-group: unexpected argument %q", fs.Arg(0))
	}
	flagged := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { flagged[f.Name] = true })
	file, err := readConfigFile(c.ConfigFile)
	if err != nil {
		return Config{}, err
	}

	// text is the value of a setting that is not a list: its flag, else the file's, else the variable's.
	text := func(name, env string, flagValue string, fromFile *string) string {
		switch {
		case flagged[name]:
			return flagValue
		case fromFile != nil:
			return *fromFile
		}
		return getenv(env)
	}
	list := func(name, env string, flagValue []string, fromFile *[]string, split bool) []string {
		switch {
		case flagged[name]:
			return flagValue
		case fromFile != nil:
			return *fromFile
		case getenv(env) == "":
			return nil
		case split:
			return splitList(getenv(env))
		}
		return []string{getenv(env)}
	}
	duration := func(name, env, flagValue string, fromFile *string) (d time.Duration, err error) {
		if v := text(name, env, flagValue, fromFile); v != "" {
			if d, err = time.ParseDuration(v); err != nil {
				err = &ConfigError{Setting: name, Err: fmt.Errorf("%w: %w", ErrMalformed, err)}
			}
		}
		return d, err
	}
	c.DataDir = text("data", "KOLO_GROUP_DATA", c.DataDir, file.Data)
	c.Bootstrap = text("bootstrap", "KOLO_GROUP_BOOTSTRAP", c.Bootstrap, file.Bootstrap)
	c.Listen = text("listen", "KOLO_GROUP_LISTEN", c.Listen, file.Listen)
	c.WelcomeFile = text("welcome-file", "KOLO_GROUP_WELCOME_FILE", c.WelcomeFile, file.WelcomeFile)
	c.Pins = list("pin", "KOLO_GROUP_PIN", pins, file.Pin, true)
	c.PinFiles = list("pin-file", "KOLO_GROUP_PIN_FILE", pinFiles, file.PinFile, false)
	c.RelayFiles = list("relay-file", "", relayFiles, file.RelayFile, false)
	c.Relays = list("relay", "KOLO_GROUP_RELAYS", relays, file.Relay, false)
	c.relaysEnv = len(c.Relays) > 0 && !flagged["relay"] && file.Relay == nil
	if c.PinTTL, err = duration("pin-ttl", "KOLO_GROUP_PIN_TTL", ttl, file.PinTTL); err != nil {
		return Config{}, err
	}
	if c.LookupInterval, err = duration("lookup-interval", "KOLO_GROUP_LOOKUP_INTERVAL", lookup, file.LookupInterval); err != nil {
		return Config{}, err
	}
	if c.DataDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return Config{}, errors.New("kolo-group: no -data given and no user config dir: " + err.Error())
		}
		c.DataDir = filepath.Join(dir, "kolo-group")
	}
	return c, nil
}

// readConfigFile reads the JSON config file at path; an empty path is an empty file. An unknown key or anything after
// the object is an error.
func readConfigFile(path string) (fileConfig, error) {
	var f fileConfig
	if path == "" {
		return f, nil
	}
	b, err := readSmallFile(path)
	if err != nil {
		return f, &ConfigError{Setting: "config", Err: err}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, &ConfigError{Setting: "config", Err: fmt.Errorf("%w: %s: %w", ErrMalformed, path, err)}
	}
	if _, err := dec.Token(); err != io.EOF {
		return f, &ConfigError{Setting: "config", Err: fmt.Errorf("%w: %s: data after the object", ErrMalformed, path)}
	}
	return f, nil
}

// splitList returns the entries of a comma-separated list, trimmed, without empty ones.
func splitList(s string) []string {
	var out []string
	for e := range strings.SplitSeq(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// settings is a Config after defaults and checks: values the code can use as they are.
type settings struct {
	Config
	pins       []pinRef
	bootstrap  []string          // the entry nodes of your own network; nil is the public network
	relays     []koloswarm.Relay // connection relays, for koloswarm.Config.Relays
	relaysFrom string            // where they came from: "-relay", "-relay-file", both, "KOLO_GROUP_RELAYS" or ""
}

// resolve applies the defaults and checks every setting, returning a *ConfigError for the first bad one.
func (c Config) resolve() (settings, error) {
	s := settings{Config: c}
	var err error
	switch {
	case s.PinTTL < 0:
		return s, &ConfigError{Setting: "pin-ttl", Err: fmt.Errorf("%w: want a positive duration", ErrMalformed)}
	case s.LookupInterval < 0:
		return s, &ConfigError{Setting: "lookup-interval", Err: fmt.Errorf("%w: want a positive duration", ErrMalformed)}
	}
	if s.PinTTL == 0 {
		s.PinTTL = defaultPinTTL
	}
	if s.LookupInterval == 0 {
		s.LookupInterval = defaultLookupInterval
	}
	if s.Bootstrap != "" {
		s.bootstrap, err = koloswarm.ParseNodes(s.Bootstrap)
		switch {
		case err != nil:
			return s, &ConfigError{Setting: "bootstrap", Err: relayError(err, "")}
		case len(s.bootstrap) == 0:
			return s, &ConfigError{Setting: "bootstrap", Err: fmt.Errorf("%w: lists no entry node", ErrMalformed)}
		}
	}
	if s.Listen == "" {
		s.Listen = publicListen
		if loopbackOnly(s.bootstrap) {
			s.Listen = loopbackListen
		}
	}
	if s.relays, s.relaysFrom, err = c.loadRelays(); err != nil {
		return s, err
	}
	s.pins, err = c.loadPins()
	return s, err
}

// loopbackOnly says whether every entry node is a loopback IPv4 literal, so the app can stay on 127.0.0.1.
func loopbackOnly(nodes []string) bool {
	if len(nodes) == 0 {
		return false
	}
	for _, n := range nodes {
		if a, err := netip.ParseAddrPort(n); err != nil || !a.Addr().IsLoopback() {
			return false
		}
	}
	return true
}

// NetworkInfo says which network the app joins: Mode is "public" (the entry nodes built into Kolo) or "own" (the
// -bootstrap list). Nodes are the entry nodes of "own".
type NetworkInfo struct {
	Mode  string   `json:"mode"`
	Nodes []string `json:"nodes"`
}

func (s settings) network() NetworkInfo {
	if len(s.bootstrap) > 0 {
		return NetworkInfo{Mode: "own", Nodes: slices.Clone(s.bootstrap)}
	}
	return NetworkInfo{Mode: "public", Nodes: []string{}}
}

// pinRef is one pin: where it listens and its public key.
type pinRef struct {
	Addr string
	Key  ed25519.PublicKey
}

func (p pinRef) peer() pin.Peer { return pin.Peer{ID: p.Key, Addr: p.Addr} }

// parsePin reads a pin written as IP:PORT@KEY, KEY being the pin's public key in hex.
func parsePin(entry string) (pinRef, error) {
	addr, key, ok := strings.Cut(entry, "@")
	if !ok {
		return pinRef{}, fmt.Errorf("%w: %q: want IP:PORT@KEY", ErrMalformed, entry)
	}
	if ap, err := netip.ParseAddrPort(addr); err != nil || !ap.Addr().Is4() || ap.Addr().IsUnspecified() || ap.Addr().IsMulticast() || ap.Port() == 0 {
		return pinRef{}, fmt.Errorf("%w: %q: want an IPv4 address and a port from 1 to 65535", ErrMalformed, entry)
	}
	b, err := hex.DecodeString(key)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return pinRef{}, fmt.Errorf("%w: %q: want a key of %d bytes in hex", ErrMalformed, entry, ed25519.PublicKeySize)
	}
	return pinRef{Addr: addr, Key: b}, nil
}

// loadPins returns the pins of -pin, then those of each -pin-file, without exact repeats; a key at two addresses is refused.
func (c Config) loadPins() ([]pinRef, error) {
	var pins []pinRef
	add := func(entry string) error {
		p, err := parsePin(entry)
		if err != nil {
			return err
		}
		if i := slices.IndexFunc(pins, func(q pinRef) bool { return q.Key.Equal(p.Key) }); i >= 0 {
			if pins[i].Addr != p.Addr {
				return fmt.Errorf("%w: the pin %x is listed at two addresses", ErrMalformed, []byte(p.Key))
			}
			return nil
		}
		pins = append(pins, p)
		return nil
	}
	for _, e := range c.Pins {
		if err := add(e); err != nil {
			return nil, &ConfigError{Setting: "pin", Err: err}
		}
	}
	for _, path := range c.PinFiles {
		b, err := readSmallFile(path)
		if err != nil {
			return nil, &ConfigError{Setting: "pin-file", Err: fmt.Errorf("%s: %w", path, err)}
		}
		entries := 0
		for n, line := range slices.Collect(strings.Lines(strings.TrimPrefix(string(b), "\xef\xbb\xbf"))) {
			line, _, _ = strings.Cut(line, "#")
			for _, e := range splitList(line) {
				entries++
				if err := add(e); err != nil {
					return nil, &ConfigError{Setting: "pin-file", Err: fmt.Errorf("%s line %d: %w", path, n+1, err)}
				}
			}
		}
		if entries == 0 {
			return nil, &ConfigError{Setting: "pin-file", Err: fmt.Errorf("%w: %s lists no pin", ErrMalformed, path)}
		}
	}
	return pins, nil
}

// loadRelays returns the relays of -relay and of each -relay-file, or of KOLO_GROUP_RELAYS, checked as one list by
// kolo-swarm, and where they came from.
func (c Config) loadRelays() ([]koloswarm.Relay, string, error) {
	where := ""
	if c.relaysEnv {
		where = "KOLO_GROUP_RELAYS: "
	}
	relays, err := koloswarm.ParseRelays(strings.Join(c.Relays, ","))
	if err != nil {
		return nil, "", &ConfigError{Setting: "relay", Err: relayError(err, where)}
	}
	for _, path := range c.RelayFiles {
		rs, err := koloswarm.LoadRelays(path)
		if err == nil { // checked again with the relays before it: no key or address twice
			rs, err = koloswarm.ParseRelays(relaysText(slices.Concat(relays, rs)))
		}
		if err != nil {
			return nil, "", &ConfigError{Setting: "relay-file", Err: relayError(err, path+": ")}
		}
		relays = rs
	}
	var from []string
	switch {
	case len(relays) == 0:
	case c.relaysEnv:
		from = append(from, "KOLO_GROUP_RELAYS")
	default:
		if len(c.Relays) > 0 {
			from = append(from, "-relay")
		}
		if len(c.RelayFiles) > 0 {
			from = append(from, "-relay-file")
		}
	}
	return relays, strings.Join(from, ", "), nil
}

// relaysText writes relays as a comma list that ParseRelays reads.
func relaysText(relays []koloswarm.Relay) string {
	out := make([]string, len(relays))
	for i, r := range relays {
		out[i] = r.Addr + "@" + hex.EncodeToString(r.PublicKey)
	}
	return strings.Join(out, ",")
}

// relayError is an error of kolo-swarm's relay or entry node parsing as the cause of a ConfigError: ErrMalformed for a
// bad entry, and the file's own error for a file that cannot be read.
func relayError(err error, where string) error {
	if !errors.Is(err, koloswarm.ErrInvalidArgument) {
		if inner := errors.Unwrap(err); inner != nil {
			err = inner
		}
		return fmt.Errorf("cannot be read: %w", err)
	}
	msg := strings.TrimPrefix(strings.TrimPrefix(err.Error(), "koloswarm: "), "invalid argument: ")
	return fmt.Errorf("%w: %s%s", ErrMalformed, where, msg)
}

// readSmallFile reads a regular file of at most maxConfigFile bytes. It checks the path before opening it, so a device
// or a FIFO is never opened.
func readSmallFile(path string) ([]byte, error) {
	if st, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("cannot be read: %w", err)
	} else if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: not a regular file", ErrMalformed)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot be read: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxConfigFile+1))
	if err == nil && len(b) > maxConfigFile {
		err = fmt.Errorf("%w: larger than %d bytes", ErrMalformed, maxConfigFile)
	}
	return b, err
}
