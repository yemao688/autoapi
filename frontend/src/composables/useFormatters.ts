export function useFormatters() {
  function tokens(n: number): string {
    if (!n) return '0'
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`
    if (n >= 1_000) return `${Math.round(n / 1_000)}K`
    return String(n)
  }

  function latency(ms: number): string {
    if (!ms) return '—'
    return `${(ms / 1000).toFixed(2)}s`
  }

  function currency(n: number): string {
    return `¥${n.toFixed(2)}`
  }

  return { tokens, latency, currency }
}
