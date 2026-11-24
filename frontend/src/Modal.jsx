import { useEffect, useId, useRef } from 'react'
import { CloseIcon } from './Icons.jsx'
import { focusables, trapStep } from './focus.js'
import { t } from './i18n.js'

/**
 * A dialog over a backdrop. Escape, the close button and a click outside close it unless closable is false. Focus
 * starts on the first field, stays inside while it is open, and goes back to the element of returnFocus when it closes.
 */
export function Modal({ lang, title, onClose, closable = true, returnFocus, children }) {
  const box = useRef(null)
  const titleId = useId()
  const close = useRef(onClose)
  close.current = closable ? onClose : () => {}

  useEffect(() => {
    const el = box.current
    ;(el.querySelector('.modal__body input:not([disabled])') ?? focusables(el)[0] ?? el).focus()
    const onKey = (ev) => {
      if (ev.key === 'Escape') {
        ev.stopPropagation()
        close.current()
      } else if (ev.key === 'Tab') {
        const items = focusables(el)
        const next = trapStep(items.indexOf(document.activeElement), items.length, ev.shiftKey)
        if (next >= 0) {
          ev.preventDefault()
          items[next].focus()
        }
      }
    }
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      returnFocus?.current?.focus()
    }
  }, [returnFocus])

  return (
    <div className="modal" onMouseDown={(ev) => {
      if (ev.target === ev.currentTarget) {
        ev.preventDefault() // the click must not take the focus away from the button it goes back to
        close.current()
      }
    }}>
      <div className="modal__box" role="dialog" aria-modal="true" aria-labelledby={titleId} ref={box} tabIndex={-1}>
        <div className="modal__head">
          <h2 className="modal__title" id={titleId}>{title}</h2>
          <button type="button" className="iconbtn" aria-label={t(lang, 'conv.close')} title={t(lang, 'conv.close')} disabled={!closable} onClick={() => close.current()}>
            <CloseIcon />
          </button>
        </div>
        <div className="modal__body">{children}</div>
      </div>
    </div>
  )
}
