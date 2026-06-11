const PALETTE = ['#e06c75', '#e5c07b', '#98c379', '#56b6c2', '#61afef', '#c678dd', '#d19a66', '#be8c6c']

// FNV-1a over the name picks a stable palette color (mirrors the planned
// SP4c server-side fallback behavior).
function hashName(name: string): number {
  let h = 2166136261
  for (const ch of name) {
    h ^= ch.codePointAt(0) ?? 0
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  const first = [...parts[0]][0] ?? '?'
  if (parts.length === 1) return first.toUpperCase()
  const last = [...parts[parts.length - 1]][0] ?? ''
  return (first + last).toUpperCase()
}

export default function AvatarBubble({ name, url, size = 40 }: { name: string; url?: string | null; size?: number }) {
  if (url) return <img className="avatar" src={url} alt={name} width={size} height={size} />
  return (
    <div
      className="avatar avatar-initials"
      style={{
        width: size,
        height: size,
        background: PALETTE[hashName(name) % PALETTE.length],
        fontSize: Math.round(size * 0.4),
      }}
    >
      {initials(name)}
    </div>
  )
}
