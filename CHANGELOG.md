# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The live room in `internal/room`: `Open`, `Create`, `KeyPackage`, `Add`, `Join`, `Remove`, `Send`, `Next`, `Members`, `Allow` and `ServeConn`. A room is an MLS group whose Commits the admin settles through kolo-braid and sends to the members on the room's gossip topic. Messages are sealed with a key exported from the MLS epoch, travel as signed gossip messages, and show in the causal order of the ids they name (`Display`).
- Admission: `Node.Allow` for the swarm lets in only the devices of a room's current epoch, and `ServeConn` refuses and closes the others. A removed device is disconnected, refused after a short window and reads nothing sent after its removal.
- Protective bounds in `room.Limits`, each configurable with a written basis in `docs/limits.md`: message size, rate, parents, hold, queues, window and tree size.
- `ServeConn` serves gossip on the unidirectional streams of a swarm connection and kolo-sync on its bidirectional ones.
- Tests: rooms of 3 and 64 devices on 127.0.0.1 over real swarm connections, the removal of a device, admission, hostile messages (wrong epoch, forged sender, replay, oversized body, flood), the order of messages with property tests, and fuzz tests of every decoder.

### Changed

- `room.Config` has a `Backend` (the store of the admins' braid and of the Commits of the rooms this device creates) and `BraidLimits`. `Create` refuses a policy other than the default and `Join` takes the welcome that `Add` returns.
- The docs say what is built and what is planned: the segments, catch-up, history for new members, roles and policy, the read link, `Leave`, the window and `-headless` are not built.
- `go.mod` requires kolo-braid, kolo-codec, kolo-log, kolo-sync and golang.org/x/crypto directly.
