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
- `internal/room`: the live room on 127.0.0.1 with real swarm connections (`live_test.go`, `scale_test.go` for 64 devices), hostile messages (`hostile_test.go`), the order of messages (`order_test.go`), the decoders and their fuzz tests (`wire_test.go`, `wire_segments_test.go`), and the methods that are not built (`misc_test.go`). The segments, catch-up, receipts, restart, pins and promotion are in `segments_test.go` and `behavior_test.go`, hostile segments in `hostile_segments_test.go`, and the runs of 16 and 64 devices with two admins and two pins in `scale_segments_test.go`. The runs of 16 and 64 devices are skipped with `-short`; they run many swarms in one process, so check `uptime` first and run them when the load is under 10 (`nice go test -p 2 -run CatchUpWithSixtyFour ./internal/room`). The history chain is in `history_test.go` (a joiner of epoch 40, the policies, a removed member, late and forged links, undecryptable entries, a restart, the pins) and its decoders and fuzz tests in `history_wire_test.go`; the 40-epoch test takes minutes under a load of 10 or more. The posters, `SetPolicy` and its history rule are in `posters_test.go`, the read link and the reader in `reader_test.go`, leaving and the removed device that learns it was removed in `leave_test.go`, the push of a segment in `pinning_test.go`, and the decoders of the policy, the state and the link with their fuzz tests in `policy_test.go`. `measure_test.go` measures segment sizes, the cost of the links and of the policy, the state and the read link with `KOLO_GROUP_MEASURE=1`.

Run a fuzz test for a while with `go test -run xxx -fuzz FuzzDecodeContent -fuzztime 30s ./internal/room`.

## Release files

`scripts/release-local.sh` builds the macOS, Windows and Linux files from the committed HEAD, with Docker for Linux. It needs the published libraries, so it fails until kolo-mls and kolo-gossip have a release.
