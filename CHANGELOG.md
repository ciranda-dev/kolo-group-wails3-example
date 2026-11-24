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

- Admin segments in `internal/room`: every admin device writes the messages of the room into its own kolo-log segments (one epoch, at most `SegmentEntries` entries each) and a control log (Commits, SettledCommits, the opening and closing of segments, the control logs of other admins), and pushes them to the pins with kolo-pin. Members follow those logs with kolo-sync, derive their entry keys and serve what they hold to the other members of the room.
- `settle.Pins` over the control logs (`roomPins`), with the new `List` (paged and ordered) and `Delete` of kolo-mls.
- Catch-up: a device that was away applies the settled Commits it missed epoch by epoch and reads the segments from the admins, from other members and from the pins; an entry it cannot verify or open is reported with `ErrCatchUp` and never skipped without a report.
- Stored receipts and republishing: a message sent while no admin is online is kept until a segment holds it, shows `Stored` when one does, and is published again meanwhile; `Status` reports receipts, followed logs, catching up and admins online.
- Persistence: rooms, MLS state, epoch keys, authors, receipts, logs and the admins' braid are reopened by `Open`; a removed device forgets its room.
- `Promote` makes a second admin through the admins' braid and a settled Commit that changes the roles.
- One connection for gossip, kolo-sync and the braid, with a tag byte on each stream.
- Protective bounds in `room.Limits` with their basis in `docs/limits.md`: `SegmentEntries`, `KeepSegments`, `MaxLogs`, `PinGap`, `PinTTL`, `PinWait`, `CatchUpWait`, `RepublishAfter` and `MaxReceipts`.
- Tests: segments, catch-up, restart and pins at 3 and 4 devices, a run of 16 and one of 64 devices with 2 admins, 2 pins, an admin killed in the middle of an epoch and 10,000 messages over three epochs, hostile segments (a bad signature, a key that does not open, an unknown author, a gap, a rewritten history, a bad SettledCommit), the decoders and their fuzz tests.

- History for new members in `internal/room`: an admin writes, for each epoch it enters, a history link (the key of the epoch before, sealed under the key of the epoch) to its control log, and `Room.History` reads the messages of the epochs before the join, backward, through the chain of links and the admins' segments (from the admins, the members or the pins). A message that cannot be read is reported with `ErrCatchUp` and its cause, a chain that cannot go on with `ErrHistory`, a refused link with `ErrLink`.
- The history rule (`AllHistory`, the default, `SinceEpoch` or `NoHistory`) is chosen in `Create` and recorded in a GroupContext extension (0xF003) that the settled Commits sign and the Welcomes carry. No link is written for an epoch the rule leaves out, a link written after a segment of its epoch is refused, and a rule is never retroactive.
- Tests: a joiner of epoch 40 reads epochs 1 to 39, `SinceEpoch`, `NoHistory`, a member removed at epoch 20, late, out-of-policy, forged and missing links, undecryptable entries, a message of a device that left before the join, a restart, the pins, and fuzz tests of the new decoders. `docs/security.md` states the cost in forward secrecy and `docs/limits.md` the measured cost of the links.

### Changed

- `Create` accepts `Policy.History` and `Policy.Since`; it still refuses posters and a read link. A key package lists the policy extension among its capabilities.
- A message sealed in an epoch before the join never shows in `Next`, even when an admin stored it in a later segment: it belongs to `History`.

- A sealed message carries the signature of its author, and the id of a message is a hash of what the author signed, the same for every holder of a copy (it was gossip's id). The id of a message sent by an older build differs.
- A device publishes at most four fifths of `Rate`, waiting for the rest, and a message whose time is older than its envelope's is accepted (`MaxSkew` bounds only a time ahead).
- The validator accepts a message of an older epoch from a current member when the node holds that epoch's key.
- `room.Config` requires `Store` (the logs) and takes `Pins`; `Open` reopens the rooms of its backend.
- The kolo-mls interface `settle.Pins` is the paged one (`List(ctx, prefix, after, max)` and `Delete`); `go.mod` requires kolo-secure and kolo-transport directly.

- `room.Config` has a `Backend` (the store of the admins' braid and of the Commits of the rooms this device creates) and `BraidLimits`. `Create` refuses a policy other than the default and `Join` takes the welcome that `Add` returns.
- The docs say what is built and what is planned: history for new members, `Promote` to Settler, policy, the read link, `Leave`, Commits by reference, the window and `-headless` are not built.
- `go.mod` requires kolo-braid, kolo-codec, kolo-log, kolo-sync and golang.org/x/crypto directly.
