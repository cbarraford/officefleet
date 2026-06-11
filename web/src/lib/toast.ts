// Minimal toast bus: pages call toast(); the <Toasts/> host renders them.
export interface ToastItem {
  id: number
  kind: 'error' | 'info'
  text: string
}

let nextID = 1
let items: ToastItem[] = []
let listener: ((items: ToastItem[]) => void) | null = null

export function toast(kind: ToastItem['kind'], text: string): void {
  const item = { id: nextID++, kind, text }
  items = [...items, item]
  listener?.(items)
  setTimeout(() => dismiss(item.id), 6000)
}

export function dismiss(id: number): void {
  items = items.filter((t) => t.id !== id)
  listener?.(items)
}

export function subscribe(fn: (items: ToastItem[]) => void): () => void {
  listener = fn
  fn(items)
  return () => {
    if (listener === fn) listener = null
  }
}
