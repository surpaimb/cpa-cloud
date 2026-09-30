import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { SelfApp } from './SelfApp'
import './styles.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    {window.location.pathname.startsWith('/self/') ? <SelfApp /> : <App />}
  </StrictMode>,
)
