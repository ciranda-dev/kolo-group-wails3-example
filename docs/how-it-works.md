# How it works

Kolo Group is an example app. The live room is built in `internal/room`: creating a room, adding and removing members, and sending and reading messages in real time. The window does not use it yet, and everything marked planned is not built.

## What is built

- **A room is an MLS group** (RFC 9420, kolo-mls). Every member device is a leaf of the tree. The creator is the room's one admin and settler; its admin braid (kolo-braid) orders each change of the group and a SettledCommit proves that the settlers agreed to it.
- **Create, Add and Remove.** `Create` makes a room. `KeyPackage` makes what a device hands to an admin. `Add` takes key packages, makes one Commit for all of them, waits until it is settled and applied, and returns one welcome for the new devices, who enter with `Join`. `Remove` removes every device of a person. Each change is one new epoch.
- **Live delivery.** A message is sealed and sent on the room's gossip topic (kolo-gossip) to the members online. Every member is a gossip author because it is a leaf of the tree: the roster is read from the tree of the current epoch and of the previous one for a short window, so there is no second member list.
- **Sealing.** The key of an epoch, K_e, is exported from the MLS epoch: `Export("kolo-group/history/v0", group id and epoch, 32)`. A message is padded and sealed with XChaCha20-Poly1305 under K_e, with a random nonce, and its additional data names the room, the epoch, the sending device and the sequence number of its gossip envelope. The gossip envelope carries the device's signature, so a message cannot be moved to another sender or another number.
- **The validator.** Before a node forwards a message it checks the epoch (the current one, or the previous one for a short window), the sender's rate, the body (it opens it), the number of parents and the time. A message that no honest member sends is rejected, so its sender and the peers that sent it lose score; one that is only out of date is ignored.
- **Order.** A message carries the id it answers (`ReplyTo`) and the ids its author had shown (`Parents`), the author's own previous message first. `Next` shows a message after every message it names that this device knows. A message whose parent has not arrived waits a short time, then shows. `Display` gives the same order to everyone who holds the same messages: a topological order of the references, ties broken by author time and id. Each device's messages keep their order because each names its own previous message.
- **Admission.** `Node.Allow` is the swarm's admission rule: only devices that are leaves of a room of this node connect. A connection of another device that gets through is closed by `ServeConn`. When a device is removed its connections are closed.
- **One connection.** `ServeConn` runs gossip on the unidirectional streams of a swarm connection and kolo-sync on its bidirectional streams. The sync server offers no log yet.
- **Commits** travel on the room's topic as one message with the SettledCommit; a member applies it only with the settlers' signatures. A Commit that does not fit one gossip message is refused when it is made.

## What is not built

- **Segments, the Pins adapter, receipts and catch-up** (planned, T-L6): a device that was offline, or whose message was dropped, has no way to get the messages it missed. A message sealed for an epoch a device has not reached yet is dropped.
- **History for new members** (planned, T-L15): each message is sealed with a key exported from its epoch and every member keeps the key of each epoch it was in, but no link chain lets a new member read backward. `History` returns `room: not implemented`.
- **Roles and policy** (planned, T-L16): `Promote`, `SetPolicy`, posters, the read link and `JoinLink` return `room: not implemented`; `Create` refuses a policy other than the default.
- **Commits by reference, the encrypted tree served by members, leave, status details, the window, invites and `-headless`** (planned).
- **Reopening a room after a restart** (planned): the stores keep the group, but `Node` does not list them again at `Open`.
- **Wake-up of offline devices** (planned, needs the mobile apps).

## Design that is planned

- **One design for every group size** (planned). The app has no group size, mode or threshold.
- **Admins keep the archive** (planned). Admins append the messages they see to their own segment logs and pins mirror those logs. A pin never joins the topic and never writes a log. A room that wants offline delivery needs always-on admin devices, which run this same app with `-headless`.
- **History for new members** (planned): a chain of keys, one link per epoch, lets a new member read backward as far as the room's policy allows: all history (the default), since a given epoch, or none.
- **Read-only members and a read link** (planned). A room can list who may post. A room with a read link lets anyone with the link read, with no secrecy.
