export function faviconURL(baseURL?: string) {
  if (!baseURL) return undefined
  try {
    const url = new URL(baseURL)
    if (url.protocol !== 'https:' && url.protocol !== 'http:') return undefined
    return `${url.origin}/favicon.ico`
  } catch {
    return undefined
  }
}
