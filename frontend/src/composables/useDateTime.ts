export function toDateTimeLocal(ms: number): string {
  if (!ms || ms <= 0) return ''
  const d = new Date(ms)
  const yyyy = d.getFullYear()
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  const hh = String(d.getHours()).padStart(2, '0')
  const min = String(d.getMinutes()).padStart(2, '0')
  return `${yyyy}-${mm}-${dd}T${hh}:${min}`
}

export function fromDateTimeLocal(value: string): number {
  if (!value) return 0
  const d = new Date(value)
  const ms = d.getTime()
  return isNaN(ms) ? 0 : ms
}
