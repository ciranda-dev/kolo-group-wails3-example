import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App.jsx'
import { CrashBoundary } from './Crash.jsx'
import './group.css'

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <CrashBoundary>
      <App />
    </CrashBoundary>
  </React.StrictMode>,
)
