import { useEffect, useState } from 'react'
import { CancelAction, Leave, SetPolicy } from '../bindings/github.com/ciranda-dev/kolo-group-wails3-example/groupservice.js'
import { errorText } from './Copy.jsx'
import { PolicyFields } from './PolicyFields.jsx'
import { t } from './i18n.js'
import { policyForm, policyProblem, policyToSend, samePolicy } from './policy.js'

/** The policy of a room, which an admin changes, and leaving it. */
export function RoomSettings({ lang, room, me, onChanged, onLeft }) {
  const current = policyForm(room.policy)
  const [form, setForm] = useState(current)
  const [busy, setBusy] = useState('') // '', 'save' or 'leave'
  const [confirm, setConfirm] = useState(false)
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const problem = policyProblem(form)

  useEffect(() => setForm(policyForm(room.policy)), [room.id, room.epoch])

  const change = (next) => {
    // Switching to chosen members starts with this person, so the admin is never shut out of its own room.
    setForm(next.posters === 'chosen' && next.persons.length === 0 ? { ...next, persons: [me] } : next)
    setNote('')
  }

  const save = (ev) => {
    ev.preventDefault()
    setError('')
    setBusy('save')
    SetPolicy(room.id, policyToSend(form))
      .then(() => {
        setNote(t(lang, 'room.saved'))
        onChanged()
      })
      .catch((err) => setError(errorText(err)))
      .finally(() => setBusy(''))
  }

  const leave = () => {
    setError('')
    setBusy('leave')
    Leave(room.id)
      .then(onLeft)
      .catch((err) => setError(errorText(err)))
      .finally(() => {
        setBusy('')
        setConfirm(false)
        onChanged()
      })
  }

  return (
    <section className="panel">
      <form className="addform" onSubmit={save}>
        <h3 className="eyebrow">{t(lang, 'room.title')}</h3>
        {room.admin ? null : <p className="note">{t(lang, 'room.adminOnly')}</p>}
        <PolicyFields lang={lang} form={form} onChange={change} existing disabled={!room.admin || busy !== ''} />
        {problem ? <p className="error">{t(lang, problem)}</p> : null}
        {room.admin ? (
          <div className="panel__row">
            <button type="submit" className="btn" disabled={busy !== '' || Boolean(problem) || samePolicy(form, current)}>{t(lang, busy === 'save' ? 'room.saving' : 'room.save')}</button>
          </div>
        ) : null}
        {note ? <p className="note">{note}</p> : null}
      </form>
      <h3 className="eyebrow">{t(lang, 'room.leave.title')}</h3>
      <p className="note">{t(lang, room.admin ? 'room.leave.admin' : 'room.leave.note')}</p>
      {room.admin ? null : (
        <div className="panel__row">
          {busy === 'leave' ? (
            <>
              <span className="note">{t(lang, 'room.leave.waiting')}</span>
              <button type="button" className="btn btn--ghost" onClick={() => CancelAction()}>{t(lang, 'room.leave.stop')}</button>
            </>
          ) : confirm ? (
            <>
              <button type="button" className="btn btn--danger" onClick={leave}>{t(lang, 'room.leave.confirm')}</button>
              <button type="button" className="btn btn--ghost" onClick={() => setConfirm(false)}>{t(lang, 'member.cancel')}</button>
            </>
          ) : (
            <button type="button" className="btn btn--danger" disabled={busy !== ''} onClick={() => setConfirm(true)}>{t(lang, 'room.leave.button')}</button>
          )}
        </div>
      )}
      {error ? <p className="error">{error}</p> : null}
    </section>
  )
}
