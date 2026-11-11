function isShortHexHash(s: string): boolean {
  return /^[0-9a-f]{6}$/i.test(s)
}

/** 从 "2.6 (2.6.8)" / "latest (v3.16.0)" 中提取真实具体版本号；若仅含短哈希如 "latest (3d9048)" 则返回空串 */
export function unwrapConcreteVersion(tag?: string): string {
  if (!tag) return ''
  const s = tag.trim()
  const idx = s.indexOf(' (')
  if (idx > 0 && s.endsWith(')')) {
    const base = s.slice(0, idx).trim()
    const inner = s.slice(idx + 2, -1).trim()
    if (inner && !isShortHexHash(inner)) {
      return inner
    }
    if (/^v?\d+(\.\d+)*$/i.test(base)) {
      return base
    }
    return ''
  }
  return s
}

function parseCoreSegs(raw: string): string[] {
  const clean = raw.trim().replace(/^v/i, '').split(/[-+_]/)[0]
  if (!/^\d+(\.\d+)+$/.test(clean)) return []
  return clean.split('.')
}

/** 提取多段纯数字版本号（至少 2 段，支持从 "8.4 (8.4.11)" / "latest (v3.9.1)" 括号内提取） */
export function parseNumericSegs(tag?: string): string[] {
  if (!tag) return []
  const s = tag.trim()
  const idx = s.indexOf(' (')
  if (idx > 0 && s.endsWith(')')) {
    const inner = s.slice(idx + 2, -1).trim()
    if (inner && !isShortHexHash(inner)) {
      const innerSegs = parseCoreSegs(inner)
      if (innerSegs.length > 0) return innerSegs
    }
    return parseCoreSegs(s.slice(0, idx))
  }
  return parseCoreSegs(s)
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
