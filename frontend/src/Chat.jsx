import { useEffect, useRef } from 'react'
import { Avatar } from './Avatar.jsx'
import { LinkIcon, LockIcon, SendIcon, UsersIcon } from './Icons.jsx'
import { countKey, t } from './i18n.js'
import { answered, groupMessages, oneLine } from './messages.js'
import { shortKey } from './state.js'
import { clock, dayLabel } from './times.js'

/** The header of a room: its avatar and name, how many devices and how many are connected, and the buttons. */
export function ChatHeader({ lang, room, status, page, membersOpen, onPage, onMembers }) {
  const peers = status?.peers ?? 0
  return (
    <header className="chathead">
      <Avatar name={room.name} seed={room.id} size={40} />
      <div className="chathead__text">
        <h1 className="chathead__name" title={room.name}>{room.name}</h1>
        <p className="chathead__sub">
          {t(lang, countKey(lang, 'chat.members', room.members), room.members)}
          {' · '}
          <span className={peers > 0 ? 'is-online' : 'is-pending'}>{peers > 0 ? t(lang, 'chat.online', peers) : t(lang, 'chat.nobodyOnline')}</span>
        </p>
      </div>
      <div className="chathead__actions">
        <button type="button" className="pill" aria-pressed={page === 'invite'} onClick={() => onPage('invite')}>
          <LinkIcon size={16} />
          <span className="pill__text">{t(lang, 'chat.invite')}</span>
        </button>
        <button type="button" className="pill" aria-pressed={membersOpen} aria-expanded={membersOpen} aria-controls="members-panel" onClick={onMembers}>
          <UsersIcon size={16} />
          <span className="pill__text">{t(lang, 'chat.membersButton')}</span>
        </button>
        <button type="button" className="pill" aria-pressed={page === 'status'} onClick={() => onPage('status')}>
          <span className="pill__text">{t(lang, 'chat.status')}</span>
        </button>
        <button type="button" className="pill" aria-pressed={page === 'room'} onClick={() => onPage('room')}>
          <span className="pill__text">{t(lang, 'chat.settings')}</span>
        </button>
      </div>
    </header>
  )
}

/** The text of a day chip: Today, Yesterday, or the date. */
export function dayText(lang, time, now) {
  const d = dayLabel(time, now, lang)
  return d.rel ? t(lang, `chat.${d.rel}`) : d.text
}

function Bubble({ lang, msg, quote, onReply }) {
  return (
    <div className="bubble">
      {msg.replyTo ? (
        <div className="bubble__quote">
          {quote ? <><strong>{quote.mine ? t(lang, 'chat.you') : shortKey(quote.person)}</strong> {oneLine(quote.body)}</> : t(lang, 'chat.replyMissing')}
        </div>
      ) : null}
      <span className="bubble__text">{msg.body}</span>
      {msg.mine && msg.seq > 0 ? <span className="bubble__mark" title={t(lang, msg.stored ? 'chat.stored' : 'chat.waiting')}>{msg.stored ? '✓' : '…'}</span> : null}
      {onReply ? <button type="button" className="bubble__reply" onClick={() => onReply(msg)}>{t(lang, 'chat.reply')}</button> : null}
    </div>
  )
}

/** The messages grouped by person and by five minutes, with day chips. */
function Groups({ lang, messages, all, now, onReply }) {
  return groupMessages(messages).map((item) =>
    item.kind === 'day' ? (
      <div key={item.key} className="day"><span className="day__chip">{dayText(lang, item.time, now)}</span></div>
    ) : (
      <div key={item.key} className={`group ${item.mine ? 'is-mine' : 'is-theirs'}`}>
        <span className="group__from" title={item.person}>{item.mine ? t(lang, 'chat.you') : shortKey(item.person)}</span>
        <div className="group__bubbles">
          {item.msgs.map((m) => <Bubble key={m.id} lang={lang} msg={m} quote={answered(all, m.replyTo)} onReply={onReply} />)}
        </div>
        <span className="group__meta">{item.time > 0 ? clock(item.time, lang) : null}</span>
      </div>
    ),
  )
}

/**
 * The messages of a room: what was read from before the join (a button asks for each page), then what this device was
 * told of, with a button for the older ones. A reply shows the message it answers.
 */
export function MessageList({ lang, conv, now, onOlder, onHistory, onReply }) {
  const bottom = useRef(null)
  const last = conv.messages.at(-1)?.id
  useEffect(() => {
    bottom.current?.scrollIntoView({ block: 'end' })
  }, [last])
  const all = [...conv.history, ...conv.messages]
  return (
    <div className="messages" role="log" aria-live="polite">
      {conv.more ? <button type="button" className="btn btn--ghost" onClick={onOlder}>{t(lang, 'chat.older')}</button> : null}
      {!conv.more && !conv.historyDone ? <button type="button" className="btn btn--ghost" onClick={onHistory}>{t(lang, 'chat.history')}</button> : null}
      {!conv.more && conv.historyDone ? <p className="note">{t(lang, 'chat.historyDone')}</p> : null}
      {conv.history.length > 0 ? <p className="note">{t(lang, 'chat.historyNote')}</p> : null}
      <Groups lang={lang} messages={conv.history} all={all} now={now} />
      {conv.history.length > 0 ? <hr className="rule" /> : null}
      {conv.messages.length === 0 && conv.history.length === 0 ? <p className="empty">{t(lang, 'chat.empty')}</p> : null}
      <Groups lang={lang} messages={conv.messages} all={all} now={now} onReply={onReply} />
      <div ref={bottom} />
    </div>
  )
}

/** One rounded input and Send, the bar of the message being answered, and under them the line that says how messages travel. */
export function Composer({ lang, room, draft, reply, onDraft, onSend, onCancelReply }) {
  return (
    <div className="composer">
      {reply ? (
        <p className="composer__reply">
          {t(lang, 'chat.replyingTo', reply.mine ? t(lang, 'chat.you') : shortKey(reply.person))}: {oneLine(reply.body)}
          <button type="button" className="iconbtn" aria-label={t(lang, 'chat.cancelReply')} title={t(lang, 'chat.cancelReply')} onClick={onCancelReply}>x</button>
        </p>
      ) : null}
      {room.canPost ? (
        <form className="composer__form" onSubmit={(ev) => { ev.preventDefault(); onSend() }}>
          <input className="composer__input" aria-label={t(lang, 'chat.message')} value={draft} onChange={(ev) => onDraft(ev.target.value)} type="text" placeholder={t(lang, 'chat.messageTo', room.name)} autoComplete="off" />
          <button type="submit" className="composer__send"><SendIcon size={16} />{t(lang, 'chat.send')}</button>
        </form>
      ) : (
        <p className="note">{t(lang, room.reader ? 'chat.reader' : 'chat.readOnly')}</p>
      )}
      <p className="composer__note"><LockIcon size={13} />{t(lang, 'chat.encrypted')}</p>
    </div>
  )
}
