import { list } from './lists.js'

/** The policy of Go (any field may be missing) as the form keeps it. */
export const policyForm = (p) => ({
  history: ['all', 'since', 'none'].includes(p?.history) ? p.history : 'all',
  since: Number(p?.since) || 0,
  posters: ['all', 'admins', 'chosen'].includes(p?.posters) ? p.posters : 'all',
  persons: list(p?.persons).map(String),
  readLink: Boolean(p?.readLink),
})

/** The key of what is wrong with a form, or ''. A read link needs posters, and chosen posters need at least one person. */
export function policyProblem(form) {
  if (form.readLink && form.posters === 'all') {
    return 'policy.linkNeedsPosters'
  }
  if (form.posters === 'chosen' && form.persons.length === 0) {
    return 'policy.chosenEmpty'
  }
  return ''
}

/** The form with a person's right to post changed: chosen posters, with the person added or taken out. */
export function withPoster(form, person, may) {
  const persons = form.persons.filter((p) => p !== person)
  return { ...form, posters: 'chosen', persons: may ? [...persons, person] : persons }
}

/** The form as Go gets it: the epoch of "since" is left to the app, which uses the next one. */
export const policyToSend = (form) => ({ ...form, since: form.history === 'since' ? form.since : 0 })

/** Whether two forms say the same. */
export const samePolicy = (a, b) =>
  a.history === b.history && a.since === b.since && a.posters === b.posters && a.readLink === b.readLink &&
  a.persons.length === b.persons.length && a.persons.every((p) => b.persons.includes(p))
