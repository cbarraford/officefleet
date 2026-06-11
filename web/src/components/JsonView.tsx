// Collapsible viewer for payloads/transcripts. Pass text for prose
// (transcripts, prompts) or value for JSON structures.
export default function JsonView({
  label,
  value,
  text,
  open = false,
}: {
  label: string
  value?: unknown
  text?: string
  open?: boolean
}) {
  const content = text !== undefined ? text : JSON.stringify(value, null, 2)
  if (content === undefined || content === 'null' || content === '') return null
  return (
    <details className="fold" open={open}>
      <summary>{label}</summary>
      <pre>{content}</pre>
    </details>
  )
}
