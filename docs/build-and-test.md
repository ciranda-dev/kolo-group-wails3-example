# Build and test

The repository has no `go.work` and no `replace`, so use `GOWORK=off` when this folder sits inside a Go workspace.

```sh
gofmt -l .
go vet ./...
go test -p 2 ./...
wails3 generate bindings -clean=true -time-type=Date
(cd frontend && npm ci && npm run build)
(cd frontend && npm test && npm run lint)
go build -tags production -o bin/kolo-group .
```

The generate, build and go build commands are the steps `wails3 build` runs.

## What the tests cover

- `build_test.go`: the development version in the build files, `scripts/set-version.sh`, the version in every build task and the installer files.
- `config_test.go`: the flags, the config file and the order flag, file, variable, default, the checks of pins, relays and entry nodes, and the data folder (private, one layout).
- `service_test.go`: the window's service over real devices on 127.0.0.1 and one entry node: two devices that talk and reply, what the window keeps across a restart, posters and policy changes, the history scrolled back, promoting, removing and leaving, a read link and its reader, the status, one action at a time and its cancel, the checks of the texts and arguments, and the member pages. `headless_test.go`: a headless device that joins from a welcome file, becomes an admin, pushes to a pin and serves a member that was away, the welcome used once, and one data folder run by the window and by `-headless`. `messages_test.go`: the messages the window keeps, and that no payload is `null`. `thirtyk_test.go`: the target of the 30,000-member tests.
- `frontend/src`: `npm test` runs `node --test` on the pure parts (the copy in three languages, the reducer, the groups of messages, the policy form, the member pages, the pasted texts, the content security policy) and `window.test.js`, which opens the window with `frontend/dev/mock.js` in place of Go in a headless Google Chrome on 127.0.0.1 (skipped without Chrome): the rooms, a reply, sending, a read-only room, a room of 30,000 devices read a page at a time, the dialogs, the settings, a failed start and the other two languages. `npm run lint` checks that every copy key exists and that the page keeps the content security policy.
- `readme_test.go`: the links of the README and the docs, and the quick start: each command of the README is read with the flags of the app and run (a window device and a headless device with the README's config) against an entry node on 127.0.0.1.
- `internal/room`: the live room on 127.0.0.1 with real swarm connections (`live_test.go`, `scale_test.go` for 64 devices), hostile messages (`hostile_test.go`), the order of messages (`order_test.go`), the decoders and their fuzz tests (`wire_test.go`, `wire_segments_test.go`), and the methods that are not built (`misc_test.go`). The segments, catch-up, receipts, restart, pins and promotion are in `segments_test.go` and `behavior_test.go`, hostile segments in `hostile_segments_test.go`, and the runs of 16 and 64 devices with two admins and two pins in `scale_segments_test.go`. The runs of 16 and 64 devices are skipped with `-short`; they run many swarms in one process, so check `uptime` first and run them when the load is under 10 (`nice go test -p 2 -run CatchUpWithSixtyFour ./internal/room`). The history chain is in `history_test.go` (a joiner of epoch 40, the policies, a removed member, late and forged links, undecryptable entries, a restart, the pins) and its decoders and fuzz tests in `history_wire_test.go`; the 40-epoch test takes minutes under a load of 10 or more. Commits by reference are in `commitref_test.go` (pieces, a cut or wrong Commit, a Commit of a large group reaching 64 members) and the tree log, the joiner's fetch and the retention in `treeserve_test.go`; `large_test.go` builds a group of `KOLO_GROUP_LEAVES` leaves in one process (1,024 by default; 30,000 for the measurements of `docs/limits.md`, which takes about 13 seconds to build) and `KOLO_GROUP_JOINERS` sets the joiners of the tree test (16 below 30,000 leaves, 100 at 30,000) and `KOLO_GROUP_FETCHERS` how many of them fetch at a time (8). The posters, `SetPolicy` and its history rule are in `posters_test.go`, the read link and the reader in `reader_test.go`, leaving and the removed device that learns it was removed in `leave_test.go`, the push of a segment in `pinning_test.go`, and the decoders of the policy, the state and the link with their fuzz tests in `policy_test.go`. `measure_test.go` measures segment sizes, the cost of the links and of the policy, the state and the read link with `KOLO_GROUP_MEASURE=1`.

Run a fuzz test for a while with `go test -run xxx -fuzz FuzzDecodeContent -fuzztime 30s ./internal/room`.

## The 30,000-member tests

`task test-30k` (or `scripts/test-30k.sh`; `--dry-run` prints the commands) runs three steps and is the only place they run: no workflow does, since they need the Kolo workspace with a kolo-gossip checkout, about 13 GB of memory and hours. It checks the load first (under 10, or `KOLO_GROUP_FORCE=1`).

1. The gossip simulator of kolo-gossip at 30,000 nodes, scenarios S1, S2, S3, S7, S9, S10 and M1 (`KOLO_GOSSIP_DIR` names the folder, `KOLO_GOSSIP_MAXHEAP` stops the run past a heap, 13.2 GB by default, twice the 6.6 GB the simulator held at that size). The command exits 1 when a scenario fails its criteria.
2. `TestMeasureReaders`: the sizes of a room of 100,000 readers.
3. The room tests of 30,000 leaves built in one process, with `KOLO_GROUP_LEAVES` and `KOLO_GROUP_JOINERS` (30000 and 100): `TestLargeCommitReachesSixtyFourMembers` and `TestManyJoinersFetchALargeTreeFromThreeDevices`. They come with the commit-by-reference change of `internal/room`: a run matches no test without them, so the script stops first and says so.

## Release files

`scripts/release-local.sh` builds the macOS, Windows and Linux files from the committed HEAD, with Docker for Linux. It needs the published libraries, so it fails until kolo-mls and kolo-gossip have a release.
