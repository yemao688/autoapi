// Compact number formatter for large counts in the UI.
//
// Examples:
//   999         -> "999"
//   1000        -> "1K"
//   1250        -> "1.3K"
//   999949      -> "999.9K"
//   999999      -> "1M"
//   1234567     -> "1.23M"
//   999999999   -> "1B"
//   1234567890  -> "1.23B"
//
// Rules:
//   - < 1000     : exact integer (e.g. "999")
//   - < 1_000_000     : K suffix, 1 decimal
//   - < 1_000_000_000 : M suffix, 2 decimals
//   - >= 1_000_000_000: B suffix, 2 decimals
//
// Promotes to the next unit when rounding would push the mantissa to
// >= 1000, so 999999 shows as "1M" not "1000K".
//
// Trailing ".0" and unnecessary trailing zeros are trimmed: "1.0K" -> "1K".
//
// Null/undefined/NaN/non-finite inputs return the em-dash placeholder used
// elsewhere in the UI.
export function useCompactNumber() {
  function format(n: number | undefined | null): string {
    if (n == null || !Number.isFinite(n)) return '—'
    if (n < 1000) return String(Math.trunc(n))
    if (n < 1_000_000) {
      const v = n / 1000
      // Promote when rounding would push mantissa to 1000K → show as 1M
      if (v >= 999.95) return trimZero((n / 1_000_000).toFixed(2)) + 'M'
      return trimZero(v.toFixed(1)) + 'K'
    }
    if (n < 1_000_000_000) {
      const v = n / 1_000_000
      // Promote when rounding would push mantissa to 1000M → show as 1B
      if (v >= 999.995) return trimZero((n / 1_000_000_000).toFixed(2)) + 'B'
      return trimZero(v.toFixed(2)) + 'M'
    }
    return trimZero((n / 1_000_000_000).toFixed(2)) + 'B'
  }
  function trimZero(s: string): string {
    return s.replace(/\.?0+$/, '')
  }
  return { format }
}