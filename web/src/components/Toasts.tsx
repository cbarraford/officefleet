import { useEffect, useState } from 'react'
import { dismiss, subscribe, type ToastItem } from '../lib/toast'

export default function Toasts() {
  const [items, setItems] = useState<ToastItem[]>([])
  useEffect(() => subscribe(setItems), [])
  if (items.length === 0) return null
  return (
    <div className="toasts">
      {items.map((t) => (
        <div key={t.id} className={`toast ${t.kind}`} onClick={() => dismiss(t.id)}>
          {t.text}
        </div>
      ))}
    </div>
  )
}
