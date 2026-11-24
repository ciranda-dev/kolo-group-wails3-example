import { useState } from 'react'
import { t } from './i18n.js'

/** A button that puts text on the clipboard and says whether it did. */
export function CopyButton({ lang, text }) {
  const [state, setState] = useState('')
  const copy = () =>
    navigator.clipboard
      .writeText(text)
      .then(() => setState('copied'))
      .catch(() => setState('copyFailed'))
      .finally(() => setTimeout(() => setState(''), 2000))
  return (
    <button type="button" className="btn btn--ghost" onClick={copy}>
      {t(lang, state || 'copy')}
    </button>
  )
}

/** Turns whatever a rejected call threw into a string for the UI. */
export const errorText = (err) => (err instanceof Error && err.message ? err.message : String(err))
