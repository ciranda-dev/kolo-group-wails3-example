import { useState } from 'react'
import { CancelAction, JoinLink, JoinRoom, KeyPackage } from '../bindings/github.com/ciranda-dev/kolo-group-wails3-example/groupservice.js'
import { CopyButton, errorText } from './Copy.jsx'
import { t } from './i18n.js'
import { kindOf } from './text.js'

/** The hint for a text pasted in the wrong place, or ''. */
const wrongKind = (text, want) => {
  const kind = kindOf(text)
  return kind && kind !== want ? `join.wrongKind.${kind}` : ''
}

/**
 * Join a room, with a welcome that an admin made for a key package of this device, or with a read link. A join that waits
 * for the devices of the room can be stopped.
 */
export function JoinForm({ lang, onJoined, onBusy }) {
  const [tab, setTab] = useState('welcome')
  const [keyPackage, setKeyPackage] = useState('')
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const hint = wrongKind(text, tab)

  const run = (call) => {
    setError('')
    setBusy(true)
    onBusy(true)
    call(text.trim())
      .then(onJoined)
      .catch((err) => setError(errorText(err)))
      .finally(() => {
        setBusy(false)
        onBusy(false)
      })
  }

  return (
    <div className="addform">
      <div className="panel__row" role="tablist">
        {['welcome', 'link'].map((name) => (
          <button key={name} type="button" role="tab" aria-selected={tab === name} className="pill" aria-pressed={tab === name} disabled={busy} onClick={() => { setTab(name); setText(''); setError('') }}>
            {t(lang, `join.tab.${name}`)}
          </button>
        ))}
      </div>
      {tab === 'welcome' ? (
        <>
          <h3 className="eyebrow">{t(lang, 'join.step1')}</h3>
          <p className="note">{t(lang, 'join.keyPackage.note')}</p>
          {keyPackage ? (
            <>
              <textarea className="field field--code" readOnly rows={3} value={keyPackage} aria-label={t(lang, 'join.keyPackage.make')} />
              <div className="panel__row"><CopyButton lang={lang} text={keyPackage} /></div>
            </>
          ) : (
            <div className="panel__row">
              <button type="button" className="btn btn--ghost" disabled={busy} onClick={() => KeyPackage().then(setKeyPackage).catch((err) => setError(errorText(err)))}>{t(lang, 'join.keyPackage.make')}</button>
            </div>
          )}
          <h3 className="eyebrow">{t(lang, 'join.step2')}</h3>
        </>
      ) : (
        <p className="note">{t(lang, 'join.link.note')}</p>
      )}
      <textarea className="field field--code" rows={3} value={text} disabled={busy} placeholder={t(lang, tab === 'welcome' ? 'join.welcome.placeholder' : 'join.link.placeholder')} aria-label={t(lang, tab === 'welcome' ? 'join.step2' : 'join.tab.link')} onChange={(ev) => setText(ev.target.value)} />
      {hint ? <p className="note">{t(lang, hint)}</p> : null}
      <div className="panel__row">
        <button type="button" className="btn" disabled={busy || !text.trim() || Boolean(hint)} onClick={() => run(tab === 'welcome' ? JoinRoom : JoinLink)}>{t(lang, busy ? 'join.joining' : 'join.join')}</button>
        {busy ? <button type="button" className="btn btn--ghost" onClick={() => CancelAction()}>{t(lang, 'join.stop')}</button> : null}
      </div>
      {error ? <p className="error">{error}</p> : null}
    </div>
  )
}
