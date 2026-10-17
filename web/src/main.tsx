import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import './fonts'
import './styles.css'
import { bootTheme } from './theme'
import { bootErrorReporting } from './errors'

bootTheme()
void bootErrorReporting()

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={newQueryClient()}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
)
