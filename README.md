<p align="center"><img src="docs/img/logo.png" width="128" height="128" alt="Kolo Group logo"></p>

<h1 align="center">Kolo Group</h1>

<p align="center"><b>An example desktop group chat for Kolo: end-to-end encrypted, live over gossip, kept for the offline on pins, with the history for new members.</b></p>

<p align="center"><a href="docs/how-it-works.md">Docs</a> · <a href="CHANGELOG.md">Changelog</a></p>

<p align="center"><a href="CHANGELOG.md"><img src="https://img.shields.io/badge/status-experimental-red" alt="status: experimental"></a> <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="license: Apache-2.0"></a> <a href="go.mod"><img src="https://img.shields.io/badge/go-1.26%2B-00ADD8" alt="go: 1.26+"></a> <a href="go.mod"><img src="https://img.shields.io/badge/wails-3-DF0000" alt="wails: 3"></a> <a href="#platforms"><img src="https://img.shields.io/badge/platforms-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey" alt="platforms: macOS | Windows | Linux"></a></p>

This repository is private and experimental. It builds only inside the Kolo workspace (a folder with a `go.work` file that lists the `kolo-*` checkouts), because `kolo-mls` and `kolo-gossip` have no release yet.

## What it does

- **Create a room** with a name, a history rule for new members (all, from now on, or none) and who may post (every member, only admins, or chosen members), with a read link if you want a broadcast room.
- **Talk live**: a room list, the conversation with replies, and the order every member sees the same. Messages are sealed with a key of the MLS epoch and travel over gossip; admins keep them for the people who are offline and pins mirror them.
- **Add people**: a device makes a key package, an admin adds it and gives back a welcome, and the device joins. A read link lets anyone read a broadcast room and cannot post.
- **Manage the room**: members and admins (make a device an admin, remove a person, choose who may post), the room's status (epoch, catching up, pins, devices connected), settings with the pins, entry nodes and relays the app was started with, and leaving.
- **Scroll back**: the messages this device was told of, and, for a member who joined late, the history before the join as far as the room's rule allows.
- **Run with no window**: `-headless` is an always-on device over the same data folder. Made an admin, it writes the segments of its rooms and pushes them to the pins, so a room can be read by people who were away while every person's computer is off.
- **Run the room code** in `internal/room`: the live room, segments, catch-up, history, posters, the read link and leaving. [How it works](docs/how-it-works.md) says what is built and what is planned.

## Platforms

| Platform | Install | Version | Status |
|---|---|---|---|
| macOS (universal) | `.dmg` or `.zip` | none | Not released yet |
| Windows (amd64, arm64) | installer `.exe`, `.exe` or `.zip` | none | Not released yet |
| Linux (amd64, arm64) | `.deb` or `.tar.gz` | none | Not released yet |

There is no release yet. Build your own with `scripts/release-local.sh` (see [build and test](docs/build-and-test.md)).

## Run it

You need Go 1.26.0 or later, Node.js with npm, and the Wails 3 CLI (`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0`). Run the commands in this folder while it sits in the Kolo workspace.

```sh
wails3 generate bindings -clean=true -time-type=Date
(cd frontend && npm ci && npm run build)
go build -tags production -o bin/kolo-group .
```

`wails3 build` runs `go mod tidy`, which cannot find kolo-mls and kolo-gossip until they have a release, so these three commands are the steps it runs, without that one. Tests: `go test -p 2 ./...` and `(cd frontend && npm test)`. Live reload: `KOLO_GROUP_DATA="$(mktemp -d)" wails3 dev`.

### Quick start on one computer

Start an entry node for the two windows (`kolo-node run -listen 127.0.0.1:7447 -identity /tmp/kolo-group-demo/node.key -first`), then two people:

```sh
./bin/kolo-group -data "$(mktemp -d)" -bootstrap 127.0.0.1:7447
./bin/kolo-group -data "$(mktemp -d)" -bootstrap 127.0.0.1:7447
```

In the first window choose "+", then Create a room. In the second choose "+", Join a room, and Make a key package; copy it. In the first, open Add, paste the key package and Add: copy the welcome it shows and paste it in the second window's Join. Both windows are now in the room: send a message, reply to one, and open Members and Status.

### An always-on admin

`-headless` runs the same app with no window, from flags or a JSON config file (the keys are the names of the flags: `data`, `bootstrap`, `listen`, `pin`, `pin-file`, `pin-ttl`, `lookup-interval`, `relay`, `relay-file`, `welcome-file`). Precedence: flag, then file, then `KOLO_GROUP_*` variable, then default.

```json
{
  "bootstrap": "127.0.0.1:7447",
  "listen": "127.0.0.1:0",
  "welcome-file": "welcome.txt"
}
```

```sh
./bin/kolo-group -headless -data "$(mktemp -d)" -config headless.json
```

With no room yet it prints a key package. Add it to a room in a window, write the welcome to `welcome.txt` (the app reads it every 2 seconds and joins once), then make the device an admin in the member list. Run it as a service on a computer that stays on, with the folder of a real `-data` and, for people who are offline, `-pin` (see [configuration](docs/how-it-works.md#configuration)).

## How it uses Kolo

The libraries below are the direct requirements of `go.mod`. `internal/room` composes them and the window and `-headless` run it.

| Kolo library | What the app uses it for |
|---|---|
| [kolo-mls](https://github.com/ciranda-dev/kolo-mls) | end-to-end encryption of each room by MLS (RFC 9420) |
| [kolo-gossip](https://github.com/ciranda-dev/kolo-gossip) | live delivery of signed messages to the members online |
| [kolo-braid](https://github.com/ciranda-dev/kolo-braid) | the admins' order of the changes of a room |
| [kolo-sync](https://github.com/ciranda-dev/kolo-sync) | replication of the admins' logs between the devices of a room |
| [kolo-log](https://github.com/ciranda-dev/kolo-log) | the admins' control logs and segments |
| [kolo-swarm](https://github.com/ciranda-dev/kolo-swarm) | connections between devices |
| [kolo-pin](https://github.com/ciranda-dev/kolo-pin) | copies of the admins' logs for people who are offline |
| [kolo-store](https://github.com/ciranda-dev/kolo-store) | the app's local storage |
| [kolo-id](https://github.com/ciranda-dev/kolo-id) | the identity and device certificate of each person |

## Project layout

```text
main.go, config.go         the window, the flags and the config file
groupservice.go, rooms.go  the Wails service the window calls and what it shows
device.go, messages.go     the device (keys, stores, swarm, node) and the messages the window was told of
headless.go, relay.go      the -headless mode and the state of the connection relays
internal/room/             the room: Node, Room, sealing, order, admission, segments, catch-up
frontend/                  the window (Vite and React, in English, Spanish and Portuguese)
build/, scripts/           icons, installers, release scripts and the 30,000-member tests
docs/                      how it works, security, limits, build and test
```

## Learn more

- [How it works](docs/how-it-works.md), [security](docs/security.md), [limits](docs/limits.md), [build and test](docs/build-and-test.md) (the 30,000-member tests run with `task test-30k`)
- [Changelog](CHANGELOG.md), [security policy](SECURITY.md), license: [Apache-2.0](LICENSE)
