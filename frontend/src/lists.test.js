import test from 'node:test'
import assert from 'node:assert/strict'
import { list } from './lists.js'

test('list reads null, a missing field or a non-list as the fallback, as Go sends a nil slice as null', () => {
  assert.deepEqual(list(null), [])
  assert.deepEqual(list(undefined), [])
  assert.deepEqual(list({}), [])
  assert.deepEqual(list(null, ['a']), ['a'])
  const rooms = ['a', 'b']
  assert.equal(list(rooms, ['a']), rooms)
})
