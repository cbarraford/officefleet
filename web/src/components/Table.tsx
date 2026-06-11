import type { ReactNode } from 'react'

export interface Column<T> {
  header: string
  render: (row: T) => ReactNode
}

export default function Table<T>({
  columns,
  rows,
  rowKey,
  onRowClick,
  rowClass,
  empty = 'Nothing here yet.',
}: {
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T) => string
  onRowClick?: (row: T) => void
  rowClass?: (row: T) => string
  empty?: string
}) {
  if (rows.length === 0) return <div className="empty">{empty}</div>
  return (
    <table className="tbl">
      <thead>
        <tr>
          {columns.map((c, i) => (
            <th key={c.header || i}>{c.header}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr
            key={rowKey(row)}
            className={`${onRowClick ? 'clickable ' : ''}${rowClass ? rowClass(row) : ''}`}
            onClick={onRowClick ? () => onRowClick(row) : undefined}
          >
            {columns.map((c, i) => (
              <td key={c.header || i}>{c.render(row)}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}
