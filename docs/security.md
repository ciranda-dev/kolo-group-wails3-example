# Security

The app is experimental and has had no security review. Today it has no network code and keeps no keys, so there is nothing to protect yet.

The points below are the planned design. Each one is stated for when it is built, and none is built.

- **Message secrecy** (planned): messages are encrypted for the members of the room with keys from MLS (RFC 9420).
- **History keeps keys** (planned): whoever holds a room's epoch key reads every epoch back to the start of the history policy. A stolen device opens all the history it could read. MLS deletes used secrets so that old messages stay closed (RFC 9420 section 9.2); the app keeps the epoch keys on purpose, so that protection no longer covers the room's messages.
- **Removal** (planned): a removed member keeps what it already read and reads nothing after its removal. The keys of later epochs are not in its reach.
- **Future epochs** (planned): protection after a compromise works as MLS gives it. History does not change it.
- **Passing keys outside the protocol** (not preventable): a member can always show a message or a key to someone else.
- **Read link** (planned): a room with a read link gives integrity and authorship, not secrecy. Anyone with the link reads.
- **Always-on admin** (planned): a device that runs headless as an admin holds admin and MLS keys, so it must be kept as safe as a person's own device.
- **Sybil and spam** (planned): in a room with a read link the topic admits any device, so its defense is the gossip peer score only.
