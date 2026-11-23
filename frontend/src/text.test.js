import test from 'node:test'
import assert from 'node:assert/strict'
import { PREFIX, keyPackages, kindOf } from './text.js'

test('a pasted text is known by its prefix', () => {
  assert.equal(kindOf(` ${PREFIX.key}AAAA`), 'key')
  assert.equal(kindOf(`${PREFIX.welcome}AAAA`), 'welcome')
  assert.equal(kindOf(`${PREFIX.link}AAAA`), 'link')
  assert.equal(kindOf('hello'), '')
  assert.equal(kindOf(null), '')
})

test('key packages are taken one per word, in any layout', () => {
  const text = `${PREFIX.key}AAA\n\n  ${PREFIX.key}BBB ${PREFIX.key}CCC garbage ${PREFIX.welcome}ZZZ`
  assert.deepEqual(keyPackages(text), [`${PREFIX.key}AAA`, `${PREFIX.key}BBB`, `${PREFIX.key}CCC`])
  assert.deepEqual(keyPackages(''), [])
})
