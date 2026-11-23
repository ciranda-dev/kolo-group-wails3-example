# Limits

The room keeps only protective bounds: each one stops a hostile or broken peer from making a node use memory, time or bandwidth without end. None caps the number of members, messages or history. Every bound below is a field of `room.Limits` (a zero field takes its default, a negative one is refused), so a node can raise or lower it. The defaults come from a number another Kolo library already measured, or from a measurement of this app; a bound with no source says so.

## Bounds of the live room

| Bound | Default | What it protects | Basis | To raise it |
|---|---|---|---|---|
| `MaxMessageSize` | 1 MiB | the size of one gossip envelope of a room; a larger message is refused before it is signed | kolo-gossip's own default (`Config.MaxMessageSize`). A Commit and its SettledCommit travel in one such message, so `Add` refuses one that does not fit. | set it, at most kolo-gossip's `Config.MaxMessageSize` |
| `Rate` and `Burst` | 10 messages a second, 10 at once, for each device | the work of every member, which pays a signature check, an opening and about 7 KB of gossip traffic for each message | 10 messages a second is the highest rate for a whole room that kolo-gossip's simulator measured, at 30,000 nodes; one device is held to what a whole room was measured at | set `Rate` and `Burst` |
| `MaxParents` | 16 | the size of a message: each parent costs 32 bytes | measured: in a room of 64 devices on 127.0.0.1, with the cap off, the most parents one message named was 8 when devices sent in waves of eight and 9 when all 64 sent at once (three runs); 16 is the next power of two above. Loopback shows little concurrency; a real network may show more (UNVERIFIED). | set it |
| `ParentHold` | 5 seconds | the time a message waits for a message it names | five history windows of kolo-gossip at its default heartbeat of one second: after that gossip no longer repairs a message that was lost on the mesh | set it |
| `MaxHeld` | 8,333 | memory: messages that wait for a parent; past it the oldest shows at once | kolo-gossip's `MaxCacheMessages`, the messages it keeps per topic | set it |
| `MaxKnown` | 200,000 | memory: the ids the order remembers; a reply to a message older than that waits `ParentHold` | kolo-gossip's `MaxSeen`, the ids it keeps for duplicates | set it |
| `EventQueue` | 1,024 | memory: events not yet read with `Next`; when it is full the room stops reading its topic, so gossip drops for this room only | kolo-braid's `MaxEvents`, the events it queues for `Next` | set it |
| `PastEpoch` | 10 seconds | how long a message of the previous epoch is still accepted, so messages in flight when a Commit applies are not lost; it is also the time a removed device can still send under its old key and stays connected, so the Commit that removes it reaches it | kolo-mls's `PastEpochWindow` | set it |
| `MaxSkew` | 1 minute | how far ahead of its envelope the time inside a message may be (a message published again is older than its envelope and is not refused for that); the author already chooses the envelope's time inside this window, so the bound adds no power over the order | kolo-gossip's `MaxAge` and `MaxSkew` | set it |
| `MaxTreeBytes` | 128 MiB | memory of a device that joins: the ratchet tree it accepts | kolo-mls's `DefaultMaxTreeBytes` | set it |

## Bounds of the segments and of catch-up

| Bound | Default | What it protects | Basis | To raise it |
|---|---|---|---|---|
| `SegmentEntries` | 10,000 | memory and time of the log an admin writes and a member holds: an open log costs about 244 bytes for each entry, reopening it about 20 microseconds for each | measured on Pebble (one run on a laptop under a load of 10): a log of 1,000 entries reopens in 19 ms, 10,000 in 208 ms and 100,000 in 1.96 s, and holds 227, 243 and 244 bytes for each entry; a replica of 10,000 entries is pulled over loopback in 0.48 s, keyed in 54 ms and read in 33 ms. 10,000 entries are 2.4 MB of memory and take 0.2 s to reopen | set it; a larger segment is fewer logs and a slower reopen |
| `KeepSegments` | 32 | memory and disk of a device: closed segments it has read and keeps to serve to others; the older ones are forgotten and deleted (the pins and the other members hold them) | derived: 32 segments of 10,000 entries are 78 MB of memory | set it |
| `MaxLogs` | 4,096 | memory: logs one room follows; a hostile admin could name more | measured: an open log holding one entry costs 2.7 KB, so 4,096 logs cost 11 MB | set it |
| `PinGap` | 1 second | the pins' work: the least time between two pushes of one log | UNSOURCED: a pin pulls the new entries of a log in one request, so an admin asks it for each log at most once a second | set it |
| `PinTTL` | 30 days | how long the capability that lets a pin keep the admin's logs lasts; it is issued again after half of it | kolo-pin's own example | set it |
| `PinWait` | 5 seconds | the pins' load: how long a device waits before it asks them, how often it asks again, and (twice that) how long one pull from a pin may take, so a pin that does not answer does not keep a replica from the other peers | five kolo-gossip heartbeats at the default of one second: time for a peer to connect | set it |
| `CatchUpWait` | 30 seconds | when a closed segment that lacks entries is reported, and the longest wait of kolo-sync before it tries a failed pull again (its own default is 10 minutes) | the time kolo-sync's default backoff (1 second, doubling) takes to reach its fifth retry | set it |
| `RepublishAfter` | 30 seconds | the traffic of republishing: how long a message may be unstored before its author publishes it again | longer than kolo-gossip's `IgnoredTTL` (5 seconds) and `PastEpoch` (10 seconds), so a copy that was ignored has been forgotten everywhere | set it |
| `MaxReceipts` | 10,000 | memory and disk of a device whose messages are not stored: `Send` fails past it | derived: at the default rate (8 messages a second) it is 21 minutes with no admin online | set it |

A device never publishes faster than four fifths of `Rate` with a burst of `Burst` minus one, so what it sends, a message published again included, stays below what its peers accept.

An admin on Pebble commits every entry to disk before it goes on: an append took 2.9 to 3.6 ms (about 300 messages a second for one admin device) in the measurement above. The rooms of the tests keep their logs in memory.

## Fixed bounds

| Bound | Value | What it protects | Basis |
|---|---|---|---|
| gossip envelope reserve (`envelopeReserve`) | 256 bytes | the check that a Commit fits one message | measured: the envelope adds 178 bytes to a body (`TestEnvelopeOverhead`) |
| welcome and SettledCommit in an invite | 1 MiB each | a join reads at most this much of either | kolo-invite's default payload bound, and one gossip message |
| tree in an invite | `MaxTreeBytes` plus 64 bytes | a join reads at most this much | the bound above |
| room name | 256 bytes | the size of an invite | UNSOURCED: a display name; the window shows the error of `Create` for a longer one |
| signed log header in a record | 512 bytes | the size of a record of the control log, an invite and a request to be promoted | measured: a header is 157 bytes |
| join request and welcome of the braid | 64 KiB and 1 MiB | what a device reads from a room message about a promotion | kolo-braid's `MaxJoinRequest` and `MaxWelcome`; measured: 202 and 549 bytes |
| a Commit or SettledCommit in a control log | `MaxMessageSize` | what a member reads for one key | the room's gossip message, which carried them before |
| epoch keys, authors, receipts and logs on disk | 32 bytes for each epoch, a certificate for each device the room had, a body for each message not stored | disk; they grow with the room's epochs and devices, since reading a segment needs them | derived |

A message body can be about a ninth smaller than `MaxMessageSize`: the sealed message adds its header, parents (up to `MaxParents` times 32 bytes) and a Padme padding of at most 12 percent, and gossip refuses an envelope past the limit with `room: message too large`.

## Bounds of the libraries the room runs on

- **Admins and the braid**: the admins' braid has the bounds of kolo-braid. A room has one settler and may have more admins by `Promote`. The braid holds admins only, so its bounds are not a room limit.
- **Admins' logs**: each admin adds a control log and one segment for each epoch it writes in and each `SegmentEntries`; a member holds the closed ones it has read only up to `KeepSegments`.
- **Rooms on one device**: the room sets no cap. kolo-swarm and kolo-gossip set none by default either (`MaxTopics` is zero), and all the rooms of a device share its `MaxPeers` places, which kolo-swarm splits fairly between its joined topics.
- **Devices on one machine**: the 64-device test runs in one process on 127.0.0.1, where every device shares one address. kolo-dht admits at most a sixteenth of a topic's records from one /24 and 20 new announcements a second from one address, and kolo-transport four connections from one address, so the test raises these on its own server and swarms.

## Measured runs of the tests

One run on a laptop under a load of 10 to 13, 127.0.0.1, all connections real, the logs in memory:

| Run | Result |
|---|---|
| 64 devices, 2 admins, 2 pins; 3 members away through 3 epochs and 10,000 messages from 8 devices, an admin killed in the middle of an epoch and started again | 61 devices joined in 7 s; the 10,000 messages were sent in 1 m 43 s and every one was stored; a member away read 3 epochs and all 10,000 messages in 1.3 s with the admins present, in 7.7 s with every other member away (from the pins only) and in 19.7 s with the pins down (the stopped pins still accept a connection and never answer, so the device waits for two pulls of 2 `PinWait` each before the peers serve it) |
| 16 devices, 800 messages, the same three recoveries | 0.27 s, 5.3 s and 2.1 s |

## History

History adds no field to `room.Limits`. Its waits are `CatchUpWait` (the longest `History` waits for a link, for the control logs and for the segments of one epoch) and `PinWait` (when a device asks the pins), and `MaxLogs` bounds the segments of one epoch that `History` follows at once. `History` follows the segments of one epoch, reads them and forgets them, so a read of any depth keeps one epoch in memory, with the page it has not returned yet. What grows with the room, derived from the admins' control logs that a member follows anyway: one link of 72 bytes held in memory for each epoch before the join and each admin that wrote it, and a few bytes for each segment of those epochs. A hostile admin can make these grow only by writing records to its own log, as it can with the index of Commits.

| What | Measured | How |
|---|---|---|
| a link | 72 bytes of data, 76 bytes as a control record | the nonce, the sealed key and its tag |
| a link on disk | 930 bytes (1,000 links) to 1,170 bytes (10,000 links) in a Pebble log | one run, `TestMeasureLinks` (`KOLO_GROUP_MEASURE=1`); the kolo-log entry, its signature and the store's own records account for the rest |
| the walk of a chain | 2 microseconds a link on Pebble with all keys written in one batch (12 ms for 1,000, 21 ms for 10,000, 167 ms for 100,000) | the same test, a room of 3 devices; opening a link takes 1 microsecond |
| the links in memory | 150 to 174 bytes each (116 at 1,000 epochs) | the same test |
| the walk and the link at 30,000 leaves | Export 6 microseconds, sealing one link 11 microseconds, opening one 1 microsecond, 72 bytes; at 4 and 1,024 leaves Export takes 3 and 6 microseconds and sealing 10 and 8 | a 30,000-leaf MLS group in one process, 8 epochs, a copy of kolo-mls's `TestMeasureKolo` that also exports and seals (not kept in this repository); the group itself costs 4 seconds an epoch at 30,000 leaves (Commit, settle and apply, under a load of 13), 170 ms at 1,024 |
| 40 epochs at 3 devices | 39 messages read in 3.7 s (one message in each epoch, one segment of one entry in each) | `TestJoinerAtEpoch40ReadsEveryEpoch`, one run under a load of 13 |

The cost of the links does not depend on the number of members, since a link holds one key; a year at 100 epochs a day is 36,500 links, 2.6 MB of data, 5.5 MB in memory and about 43 MB on disk at the numbers above. What `History` costs at 30,000 members is the segments, which are the same as for catch-up (see `SegmentEntries`).

The walk writes the keys it derives with one write for each call of `History` that needs a key, so a joiner pays one write for each epoch it loads (4 ms on Pebble on the machine of the runs above, most of it the disk sync).

## Policy, the read link and leaving

None adds a field to `room.Limits`. What each costs and what bounds it:

| What | Size or cost | Bound and basis | To raise it |
|---|---|---|---|
| the policy extension | 70 bytes with one poster and a read link, 134 with 3, 3,238 with 100, 320,039 with 10,000 (32 bytes for each poster or device, 32 for the read secret, 6 for the rest) | a Commit that changes it travels in one gossip message, so `SetPolicy` refuses one that does not fit `MaxMessageSize` with `ErrTooLarge`; the decoder also bounds each list by the bytes it holds | `MaxMessageSize` |
| the state of an epoch for the readers | 514 bytes sealed for one admin that is the one poster; 267 bytes for each admin and each device that may post | one control log entry at most `MaxMessageSize` long: `writeRead` refuses a larger one with `ErrTooLarge` and readers refuse it | `MaxMessageSize` |
| a read link | 779 bytes with one control log | the state it holds is bounded as above; its log headers by `maxHeader` each | |
| a reader installing a state | 4 microseconds (open, decode the certificates, build the view) | none needed | |
| a request to leave | one byte of body | one gossip message, under the rate of the device; sent again every `RepublishAfter` while `Leave` waits | `RepublishAfter` |

These come from `TestMeasureReaders` (`KOLO_GROUP_MEASURE=1`, one run on a laptop under a load of 10 to 13). A reader keeps in memory one view (the admins' and posters' certificates) and one key for each epoch it has read, as a member does for the epochs it was in. The state of an epoch costs the same for 3 members and for 100,000 readers, since it is written once by each admin. What the link readers cost the nodes that serve them (connections, pulls of the admins' logs) is not measured: the largest run of this app is 64 devices.

## Bounds of the app

The window and `-headless` add no cap on rooms, members, messages or message length: the bounds above are the room's. What they add is how much is read or drawn at once, and how often the app asks.

| Value | What it is for | Basis |
|---|---|---|
| 50 messages a page | one read of the messages kept for the window, and of the history before the join | a screen of messages; each page is a request, a room has no limit |
| 100 devices a page | one read of the member list, and the rows drawn at once; a room of 30,000 draws 100 (and the admins) until the person asks for more | the 30,000-device test of the window (`frontend/src/window.test.js` with a mock of 30,000, `members.test.js`) |
| one action at a time | creating, joining, adding, promoting, removing, changing the policy and leaving wait for other devices, and the window keeps one wait to stop | the room asks no concurrency of the window |
| 2 s to 30 s | the wait before asking the entry nodes again, doubling | the other Kolo apps' wait |
| 2 s | how often `-welcome-file` is read | a poll of a file the owner writes by hand |
| 2 s | how often the window asks for the status of the room on screen | the status holds the connections of the swarm, which tell nothing by events |
| 64 KiB | the size of the config file and of each pin and relay file | kolo-node's bound for its config file |

The messages the window keeps grow with the room, one record each, and are deleted with the room. The pins are the user's list: no count is capped.

## Not built yet

- **Wake-up of closed phones** (not built): it needs the mobile apps and a relay run by the app's owner.
- **Published build** (blocked): `GOWORK=off` builds fail until kolo-mls and kolo-gossip have a release.
- **Segment sizes at 30,000 members** (not measured): the runs above go to 64 devices. `task test-30k` runs the tests that build a group of 30,000 leaves (see [build and test](build-and-test.md)); their numbers are not recorded here.
- **Readers at 100,000** (not measured): see the section on policy and the read link.

## What the room can still lose

A message is stored once a segment of an admin holds it, and its author keeps it until then (see `MaxReceipts`). It is lost only if its author loses the device before any admin stored it. A message that a device never received live is read from the segments, so a device that is behind loses nothing the admins stored. Reading a segment needs the key of its epoch: a device reads the segments of epochs before it joined only with `History`, within the room's history rule. The tests wait for the mesh before they send the first message; later losses on the mesh are repaired by republishing.
