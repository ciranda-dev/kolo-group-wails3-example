import test from 'node:test'
import assert from 'node:assert/strict'
import { failedRelays, newerRelays, relayStateKey, relayStatus } from './relays.js'
import { strings } from './i18n.js'

test('relayStatus reads a "group:relays" payload with null relays, or none, with every field in place', () => {
  assert.deepEqual(relayStatus(null), { seq: 0, from: '', relays: [] })
  assert.deepEqual(relayStatus({ from: '-relay', relays: null }), { seq: 0, from: '-relay', relays: [] })
  const relays = [{ addr: '127.0.0.1:7448', key: 'aa', state: 'ready', error: '' }]
  assert.deepEqual(relayStatus({ seq: 4, from: 'KOLO_GROUP_RELAYS', relays }), { seq: 4, from: 'KOLO_GROUP_RELAYS', relays })
})

test('relayStateKey says ready, waiting or why a relay failed, in a string every language has', () => {
  assert.equal(relayStateKey({ state: 'ready', error: '' }), 'relay.ready')
  assert.equal(relayStateKey({ state: 'waiting', error: '' }), 'relay.waiting')
  assert.equal(relayStateKey({ state: 'failed', error: 'wrong-key' }), 'relay.failed.wrong-key')
  assert.equal(relayStateKey({ state: 'failed', error: 'something new' }), 'relay.failed.unreachable')
  assert.equal(relayStateKey({ state: 'failed', error: 'ended' }), 'relay.failed.unreachable')
  assert.equal(relayStateKey(null), 'relay.waiting')
  for (const error of ['wrong-key', 'bad-key', 'not-allowed', 'full', 'unreachable']) {
    for (const lang of ['en', 'es', 'pt']) {
      assert.ok(strings[lang][relayStateKey({ state: 'failed', error })], `${lang} ${error}`)
    }
  }
})

test('failedRelays keeps only the relays that failed', () => {
  const bad = { addr: '127.0.0.1:7449', key: 'bb', state: 'failed', error: 'wrong-key' }
  const status = relayStatus({ relays: [{ addr: '127.0.0.1:7448', key: 'aa', state: 'ready', error: '' }, bad, null] })
  assert.deepEqual(failedRelays(status), [bad])
})

test('a waiting relay is not called unused, since the app dials through it before it holds a place', () => {
  for (const [lang, unused] of [['en', 'not in use'], ['es', 'sin uso'], ['pt', 'sem uso']]) {
    assert.ok(!strings[lang]['relay.waiting'].includes(unused), lang)
  }
})

test('an older relay event that arrives last does not hide a wrong key', () => {
  const failed = relayStatus({ seq: 3, relays: [{ addr: 'x', key: 'aa', state: 'failed', error: 'wrong-key' }, { addr: 'z', key: 'bb', state: 'failed', error: 'wrong-key' }] })
  const older = relayStatus({ seq: 2, relays: [{ addr: 'x', key: 'aa', state: 'failed', error: 'wrong-key' }, { addr: 'z', key: 'bb', state: 'waiting', error: '' }] })
  let shown = relayStatus(null)
  for (const ev of [failed, older]) {
    shown = newerRelays(shown, ev)
  }
  assert.equal(shown, failed)
  assert.equal(failedRelays(shown).length, 2)
  assert.equal(newerRelays(older, failed), failed)
})
