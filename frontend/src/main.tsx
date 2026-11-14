import React from 'react';
import ReactDOM from 'react-dom/client';
import { App } from './App';
import { PhoneApp } from './PhoneApp';
import { phoneMode } from './phone';
import './style.css';

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    {phoneMode ? <PhoneApp /> : <App />}
  </React.StrictMode>,
);
