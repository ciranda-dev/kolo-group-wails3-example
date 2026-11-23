// A stand-in for the Go service and the Wails runtime, so the window runs in a browser on 127.0.0.1. Three rooms: "Family"
// (this device is an admin, with a reply and a message waiting for an admin), "News" (a read-only member) and "Big", with
// 30,000 devices. The page takes ?scene=error to fail at Start.
const ME = 'a1'.repeat(32)
const DEVICE = 'd1'.repeat(32)
const person = (i) => i.toString(16).padStart(64, '0')
const now = Date.now()

const policy = (extra = {}) => ({ history: 'all', since: 0, posters: 'all', persons: [], readLink: false, ...extra })
const rooms = [
  { id: 'f'.repeat(64), name: 'Family', epoch: 4, members: 3, admin: true, reader: false, canPost: true, policy: policy() },
  { id: 'e'.repeat(64), name: 'News', epoch: 2, members: 2, admin: false, reader: false, canPost: false, policy: policy({ posters: 'admins', readLink: true }) },
  { id: 'b'.repeat(64), name: 'Big', epoch: 30, members: 30000, admin: false, reader: false, canPost: true, policy: policy() },
]
const message = (room, seq, who, body, extra = {}) => ({ id: seq.toString(16).padStart(63, '0') + room[0], replyTo: '', person: who, device: who, time: now - (100 - seq) * 60000, body, epoch: 1, mine: who === ME, error: '', seq, stored: true, ...extra })
const store = {
  [rooms[0].id]: [
    message('f', 1, person(2), 'Dinner on Sunday?'),
    message('f', 2, ME, 'Yes, at eight'),
    message('f', 3, person(2), 'Great', { replyTo: message('f', 2, ME, '').id }),
    message('f', 4, ME, 'I will bring dessert', { stored: false }),
  ],
  [rooms[1].id]: [message('e', 1, person(3), 'The release is out')],
  [rooms[2].id]: [],
}
const history = [message('h', 0, person(2), 'Before you joined', { epoch: 0, seq: 0 })]

const handlers = []
const emit = (data) => handlers.forEach((h) => h({ data }))
export const Events = {
  On(name, handler) {
    if (name === 'group:event') {
      handlers.push(handler)
    }
    return () => handlers.splice(handlers.indexOf(handler), 1)
  },
}

const wait = (v) => new Promise((resolve) => setTimeout(() => resolve(v), 5))
const find = (id) => rooms.find((r) => r.id === id)

export const Start = () =>
  new URLSearchParams(location.search).get('scene') === 'error'
    ? Promise.reject(new Error('kolo-group: the data folder is open to other users: run chmod 700 on it'))
    : wait({ version: 'dev', person: ME, device: DEVICE, network: { mode: 'own', nodes: ['127.0.0.1:7447'] }, pins: [], relays: [], relaysFrom: '', rooms })
export const Rooms = () => wait(rooms.map((r) => ({ ...r })))
export const Messages = (id, before, n) => {
  const all = store[id] ?? []
  const older = before > 0 ? all.filter((m) => m.seq < before) : all
  return wait(older.slice(-n))
}
export const History = () => wait({ messages: history, done: true })
export const Send = (id, body, replyTo) => {
  const m = message(id[0], store[id].length + 1, ME, body, { replyTo, stored: false })
  store[id].push(m)
  emit({ room: id, kind: 'message', message: m })
  return wait(m.id)
}
export const Members = (id, offset, n) => {
  const room = find(id)
  const admins = [{ person: ME, device: DEVICE, role: 'settler', me: true, mine: true, poster: true }]
  const count = room.members - admins.length
  const items = Array.from({ length: Math.max(0, Math.min(n, count - offset)) }, (_, i) => ({ person: person(offset + i + 10), device: person(offset + i + 10), role: 'member', me: false, mine: false, poster: true }))
  return wait({ total: room.members, admins, items })
}
export const RoomStatus = () => wait({ epoch: 4, catchingUp: false, receipts: 1, logs: 3, adminsOnline: 1, peers: 2, online: true, netError: '', pinError: '' })
export const CreateRoom = (name, p) => {
  const room = { id: (rooms.length + 1).toString(16).repeat(64).slice(0, 64), name, epoch: 0, members: 1, admin: true, reader: false, canPost: true, policy: p }
  rooms.push(room)
  store[room.id] = []
  return wait(room)
}
export const KeyPackage = () => wait('kolo-group-key:AAAA')
export const AddMembers = () => wait('kolo-group-welcome:BBBB')
export const JoinRoom = () => Promise.reject(new Error('room: invalid argument: welcome'))
export const JoinLink = () => Promise.reject(new Error('room: invalid argument: link'))
export const RoomLink = () => wait('kolo-group-link:CCCC')
export const Promote = () => wait()
export const Remove = () => wait()
export const SetPolicy = (id, p) => {
  find(id).policy = p
  return wait()
}
export const Leave = () => wait()
export const CancelAction = () => {}
