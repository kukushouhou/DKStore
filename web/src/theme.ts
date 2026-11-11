import { theme, type ThemeConfig } from 'antd'

const base: ThemeConfig['token'] = {
  colorPrimary: '#2f6bff',
}

export function buildTheme(dark: boolean): ThemeConfig {
  return {
    algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: base,
  }
}
