import { list } from './lists.js'

const reasons = ['wrong-key', 'bad-key', 'not-allowed', 'full', 'unreachable']

/** The payload of "group:relays" with every field in place. */
export const relayStatus = (data) => ({ seq: Number(data?.seq) || 0, from: String(data?.from ?? ''), relays: list(data?.relays) })

/** The newer of two relay states by their seq: events can arrive out of order, and an older one must not undo a newer. */
export const newerRelays = (prev, next) => (next.seq >= prev.seq ? next : prev)

/** The string key that says a relay's state: ready, waiting, or why it failed. */
export const relayStateKey = (r) => {
  if (r?.state === 'ready') {
    return 'relay.ready'
  }
  if (r?.state === 'failed') {
    return `relay.failed.${reasons.includes(r.error) ? r.error : 'unreachable'}`
  }
  return 'relay.waiting'
}

/** The relays that failed, which the room shows too. */
export const failedRelays = (status) => status.relays.filter((r) => r?.state === 'failed')
