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
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { Dot } from '@/components/status'
import { format, since, statusLabel, statusTone } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { RequestLog } from '@/lib/api'

const dotFor = { positive: 'positive', warning: 'warning', negative: 'negative' } as const

/**
 * Recent requests, kept to four columns: what ran, whether it worked, how long
 * it took, and when. Provider, endpoint and token counts live in the detail
 * panel so the row stays scannable.
 */
export function RequestTable({ rows, loading, onSelect, pageSize = 10 }: {
  rows: RequestLog[]
  loading?: boolean
  onSelect?: (row: RequestLog) => void
  pageSize?: number
}) {
  const [sorting, setSorting] = useState<SortingState>([])
  const [filter, setFilter] = useState('')

  const columns = useMemo<ColumnDef<RequestLog>[]>(() => [
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
        return (
          <span className="inline-flex items-center gap-2 text-sm">
            <Dot tone={dotFor[statusTone(status)]} />
            {statusLabel(status)}
          </span>
        )
      },
    },
    {
      accessorKey: 'duration_ms',
      header: 'Latency',
      cell: ({ getValue }) => <span className="text-sm tabular text-muted-foreground">{format.latency(getValue<number>())}</span>,
    },
    {
      accessorKey: 'timestamp',
      header: 'Time',
      cell: ({ getValue }) => (
        <span className="text-sm text-muted-foreground" title={since(getValue<string>())}>
          {format.time(getValue<string>())}
        </span>
      ),
    },
  ], [])

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

  const shown = table.getFilteredRowModel().rows.slice(0, pageSize)

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
                <TableHead key={header.id} className="px-4 text-xs font-normal text-muted-foreground">
                  {header.column.getCanSort() ? (
                    <Button
                      variant="ghost"
                      size="xs"
                      className="-ml-2 gap-1 px-2 font-normal text-muted-foreground"
                      onClick={header.column.getToggleSortingHandler()}
                    >
                      {flexRender(header.column.columnDef.header, header.getContext())}
                      {header.column.getIsSorted() === 'asc' ? <ArrowUp className="size-3" />
                        : header.column.getIsSorted() === 'desc' ? <ArrowDown className="size-3" />
                          : <ChevronsUpDown className="size-3 opacity-40" />}
                    </Button>
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
              className={cn(onSelect && 'cursor-pointer')}
            >
              {row.getVisibleCells().map(cell => (
                <TableCell key={cell.id} className="px-4 py-3">
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
