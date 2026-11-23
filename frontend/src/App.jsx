import { useCallback, useEffect, useReducer, useState } from 'react'
import { Events } from '@wailsio/runtime'
import { History, Messages, RoomStatus, Rooms, Send, Start } from '../bindings/github.com/ciranda-dev/kolo-group-wails3-example/groupservice.js'
import { ChatHeader, Composer, MessageList } from './Chat.jsx'
import { errorText } from './Copy.jsx'
import { InvitePanel } from './Invite.jsx'
import { MembersPanel } from './Members.jsx'
import { Page } from './Page.jsx'
import { RoomList } from './Rooms.jsx'
import { RoomSettings } from './RoomSettings.jsx'
import { SettingsPanel } from './Settings.jsx'
import { StatusPanel } from './Status.jsx'
import { browserLanguage, t } from './i18n.js'
import { list } from './lists.js'
import { relayStatus } from './relays.js'
import { readCollapsed, writeCollapsed } from './sidebar.js'
import { convOf, initialState, reduce, rowOf } from './state.js'

/** How many messages one request reads: a page of the screen, never a limit of the room. */
const PAGE = 50

/** The window calls Start once, also when a development reload runs the effect twice. */
let starting = null
const start = () => (starting ??= Start())

function App() {
  const [state, dispatch] = useReducer(reduce, initialState)
  const [info, setInfo] = useState(null)
  const [relays, setRelays] = useState(null)
  const [startError, setStartError] = useState(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [collapsed, setCollapsed] = useState(() => readCollapsed())
  const [membersOpen, setMembersOpen] = useState(false)
  const [page, setPage] = useState('') // '', 'invite', 'status', 'room' or 'settings'
  const [status, setStatus] = useState(null)
  const lang = browserLanguage()

  const room = state.rooms.find((r) => r.id === state.selected)
  const conv = convOf(state, state.selected)
  const rows = state.rooms.map((r) => rowOf(state, r))
  const fail = useCallback((err) => setError(errorText(err)), [])

  const refreshRooms = useCallback(() => Rooms().then((rooms) => dispatch({ type: 'rooms', rooms })).catch(fail), [fail])

  useEffect(() => {
    const offs = [
      Events.On('group:event', (e) => {
        dispatch({ type: 'event', event: e.data })
        if (e.data?.kind === 'members' || e.data?.kind === 'removed') {
          refreshRooms()
        }
        if (e.data?.kind === 'removed') {
          setNotice(t(browserLanguage(), 'conv.removed'))
        }
      }),
      Events.On('group:relays', (e) => setRelays(relayStatus(e.data))),
    ]
    start()
      .then((i) => {
        setInfo(i)
        dispatch({ type: 'rooms', rooms: i.rooms })
      })
      .catch(setStartError)
    return () => offs.forEach((off) => off())
  }, [refreshRooms])

  // The first page of a room is read from the store when the room comes on screen.
  useEffect(() => {
    if (state.selected && !conv.loaded) {
      const id = state.selected
      Messages(id, 0, PAGE).then((messages) => dispatch({ type: 'loaded', id, messages })).catch(fail)
    }
  }, [state.selected, conv.loaded, fail])

  // The status of the room on screen: peers and pins come from the swarm, which tells nothing by events.
  useEffect(() => {
    if (!state.selected) {
      setStatus(null)
      return
    }
    const id = state.selected
    const read = () => RoomStatus(id).then(setStatus).catch(() => {})
    read()
    const timer = setInterval(read, 2000)
    return () => clearInterval(timer)
  }, [state.selected])

  const select = (id) => {
    dispatch({ type: 'select', id })
    setPage('')
    setError('')
    setNotice('')
  }
  const added = (r) => {
    refreshRooms().then(() => select(r.id))
  }
  const show = (name) => setPage((p) => (p === name ? '' : name))
  const toggleSidebar = () => {
    writeCollapsed(!collapsed)
    setCollapsed(!collapsed)
  }

  const older = () => {
    const id = state.selected
    Messages(id, conv.messages[0].seq, PAGE).then((messages) => dispatch({ type: 'older', id, messages })).catch(fail)
  }
  const earlier = () => {
    const id = state.selected
    History(id, conv.historyBefore, PAGE)
      .then((p) => dispatch({ type: 'history', id, messages: p.messages, done: p.done }))
      .catch(fail)
  }
  const send = () => {
    const text = conv.draft.trim()
    if (!text) {
      return
    }
    Send(room.id, text, conv.reply?.id ?? '')
      .then(() => {
        setError('')
        dispatch({ type: 'draft', id: room.id, text: '' })
        dispatch({ type: 'reply', id: room.id, message: null })
      })
      .catch(fail)
  }

  if (startError) {
    return (
      <div className="app">
        <div className="titlebar" />
        <div className="fault">
          <h2 className="fault__title">{t(lang, 'start.failed')}</h2>
          <p className="fault__text">{errorText(startError)}</p>
          <p className="fault__hint">{t(lang, 'start.hint')}</p>
        </div>
      </div>
    )
  }

  const inRoom = Boolean(room) && ['', 'invite', 'status', 'room'].includes(page)
  const pins = list(info?.pins)
  return (
    <div className="app">
      <div className="titlebar" />
      <div className="body">
        <RoomList
          lang={lang}
          rows={rows}
          selected={state.selected}
          collapsed={collapsed}
          onToggle={toggleSidebar}
          noPin={Boolean(info) && pins.length === 0}
          me={info?.person}
          settingsOpen={page === 'settings'}
          onSettings={() => show('settings')}
          onSelect={select}
          onAdded={added}
        />
        <div className="main">
          {inRoom ? <ChatHeader lang={lang} room={room} status={status} page={page} membersOpen={membersOpen} onPage={show} onMembers={() => setMembersOpen((o) => !o)} /> : null}
          <div className="main__body">
            <div className="stage">
              {page === 'settings' && info ? (
                <Page title={t(lang, 'settings.title')} closeLabel={t(lang, 'conv.close')} onClose={() => setPage('')}>
                  <SettingsPanel lang={lang} info={info} relays={relays} />
                </Page>
              ) : null}
              {page === 'invite' && room ? (
                <Page title={t(lang, 'invite.title')} closeLabel={t(lang, 'conv.close')} onClose={() => setPage('')}>
                  <InvitePanel lang={lang} room={room} onAdded={refreshRooms} />
                </Page>
              ) : null}
              {page === 'status' && room ? (
                <Page title={t(lang, 'status.title')} closeLabel={t(lang, 'conv.close')} onClose={() => setPage('')}>
                  <StatusPanel lang={lang} room={room} status={status} pins={pins} />
                </Page>
              ) : null}
              {page === 'room' && room ? (
                <Page title={t(lang, 'room.title')} closeLabel={t(lang, 'conv.close')} onClose={() => setPage('')}>
                  <RoomSettings lang={lang} room={room} me={info.person} onChanged={refreshRooms} onLeft={() => setPage('')} />
                </Page>
              ) : null}
              {page === '' && room ? (
                <MessageList lang={lang} conv={conv} now={Date.now()} onOlder={older} onHistory={earlier} onReply={(message) => dispatch({ type: 'reply', id: room.id, message })} />
              ) : null}
              {page === '' && !room ? <p className="empty">{t(lang, 'conv.empty')}</p> : null}

              <div className="notices">
                {notice ? <p className="note">{notice}</p> : null}
                {status && !status.online ? <p className="note">{t(lang, 'net.offline')}</p> : null}
                {room && status?.catchingUp ? <p className="note">{t(lang, 'chat.catchup')}</p> : null}
                {conv.catchup ? <p className="error">{conv.catchup}</p> : null}
                {error ? <p className="error">{error}</p> : null}
              </div>

              {page === '' && room ? (
                <Composer lang={lang} room={room} draft={conv.draft} reply={conv.reply} onDraft={(text) => dispatch({ type: 'draft', id: room.id, text })} onSend={send} onCancelReply={() => dispatch({ type: 'reply', id: room.id, message: null })} />
              ) : null}
            </div>
            {inRoom && membersOpen ? <MembersPanel lang={lang} room={room} tick={conv.tick} onChanged={refreshRooms} onClose={() => setMembersOpen(false)} /> : null}
          </div>
        </div>
      </div>
    </div>
  )
}

export default App
