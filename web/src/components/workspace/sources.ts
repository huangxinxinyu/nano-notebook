import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

export type MemberSource = {
  id: string;
  notebook_id: string;
  title: string;
  format: string;
  byte_size: number;
  state: "processing" | "ready" | "failed";
  failure_reason?: "limits_exceeded" | "source_unavailable" | "content_unreadable" | "indexing_failed" | "retrieval_unavailable" | "processing_interrupted" | "processing_failed";
};

export type SourcesController = {
  sources: MemberSource[];
  selectedSourceIDs: string[];
  isLoading: boolean;
  error: string | null;
  toggle: (sourceID: string) => void;
  refresh: () => Promise<unknown>;
};

export function useNotebookSources(notebookID: string, unavailableLabel: string): SourcesController {
  const [selection, setSelection] = useState<{ notebookID: string; overrides: Record<string, boolean> }>(() => ({ notebookID, overrides: {} }));
  const query = useQuery({
    queryKey: ["notebook-sources", notebookID],
    queryFn: async () => {
      const response = await fetch(`/api/v1/notebooks/${notebookID}/sources`, { credentials: "include" });
      if (!response.ok) throw new Error(unavailableLabel);
      return ((await response.json()) as { sources: MemberSource[] }).sources;
    },
    refetchInterval: ({ state }) => state.data?.some((item) => item.state === "processing") ? 2500 : false,
    retry: false
  });
  const overrides = selection.notebookID === notebookID ? selection.overrides : {};
  const selectedSourceIDs = (query.data ?? [])
    .filter((item) => item.state === "ready" && overrides[item.id] !== false)
    .map((item) => item.id);

  return {
    sources: query.data ?? [],
    selectedSourceIDs,
    isLoading: query.isLoading,
    error: query.isError ? unavailableLabel : null,
    toggle: (sourceID) => setSelection((current) => {
      const currentOverrides = current.notebookID === notebookID ? current.overrides : {};
      const isSelected = selectedSourceIDs.includes(sourceID);
      return { notebookID, overrides: { ...currentOverrides, [sourceID]: !isSelected } };
    }),
    refresh: query.refetch
  };
}
