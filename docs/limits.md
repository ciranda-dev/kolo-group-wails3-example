# Limits

Nothing is limited yet, because nothing is built. This page states the rules the app will follow.

- **No limit from the chat example is carried over** (planned): the app keeps only measured and configurable bounds that protect against hostile peers.
- **No arbitrary caps** (planned): the number of members, messages and history is not capped by the app.
- **Admins and the braid** (planned): the admins' braid has the bounds of kolo-braid: 64 members by default, from 2 to 1,024, derived from a full mesh of connections and not measured at 1,024. The braid holds admins only, so this is not a room limit.
- **Rooms on one device** (planned): kolo-swarm allows 1 to 32 topics, which bounds the rooms one device can follow until that range is measured.
- **Cost of history keys** (planned, derived and not measured): about 120 bytes of data per epoch and about 600 bytes on disk per link.
- **Numbers waiting for a measurement** (planned): the size of a log segment, the wait for a missing parent message, the rate each device may send, and the number of parents a message may carry have no value yet.
- **Wake-up of closed phones** (not built): it needs the mobile apps and a relay run by the app's owner.
- **Published build** (blocked): `GOWORK=off` builds fail until kolo-mls and kolo-gossip have a release.
