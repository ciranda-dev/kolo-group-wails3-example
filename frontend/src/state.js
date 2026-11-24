import { list } from './lists.js'

/** What the window keeps of one room. */
const emptyConv = () => ({
  messages: [], // the messages this device was told of, by their number
  loaded: false, // the first page was read from the store
  more: false, // older messages are in the store
  history: [], // the messages before the join, oldest first
  historyBefore: '', // the id of the oldest of them, where the next page starts
  historyDone: false,
  unread: 0,
  draft: '',
  reply: null,
  catchup: '',
  tick: 0, // grows when the members changed: the window reads them again
})

export const initialState = { rooms: [], selected: '', convs: {} }

/** The state of a room, empty for one the window has not touched. */
export const convOf = (state, id) => state.convs[id] ?? emptyConv()

/** Messages by number, without repeats: a number or an id seen twice is kept once. */
export function merge(a, b) {
  const seen = new Set()
  const out = []
  for (const m of [...a, ...b]) {
    if (!seen.has(m.id)) {
      seen.add(m.id)
      out.push(m)
    }
  }
  return out.sort((x, y) => x.seq - y.seq)
}

const withConv = (state, id, change) => ({ ...state, convs: { ...state.convs, [id]: { ...convOf(state, id), ...change(convOf(state, id)) } } })

/**
 * The reducer of the window. Actions: rooms (the list changed), select, loaded (the first page of a room), older (an older
 * page), history (a page from before the join), event (what "group:event" told), draft, reply.
 */
export function reduce(state, action) {
  switch (action.type) {
    case 'rooms': {
      const rooms = list(action.rooms)
      const convs = Object.fromEntries(Object.entries(state.convs).filter(([id]) => rooms.some((r) => r.id === id)))
      const selected = rooms.some((r) => r.id === state.selected) ? state.selected : (rooms[0]?.id ?? '')
      return { rooms, convs, selected }
    }
    case 'select':
      return withConv({ ...state, selected: action.id }, action.id, () => ({ unread: 0 }))
    case 'loaded':
      return withConv(state, action.id, (c) => {
        const messages = merge(c.messages, list(action.messages))
        return { loaded: true, messages, more: (messages[0]?.seq ?? 0) > 1 }
      })
    case 'older':
      return withConv(state, action.id, (c) => {
        const messages = merge(c.messages, list(action.messages))
        return { messages, more: (messages[0]?.seq ?? 0) > 1 }
      })
    case 'history':
      return withConv(state, action.id, (c) => {
        const page = list(action.messages)
        return { history: [...page, ...c.history], historyBefore: page[0]?.id ?? c.historyBefore, historyDone: Boolean(action.done) }
      })
    case 'draft':
      return withConv(state, action.id, () => ({ draft: action.text }))
    case 'reply':
      return withConv(state, action.id, () => ({ reply: action.message }))
    case 'event':
      return applyEvent(state, action.event)
    default:
      return state
  }
}

function applyEvent(state, ev) {
  if (!ev?.room) {
    return state
  }
  const m = ev.message ?? {}
  switch (ev.kind) {
    case 'message':
      if (m.error) {
        return withConv(state, ev.room, () => ({ catchup: m.error }))
      }
      return withConv(state, ev.room, (c) => ({
        messages: merge(c.messages, [m]),
        unread: ev.room === state.selected || m.mine ? c.unread : c.unread + 1,
      }))
    case 'stored':
      return withConv(state, ev.room, (c) => ({ messages: c.messages.map((x) => (x.id === m.id ? { ...x, stored: true } : x)) }))
    case 'members':
      return withConv(state, ev.room, (c) => ({ tick: c.tick + 1 }))
    case 'catchup':
      return withConv(state, ev.room, () => ({ catchup: m.error || '' }))
    default:
      return state
  }
}

/** The person a message came from, as a short name: the first 8 characters of the key. */
export const shortKey = (key) => String(key ?? '').slice(0, 8)

/** The rooms as the sidebar lists them: the last message of each and its unread count. */
export const rowOf = (state, room) => {
  const c = convOf(state, room.id)
  return { ...room, unread: c.unread, last: c.messages.at(-1) ?? null }
}
