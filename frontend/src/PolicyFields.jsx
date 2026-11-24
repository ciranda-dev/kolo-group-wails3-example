import { t } from './i18n.js'

/**
 * The fields of a room policy: the history rule for new members, who may post, and the read link. "since" (only from the
 * next change on) and chosen members are offered when the room exists; the chosen members are picked in the member list.
 */
export function PolicyFields({ lang, form, onChange, existing = false, disabled = false }) {
  const set = (change) => onChange({ ...form, ...change })
  const radio = (name, value, label) => (
    <label className="check" key={value}>
      <input type="radio" name={name} checked={form[name] === value} disabled={disabled} onChange={() => set({ [name]: value })} />
      {t(lang, label)}
    </label>
  )
  return (
    <>
      <fieldset className="fields">
        <legend className="panel__label">{t(lang, 'create.history')}</legend>
        {radio('history', 'all', 'history.all')}
        {existing ? radio('history', 'since', 'history.since') : null}
        {radio('history', 'none', 'history.none')}
        <p className="note">{t(lang, 'history.note')}</p>
      </fieldset>
      <fieldset className="fields">
        <legend className="panel__label">{t(lang, 'create.posters')}</legend>
        {radio('posters', 'all', 'posters.all')}
        {radio('posters', 'admins', 'posters.admins')}
        {existing ? radio('posters', 'chosen', 'posters.chosen') : null}
        {form.posters === 'chosen' ? <p className="note">{t(lang, 'posters.chosen.note')}</p> : null}
      </fieldset>
      <label className="check">
        <input type="checkbox" checked={form.readLink} disabled={disabled || form.posters === 'all'} onChange={(ev) => set({ readLink: ev.target.checked })} />
        {t(lang, 'create.readLink')}
      </label>
    </>
  )
}
