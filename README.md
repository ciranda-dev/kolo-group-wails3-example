<p align="center"><img src="docs/img/logo.png" width="128" height="128" alt="Kolo Group logo"></p>

<h1 align="center">Kolo Group</h1>

<p align="center"><b>An example desktop group chat for Kolo, in progress: today it opens a window with an empty room list.</b></p>

<p align="center"><a href="docs/how-it-works.md">Docs</a> · <a href="CHANGELOG.md">Changelog</a></p>

<p align="center"><a href="CHANGELOG.md"><img src="https://img.shields.io/badge/status-experimental-red" alt="status: experimental"></a> <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="license: Apache-2.0"></a> <a href="go.mod"><img src="https://img.shields.io/badge/go-1.26%2B-00ADD8" alt="go: 1.26+"></a> <a href="go.mod"><img src="https://img.shields.io/badge/wails-3-DF0000" alt="wails: 3"></a> <a href="#platforms"><img src="https://img.shields.io/badge/platforms-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey" alt="platforms: macOS | Windows | Linux"></a></p>

This repository is private and experimental. It builds only inside the Kolo workspace (a folder with a `go.work` file that lists the `kolo-*` checkouts), because `kolo-mls` and `kolo-gossip` have no release yet.

## What it does

- **Show the app window** with its title and an empty list of rooms.
- **Take two flags**: `-data` for the data folder and `-headless`, which fails with an error until headless mode is built.
- **Run a room in `internal/room`** (tested, not yet used by the window): create a room, add, remove and promote members through settled Commits, send messages sealed with a key of the MLS epoch over gossip, and read them in causal order. Admins keep the messages in their own logs, pins mirror them, members serve them to each other, and a device that was offline catches up from them, also after a restart. Only member devices connect, and a removed device reads nothing sent after its removal.
- **Keep history for new members, read-only members**: planned, none of it is built. [How it works](docs/how-it-works.md) says what is built and what is planned.

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
./bin/kolo-group -data "$(mktemp -d)"
```

`wails3 build` runs `go mod tidy`, which cannot find kolo-mls and kolo-gossip until they have a release, so the first three commands are the steps it runs, without that one. The window shows the title and an empty room list. Tests: `go test ./...`. Live reload: `KOLO_GROUP_DATA="$(mktemp -d)" wails3 dev`.

## How it uses Kolo

The libraries below are the direct requirements of `go.mod`. `internal/room` uses them; the window does not call it yet.

| Kolo library | What the app will use it for |
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
main.go, config.go         the window and the flags
groupservice.go            the Wails service the window calls
headless.go                the -headless stub
internal/room/             the room: Node, Room, sealing, order, admission, segments, catch-up
frontend/                  the window (Vite)
build/, scripts/           icons, installers and release scripts
docs/                      how it works, security, limits, build and test
```

## Learn more

- [How it works](docs/how-it-works.md), [security](docs/security.md), [limits](docs/limits.md), [build and test](docs/build-and-test.md)
- [Changelog](CHANGELOG.md), [security policy](SECURITY.md), license: [Apache-2.0](LICENSE)
