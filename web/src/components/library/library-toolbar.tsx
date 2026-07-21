import type { ReactNode } from "react";
import { IconButton } from "../icons/icon-button";
import { MaterialSymbol } from "../icons/material-symbol";
import { Button } from "../ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "../ui/dropdown-menu";
import { Input } from "../ui/input";
import { Tabs, TabsList, TabsTrigger } from "../ui/tabs";
import { ToggleGroup, ToggleGroupItem } from "../ui/toggle-group";

export type LibraryView = "grid" | "list";
export type NotebookSort = "recent" | "title";
export type LibraryScope = "all" | "owned" | "shared";

type LibraryToolbarProps = {
  allLabel: string;
  featuredLabel: string;
  sharedLabel: string;
  searchLabel: string;
  closeSearchLabel: string;
  gridLabel: string;
  listLabel: string;
  sortLabel: string;
  recentLabel: string;
  titleLabel: string;
  searchOpen: boolean;
  query: string;
  view: LibraryView;
  sort: NotebookSort;
  scope: LibraryScope;
  createAction: ReactNode;
  onSearchOpen: () => void;
  onSearchClose: () => void;
  onQueryChange: (value: string) => void;
  onViewChange: (view: LibraryView) => void;
  onSortChange: (sort: NotebookSort) => void;
  onScopeChange: (scope: LibraryScope) => void;
};

export function LibraryToolbar(props: LibraryToolbarProps) {
  return (
    <div className="library-toolbar">
      <Tabs value={props.scope} onValueChange={(value) => props.onScopeChange(value as LibraryScope)} className="library-filter-tabs">
        <TabsList aria-label={props.allLabel}>
          <TabsTrigger value="all">{props.allLabel}</TabsTrigger>
          <TabsTrigger value="owned">{props.featuredLabel}</TabsTrigger>
          <TabsTrigger value="shared">{props.sharedLabel}</TabsTrigger>
        </TabsList>
      </Tabs>
      <div className="library-tools">
        {props.searchOpen ? (
          <div className="library-search">
            <MaterialSymbol name="search" size={20} />
            <Input autoFocus aria-label={props.searchLabel} placeholder={props.searchLabel} value={props.query} onChange={(event) => props.onQueryChange(event.target.value)} />
            <IconButton icon="close" label={props.closeSearchLabel} symbolSize={18} onClick={props.onSearchClose} />
          </div>
        ) : (
          <IconButton className="library-tool-button" icon="search" label={props.searchLabel} onClick={props.onSearchOpen} />
        )}
        <ToggleGroup aria-label={props.listLabel} type="single" value={props.view} onValueChange={(value) => value && props.onViewChange(value as LibraryView)}>
          <ToggleGroupItem aria-label={props.gridLabel} value="grid"><MaterialSymbol name="grid_view" size={20} /></ToggleGroupItem>
          <ToggleGroupItem aria-label={props.listLabel} value="list"><MaterialSymbol name="view_list" size={21} /></ToggleGroupItem>
        </ToggleGroup>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button aria-label={props.sortLabel} className="library-sort" variant="outline">
              {props.sort === "recent" ? props.recentLabel : props.titleLabel}
              <MaterialSymbol name="arrow_drop_down" size={18} />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => props.onSortChange("recent")}>{props.recentLabel}</DropdownMenuItem>
            <DropdownMenuItem onSelect={() => props.onSortChange("title")}>{props.titleLabel}</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        {props.createAction}
      </div>
    </div>
  );
}
