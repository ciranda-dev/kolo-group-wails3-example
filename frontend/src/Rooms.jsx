import { useEffect, useRef, useState } from 'react'
import { Avatar } from './Avatar.jsx'
import { GearIcon, LinkIcon, PlusIcon, RoomPlusIcon, SidebarIcon } from './Icons.jsx'
import { Mark } from './Mark.jsx'
import { Modal } from './Modal.jsx'
import { CreateForm } from './CreateForm.jsx'
import { JoinForm } from './JoinForm.jsx'
import { menuStep } from './focus.js'
import { t } from './i18n.js'
import { oneLine } from './messages.js'
import { shortKey } from './state.js'
import { listStamp } from './times.js'

/** The last message of a room as the list shows it: who, the text on one line, and when. */
export function lastMessage(m, lang, now) {
  if (!m) {
    return null
  }
  const who = m.mine ? t(lang, 'chat.you') : shortKey(m.person)
  const stamp = m.time > 0 ? listStamp(m.time, now, lang) : null
  return { text: `${who}: ${oneLine(m.body)}`, stamp: stamp ? (stamp.rel ? t(lang, `chat.${stamp.rel}`) : stamp.text) : '' }
}

/** The "+" next to Rooms: a menu with Create a room and Join a room, each opening a dialog with its form. */
function AddRoom({ lang, onAdded }) {
  const [open, setOpen] = useState('') // '', 'menu', 'create' or 'join'
  const [busy, setBusy] = useState(false)
  const button = useRef(null)
  const menu = useRef(null)
  const items = () => [...(menu.current?.querySelectorAll('[role="menuitem"]:not(:disabled)') ?? [])]

  useEffect(() => {
    if (open !== 'menu') {
      return
    }
    ;(items()[0] ?? menu.current)?.focus()
    const away = (ev) => {
      if (!menu.current?.contains(ev.target) && !button.current?.contains(ev.target)) {
        setOpen('')
      }
    }
    document.addEventListener('mousedown', away)
    return () => document.removeEventListener('mousedown', away)
  }, [open])

  const onMenuKey = (ev) => {
    if (ev.key === 'Escape') {
      ev.preventDefault()
      ev.stopPropagation()
      setOpen('')
      button.current?.focus()
    } else if (ev.key === 'Tab') {
      setOpen('')
    } else {
      const all = items()
      const next = menuStep(all.indexOf(document.activeElement), all.length, ev.key)
      if (next >= 0) {
        ev.preventDefault()
        all[next].focus()
      }
    }
  }

  const added = (info) => {
    setOpen('')
    onAdded(info)
  }

  return (
    <div className="add">
      <button
        type="button"
        ref={button}
        className="iconbtn iconbtn--box"
        aria-label={t(lang, 'conv.add')}
        title={t(lang, 'conv.add')}
        aria-haspopup="menu"
        aria-expanded={open === 'menu'}
        onClick={() => setOpen(open === 'menu' ? '' : 'menu')}
      >
        <PlusIcon />
      </button>
      {open === 'menu' ? (
        <div className="popover" role="menu" aria-label={t(lang, 'conv.add')} ref={menu} tabIndex={-1} onKeyDown={onMenuKey}>
          <button type="button" role="menuitem" className="choice" onClick={() => setOpen('create')}>
            <span className="choice__icon"><RoomPlusIcon size={20} /></span>
            <span className="choice__text"><span className="choice__title">{t(lang, 'conv.create.title')}</span><span className="choice__sub">{t(lang, 'conv.create.sub')}</span></span>
          </button>
          <button type="button" role="menuitem" className="choice" onClick={() => setOpen('join')}>
            <span className="choice__icon"><LinkIcon size={20} /></span>
            <span className="choice__text"><span className="choice__title">{t(lang, 'conv.join.title')}</span><span className="choice__sub">{t(lang, 'conv.join.sub')}</span></span>
          </button>
        </div>
      ) : null}
      {open === 'create' ? (
        <Modal lang={lang} title={t(lang, 'conv.create.title')} returnFocus={button} onClose={() => setOpen('')}>
          <CreateForm lang={lang} onCreated={added} onCancel={() => setOpen('')} />
        </Modal>
      ) : null}
      {open === 'join' ? (
        <Modal lang={lang} title={t(lang, 'conv.join.title')} returnFocus={button} closable={!busy} onClose={() => setOpen('')}>
          <JoinForm lang={lang} onJoined={added} onBusy={setBusy} />
        </Modal>
      ) : null}
    </div>
  )
}

/**
 * The sidebar: the Kolo mark and the button that collapses it to a rail of avatars, the list of rooms with "+" beside
 * the heading, a quiet line when no pin is set, and your card with the settings button. A room shows its avatar, name,
 * last message and its time, and the number of messages not read.
 */
export function RoomList({ lang, rows, selected, collapsed, onToggle, noPin, me, settingsOpen, onSettings, onSelect, onAdded }) {
  const now = Date.now()
  const toggle = t(lang, collapsed ? 'sidebar.expand' : 'sidebar.collapse')
  return (
    <nav className={`convs ${collapsed ? 'is-collapsed' : ''}`} id="rooms-panel" aria-label={t(lang, 'conv.title')}>
      <div className="convs__brand">
        <Mark className="convs__logo" size={28} />
        <span className="convs__name">kolo</span>
        <button type="button" className="iconbtn" aria-label={toggle} title={toggle} aria-expanded={!collapsed} aria-controls="rooms-panel" onClick={onToggle}>
          <SidebarIcon />
        </button>
      </div>
      <div className="convs__heading">
        <h2 className="sidebar__title">{t(lang, 'conv.title')}</h2>
        <AddRoom lang={lang} onAdded={onAdded} />
      </div>
      {rows.length === 0 ? <p className="note convs__empty">{t(lang, 'conv.empty')}</p> : null}
      <ul className="convs__list">
        {rows.map((r) => {
          const last = lastMessage(r.last, lang, now)
          return (
            <li key={r.id}>
              <button type="button" className={`conv ${r.id === selected ? 'is-active' : ''}`} aria-current={r.id === selected} aria-label={collapsed ? r.name : undefined} title={r.name} onClick={() => onSelect(r.id)}>
                <span className="conv__avatar">
                  <Avatar name={r.name} seed={r.id} size={44} />
                  {r.unread > 0 ? <span className="conv__dot" /> : null}
                </span>
                <span className="conv__text">
                  <span className="conv__top">
                    <span className="conv__name">{r.name}</span>
                    <span className="conv__time">{last?.stamp}</span>
                  </span>
                  <span className="conv__bottom">
                    <span className="conv__preview">{last ? last.text : t(lang, 'conv.noMessages')}</span>
                    {r.unread > 0 ? <span className="conv__unread">{r.unread}</span> : null}
                  </span>
                </span>
              </button>
            </li>
          )
        })}
      </ul>
      {noPin ? <p className="convs__note">{t(lang, 'conv.noPinsShort')}</p> : null}
      <div className="me">
        {me ? (
          <>
            <Avatar name="" seed={me} size={40} />
            <span className="me__text">
              <span className="me__name">{t(lang, 'chat.you')}</span>
              <span className="me__key" title={me}>{shortKey(me)}</span>
            </span>
          </>
        ) : <span className="me__text" />}
        <button type="button" className="iconbtn" aria-pressed={settingsOpen} aria-label={t(lang, 'settings.button')} title={t(lang, 'settings.button')} onClick={onSettings}>
          <GearIcon />
        </button>
      </div>
    </nav>
  )
}

