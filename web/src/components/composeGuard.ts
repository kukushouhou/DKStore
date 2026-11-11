const SAMPLE_PLACEHOLDERS = new Set([
  'password',
  'secret',
  'secretkey',
  'changeme',
  'your_password',
  'your_secret',
  'example',
  '123456',
  'admin',
  'postgres',
  'root',
])

export function normalizeImageRepo(imageRef: string): string {
  const withoutDigest = imageRef.trim().split('@')[0]
  const lastSlash = withoutDigest.lastIndexOf('/')
  const colonAfterSlash = withoutDigest.indexOf(':', lastSlash + 1)
  const repoPath = colonAfterSlash >= 0 ? withoutDigest.slice(0, colonAfterSlash) : withoutDigest
  return repoPath
    .replace(/^docker\.io\/library\//, '')
    .replace(/^docker\.io\//, '')
    .replace(/^index\.docker\.io\/library\//, '')
    .replace(/^index\.docker\.io\//, '')
    .replace(/^library\//, '')
    .toLowerCase()
}

/** 将 YAML 配置中对应容器的 image: 标签同步替换为当前选定的版本号，保留原有注释与缩进 */
export function replaceComposeImageTag(
  yamlContent: string,
  serviceImageRef: string,
  nextTag: string,
): string {
  if (!yamlContent || !nextTag.trim()) return yamlContent
  const targetTag = nextTag.trim()
  const targetRepo = normalizeImageRepo(serviceImageRef)
  const lines = yamlContent.split('\n')
  const imgRegex = /^(\s*image\s*:\s*)(["']?)([^\s#"']+)(["']?)(\s*(?:#.*)?\r?)$/i

  const matchIndices: number[] = []
  const allImageIndices: number[] = []

  for (let i = 0; i < lines.length; i++) {
    const m = imgRegex.exec(lines[i])
    if (!m) continue
    allImageIndices.push(i)
    if (targetRepo && normalizeImageRepo(m[3]) === targetRepo) {
      matchIndices.push(i)
    }
  }

  const indicesToUpdate =
    matchIndices.length > 0
      ? matchIndices
      : allImageIndices.length === 1
        ? allImageIndices
        : []

  if (indicesToUpdate.length === 0) return yamlContent

  const nextLines = [...lines]
  for (const idx of indicesToUpdate) {
    const m = imgRegex.exec(nextLines[idx])
    if (!m) continue
    const [, prefix, openQuote, rawRef, closeQuote, trailing] = m
    const withoutDigest = rawRef.split('@')[0]
    const lastSlash = withoutDigest.lastIndexOf('/')
    const colonAfterSlash = withoutDigest.indexOf(':', lastSlash + 1)
    const repoPart = colonAfterSlash >= 0 ? withoutDigest.slice(0, colonAfterSlash) : withoutDigest
    nextLines[idx] = `${prefix}${openQuote}${repoPart}:${targetTag}${closeQuote}${trailing}`
  }

  return nextLines.join('\n')
}

/** 提取原配置中的宿主机绝对挂载路径与敏感环境变量，检测粘贴后是否被示例模板覆盖 */
export function detectComposeOverwriteRisks(
  origYaml: string,
  nextYaml: string,
): { missingPaths: string[]; overwrittenSecrets: string[] } {
  if (!origYaml.trim() || !nextYaml.trim()) {
    return { missingPaths: [], overwrittenSecrets: [] }
  }

  const hostPaths = new Set<string>()
  const lines = origYaml.split(/\r?\n/)
  const volRegex = /^\s*-\s*["']?(\/[A-Za-z0-9._\-/]+):/
  const sourceRegex = /^\s*source:\s*["']?(\/[A-Za-z0-9._\-/]+)["']?\s*$/

  for (const line of lines) {
    const m = volRegex.exec(line) || sourceRegex.exec(line)
    if (m && m[1]) {
      const p = m[1]
      if (p !== '/etc/localtime' && p !== '/etc/timezone') {
        hostPaths.add(p)
      }
    }
  }

  const missingPaths = [...hostPaths].filter((p) => !nextYaml.includes(p))

  const secretEntries = new Map<string, string>()
  const envListRegex =
    /^\s*-\s*([A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|KEY|TOKEN)[A-Z0-9_]*)\s*=\s*(.+?)\s*$/i
  const envMapRegex =
    /^\s*([A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|KEY|TOKEN)[A-Z0-9_]*)\s*:\s*(.+?)\s*$/i

  for (const line of lines) {
    const m = envListRegex.exec(line) || envMapRegex.exec(line)
    if (m && m[1] && m[2]) {
      const key = m[1]
      const val = m[2].replace(/^["']|["']$/g, '').trim()
      if (val && !val.startsWith('${') && !SAMPLE_PLACEHOLDERS.has(val.toLowerCase())) {
        secretEntries.set(key, val)
      }
    }
  }

  const overwrittenSecrets: string[] = []
  for (const [key, origVal] of secretEntries) {
    if (!nextYaml.includes(origVal)) {
      overwrittenSecrets.push(key)
    }
  }

  return { missingPaths, overwrittenSecrets }
}
