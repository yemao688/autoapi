export function formatTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

export function formatCost(n: number): string {
  return '$' + n.toFixed(4)
}

export function formatDuration(ms: number): string {
  if (ms >= 1000) return (ms / 1000).toFixed(2) + 's'
  return Math.round(ms) + 'ms'
}

export function formatNumber(n: number): string {
  return n.toLocaleString()
}

export const chartColors = {
  input: '#3b82f6',
  output: '#22c55e',
  cost: '#f43f5e',
  success: '#0071e3',
  rateLimited: '#f5a623',
  error: '#d93025',
  latency: '#8b5cf6',
  ttft: '#06b6d4',
}
