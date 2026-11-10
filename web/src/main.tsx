import './layers.css'
import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import './fonts'
import './styles/tokens.css'
import './styles/base.css'
import './styles/typography.css'
import './styles/forms.css'
import './styles/text.css'
import './styles/shell.css'
import './styles/code.css'
import './styles/card.css'
import './styles/chip.css'
import './styles/utilities.css'
import './styles/markdown.css'
import './styles/console.css'
import './styles/tabs.css'
import './styles/tables.css'
import './styles/table-scroll.css'
import './styles/integrations.css'
import './styles/charts.css'
import './styles/modal.css'
import './styles/projects.css'
// Rows can carry both classes: shared list geometry wins over project geometry.
import './styles/list-row.css'
import './styles/infrastructure.css'
import './styles/change.css'
import './styles/decision.css'
import './styles/page-head.css'
import './styles/collections.css'
import './styles/sections.css'
// Popups last among components: their rules beat the rows they open from
// (.list-action button) at equal specificity.
import './styles/menus.css'
import { bootSize } from './size'
import { bootTheme } from './theme'
import { bootErrorReporting } from './errors'
import { PRODUCT } from './brand'

document.title = PRODUCT
bootTheme()
bootSize()
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
