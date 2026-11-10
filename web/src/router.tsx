import { createBrowserRouter } from 'react-router'
import AppLayout from './layouts/AppLayout'
import HomePage from './pages/Home'
import HostsPage from './pages/Hosts'
import HistoryPage from './pages/History'
import SettingsPage from './pages/Settings'

const basename =
  typeof window !== 'undefined' && window.location.pathname.startsWith('/dkstore')
    ? '/dkstore'
    : '/'

export const router = createBrowserRouter(
  [
    {
      path: '/',
      Component: AppLayout,
      children: [
        { index: true, Component: HomePage },
        { path: 'hosts', Component: HostsPage },
        { path: 'history', Component: HistoryPage },
        { path: 'settings', Component: SettingsPage },
      ],
    },
  ],
  { basename },
)
