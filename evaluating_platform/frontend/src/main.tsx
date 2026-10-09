import React from 'react'
import ReactDOM from 'react-dom/client'
import { ConfigProvider, App as AntApp, theme } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import App from './App.tsx'
import './styles/theme.css'

const brandBlue = '#2563eb'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={{
        algorithm: theme.defaultAlgorithm,
        token: {
          colorPrimary: brandBlue,
          colorBgBase: '#f6f7f9',
          colorBgContainer: '#ffffff',
          colorBgElevated: '#ffffff',
          colorBorder: '#e2e8f0',
          colorText: '#0f172a',
          colorTextSecondary: '#475569',
          borderRadius: 8,
          fontFamily: "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
        },
        components: {
          Layout: {
            siderBg: '#ffffff',
            headerBg: '#ffffff',
            bodyBg: '#f6f7f9',
          },
          Menu: {
            itemBg: 'transparent',
            itemSelectedBg: 'rgba(37, 99, 235, 0.10)',
            itemSelectedColor: brandBlue,
            itemColor: '#475569',
            itemHoverBg: '#f2f6fd',
          },
          Table: {
            headerBg: '#f8fafc',
            rowHoverBg: '#f1f5f9',
          },
          Card: {
            colorBgContainer: '#ffffff',
          },
        },
      }}
    >
      <AntApp>
        <App />
      </AntApp>
    </ConfigProvider>
  </React.StrictMode>,
)
