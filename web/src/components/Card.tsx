import type { CSSProperties, ReactNode } from 'react'

export default function Card({
  title,
  children,
  className = '',
  style,
}: {
  title?: string
  children: ReactNode
  className?: string
  style?: CSSProperties
}) {
  return (
    <div className={`card ${className}`} style={style}>
      {title && <h2>{title}</h2>}
      {children}
    </div>
  )
}
