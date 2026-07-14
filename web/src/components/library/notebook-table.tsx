import { flexRender, getCoreRowModel, getSortedRowModel, useReactTable, type ColumnDef, type SortingState } from "@tanstack/react-table";
import { useMemo } from "react";
import { toast } from "sonner";
import { MaterialSymbol } from "../icons/material-symbol";
import { Button } from "../ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "../ui/dropdown-menu";
import { Skeleton } from "../ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import type { NotebookSort } from "./library-toolbar";

export type LibraryNotebook = { id: string; title: string; recent_at?: string };

type NotebookTableProps = {
  notebooks: LibraryNotebook[];
  sort: NotebookSort;
  label: string;
  titleLabel: string;
  sourceLabel: string;
  creationDateLabel: string;
  roleLabel: string;
  ownerLabel: string;
  zeroSourcesLabel: string;
  missingDateLabel: string;
  openLabel: (title: string) => string;
  moreLabel: (title: string) => string;
  renameLabel: string;
  shareLabel: string;
  deleteLabel: string;
  comingSoonMessage: string;
  emptyMessage: string;
  errorMessage: string;
  loading: boolean;
  error: boolean;
  retryLabel: string;
  onOpen: (id: string) => void;
  onRetry: () => void;
};

export function NotebookTable(props: NotebookTableProps) {
  const columns = useMemo<ColumnDef<LibraryNotebook>[]>(() => [
    {
      accessorKey: "title",
      header: props.titleLabel,
      cell: ({ row }) => (
        <Button className="library-item-action notebook-title-action" variant="ghost" onClick={(event) => { event.stopPropagation(); props.onOpen(row.original.id); }} aria-label={props.openLabel(row.original.title)}>
          <MaterialSymbol name="folder" size={18} fill />
          <span>{row.original.title}</span>
        </Button>
      )
    },
    { id: "source", header: props.sourceLabel, cell: () => props.zeroSourcesLabel },
    { id: "created", header: props.creationDateLabel, cell: () => props.missingDateLabel },
    { id: "visibility", header: "", cell: () => null },
    { id: "role", header: props.roleLabel, cell: () => props.ownerLabel },
    {
      id: "actions",
      header: "",
      cell: ({ row }) => (
        <DropdownMenu>
          <DropdownMenuTrigger asChild onClick={(event) => event.stopPropagation()}>
            <Button className="notebook-more" aria-label={props.moreLabel(row.original.title)} size="icon" variant="ghost"><MaterialSymbol name="more_vert" size={20} /></Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {[props.renameLabel, props.shareLabel, props.deleteLabel].map((label) => (
              <DropdownMenuItem key={label} onSelect={() => toast(props.comingSoonMessage)}>{label}</DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )
    },
    { accessorKey: "recent_at" }
  ], [props]);

  const sorting = useMemo<SortingState>(() => props.sort === "title" ? [{ id: "title", desc: false }] : [{ id: "recent_at", desc: true }], [props.sort]);
  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Table intentionally owns its mutable row-model helpers.
  const table = useReactTable({ data: props.notebooks, columns, state: { sorting }, getCoreRowModel: getCoreRowModel(), getSortedRowModel: getSortedRowModel() });
  const visibleColumns = table.getVisibleLeafColumns().filter((column) => column.id !== "recent_at");

  return (
    <Table aria-label={props.label}>
      <TableHeader>
        {table.getHeaderGroups().map((headerGroup) => (
          <TableRow key={headerGroup.id}>
            {headerGroup.headers.filter((header) => header.id !== "recent_at").map((header) => <TableHead key={header.id}>{header.isPlaceholder ? null : flexRender(header.column.columnDef.header, header.getContext())}</TableHead>)}
          </TableRow>
        ))}
      </TableHeader>
      <TableBody>
        {props.loading ? Array.from({ length: 3 }, (_, index) => (
          <TableRow key={index} className="notebook-skeleton-row">
            {visibleColumns.map((column) => <TableCell key={column.id}><Skeleton className="notebook-skeleton" /></TableCell>)}
          </TableRow>
        )) : null}
        {props.error ? (
          <TableRow><TableCell colSpan={visibleColumns.length}><div className="table-inline-state"><span>{props.errorMessage}</span><Button variant="outline" onClick={props.onRetry}>{props.retryLabel}</Button></div></TableCell></TableRow>
        ) : null}
        {!props.loading && !props.error && table.getRowModel().rows.length === 0 ? (
          <TableRow><TableCell className="table-empty-state" colSpan={visibleColumns.length}>{props.emptyMessage}</TableCell></TableRow>
        ) : null}
        {!props.loading && !props.error ? table.getRowModel().rows.map((row) => (
          <TableRow key={row.id} className="notebook-data-row" tabIndex={0} onClick={() => props.onOpen(row.original.id)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") props.onOpen(row.original.id); }}>
            {row.getVisibleCells().filter((cell) => cell.column.id !== "recent_at").map((cell) => <TableCell key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</TableCell>)}
          </TableRow>
        )) : null}
      </TableBody>
    </Table>
  );
}
