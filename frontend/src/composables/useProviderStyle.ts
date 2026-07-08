export function useProviderStyle() {
  const colors: Record<string, string> = {
    openai: '#10a37f',
    anthropic: '#d97757',
    deepseek: '#272729',
    moonshot: '#0071e3',
    '智谱 glm': '#2563eb',
    glm: '#2563eb',
  }

  const letters: Record<string, string> = {
    openai: 'O',
    anthropic: 'A',
    deepseek: 'D',
    moonshot: 'M',
    glm: 'G',
  }

  function color(name: string): string {
    return colors[name.toLowerCase()] || '#6e6e73'
  }

  function initial(name: string): string {
    const key = name.toLowerCase()
    if (letters[key]) return letters[key]
    const cjk = name.match(/[\u4e00-\u9fa5]/)
    if (cjk) {
      return name[name.length - 1]
    }
    const trimmed = name.trim()
    return trimmed ? trimmed.charAt(0).toUpperCase() : name.charAt(0).toUpperCase()
  }

  function textColor(name: string): string {
    return color(name) === '#272729' ? 'rgba(255,255,255,0.86)' : '#fff'
  }

  return { color, initial, textColor }
}
