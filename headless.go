package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ciranda-dev/kolo-group-wails3-example/internal/room"
)

// welcomePoll is how often -welcome-file is read while the device runs.
const welcomePoll = 2 * time.Second

// runHeadless runs a device with no window until ctx ends: a member that stays online and holds the rooms it was added to.
// Once an admin promotes it, it also writes the segments of those rooms and pushes them to the pins.
func runHeadless(ctx context.Context, cfg Config) error {
	d, err := startHeadless(ctx, cfg, nil, os.Stdout)
	if err != nil {
		return err
	}
	<-ctx.Done()
	return d.Close()
}

// startHeadless opens the device, says who it is and what it holds, and starts to read the welcome file. A device with no
// room writes a key package to out, for an admin to add.
func startHeadless(ctx context.Context, cfg Config, tune tuning, out io.Writer) (*device, error) {
	set, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	d, err := openDevice(ctx, set, headlessEvent, tune)
	if err != nil {
		return nil, err
	}
	log.Printf("kolo-group headless: person %x, device %x", d.person.PublicKey(), d.key.PublicKey())
	for _, r := range d.rooms() {
		log.Printf("room %q %s: epoch %d, %d devices, admin %v", r.Name, r.ID[:8], r.Epoch, r.Members, r.Admin)
	}
	if len(d.rooms()) == 0 {
		kp, err := d.keyPackage()
		if err != nil {
			d.Close()
			return nil, err
		}
		fmt.Fprintln(out, "no room yet: give this key package to an admin, who adds it and gives back a welcome")
		fmt.Fprintln(out, kp)
	}
	if cfg.WelcomeFile != "" {
		d.wg.Go(func() { d.joinWelcomes(cfg.WelcomeFile) })
	}
	return d, nil
}

// headlessEvent logs what happens to the rooms of a headless device, never a message.
func headlessEvent(name string, data any) {
	if ev, ok := data.(EventInfo); ok && (ev.Kind == "removed" || ev.Kind == "members") {
		log.Printf("room %s: %s", ev.Room[:8], ev.Kind)
	}
}

// joinWelcomes joins the room of the welcome in the file each time the file holds one that was not tried before. A welcome
// is tried once: a key package is used by the first try, so a try that failed is not repeated, and the admin adds the
// device again.
func (d *device) joinWelcomes(path string) {
	reported := ""
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(welcomePoll):
		}
		text, err := readWelcome(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && reported != err.Error() {
				reported = err.Error()
				log.Printf("welcome file: %v", err)
			}
			continue
		}
		sum := sha256.Sum256([]byte(text))
		id := hex.EncodeToString(sum[:])
		if d.usedWelcome(id) {
			continue
		}
		d.markWelcome(id)
		w, err := decodeText(prefixWelcome, text)
		if err == nil {
			log.Print("welcome file: joining")
			var r *room.Room
			if r, err = d.join(w); err == nil {
				log.Printf("room %q joined", r.Name())
				continue
			}
		}
		log.Printf("welcome file: %v", err)
	}
}

// readWelcome reads the welcome file, which must be a regular file.
func readWelcome(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("kolo-group: the welcome file is not a regular file")
	}
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

func (d *device) welcomesPath() string { return filepath.Join(d.set.DataDir, "welcomes") }

// usedWelcome says whether a welcome with this hash was joined before.
func (d *device) usedWelcome(id string) bool {
	b, _ := os.ReadFile(d.welcomesPath())
	return slices.Contains(strings.Fields(string(b)), id)
}

func (d *device) markWelcome(id string) {
	f, err := os.OpenFile(d.welcomesPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("welcome file: %v", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, id)
}
