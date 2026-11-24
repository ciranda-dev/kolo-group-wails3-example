# Build and test

Run the commands in this folder while it sits in the Kolo workspace. `GOWORK=off` does not work yet: kolo-mls and kolo-gossip have no release.

```sh
gofmt -l .
go vet ./...
go test -p 2 ./...
wails3 generate bindings -clean=true -time-type=Date
(cd frontend && npm ci && npm run build)
go build -tags production -o bin/kolo-group .
```

`wails3 build` runs `go mod tidy`, which cannot find kolo-mls and kolo-gossip until they have a release. The last three commands are the steps it runs, without that one.

## What the tests cover

- `build_test.go`: the development version in the build files, `scripts/set-version.sh`, the version in every build task and the installer files.
- `config_test.go` and `groupservice_test.go`: the flags, the environment default, the service and the headless stub.
- `readme_test.go`: the links of the README and the docs, and the commands of the README.
- `internal/room/room_test.go`: every method that is not built returns `room: not implemented`.

## Release files

`scripts/release-local.sh` builds the macOS, Windows and Linux files from the committed HEAD, with Docker for Linux. It needs the published libraries, so it fails until kolo-mls and kolo-gossip have a release.
