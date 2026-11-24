import { list } from './lists.js'

/** The prefixes of the texts that people copy between devices, as the app writes them. */
export const PREFIX = { key: 'kolo-group-key:', welcome: 'kolo-group-welcome:', link: 'kolo-group-link:' }

/** What a pasted text is: "key", "welcome", "link", or "" for anything else. */
export function kindOf(text) {
  const s = String(text ?? '').trim()
  return Object.keys(PREFIX).find((k) => s.startsWith(PREFIX[k])) ?? ''
}

/** The key packages in a pasted text, one per word that starts with the prefix. */
export const keyPackages = (text) => list(String(text ?? '').split(/\s+/)).filter((w) => w.startsWith(PREFIX.key))
