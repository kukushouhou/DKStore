import { theme, type ThemeConfig } from 'antd'

// 应用商店风格：大圆角、适度留白、柔和品牌色。
const base: ThemeConfig['token'] = {
  colorPrimary: '#2f6bff',
  borderRadius: 10,
  borderRadiusLG: 14,
  fontFamily:
    "-apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', 'Noto Sans SC', sans-serif",
}

export function buildTheme(dark: boolean): ThemeConfig {
  return {
    algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: base,
    components: {
      Card: { paddingLG: 20 },
      Collapse: { headerPadding: '12px 4px' },
    },
  }
}
