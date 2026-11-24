/** A message that comes more than this long after the one before it starts a new group: 5 minutes. */
export const GROUP_GAP = 5 * 60 * 1000

const dayOf = (time) => new Date(time).toDateString()

/**
 * Turns messages, oldest first, into what the window draws: a day chip when the day changes, then groups of consecutive
 * messages of one person. A group ends when the person changes, when more than gap passed since the message before, or
 * at a day chip. A message with no time (0) never breaks a group by time and never makes a chip.
 * @param {{ id: string, person: string, mine: boolean, time: number }[]} msgs
 */
export function groupMessages(msgs, gap = GROUP_GAP) {
  const items = []
  let group = null
  let prev = null
  let day = ''
  for (const m of msgs) {
    let fresh = !group || group.person !== m.person
    if (m.time > 0) {
      if (dayOf(m.time) !== day) {
        day = dayOf(m.time)
        items.push({ kind: 'day', key: `day-${m.id}`, time: m.time })
        fresh = true
      }
      if (prev && prev.time > 0 && Math.abs(m.time - prev.time) > gap) {
        fresh = true
      }
    }
    if (fresh) {
      group = { kind: 'group', key: `group-${m.id}`, person: m.person, mine: m.mine, time: 0, msgs: [] }
      items.push(group)
    }
    group.msgs.push(m)
    if (m.time > 0) {
      group.time = m.time
    }
    prev = m
  }
  return items
}

/** The message a reply answers, among the messages on screen, or null. */
export const answered = (messages, replyTo) => (replyTo ? (messages.find((m) => m.id === replyTo) ?? null) : null)

/** The text of a reply bar or a quote: one line of the message. */
export const oneLine = (text) => String(text ?? '').replace(/\s+/g, ' ').trim()
