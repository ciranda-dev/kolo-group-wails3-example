import { useState } from 'react'
import { CreateRoom } from '../bindings/github.com/ciranda-dev/kolo-group-wails3-example/groupservice.js'
import { errorText } from './Copy.jsx'
import { PolicyFields } from './PolicyFields.jsx'
import { t } from './i18n.js'
import { policyForm, policyProblem, policyToSend } from './policy.js'

/** The form of "Create a room": its name, the history rule, who may post and whether it has a read link. */
export function CreateForm({ lang, onCreated, onCancel }) {
  const [name, setName] = useState('')
  const [form, setForm] = useState(policyForm({}))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const problem = policyProblem(form)

  const create = (ev) => {
    ev.preventDefault()
    setError('')
    setBusy(true)
    CreateRoom(name, policyToSend(form))
      .then(onCreated)
      .catch((err) => {
        setError(errorText(err))
        setBusy(false)
      })
  }

  return (
    <form className="addform" onSubmit={create}>
      <label className="panel__label">
        {t(lang, 'create.name')}
        <input className="field" value={name} placeholder={t(lang, 'create.namePlaceholder')} onChange={(ev) => setName(ev.target.value)} autoComplete="off" />
      </label>
      <PolicyFields lang={lang} form={form} onChange={setForm} disabled={busy} />
      {problem ? <p className="error">{t(lang, problem)}</p> : null}
      <div className="panel__row">
        <button type="submit" className="btn" disabled={busy || Boolean(problem) || !name.trim()}>{t(lang, busy ? 'create.creating' : 'create.submit')}</button>
        <button type="button" className="btn btn--ghost" disabled={busy} onClick={onCancel}>{t(lang, 'create.cancel')}</button>
      </div>
      {error ? <p className="error">{error}</p> : null}
    </form>
  )
}
