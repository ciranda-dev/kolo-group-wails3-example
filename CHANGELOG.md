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

- Posters in `internal/room`: the room policy (the GroupContext extension 0xF003, signed by every settled Commit) says who may post: every member (the default), `Policy.Posters` (persons), `Policy.PosterDevices`, `Policy.AdminsPost`, or a union. A post by a device that may not post is refused with `ErrNotPoster` by `Send`, by the validator of every node (verdict Reject) and by an admin's segment writer, and reported by a reader of a segment. A member that may not post is a read-only member: it reads what its history rule allows, live and before its join. A message is judged by the policy of its own epoch.
- `SetPolicy` and `Room.Policy`: an admin changes the posters, the history rule and the read link with one settled Commit, for the epochs from that Commit on and never before. kolo-mls accepts the change of the extension, so no library change was needed. A Commit that does not fit one gossip message is refused with `ErrTooLarge` before it is made, for `SetPolicy` and for `Promote`.
- The read link: `Room.Link` and `Node.JoinLink`. A room with a read link (it needs a list of posters or `AdminsPost`) gives a link; a device that joins with it reads the room live and its history through the same chain of links as a member, is not a leaf of the tree and cannot post. Each admin writes the state of every epoch (the key, the roles, the policy and the certificates of the admins and posters) to its control log, sealed under a key derived from a read secret, in a new control record `ctlRead`. Turning the read link off and on again with `SetPolicy` makes a new link and stops the readers of the old one at the next epoch. A node that has a room with a read link, or reads one, admits any device. A reader room is not kept across a restart. `docs/security.md` says that a public read link gives no secrecy against anyone who holds it.
- `Room.Leave`: a member asks the admins to remove its person (one admin answers, so two admins make one Commit), waits until they have or until its context ends, and stops the room on the device. A reader's `Leave` only stops and forgets its room.
- Tests: a non-poster refused at the validators and at an admin, a read-only member that reads live and history, `SetPolicy` holding from the next epoch and not before (also across a restart), a history rule changed by a Commit that is never retroactive, a policy that does not fit, a reader that joins by a link, reads across epochs and history and cannot post, a new link that stops the old reader, a bad link, `Leave` with one admin, with two and with none, the removed device that learns it was removed, the segment that is pushed while it is forgotten, and fuzz tests of the new decoders (the policy, the state, the link, the opened state and the new control record). `docs/limits.md` has the measured sizes.

- The window: create a room (name, history rule, who may post, read link), the list of rooms, the conversation with replies, sending and the order of `Next`, the messages kept across a restart, scrolling back (the messages this device was told of, then the history before the join), members a page at a time with promoting, removing and choosing who may post, adding devices by key package and welcome, the read link, the status of a room (epoch, catching up, pins, devices connected), room settings (the policy) and leaving, and the settings of the app (network, pins, relays, keys). The frontend is Vite and React in English, Spanish and Portuguese, in the style of the other Kolo apps.
- `-headless`: an always-on device with no window over the same data folder, with a JSON config file (`-config`) and the flags and variables of the other apps (`-bootstrap`, `-listen`, `-pin`, `-pin-file`, `-pin-ttl`, `-lookup-interval`, `-relay`, `-relay-file`). It prints a key package when it has no room, joins the welcome in `-welcome-file`, and, once an admin promotes it, writes the segments of its rooms and pushes them to the pins.
- `task test-30k` (`scripts/test-30k.sh`): the gossip simulator at 30,000 nodes, `TestMeasureReaders` and the room tests of a 30,000-leaf group, in one documented target that no workflow runs.
- Tests: the window's service over real devices on 127.0.0.1 (talking and replying, what the window keeps across a restart, posters and policy changes, history scrolled back, promoting, removing, leaving, the read link and its reader, status, one action at a time and its cancel), a headless device that joins from a file, becomes an admin, pushes to a pin and serves a member that was away, one data folder run by the window and by `-headless`, the config file and its order, the README's quick start run for real, and `npm test` with a window test in headless Chrome that includes a room of 30,000 devices read a page at a time.

### Changed

- **Breaking**: `GroupService.Rooms` returns the rooms and an error, `Start` must be called first, and `-headless` no longer returns an error. The data folder has a `layout.json` ({"layout":1}), a person key and a device key, a certificate and four stores; a folder of another layout is refused.
- `go.mod` requires kolo-relay directly (the window reads the state of the connection relays).

- `Create` accepts the whole `Policy`; `Policy` gains `PosterDevices` and `AdminsPost`. The policy extension of an earlier build (history rule only) does not decode: create the room again. The history rule under `AllHistory` now carries the epoch the chain starts at, which `SetPolicy` sets, and a device reads its past by the rule it joined under. A link of an epoch before the join that the rule of the join leaves out is dropped without a `ErrLink` report. A key package lists the policy extension among its capabilities.
- The members close their connections to a removed device after `PastEpoch` instead of at once, and refuse a new connection from it at once.
- A message sealed in an epoch before the join never shows in `Next`, even when an admin stored it in a later segment: it belongs to `History`.

- A sealed message carries the signature of its author, and the id of a message is a hash of what the author signed, the same for every holder of a copy (it was gossip's id). The id of a message sent by an older build differs.
- A device publishes at most four fifths of `Rate`, waiting for the rest, and a message whose time is older than its envelope's is accepted (`MaxSkew` bounds only a time ahead).
- The validator accepts a message of an older epoch from a current member when the node holds that epoch's key.
- `room.Config` requires `Store` (the logs) and takes `Pins`; `Open` reopens the rooms of its backend.
- The kolo-mls interface `settle.Pins` is the paged one (`List(ctx, prefix, after, max)` and `Delete`); `go.mod` requires kolo-secure and kolo-transport directly.

- `room.Config` has a `Backend` (the store of the admins' braid and of the Commits of the rooms this device creates) and `BraidLimits`. `Join` takes the welcome that `Add` returns.
- The docs say what is built and what is planned: `Promote` to Settler, Commits by reference, the window and `-headless` are not built.
- `go.mod` requires kolo-braid, kolo-codec, kolo-log, kolo-sync and golang.org/x/crypto directly.

### Fixed

- A device that an admin removes now learns that it was removed. The other members closed their connections to it as soon as they applied the Commit, often before the Commit reached it, so it never set its removed state (in a room that had passed 8 epochs this happened in 5 of 10 runs; with the delay in none of 15, `TestRemovedDeviceLearnsItWasRemoved`). The Commit itself is the signed notice; no other message is needed.
- An admin with few kept segments (`KeepSegments`) no longer forgets a segment while it is pushed to the pins. The push took its logs out of the list of logs to push and the trim of old segments then closed one of them; the push failed and was tried again for ever, and `Status().PinError` stayed at "pin: no pinning peer accepted the log" (5 of 15 runs with `KeepSegments: 1` and two pins; `TestSegmentBeingPushedIsNotForgotten` fails on the old code every time).
