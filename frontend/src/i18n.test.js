import test from 'node:test'
import assert from 'node:assert/strict'
import { strings, pickLanguage, t, countKey } from './i18n.js'

test('every language has every string, with the same placeholders', () => {
  const keys = Object.keys(strings.en).sort()
  for (const lang of ['es', 'pt']) {
    assert.deepEqual(Object.keys(strings[lang]).sort(), keys, lang)
    for (const key of keys) {
      assert.ok(strings[lang][key].trim(), `${lang} ${key}`)
      for (const mark of ['{n}', '{name}']) {
        assert.equal(strings[lang][key].includes(mark), strings.en[key].includes(mark), `${lang} ${key} ${mark}`)
      }
    }
  }
})

test('no string has an em dash', () => {
  for (const lang of Object.keys(strings)) {
    for (const [key, text] of Object.entries(strings[lang])) {
      assert.ok(!text.includes(String.fromCharCode(0x2014)), `${lang} ${key}`)
    }
  }
})

test('pickLanguage takes the first language we have', () => {
  assert.equal(pickLanguage(['pt-BR', 'en']), 'pt')
  assert.equal(pickLanguage(['fr-FR', 'es_ES']), 'es')
  assert.equal(pickLanguage(['de']), 'en')
  assert.equal(pickLanguage(), 'en')
  assert.equal(pickLanguage(['constructor']), 'en')
})

test('t fills the count and the name and falls back to English', () => {
  assert.equal(t('en', 'chat.online', 5), '5 connected')
  assert.equal(t('es', 'chat.messageTo', 'Work'), 'Escribe a Work')
  assert.equal(t('pt', 'chat.send'), 'Enviar')
  assert.equal(t('fr', 'chat.send'), 'Send')
  assert.equal(t('en', 'no.such.key'), 'no.such.key')
})

test('countKey picks the plural form', () => {
  assert.equal(t('en', countKey('en', 'chat.members', 1), 1), '1 device')
  assert.equal(t('en', countKey('en', 'chat.members', 0), 0), '0 devices')
  assert.equal(t('pt', countKey('pt', 'chat.members', 30000), 30000), '30000 dispositivos')
})

test('the texts that name a flag or a variable keep it in every language', () => {
  const named = { 'settings.pins.note': ['-pin', '-pin-file', 'KOLO_GROUP_PIN'], 'settings.relays.note': ['-relay', '-relay-file', 'KOLO_GROUP_RELAYS'], 'settings.network.own': ['-bootstrap', 'KOLO_GROUP_BOOTSTRAP'], 'settings.headless': ['-headless'], 'invite.headless': ['-headless'], 'status.pins.note': ['-pin', 'KOLO_GROUP_PIN'] }
  for (const lang of Object.keys(strings)) {
    for (const [key, words] of Object.entries(named)) {
      for (const word of words) {
        assert.ok(strings[lang][key].includes(word), `${lang} ${key} names ${word}`)
      }
    }
  }
})

test('the three texts that people copy keep their prefix in every language', () => {
  for (const lang of Object.keys(strings)) {
    assert.ok(strings[lang]['join.welcome.placeholder'].startsWith('kolo-group-welcome:'), lang)
    assert.ok(strings[lang]['join.link.placeholder'].startsWith('kolo-group-link:'), lang)
  }
})
