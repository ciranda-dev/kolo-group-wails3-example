import { t } from './i18n.js'

/** What the room reports about itself: its epoch, whether it is catching up, the pins, the devices connected. */
export function StatusPanel({ lang, room, status, pins }) {
  if (!status) {
    return <section className="panel" />
  }
  const row = (label, value) => (
    <div className="panel__row"><span className="panel__label panel__label--grow">{label}</span><span>{value}</span></div>
  )
  return (
    <section className="panel">
      {row(t(lang, 'status.epoch'), status.epoch)}
      {row(t(lang, 'status.catchUp'), t(lang, status.catchingUp ? 'chat.catchup' : 'status.upToDate'))}
      {row(t(lang, 'status.peers'), status.peers)}
      {row(t(lang, 'status.admins'), status.adminsOnline)}
      {row(t(lang, 'status.receipts'), status.receipts)}
      {row(t(lang, 'status.logs'), status.logs)}
      {row(t(lang, 'status.network'), t(lang, status.online ? 'status.online' : 'status.offline'))}
      <h3 className="eyebrow">{t(lang, 'status.pins')}</h3>
      {pins.length === 0 ? <p className="note">{t(lang, 'status.pins.none')}</p> : (
        <ul className="pins">
          {pins.map((p) => <li key={p.key} className="pins__item"><span className="pins__addr">{p.addr}</span></li>)}
        </ul>
      )}
      {pins.length > 0 ? <p className={status.pinError ? 'error' : 'note'}>{status.pinError ? `${t(lang, 'status.pins.error')}: ${status.pinError}` : t(lang, 'status.pins.ok')}</p> : null}
      <p className="note">{t(lang, 'status.pins.note')}</p>
    </section>
  )
}
