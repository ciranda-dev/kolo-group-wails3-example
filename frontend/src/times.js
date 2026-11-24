const DAY = 24 * 60 * 60 * 1000
const startOfDay = (time) => new Date(new Date(time).getFullYear(), new Date(time).getMonth(), new Date(time).getDate()).getTime()

/** How many calendar days before now's day the day of time is: 0 today, 1 yesterday. */
const daysAgo = (time, now) => Math.round((startOfDay(now) - startOfDay(time)) / DAY)

/** The hour and minute of time, in the language's own style, such as "2:05 PM" or "14:05". */
export const clock = (time, lang) => new Intl.DateTimeFormat(lang, { hour: 'numeric', minute: '2-digit' }).format(time)

/** The label of a day chip: { rel: 'today' | 'yesterday', text: '' }, or { rel: '', text: 'Monday, November 9' } (with the year when it is not this year's). */
export function dayLabel(time, now, lang) {
  const ago = daysAgo(time, now)
  if (ago === 0 || ago === 1) {
    return { rel: ago === 0 ? 'today' : 'yesterday', text: '' }
  }
  const sameYear = new Date(time).getFullYear() === new Date(now).getFullYear()
  const text = new Intl.DateTimeFormat(lang, { weekday: 'long', day: 'numeric', month: 'long', ...(sameYear ? {} : { year: 'numeric' }) }).format(time)
  return { rel: '', text }
}

/** The time of the last message in a list of rooms: the clock today, "yesterday", the weekday within a week, else the date. */
export function listStamp(time, now, lang) {
  const ago = daysAgo(time, now)
  if (ago === 0) {
    return { rel: '', text: clock(time, lang) }
  }
  if (ago === 1) {
    return { rel: 'yesterday', text: '' }
  }
  if (ago > 1 && ago < 7) {
    return { rel: '', text: new Intl.DateTimeFormat(lang, { weekday: 'short' }).format(time) }
  }
  const sameYear = new Date(time).getFullYear() === new Date(now).getFullYear()
  return { rel: '', text: new Intl.DateTimeFormat(lang, { day: 'numeric', month: 'short', ...(sameYear ? {} : { year: 'numeric' }) }).format(time) }
}
