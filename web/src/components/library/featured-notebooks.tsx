import { toast } from "sonner";
import { MaterialSymbol } from "../icons/material-symbol";
import { Button } from "../ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";

type FeaturedNotebooksProps = {
  label: string;
  titleLabel: string;
  sourceLabel: string;
  creationDateLabel: string;
  roleLabel: string;
  readerLabel: string;
  openLabel: (title: string) => string;
  comingSoonMessage: string;
  locale: "en" | "zh";
};

const featuredRowsEn = [
  ["Benjamin Franklin's Science", "38", "2025-09-30"],
  ["William Shakespeare: Complete Works", "45", "2025-04-26"],
  ["Health, Wealth and Happiness", "24", "2025-04-15"],
  ["Jane Austen: Complete Works", "11", "2025-11-12"],
  ["How to Build a Life", "46", "2025-04-23"]
] as const;

const featuredRowsZh = [
  ["Benjamin Franklin 的科学", "38", "2025年9月30日"],
  ["William Shakespeare：全集", "45", "2025年4月26日"],
  ["健康、财富与幸福的趋势", "24", "2025年4月15日"],
  ["Jane Austen：全集", "11", "2025年11月12日"],
  ["如何建立理想生活", "46", "2025年4月23日"]
] as const;

export function FeaturedNotebooks(props: FeaturedNotebooksProps) {
  const featuredRows = props.locale === "zh" ? featuredRowsZh : featuredRowsEn;
  return (
    <Table aria-label={props.label}>
      <TableHeader><TableRow><TableHead>{props.titleLabel}</TableHead><TableHead>{props.sourceLabel}</TableHead><TableHead>{props.creationDateLabel}</TableHead><TableHead /><TableHead>{props.roleLabel}</TableHead><TableHead /></TableRow></TableHeader>
      <TableBody>
        {featuredRows.map(([title, sources, date]) => (
          <TableRow key={title} data-placeholder="true">
            <TableCell><Button className="featured-title-action" variant="ghost" aria-label={props.openLabel(title)} onClick={() => toast(props.comingSoonMessage)}><MaterialSymbol name="public" size={18} /><span>{title}</span></Button></TableCell>
            <TableCell>{props.locale === "zh" ? `${sources} 个来源` : `${sources} sources`}</TableCell>
            <TableCell>{date}</TableCell>
            <TableCell><MaterialSymbol name="public" size={19} /></TableCell>
            <TableCell>{props.readerLabel}</TableCell>
            <TableCell />
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
