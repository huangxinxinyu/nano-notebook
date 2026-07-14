import { MaterialSymbol } from "../icons/material-symbol";

export function ProductMark({ name, compact = false }: { name: string; compact?: boolean }) {
  return (
    <div className="product-mark" data-compact={compact || undefined}>
      <span className="product-mark-icon" aria-hidden="true">
        <MaterialSymbol name="book_2" size={compact ? 24 : 28} weight={500} />
      </span>
      <span className="product-mark-name">{name}</span>
    </div>
  );
}
