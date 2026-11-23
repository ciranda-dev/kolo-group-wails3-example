import { Component } from 'react'
import { browserLanguage, t } from './i18n.js'

/** Shows the error of a render, or of an event or promise nobody caught, over the window until it is dismissed. */
export class CrashBoundary extends Component {
  state = { error: null }

  static getDerivedStateFromError(error) {
    return { error }
  }

  componentDidMount() {
    this.onError = (ev) => this.setState({ error: ev.error ?? ev.reason ?? new Error(String(ev.message)) })
    window.addEventListener('error', this.onError)
    window.addEventListener('unhandledrejection', this.onError)
  }

  componentWillUnmount() {
    window.removeEventListener('error', this.onError)
    window.removeEventListener('unhandledrejection', this.onError)
  }

  render() {
    const { error } = this.state
    if (!error) {
      return this.props.children
    }
    const lang = browserLanguage()
    return (
      <div className="fault crash">
        <h2 className="fault__title">{t(lang, 'crash.title')}</h2>
        <p className="fault__hint">{t(lang, 'crash.note')}</p>
        <pre className="fault__text">{String(error?.stack ?? error)}</pre>
        <button type="button" className="btn btn--ghost" onClick={() => this.setState({ error: null })}>{t(lang, 'crash.dismiss')}</button>
      </div>
    )
  }
}
