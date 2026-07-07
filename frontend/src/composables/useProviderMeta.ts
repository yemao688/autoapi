export function useProviderMeta() {
  const colors: Record<string, string> = {
    OpenAI: '#10a37f',
    Anthropic: '#d97757',
    DeepSeek: '#272729',
    Moonshot: '#0071e3',
    GLM: '#2563eb',
  }

  const letters: Record<string, string> = {
    OpenAI: 'O',
    Anthropic: 'A',
    DeepSeek: 'D',
    Moonshot: 'M',
    GLM: 'G',
  }

  function color(name: string): string {
    return colors[name] || '#6e6e73'
  }

  function letter(name: string): string {
    return letters[name] || name.charAt(0).toUpperCase()
  }

  return { color, letter }
}
