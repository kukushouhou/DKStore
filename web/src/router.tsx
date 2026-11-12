import { createBrowserRouter } from 'react-router'
import { gatewayPrefix } from './api/client'
import AppLayout from './layouts/AppLayout'
import HomePage from './pages/Home'
import ContainersPage from './pages/Containers'
import HostsPage from './pages/Hosts'
import HistoryPage from './pages/History'
import SettingsPage from './pages/Settings'

const basename = gatewayPrefix() || '/'

export const router = createBrowserRouter(
  [
    {
      path: '/',
      Component: AppLayout,
      children: [
        { index: true, Component: HomePage },
        { path: 'containers', Component: ContainersPage },
        { path: 'hosts', Component: HostsPage },
        { path: 'history', Component: HistoryPage },
        { path: 'settings', Component: SettingsPage },
      ],
    },
  ],
  { basename },
)
