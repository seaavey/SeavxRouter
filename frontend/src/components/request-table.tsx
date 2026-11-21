import { useMemo, useState } from 'react'
import {
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
} from '@tanstack/react-table'
import { ArrowDown, ArrowUp, ChevronsUpDown } from 'lucide-react'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Input } from '@/components/ui/input'
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useNewRowIds } from '@/hooks/use-live-logs'
import { format, since, statusLabel } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { RequestLog } from '@/lib/api'

/** A right-aligned figure, so tokens and latency line up in one column. */
function Num({ value, muted = true, title, className }: { value: string; muted?: boolean; title?: string; className?: string }) {
  return (
    <span className={cn('tabular', muted ? 'text-muted-foreground' : 'text-foreground', className)} title={title}>
      {value}
    </span>
  )
}

/**
 * Requests table. The overview shows four scannable columns; `/requests` adds
 * the per-request token breakdown (input, output, cache, total) as separate
 * sortable columns, since that is the question that page is opened to answer.
 */
export function RequestTable({ rows, loading, onSelect, pageSize = 10, detailed = false }: {
  rows: RequestLog[]
  loading?: boolean
  onSelect?: (row: RequestLog) => void
  pageSize?: number
  detailed?: boolean
}) {
  const [sorting, setSorting] = useState<SortingState>([])
  const [filter, setFilter] = useState('')

  const columns = useMemo<ColumnDef<RequestLog>[]>(() => {
    const base: ColumnDef<RequestLog>[] = [
      {
        accessorKey: 'model',
        header: 'Request',
        cell: ({ row }) => (
          <div className="grid gap-0.5">
            <span className="text-sm font-medium">{row.original.model || 'Unnamed model'}</span>
            <span className="text-xs text-muted-foreground">
              {[row.original.provider, row.original.endpoint].filter(Boolean).join(' · ') || 'no provider recorded'}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'status',
        header: 'Status',
        cell: ({ getValue }) => {
          const status = getValue<number>()
          // The code itself is the label. A success needs no colour: four
          // hundred of them in a row should look like a tidy column of numbers,
          // and only a failure earns attention.
          return (
            <Num
              value={String(status)}
              muted={status < 400}
              className={status >= 400 ? 'text-negative' : undefined}
              title={statusLabel(status)}
            />
          )
        },
      },
    ]

    const tokens: ColumnDef<RequestLog>[] = detailed
      ? [
          {
            id: 'input',
            accessorFn: row => row.usage.prompt_tokens,
            header: 'Input',
            cell: ({ getValue }) => <Num value={format.count(getValue<number>())} title="Prompt tokens" />,
          },
          {
            id: 'output',
            accessorFn: row => row.usage.completion_tokens,
            header: 'Output',
            cell: ({ getValue }) => <Num value={format.count(getValue<number>())} title="Completion tokens" />,
          },
          {
            id: 'cached',
            accessorFn: row => row.usage.cached_tokens,
            header: 'Cache',
            cell: ({ getValue }) => {
              const cached = getValue<number>()
              return cached > 0
                ? <Num value={format.count(cached)} title="Tokens served from the provider cache" />
                : <span className="text-muted-foreground/50">—</span>
            },
          },
          {
            id: 'total',
            accessorFn: row => row.usage.total_tokens,
            header: 'Total',
            cell: ({ getValue }) => <Num value={format.count(getValue<number>())} muted={false} />,
          },
        ]
      : [
          {
            id: 'total',
            accessorFn: row => row.usage.total_tokens,
            header: 'Tokens',
            cell: ({ getValue }) => <Num value={format.count(getValue<number>())} />,
          },
        ]

    return [
      ...base,
      ...tokens,
      {
        accessorKey: 'duration_ms',
        header: 'Latency',
        cell: ({ getValue }) => <Num value={format.latency(getValue<number>())} />,
      },
      {
        accessorKey: 'timestamp',
        header: 'Time',
        cell: ({ getValue }) => (
          <span className="text-sm whitespace-nowrap text-muted-foreground" title={since(getValue<string>())}>
            {format.time(getValue<string>())}
          </span>
        ),
      },
    ]
  }, [detailed])

  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting, globalFilter: filter },
    onSortingChange: setSorting,
    globalFilterFn: 'includesString',
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
  })

  // New ids animate in; the hook runs before any early return so the order holds.
  const fresh = useNewRowIds(rows.map(row => row.id))

  if (loading) {
    return <div className="grid gap-2 p-4">{Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-11 w-full" />)}</div>
  }

  if (!rows.length) {
    return (
      <Empty className="border-0 py-12">
        <EmptyHeader>
          <EmptyTitle>No requests yet</EmptyTitle>
          <EmptyDescription>Traffic through the gateway shows up here as soon as it arrives.</EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }

  const shown = table.getRowModel().rows.slice(0, pageSize)
  const cellPad = detailed ? 'px-3' : 'px-4'

  return (
    <div>
      <div className="flex items-center justify-between gap-3 px-4 py-3">
        <Input
          value={filter}
          onChange={event => setFilter(event.target.value)}
          placeholder="Search requests"
          aria-label="Search requests"
          className="h-8 min-w-0 max-w-64"
        />
        <span className="shrink-0 text-xs whitespace-nowrap text-muted-foreground">
          {table.getFilteredRowModel().rows.length} of {rows.length}
        </span>
      </div>
      <Table>
        <TableHeader>
          {table.getHeaderGroups().map(group => (
            <TableRow key={group.id} className="hover:bg-transparent">
              {group.headers.map(header => (
                <TableHead
                  key={header.id}
                  className={cn(cellPad, 'h-9 select-none text-xs font-normal text-muted-foreground', header.column.id !== 'model' && 'text-right')}
                >
                  {header.column.getCanSort() ? (
                    <button
                      type="button"
                      className={cn(
                        'inline-flex h-7 cursor-pointer items-center gap-1 rounded px-2 font-normal text-muted-foreground transition-colors select-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
                        header.column.id === 'model' ? '-ml-2' : '-mr-2 ml-auto',
                      )}
                      onClick={header.column.getToggleSortingHandler()}
                      aria-label={`Sort by ${String(header.column.columnDef.header)}`}
                    >
                      {flexRender(header.column.columnDef.header, header.getContext())}
                      {header.column.getIsSorted() === 'asc' ? <ArrowUp className="size-3" />
                        : header.column.getIsSorted() === 'desc' ? <ArrowDown className="size-3" />
                          : <ChevronsUpDown className="size-3 opacity-40" />}
                    </button>
                  ) : flexRender(header.column.columnDef.header, header.getContext())}
                </TableHead>
              ))}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {shown.map(row => (
            <TableRow
              key={row.id}
              tabIndex={onSelect ? 0 : undefined}
              onClick={onSelect ? () => onSelect(row.original) : undefined}
              onKeyDown={onSelect ? event => {
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault()
                  onSelect(row.original)
                }
              } : undefined}
              className={cn(onSelect && 'cursor-pointer', fresh.has(row.original.id) && 'row-enter')}
            >
              {row.getVisibleCells().map(cell => (
                <TableCell
                  key={cell.id}
                  className={cn(cellPad, 'py-3', cell.column.id !== 'model' && 'text-right text-sm')}
                >
                  {flexRender(cell.column.columnDef.cell, cell.getContext())}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
