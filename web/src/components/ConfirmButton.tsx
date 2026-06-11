import { useEffect, useRef, useState } from 'react'

// Two-step destructive button: first click arms it ("Confirm?"), second
// click within 3s fires onConfirm.
export default function ConfirmButton({
  label,
  onConfirm,
  disabled = false,
  title,
}: {
  label: string
  onConfirm: () => void
  disabled?: boolean
  title?: string
}) {
  const [arming, setArming] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])

  const click = () => {
    if (!arming) {
      setArming(true)
      timer.current = setTimeout(() => setArming(false), 3000)
      return
    }
    if (timer.current) clearTimeout(timer.current)
    setArming(false)
    onConfirm()
  }

  return (
    <button className={`danger small ${arming ? 'confirming' : ''}`} onClick={click} disabled={disabled} title={title}>
      {arming ? 'Confirm?' : label}
    </button>
  )
}
