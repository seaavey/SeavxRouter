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
import { format, statusTone } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { RequestLog } from '@/lib/api'

const dotFor = { positive: 'positive', warning: 'warning', destructive: 'destructive' } as const

function StatusCell({ status }: { status: number }) {
  return (
    <span className="inline-flex items-center gap-2 font-mono text-xs tabular">
      <Dot tone={dotFor[statusTone(status)]} />
      {status}
    </span>
  )
}

/**
 * Recent requests. Sorting and filtering stay in the browser because the
 * router already hands us the page; the search box filters every column.
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
      header: 'Model',
      cell: ({ row }) => (
        <span className="font-mono text-xs text-foreground">{row.original.model || '—'}</span>
      ),
    },
    { accessorKey: 'provider', header: 'Provider', cell: ({ getValue }) => <span className="text-muted-foreground">{getValue<string>() || '—'}</span> },
    { accessorKey: 'endpoint', header: 'Endpoint', cell: ({ getValue }) => <span className="font-mono text-xs text-muted-foreground">{getValue<string>() || '—'}</span> },
    { accessorKey: 'status', header: 'Status', cell: ({ getValue }) => <StatusCell status={getValue<number>()} /> },
    { accessorKey: 'duration_ms', header: 'Latency', cell: ({ getValue }) => <span className="font-mono text-xs tabular text-muted-foreground">{format.latency(getValue<number>())}</span> },
    { accessorKey: 'usage.total_tokens', header: 'Tokens', cell: ({ row }) => <span className="font-mono text-xs tabular text-muted-foreground">{format.count(row.original.usage.total_tokens)}</span> },
    { accessorKey: 'timestamp', header: 'Time', cell: ({ getValue }) => <span className="font-mono text-xs tabular text-muted-foreground">{format.time(getValue<string>())}</span> },
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
    return <div className="grid gap-2 p-4">{Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-9 w-full" />)}</div>
  }

  if (!rows.length) {
    return (
      <Empty className="border-0 py-12">
        <EmptyHeader>
          <EmptyTitle>No requests recorded</EmptyTitle>
          <EmptyDescription>Requests appear here once the gateway handles traffic on this usage database.</EmptyDescription>
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
          placeholder="Filter requests"
          aria-label="Filter requests"
          className="h-8 min-w-0 max-w-64"
        />
        <span className="shrink-0 font-mono text-[11px] whitespace-nowrap text-muted-foreground/70">
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
              className={cn(onSelect && 'cursor-pointer focus-visible:bg-muted/60 focus-visible:outline-none')}
            >
              {row.getVisibleCells().map(cell => (
                <TableCell key={cell.id} className="px-4 py-2">
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
