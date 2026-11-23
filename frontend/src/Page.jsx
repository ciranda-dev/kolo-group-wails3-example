import { useEffect } from 'react'

/** A full page in the main column, in place of the room: the close button or Escape goes back. */
export function Page({ title, closeLabel, onClose, children }) {
  useEffect(() => {
    const onKey = (ev) => {
      if (ev.key === 'Escape') {
        onClose()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <section className="page" aria-label={title}>
      <div className="page__head">
        <h2 className="page__title">{title}</h2>
        <button type="button" className="pill" onClick={onClose}>{closeLabel}</button>
      </div>
      {children}
    </section>
  )
}
