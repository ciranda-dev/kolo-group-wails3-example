import test from 'node:test'
import assert from 'node:assert/strict'
import { convOf, initialState, merge, reduce } from './state.js'

const msg = (seq, extra = {}) => ({ id: String(seq).padStart(4, '0'), seq, person: 'p1', mine: false, body: `m${seq}`, time: 1000 * seq, stored: false, ...extra })
const rooms = [{ id: 'a', name: 'A' }, { id: 'b', name: 'B' }]
const start = () => reduce(initialState, { type: 'rooms', rooms })

test('the room list selects the first room and drops what a removed room held', () => {
  let s = start()
  assert.equal(s.selected, 'a')
  s = reduce(s, { type: 'select', id: 'b' })
  s = reduce(s, { type: 'draft', id: 'b', text: 'hi' })
  s = reduce(s, { type: 'rooms', rooms: [rooms[0]] })
  assert.equal(s.selected, 'a')
  assert.deepEqual(Object.keys(s.convs), [])
  assert.equal(reduce(s, { type: 'rooms', rooms: null }).selected, '')
})

test('the first page, an older page and an event make one list in order, without repeats', () => {
  let s = start()
  s = reduce(s, { type: 'loaded', id: 'a', messages: [msg(8), msg(9), msg(10)] })
  assert.equal(convOf(s, 'a').more, true)
  s = reduce(s, { type: 'event', event: { room: 'a', kind: 'message', message: msg(11) } })
  s = reduce(s, { type: 'event', event: { room: 'a', kind: 'message', message: msg(10) } })
  s = reduce(s, { type: 'older', id: 'a', messages: [msg(5), msg(6), msg(7), msg(8)] })
  assert.deepEqual(convOf(s, 'a').messages.map((m) => m.seq), [5, 6, 7, 8, 9, 10, 11])
  s = reduce(s, { type: 'older', id: 'a', messages: [msg(1), msg(2), msg(3), msg(4)] })
  assert.equal(convOf(s, 'a').more, false)
})

test('an event that comes before the first page is not lost', () => {
  let s = reduce(start(), { type: 'event', event: { room: 'b', kind: 'message', message: msg(3) } })
  s = reduce(s, { type: 'loaded', id: 'b', messages: [msg(2), msg(3)] })
  assert.deepEqual(convOf(s, 'b').messages.map((m) => m.seq), [2, 3])
})

test('unread counts messages of others in a room that is not on screen, and selecting clears it', () => {
  let s = start()
  s = reduce(s, { type: 'event', event: { room: 'b', kind: 'message', message: msg(1) } })
  s = reduce(s, { type: 'event', event: { room: 'b', kind: 'message', message: msg(2, { mine: true }) } })
  s = reduce(s, { type: 'event', event: { room: 'a', kind: 'message', message: msg(1) } })
  assert.equal(convOf(s, 'b').unread, 1)
  assert.equal(convOf(s, 'a').unread, 0)
  assert.equal(convOf(reduce(s, { type: 'select', id: 'b' }), 'b').unread, 0)
})

test('stored marks one message, members and catch-up events are noted, a report is not a message', () => {
  let s = reduce(start(), { type: 'loaded', id: 'a', messages: [msg(1, { mine: true }), msg(2)] })
  s = reduce(s, { type: 'event', event: { room: 'a', kind: 'stored', message: { id: msg(1).id } } })
  assert.deepEqual(convOf(s, 'a').messages.map((m) => m.stored), [true, false])
  s = reduce(s, { type: 'event', event: { room: 'a', kind: 'members', message: {} } })
  assert.equal(convOf(s, 'a').tick, 1)
  s = reduce(s, { type: 'event', event: { room: 'a', kind: 'message', message: { id: 'x', seq: 0, error: 'room: stored message not read' } } })
  assert.equal(convOf(s, 'a').catchup, 'room: stored message not read')
  assert.equal(convOf(s, 'a').messages.length, 2)
  s = reduce(s, { type: 'event', event: { room: 'a', kind: 'catchup', message: {} } })
  assert.equal(convOf(s, 'a').catchup, '')
})

test('history pages go above the older ones and remember where the next starts', () => {
  let s = reduce(start(), { type: 'history', id: 'a', messages: [{ id: 'h3' }, { id: 'h4' }], done: false })
  s = reduce(s, { type: 'history', id: 'a', messages: [{ id: 'h1' }, { id: 'h2' }], done: true })
  const c = convOf(s, 'a')
  assert.deepEqual(c.history.map((m) => m.id), ['h1', 'h2', 'h3', 'h4'])
  assert.equal(c.historyBefore, 'h1')
  assert.equal(c.historyDone, true)
})

test('nulls from Go never break the lists', () => {
  let s = reduce(start(), { type: 'loaded', id: 'a', messages: null })
  s = reduce(s, { type: 'older', id: 'a', messages: undefined })
  s = reduce(s, { type: 'history', id: 'a', messages: null, done: true })
  assert.deepEqual(convOf(s, 'a').messages, [])
  assert.equal(reduce(s, { type: 'event', event: null }), s)
  assert.equal(reduce(s, { type: 'nope' }), s)
})

test('a room of 30,000 members and 30,000 messages: the list is merged once and in order', () => {
  const a = Array.from({ length: 15000 }, (_, i) => msg(2 * i + 1))
  const b = Array.from({ length: 15000 }, (_, i) => msg(2 * i + 2))
  const all = merge(b, a)
  assert.equal(all.length, 30000)
  assert.ok(all.every((m, i) => m.seq === i + 1))
  assert.equal(merge(all, all).length, 30000)
})
