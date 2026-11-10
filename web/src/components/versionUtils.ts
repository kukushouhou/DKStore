/** 提取多段纯数字版本号（至少 2 段，如 8.3.2、131.0.6778.108、v1.32.0-alpine -> ['1','32','0']） */
export function parseNumericSegs(tag?: string): string[] {
  if (!tag) return []
  const clean = tag.trim().replace(/^v/i, '').split(/[-+_]/)[0]
  if (!/^\d+(\.\d+)+$/.test(clean)) return []
  return clean.split('.')
}

/** 逐段比较两个数字版本号大小：a > b 返回正数，a < b 返回负数，无法比较或相等返回 0 */
export function compareNumericSegs(tagA?: string, tagB?: string): number {
  const segA = parseNumericSegs(tagA)
  const segB = parseNumericSegs(tagB)
  if (segA.length === 0 || segB.length === 0) return 0
  const maxLen = Math.max(segA.length, segB.length)
  for (let i = 0; i < maxLen; i++) {
    const na = Number(segA[i] ?? 0)
    const nb = Number(segB[i] ?? 0)
    if (na !== nb) return na - nb
  }
  return 0
}
