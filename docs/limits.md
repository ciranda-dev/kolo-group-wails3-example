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
| `PastEpoch` | 10 seconds | how long a message of the previous epoch is still accepted, so messages in flight when a Commit applies are not lost; it is also the time a removed device can still send under its old key | kolo-mls's `PastEpochWindow` | set it |
| `MaxSkew` | 1 minute | how far the time inside a message may be from the time of its envelope; the author already chooses the envelope's time inside this window, so the bound adds no power over the order | kolo-gossip's `MaxAge` and `MaxSkew` | set it |
| `MaxTreeBytes` | 128 MiB | memory of a device that joins: the ratchet tree it accepts | kolo-mls's `DefaultMaxTreeBytes` | set it |

## Fixed bounds

| Bound | Value | What it protects | Basis |
|---|---|---|---|
| gossip envelope reserve (`envelopeReserve`) | 256 bytes | the check that a Commit fits one message | measured: the envelope adds 178 bytes to a body (`TestEnvelopeOverhead`) |
| welcome and SettledCommit in an invite | 1 MiB each | a join reads at most this much of either | kolo-invite's default payload bound, and one gossip message |
| tree in an invite | `MaxTreeBytes` plus 64 bytes | a join reads at most this much | the bound above |
| room name | 256 bytes | the size of an invite | UNSOURCED: a display name, which the window will limit |
| epoch keys | 32 bytes for each epoch a device was in | memory; they grow with the number of epochs, since the history of a room needs them | derived; kept in memory only |

A message body can be about a ninth smaller than `MaxMessageSize`: the sealed message adds its header, parents (up to `MaxParents` times 32 bytes) and a Padme padding of at most 12 percent, and gossip refuses an envelope past the limit with `room: message too large`.

## Bounds of the libraries the room runs on

- **Admins and the braid**: the admins' braid has the bounds of kolo-braid. A room has one admin today. The braid holds admins only, so its bounds are not a room limit.
- **Rooms on one device**: the room sets no cap. kolo-swarm and kolo-gossip set none by default either (`MaxTopics` is zero), and all the rooms of a device share its `MaxPeers` places, which kolo-swarm splits fairly between its joined topics.
- **Devices on one machine**: the 64-device test runs in one process on 127.0.0.1, where every device shares one address. kolo-dht admits at most a sixteenth of a topic's records from one /24 and 20 new announcements a second from one address, and kolo-transport four connections from one address, so the test raises these on its own server and swarms.

## Not built yet

- **Cost of history keys** (planned, derived and not measured): about 120 bytes of data per epoch and about 600 bytes on disk per link.
- **Numbers waiting for a measurement** (planned): the size of a log segment.
- **Wake-up of closed phones** (not built): it needs the mobile apps and a relay run by the app's owner.
- **Published build** (blocked): `GOWORK=off` builds fail until kolo-mls and kolo-gossip have a release.

## What the live room loses

A message is dropped, and not repaired, in these cases until the segments and catch-up are built: its receiver had not yet applied the Commit of its epoch (the validator ignores it and gossip repairs it only inside its own window); gossip had not yet grafted the receiver's mesh link; the receiver's work budget was empty. A device that misses a Commit stays at its epoch and reads nothing more. The tests wait for the mesh before they send.
