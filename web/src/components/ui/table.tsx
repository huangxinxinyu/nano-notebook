import * as React from "react";
import { cn } from "../../lib/utils";

function Table({ className, ...props }: React.ComponentProps<"table">) { return <table data-slot="table" className={cn("nn-table", className)} {...props} />; }
function TableHeader({ className, ...props }: React.ComponentProps<"thead">) { return <thead data-slot="table-header" className={cn("nn-table-header", className)} {...props} />; }
function TableBody({ className, ...props }: React.ComponentProps<"tbody">) { return <tbody data-slot="table-body" className={cn("nn-table-body", className)} {...props} />; }
function TableFooter({ className, ...props }: React.ComponentProps<"tfoot">) { return <tfoot data-slot="table-footer" className={cn("nn-table-footer", className)} {...props} />; }
function TableRow({ className, ...props }: React.ComponentProps<"tr">) { return <tr data-slot="table-row" className={cn("nn-table-row", className)} {...props} />; }
function TableHead({ className, ...props }: React.ComponentProps<"th">) { return <th data-slot="table-head" className={cn("nn-table-head", className)} {...props} />; }
function TableCell({ className, ...props }: React.ComponentProps<"td">) { return <td data-slot="table-cell" className={cn("nn-table-cell", className)} {...props} />; }
function TableCaption({ className, ...props }: React.ComponentProps<"caption">) { return <caption data-slot="table-caption" className={cn("nn-table-caption", className)} {...props} />; }

export { Table, TableBody, TableCaption, TableCell, TableFooter, TableHead, TableHeader, TableRow };
