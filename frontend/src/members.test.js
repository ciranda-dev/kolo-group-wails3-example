import test from 'node:test'
import assert from 'node:assert/strict'
import { MEMBER_PAGE, addPage, hasMore } from './members.js'

test('a room of 30,000 members is read page by page, never all at once', () => {
  const total = 30000
  const admins = 3
  let shown = []
  let reads = 0
  while (hasMore(total, admins, shown.length)) {
    const rest = total - admins - shown.length
    const page = Array.from({ length: Math.min(MEMBER_PAGE, rest) }, (_, i) => shown.length + i)
    shown = addPage(shown, page, shown.length)
    reads++
  }
  assert.equal(shown.length, total - admins)
  assert.equal(reads, Math.ceil((total - admins) / MEMBER_PAGE))
  assert.ok(!hasMore(total, admins, shown.length))
})

test('a page that starts at the beginning replaces the list, so a change of the room shows at once', () => {
  assert.deepEqual(addPage([1, 2, 3], [9, 8], 0), [9, 8])
  assert.deepEqual(addPage([1, 2, 3], [9, 8], 3), [1, 2, 3, 9, 8])
})
