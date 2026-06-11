export default function StatusPill({ status }: { status: string }) {
  return <span className={`pill ${status}`}>{status}</span>
}
