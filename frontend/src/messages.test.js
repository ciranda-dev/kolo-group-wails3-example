import test from 'node:test'
import assert from 'node:assert/strict'
import { answered, groupMessages, oneLine } from './messages.js'

const m = (id, person, time, extra = {}) => ({ id, person, mine: person === 'me', time, ...extra })
const MIN = 60 * 1000
const noon = new Date(2026, 10, 9, 12, 0).getTime()

test('messages of one person within five minutes make one group under one day chip', () => {
  const items = groupMessages([m('1', 'a', noon), m('2', 'a', noon + MIN), m('3', 'b', noon + 2 * MIN), m('4', 'a', noon + 3 * MIN)])
  assert.deepEqual(items.map((i) => i.kind), ['day', 'group', 'group', 'group'])
  assert.deepEqual(items.filter((i) => i.kind === 'group').map((g) => g.msgs.length), [2, 1, 1])
})

test('a gap of more than five minutes and a new day start a group', () => {
  const next = noon + 24 * 60 * MIN
  const items = groupMessages([m('1', 'a', noon), m('2', 'a', noon + 6 * MIN), m('3', 'a', next)])
  assert.deepEqual(items.map((i) => i.kind), ['day', 'group', 'group', 'day', 'group'])
})

test('a message with no time joins the group and makes no chip', () => {
  const items = groupMessages([m('1', 'a', 0), m('2', 'a', 0)])
  assert.deepEqual(items.map((i) => i.kind), ['group'])
  assert.equal(items[0].msgs.length, 2)
  assert.deepEqual(groupMessages([]), [])
})

test('a reply finds the message it answers, or nothing', () => {
  const list = [m('1', 'a', noon), m('2', 'b', noon)]
  assert.equal(answered(list, '2').person, 'b')
  assert.equal(answered(list, '9'), null)
  assert.equal(answered(list, ''), null)
})

test('a quote is one line', () => {
  assert.equal(oneLine('  a\n\n b\tc  '), 'a b c')
  assert.equal(oneLine(null), '')
})
