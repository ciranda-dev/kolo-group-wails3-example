import { CopyButton } from './Copy.jsx'
import { t } from './i18n.js'
import { list } from './lists.js'
import { relayStateKey } from './relays.js'
import { shortKey } from './state.js'

const KeyLine = ({ lang, label, value }) => (
  <div className="panel__row">
    <span className="panel__label panel__label--grow">{label}</span>
    <span className="key" title={value}>{shortKey(value)}</span>
    <CopyButton lang={lang} text={value} />
  </div>
)

/** The settings of the app: the network, the pins and the relays it was started with, and who this device is. */
export function SettingsPanel({ lang, info, relays }) {
  const pins = list(info.pins)
  const status = relays ?? { from: info.relaysFrom, relays: list(info.relays) }
  return (
    <section className="panel split">
      <div className="split__col">
        <h3 className="eyebrow">{t(lang, 'settings.network')}</h3>
        <p className="note">{t(lang, info.network.mode === 'own' ? 'settings.network.own' : 'settings.network.public')}</p>
        {info.network.mode === 'own' ? <ul className="pins">{list(info.network.nodes).map((n) => <li key={n} className="pins__item"><span className="pins__addr">{n}</span></li>)}</ul> : null}
        <p className="note">{t(lang, 'settings.network.note')}</p>
        <h3 className="eyebrow">{t(lang, 'settings.pins')}</h3>
        {pins.length === 0 ? <p className="note">{t(lang, 'settings.pins.none')}</p> : (
          <ul className="pins">{pins.map((p) => <li key={p.key} className="pins__item"><span className="pins__addr">{p.addr}</span> <span className="key__short">{shortKey(p.key)}</span></li>)}</ul>
        )}
        <p className="note">{t(lang, 'settings.pins.note')}</p>
        <h3 className="eyebrow">{t(lang, 'settings.relays')}</h3>
        {status.relays.length === 0 ? <p className="note">{t(lang, 'settings.relays.none')}</p> : (
          <>
            <p className="note">{t(lang, 'settings.relays.from')} {status.from}</p>
            <ul className="pins">
              {status.relays.map((r) => <li key={r.key} className="pins__item"><span className="pins__addr">{r.addr}</span> {t(lang, 'relay.label')}: {t(lang, relayStateKey(r))}</li>)}
            </ul>
          </>
        )}
        <p className="note">{t(lang, 'settings.relays.note')}</p>
      </div>
      <div className="split__col">
        <h3 className="eyebrow">{t(lang, 'settings.about')}</h3>
        <div className="panel__row"><span className="panel__label panel__label--grow">{t(lang, 'settings.version')}</span><span>{info.version}</span></div>
        <KeyLine lang={lang} label={t(lang, 'settings.person')} value={info.person} />
        <KeyLine lang={lang} label={t(lang, 'settings.device')} value={info.device} />
        <p className="note">{t(lang, 'settings.device.note')}</p>
        <p className="note">{t(lang, 'settings.headless')}</p>
      </div>
    </section>
  )
}
