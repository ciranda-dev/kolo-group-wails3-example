# How it works

Kolo Group is an example app. Only the window and the room API as signatures exist. Everything below is planned and not built: each line says so.

## What exists today

- A window with a title and an empty room list, served by `GroupService` (`groupservice.go`).
- `internal/room`: the room API as Go signatures. Every method returns `room: not implemented`.
- The `-data` flag and a `-headless` flag that returns an error.

## Planned design

- **One design for every group size** (planned). The app has no group size, mode or threshold.
- **Roles** (planned): member, admin and settler, which is an admin with a seat in the vote that signs each change of the group. One policy bit, "may post", is kept apart from the roles.
- **Every member writes** (planned). A message is signed by the device and sent on the room's gossip topic to the members online. Each message names the message it answers and the latest messages its author saw, so a reply always shows after what it answers.
- **Admins keep the archive** (planned). Admins append the messages they see to their own segment logs and pins mirror those logs. A pin never joins the topic and never writes a log. A room that wants offline delivery needs always-on admin devices, which run this same app with `-headless`.
- **Membership** (planned). Members are MLS leaves. The admins agree on the order of membership changes in a kolo-braid braid.
- **History for new members** (planned). Each message is sealed with a key exported from the MLS epoch it was sent in. A chain of keys, one link per epoch, lets a new member read backward as far as the room's policy allows: all history (the default), since a given epoch, or none. The policy is chosen when the room is created, changes only with a group change, and is never applied to the past.
- **Read-only members and a read link** (planned). A room can list who may post. A room with a read link lets anyone with the link read, with no secrecy.
- **Catch-up** (planned). A device that was offline fetches what it missed from members, admins or pins, and reports an entry it cannot open instead of skipping it.
