# Security

The app is experimental and has had no security review. The live room is built and the points below say what it does and what it does not do. Points marked planned are not built.

## What the live room does

- **Message secrecy**: a message is sealed under K_e, a key every member of the epoch derives from the MLS exporter (RFC 9420 section 8.5), with XChaCha20-Poly1305 and a random 24-byte nonce. Only devices that are leaves of that epoch hold K_e. Message sizes are padded to a Padme length, so the exact length of a text is not visible.
- **Authorship**: the gossip envelope is signed by the sending device and the device is a leaf of the tree, with a certificate chain to its person (kolo-id). The additional data of a sealed message holds the device and the sequence number of the envelope, so a member cannot copy a sealed body into an envelope of its own or of another sender: it does not open, and the message is rejected. A member cannot sign as another member.
- **Removal**: a removed device does not hold the key of any later epoch, so it reads nothing sent after its removal. The members stop admitting it as soon as they apply the settled Commit: its connections are closed, the swarm refuses it, and its messages are refused once the short window of the previous epoch (10 seconds by default) ends. During that window a message it seals under the old key is still accepted, since it cannot be told from one in flight.
- **Applying a Commit** needs the signatures of a quorum of settlers (kolo-mls). A message of the room that claims to carry a Commit is accepted for forwarding only from an admin device and only for the current epoch.
- **Admission**: only member devices connect, and `ServeConn` refuses the others.
- **Flood**: each device may send a bounded number of messages per second to a room, ten by default, and no more than ten at once. A message over the rate is rejected, which lowers the sender's score at every node that sees it.
- **Replay**: gossip drops a message it has seen. A message of another epoch, or one whose additional data differ, is not accepted. A repeated message shows once.
- **Bounds**: every size, queue and wait of the room is bounded and can be raised (see [limits](limits.md)).

## What it does not do

- **History keeps keys** (planned): whoever holds a room's epoch key reads every message of that epoch. Each member keeps the key of every epoch it was in, in memory only for now, so a stolen device opens everything it could read. MLS deletes used secrets so that old messages stay closed (RFC 9420 section 9.2); the app keeps the epoch keys on purpose, so that protection does not cover the room's messages.
- **The invite is trusted as delivered** (not preventable here): the welcome an admin returns holds the room's topic secret and name next to the MLS Welcome. The MLS parts are authenticated; the topic secret is not. It must reach the new device over an authenticated, encrypted channel (kolo-invite). A swapped secret only sends the joiner to a topic that nobody else uses.
- **Metadata**: the mesh peers of a topic and the nodes that serve its discovery records see who takes part and when messages are sent; the envelope names the author's person and device. The topic is a hash of a secret that members hold, so a discovery node cannot link it to the room's name.
- **Messages are not stored for the offline** (planned): a message that a member did not receive, or received before its Commit, is lost to it until the segments exist.
- **Revocations** (planned): the roster holds no revocation of a device certificate yet; removing a device from the room is how it is stopped.
- **Passing keys outside the protocol** (not preventable): a member can always show a message or a key to someone else.
- **Read link** (planned): a room with a read link gives integrity and authorship, not secrecy. Anyone with the link reads.
- **Always-on admin** (planned): a device that runs headless as an admin holds admin and MLS keys, so it must be kept as safe as a person's own device.
- **Sybil and spam in a room with a read link** (planned): the topic then admits any device, so its defense is the gossip peer score only.
