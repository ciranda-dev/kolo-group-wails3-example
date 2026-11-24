import { useEffect, useState } from 'react'
import { Members, Promote, Remove, SetPolicy } from '../bindings/github.com/ciranda-dev/kolo-group-wails3-example/groupservice.js'
import { Avatar } from './Avatar.jsx'
import { errorText } from './Copy.jsx'
import { CloseIcon } from './Icons.jsx'
import { t } from './i18n.js'
import { MEMBER_PAGE, addPage, hasMore } from './members.js'
import { policyForm, policyToSend, withPoster } from './policy.js'
import { shortKey } from './state.js'

function Row({ lang, m, actions }) {
  return (
    <li className="member" title={`person ${m.person}, device ${m.device}`}>
      <Avatar name="" seed={m.person} size={32} />
      <span className="member__text">
        <span className="member__name">{shortKey(m.person)} {m.me ? <span className="member__tag">{t(lang, 'member.you')}</span> : null}</span>
        <span className="member__tags">
          {m.role === 'settler' ? <span className="member__tag">{t(lang, 'member.settler')}</span> : null}
          {m.role === 'admin' ? <span className="member__tag">{t(lang, 'member.admin')}</span> : null}
          {m.poster && m.role === 'member' ? <span className="member__tag">{t(lang, 'member.canPost')}</span> : null}
        </span>
      </span>
      {actions}
    </li>
  )
}

/**
 * The devices of a room in a panel beside the messages: the admins, then the other devices a page at a time (a room can
 * hold thirty thousand). An admin makes a device an admin, removes a person, and says who may post when the room lets
 * only chosen persons post.
 */
export function MembersPanel({ lang, room, tick, onChanged, onClose }) {
  const [page, setPage] = useState({ total: 0, admins: [], items: [] })
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const form = policyForm(room.policy)

  const load = (offset) =>
    Members(room.id, offset, MEMBER_PAGE)
      .then((p) => setPage((prev) => ({ ...p, items: addPage(prev.items, p.items ?? [], offset) })))
      .catch((err) => setError(errorText(err)))

  useEffect(() => {
    load(0)
  }, [room.id, room.epoch, tick])

  const act = (call) => {
    setError('')
    setConfirm('')
    call()
      .then(onChanged)
      .catch((err) => setError(errorText(err)))
  }

  const actions = (m) => {
    if (!room.admin || m.mine || m.role !== 'member') {
      return null
    }
    return (
      <span className="member__actions">
        {form.posters === 'chosen' ? (
          <label className="check"><input type="checkbox" checked={m.poster} onChange={(ev) => act(() => SetPolicy(room.id, policyToSend(withPoster(form, m.person, ev.target.checked))))} />{t(lang, 'member.post')}</label>
        ) : null}
        <button type="button" className="btn btn--ghost" onClick={() => act(() => Promote(room.id, m.device))}>{t(lang, 'member.promote')}</button>
        {confirm === m.person ? (
          <>
            <button type="button" className="btn btn--danger" onClick={() => act(() => Remove(room.id, m.person))}>{t(lang, 'member.confirm')}</button>
            <button type="button" className="btn btn--ghost" onClick={() => setConfirm('')}>{t(lang, 'member.cancel')}</button>
          </>
        ) : (
          <button type="button" className="btn btn--ghost member__remove" onClick={() => setConfirm(m.person)}>{t(lang, 'member.remove')}</button>
        )}
      </span>
    )
  }

  return (
    <aside className="members" id="members-panel" aria-label={t(lang, 'members.title')}>
      <div className="members__head">
        <h2 className="members__title">{t(lang, 'members.title')} <span className="members__count">{page.total}</span></h2>
        <button type="button" className="iconbtn" aria-label={t(lang, 'conv.close')} title={t(lang, 'conv.close')} onClick={onClose}><CloseIcon /></button>
      </div>
      {confirm ? <p className="note">{t(lang, 'member.removeConfirm')}</p> : null}
      {room.admin ? <p className="note">{t(lang, 'member.promoteNote')}</p> : null}
      {error ? <p className="error">{error}</p> : null}
      <h3 className="eyebrow">{t(lang, 'members.admins')}</h3>
      <ul className="members__list">{page.admins.map((m) => <Row key={m.device} lang={lang} m={m} />)}</ul>
      {page.items.length > 0 ? <h3 className="eyebrow">{t(lang, 'members.others')}</h3> : null}
      <ul className="members__list">{page.items.map((m) => <Row key={m.device} lang={lang} m={m} actions={actions(m)} />)}</ul>
      {hasMore(page.total, page.admins.length, page.items.length) ? (
        <button type="button" className="btn btn--ghost" onClick={() => load(page.items.length)}>{t(lang, 'members.more')}</button>
      ) : null}
    </aside>
  )
}
