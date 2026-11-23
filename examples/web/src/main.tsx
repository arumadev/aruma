import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';
import { I18nProvider } from './i18n';
import { ThemeProvider } from './theme';
import { SessionProvider } from './session';
import { App } from './App';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <I18nProvider>
      <ThemeProvider>
        <SessionProvider>
          <App />
        </SessionProvider>
      </ThemeProvider>
    </I18nProvider>
  </StrictMode>,
);
