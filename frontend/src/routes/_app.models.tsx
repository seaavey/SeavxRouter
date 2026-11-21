import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { flexRender, getCoreRowModel, getFilteredRowModel, useReactTable, type ColumnDef } from '@tanstack/react-table'
import { Waypoints } from 'lucide-react'
import { PageHeader } from '@/components/page-header'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { modelsQuery } from '@/lib/queries'
import type { Model } from '@/lib/api'

export const Route = createFileRoute('/_app/models')({ component: ModelsPage })

const columns: ColumnDef<Model>[] = [
  {
    accessorKey: 'id',
    header: 'Model',
    cell: ({ getValue }) => <span className="font-mono text-xs">{getValue<string>()}</span>,
  },
  {
    accessorKey: 'owned_by',
    header: 'Owner',
    cell: ({ getValue }) => <span className="text-muted-foreground">{getValue<string>() || '—'}</span>,
  },
]

function ModelsPage() {
  const models = useQuery(modelsQuery)
  const [filter, setFilter] = useState('')
  const rows = models.data ?? []

  const table = useReactTable({
    data: rows,
    columns,
    state: { globalFilter: filter },
    globalFilterFn: 'includesString',
    getCoreRowModel: getCoreRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
  })

  return (
    <>
      <PageHeader
        title="Models"
        description="Model IDs this gateway exposes on /v1/models."
        actions={
          <Input
            value={filter}
            onChange={event => setFilter(event.target.value)}
            placeholder="Find a model"
            aria-label="Find a model"
            className="h-8 w-56"
          />
        }
      />

      <Card className="py-0">
        <CardContent className="px-0">
          {models.isPending
            ? <div className="grid gap-2 p-4">{Array.from({ length: 6 }, (_, index) => <Skeleton key={index} className="h-9 w-full" />)}</div>
            : rows.length === 0
              ? (
                <Empty className="border-0 py-12">
                  <EmptyHeader>
                    <EmptyMedia variant="icon"><Waypoints /></EmptyMedia>
                    <EmptyTitle>No models exposed</EmptyTitle>
                    <EmptyDescription>The gateway has no provider accounts to list models from.</EmptyDescription>
                  </EmptyHeader>
                </Empty>
              )
              : (
                <Table>
                  <TableHeader>
                    {table.getHeaderGroups().map(group => (
                      <TableRow key={group.id} className="hover:bg-transparent">
                        {group.headers.map(header => (
                          <TableHead key={header.id} className="px-4 text-xs font-normal text-muted-foreground">
                            {flexRender(header.column.columnDef.header, header.getContext())}
                          </TableHead>
                        ))}
                      </TableRow>
                    ))}
                  </TableHeader>
                  <TableBody>
                    {table.getRowModel().rows.map(row => (
                      <TableRow key={row.id}>
                        {row.getVisibleCells().map(cell => (
                          <TableCell key={cell.id} className="px-4 py-2">
                            {flexRender(cell.column.columnDef.cell, cell.getContext())}
                          </TableCell>
                        ))}
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
        </CardContent>
      </Card>
    </>
  )
}
