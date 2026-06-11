export default function Badge({ text, kind = '' }: { text: string; kind?: '' | 'warn' | 'ok' }) {
  return <span className={`badge ${kind}`}>{text}</span>
}
