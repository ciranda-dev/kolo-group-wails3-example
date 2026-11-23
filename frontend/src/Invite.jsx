import { useEffect, useState } from 'react'
import { AddMembers, RoomLink } from '../bindings/github.com/ciranda-dev/kolo-group-wails3-example/groupservice.js'
import { CopyButton, errorText } from './Copy.jsx'
import { t } from './i18n.js'
import { keyPackages } from './text.js'

/** Add devices to a room with their key packages (an admin does), and the read link of a room that has one. */
export function InvitePanel({ lang, room, onAdded }) {
  const [text, setText] = useState('')
  const [welcome, setWelcome] = useState('')
  const [link, setLink] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const packages = keyPackages(text)

  useEffect(() => {
    setLink('')
    if (room.policy.readLink) {
      RoomLink(room.id).then(setLink).catch((err) => setError(errorText(err)))
    }
  }, [room.id, room.policy.readLink])

  const add = (ev) => {
    ev.preventDefault()
    setError('')
    setBusy(true)
    AddMembers(room.id, packages)
      .then((w) => {
        setWelcome(w)
        setText('')
        onAdded()
      })
      .catch((err) => setError(errorText(err)))
      .finally(() => setBusy(false))
  }

  return (
    <section className="panel">
      {room.admin ? (
        <form className="addform" onSubmit={add}>
          <h3 className="eyebrow">{t(lang, 'invite.title')}</h3>
          <p className="note">{t(lang, 'invite.note')}</p>
          <label className="panel__label">
            {t(lang, 'invite.keyPackages')}
            <textarea className="field field--code" rows={4} value={text} disabled={busy} placeholder="kolo-group-key:..." onChange={(ev) => setText(ev.target.value)} />
          </label>
          <div className="panel__row">
            <button type="submit" className="btn" disabled={busy || packages.length === 0}>{t(lang, busy ? 'invite.adding' : 'invite.add')}</button>
          </div>
          {welcome ? (
            <>
              <h3 className="eyebrow">{t(lang, 'invite.welcome')}</h3>
              <textarea className="field field--code" readOnly rows={4} value={welcome} aria-label={t(lang, 'invite.welcome')} />
              <p className="note">{t(lang, 'invite.welcome.note')}</p>
              <div className="panel__row"><CopyButton lang={lang} text={welcome} /></div>
            </>
          ) : null}
          <p className="note">{t(lang, 'invite.headless')}</p>
        </form>
      ) : (
        <p className="note">{t(lang, 'invite.notAdmin')}</p>
      )}
      <h3 className="eyebrow">{t(lang, 'invite.link.title')}</h3>
      {room.policy.readLink && link ? (
        <>
          <textarea className="field field--code" readOnly rows={3} value={link} aria-label={t(lang, 'invite.link.title')} />
          <p className="note">{t(lang, 'invite.link.note')}</p>
          <div className="panel__row"><CopyButton lang={lang} text={link} /></div>
        </>
      ) : (
        <p className="note">{t(lang, 'invite.link.none')}</p>
      )}
      {error ? <p className="error">{error}</p> : null}
    </section>
  )
}
