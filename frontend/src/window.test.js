import test from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { hasChrome, openChrome, sleep } from '../dev/chrome.mjs'

// The window, served with dev/mock.js in place of Go (loopback only) in a headless Chrome: the rooms, the messages with a
// reply and one that waits for an admin, sending, a read-only room, a room of 30,000 devices read a page at a time, the
// dialogs that create and join a room, the settings, a failed start, and the other two languages.
const visible = (selector) => `(() => { const el = document.querySelector(${JSON.stringify(selector)}); return !!el && el.getClientRects().length > 0 })()`
const count = (selector) => `document.querySelectorAll(${JSON.stringify(selector)}).length`

async function serve() {
  const { createServer } = await import('vite')
  const server = await createServer({
    configFile: fileURLToPath(new URL('../dev/vite.config.js', import.meta.url)),
    logLevel: 'silent',
    server: { host: '127.0.0.1', port: 0, strictPort: false },
  })
  await server.listen()
  return { server, base: `http://127.0.0.1:${server.httpServer.address().port}` }
}

test('the window shows the rooms, messages, replies, members and dialogs', { skip: !hasChrome() && 'Google Chrome is not here', timeout: 120000 }, async () => {
  const { server, base } = await serve()
  const { send, evaluate, until, close } = await openChrome(['--lang=en-US'])
  const press = (text) => evaluate(`[...document.querySelectorAll('button')].find((b) => b.textContent.trim().startsWith(${JSON.stringify(text)}) || b.getAttribute('aria-label') === ${JSON.stringify(text)}).click()`)
  const type = (selector, text) => evaluate(`(() => { const el = document.querySelector(${JSON.stringify(selector)}); const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement : HTMLInputElement; Object.getOwnPropertyDescriptor(proto.prototype, 'value').set.call(el, ${JSON.stringify(text)}); el.dispatchEvent(new Event('input', { bubbles: true })) })()`)
  const room = (name) => evaluate(`document.querySelector('.conv[title=${JSON.stringify(name)}]').click()`)
  try {
    await send('Page.enable')
    await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false })
    await send('Page.addScriptToEvaluateOnNewDocument', { source: `globalThis.violations = []; addEventListener('error', (e) => globalThis.violations.push(String(e.message)))` })
    await send('Page.navigate', { url: base + '/' })
    assert.ok(await until(`${count('.conv')} === 3`), 'three rooms')
    assert.equal(await evaluate(`document.querySelector('.chathead__name').textContent`), 'Family')

    // A reply shows what it answers, and a message that no admin keeps yet says so.
    assert.ok(await until(visible('.bubble__quote')))
    assert.match(await evaluate(`document.querySelector('.bubble__quote').textContent`), /Yes, at eight|You/)
    assert.equal(await evaluate(`[...document.querySelectorAll('.bubble__mark')].map((m) => m.textContent).join('')`), '✓…')

    // Send a message and reply to one.
    await evaluate(`[...document.querySelectorAll('.bubble__reply')][0].click()`)
    assert.ok(await until(visible('.composer__reply')), 'the reply bar')
    await type('.composer__input', 'Count me in')
    await evaluate(`document.querySelector('.composer__form').requestSubmit()`)
    assert.ok(await until(`[...document.querySelectorAll('.bubble__text')].some((b) => b.textContent === 'Count me in')`), 'the sent message shows')
    assert.ok(await until(`document.querySelector('.composer__input').value === '' && document.querySelector('.composer__reply') === null`), 'the draft and the reply bar are gone')

    // A read-only room has no input and says why.
    await room('News')
    assert.ok(await until(`document.querySelector('.composer__input') === null && ${visible('.composer .note')}`))
    assert.match(await evaluate(`document.querySelector('.composer .note').textContent`), /read this room, not post/)

    // A room of 30,000 devices is read a page at a time: the panel holds the admins and 100 more, never all of them.
    await room('Big')
    await press('Members')
    assert.ok(await until(`${count('.member')} === 101`), 'a page of members')
    assert.equal(await evaluate(`document.querySelector('.members__count').textContent`), '30000')
    await press('Show more')
    assert.ok(await until(`${count('.member')} === 201`), 'the next page')
    await press('Close')

    // The create dialog: a read link needs posters.
    await press('Add a room')
    await press('Create a room')
    assert.ok(await until(visible('.modal__box')))
    assert.equal(await evaluate(`document.querySelector('input[type=checkbox]').disabled`), true, 'no read link while every member posts')
    await evaluate(`document.querySelectorAll('input[name=posters]')[1].click()`)
    assert.equal(await evaluate(`document.querySelector('input[type=checkbox]').disabled`), false)
    await evaluate(`document.querySelector('input[type=checkbox]').click()`)
    await type('.modal__body input.field', 'Announcements')
    await press('Create')
    assert.ok(await until(`${count('.conv')} === 4`), 'the new room')
    assert.equal(await evaluate(`document.querySelector('.chathead__name').textContent`), 'Announcements')

    // The join dialog says when a text is pasted in the wrong place, and keeps Join off.
    await press('Add a room')
    await press('Join a room')
    await type('.modal__body textarea:not([readonly])', 'kolo-group-link:AAAA')
    assert.ok(await until(`document.querySelector('.modal__body .note:last-of-type') !== null`))
    assert.match(await evaluate(`document.querySelector('.modal__body').textContent`), /This is a read link/)
    assert.equal(await evaluate(`[...document.querySelectorAll('.modal__body button')].find((b) => b.textContent.trim() === 'Join').disabled`), true)
    await press('Close')

    // The settings page.
    await press('Settings')
    assert.ok(await until(visible('.page')))
    assert.match(await evaluate(`document.querySelector('.page').textContent`), /127\.0\.0\.1:7447/)
    assert.deepEqual(await evaluate('globalThis.violations'), [], 'no script error')
  } finally {
    await close()
    await server.close()
  }
})

test('a failed start shows its reason, and the window speaks Spanish and Portuguese', { skip: !hasChrome() && 'Google Chrome is not here', timeout: 120000 }, async () => {
  const { server, base } = await serve()
  try {
    for (const [lang, rooms, members] of [['en-US', 'Rooms', 'Members'], ['es-ES', 'Salas', 'Miembros'], ['pt-BR', 'Salas', 'Membros']]) {
      const { send, evaluate, until, close } = await openChrome([`--lang=${lang}`])
      try {
        await send('Page.enable')
        await send('Emulation.setUserAgentOverride', { userAgent: await evaluate('navigator.userAgent'), acceptLanguage: lang })
        await send('Page.navigate', { url: base + '/?scene=error' })
        assert.ok(await until(visible('.fault')), 'the failed start')
        assert.match(await evaluate(`document.querySelector('.fault__text').textContent`), /chmod 700/)
        await send('Page.navigate', { url: base + '/' })
        assert.ok(await until(`${count('.conv')} === 3`))
        assert.equal(await evaluate(`document.querySelector('.sidebar__title').textContent`), rooms, lang)
        assert.match(await evaluate(`document.querySelector('.chathead__actions').textContent`), new RegExp(members), lang)
      } finally {
        await close()
      }
    }
  } finally {
    await server.close()
    await sleep(100)
  }
})
