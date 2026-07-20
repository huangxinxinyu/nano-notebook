import { useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { toast } from "sonner";
import { IconButton } from "../icons/icon-button";
import { MaterialSymbol } from "../icons/material-symbol";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { acceptedSourceFormats, csrfToken, memberAPI, uploadSourceFile } from "./source-upload";
import type { MemberSource, SourcesController } from "./sources";
import { SourceImageViewer, type SourceImageRegion } from "./source-image-viewer";

type SourceCoordinate = {
  page_number?: number;
  slide_number?: number;
  start_seconds?: number;
  end_seconds?: number;
  kind?: string;
} & SourceImageRegion;

type SourceView = {
  id: string;
  title: string;
  format: string;
  revision: {
    coverage: { status: string; gaps: Array<{ reason: string; impact: string }> };
    units: Array<{ id: string; kind: string; text: string; coordinate?: SourceCoordinate }>;
  };
};

export type SourcePanelCopy = {
  title: string;
  addSourcesLabel: string;
  emptyTitle: string;
  emptyBody: string;
  collapseLabel: string;
  comingSoonMessage: string;
  addDialogTitle: string;
  addDialogBody: string;
  chooseFilesLabel: string;
  supportedFormatsLabel: string;
  urlLabel: string;
  urlPlaceholder: string;
  addURLLabel: string;
  readyLabel: string;
  processingLabel: string;
  sourceFailedLabel: string;
  retryLabel: string;
  deleteLabel: string;
  renameLabel: string;
  useSourceLabel: string;
  sourceUnavailableLabel: string;
  uploadFailedLabel: string;
  closeLabel: string;
  sourcePreviewLabel: string;
  renameDialogTitle: string;
  sourceTitleLabel: string;
  saveLabel: string;
  removeDialogTitle: string;
  removeDialogBody: string;
  removeConfirmLabel: string;
  cancelLabel: string;
  coverageWarningLabel: string;
  failureReasonLabels: Record<NonNullable<MemberSource["failure_reason"]>, string>;
};

export function SourcePanelContent({ copy, notebookID, controller }: {
  copy: SourcePanelCopy;
  notebookID: string;
  controller: SourcesController;
}) {
  const fileInput = useRef<HTMLInputElement>(null);
  const [addOpen, setAddOpen] = useState(false);
  const [url, setURL] = useState("");
  const [addingURL, setAddingURL] = useState(false);
  const [uploads, setUploads] = useState<Array<{ id: string; title: string; state: "uploading" | "failed" }>>([]);
  const [viewSourceID, setViewSourceID] = useState<string | null>(null);
  const [editingSource, setEditingSource] = useState<{ id: string; title: string } | null>(null);
  const [editTitle, setEditTitle] = useState("");
  const [removingSource, setRemovingSource] = useState<MemberSource | null>(null);

  async function addFiles(files: FileList | null) {
    if (!files?.length) return;
    const batch = [...files].map((file) => ({ id: crypto.randomUUID(), file }));
    setUploads((current) => [...current, ...batch.map(({ id, file }) => ({ id, title: file.name, state: "uploading" as const }))]);
    await Promise.allSettled(batch.map(async ({ id, file }) => {
      try {
        await uploadSourceFile(notebookID, file);
        setUploads((current) => current.filter((item) => item.id !== id));
      } catch {
        setUploads((current) => current.map((item) => item.id === id ? { ...item, state: "failed" } : item));
      }
    }));
    if (fileInput.current) fileInput.current.value = "";
    await controller.refresh();
  }

  async function addURL() {
    const requestURL = url.trim();
    if (!requestURL || addingURL) return;
    setAddingURL(true);
    try {
      const response = await memberAPI(`/api/v1/notebooks/${notebookID}/sources/urls`, {
        method: "POST",
        headers: { "Idempotency-Key": crypto.randomUUID(), "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ url: requestURL })
      });
      if (!response.ok) throw new Error(copy.uploadFailedLabel);
      setURL("");
      setAddOpen(false);
      await controller.refresh();
    } catch {
      toast.error(copy.uploadFailedLabel);
    } finally {
      setAddingURL(false);
    }
  }

  async function sourceAction(sourceID: string, action: "retry" | "delete") {
    const path = action === "retry" ? `/api/v1/sources/${sourceID}/retry` : `/api/v1/sources/${sourceID}`;
    const response = await memberAPI(path, {
      method: action === "retry" ? "POST" : "DELETE",
      headers: { "X-CSRF-Token": csrfToken() }
    });
    if (!response.ok) {
      toast.error(copy.sourceUnavailableLabel);
      return;
    }
    await controller.refresh();
  }

  async function renameSource() {
    const title = editTitle.trim();
    if (!editingSource || !title) return;
    const response = await memberAPI(`/api/v1/sources/${editingSource.id}`, {
      method: "PATCH",
      headers: { "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ title })
    });
    if (!response.ok) {
      toast.error(copy.sourceUnavailableLabel);
      return;
    }
    setEditingSource(null);
    await controller.refresh();
  }

  const statusLabels = { ready: copy.readyLabel, processing: copy.processingLabel, failed: copy.sourceFailedLabel };

  return (
    <div className="workspace-panel-content source-panel-content">
      <div className="workspace-panel-header">
        <h2>{copy.title}</h2>
        <IconButton icon="right_panel_close" label={copy.collapseLabel} symbolSize={19} onClick={() => toast(copy.comingSoonMessage)} />
      </div>
      <div className="source-panel-controls">
        <Button className="add-sources-action" variant="outline" onClick={() => setAddOpen(true)}>
          <MaterialSymbol name="add" size={20} />
          {copy.addSourcesLabel}
        </Button>
      </div>
      {controller.error ? <p className="source-panel-error" role="alert">{controller.error}</p> : null}
      {!controller.isLoading && controller.sources.length === 0 ? (
        <div className="panel-empty-state">
          <MaterialSymbol name="draft" size={28} />
          <strong>{copy.emptyTitle}</strong>
          <p>{copy.emptyBody}</p>
        </div>
      ) : (
        <div className="source-list">
          {controller.sources.map((source) => (
            <article className="source-list-item" key={source.id}>
              {source.state === "ready" ? (
                <input
                  type="checkbox"
                  aria-label={`${copy.useSourceLabel} ${source.title}`}
                  checked={controller.selectedSourceIDs.includes(source.id)}
                  onChange={() => controller.toggle(source.id)}
                />
              ) : <MaterialSymbol name={source.state === "failed" ? "error" : "hourglass_top"} size={18} />}
              <button className="source-list-title" type="button" disabled={source.state !== "ready"} onClick={() => setViewSourceID(source.id)}>{source.title}</button>
              <span className={`source-state source-state--${source.state}`}>{statusLabels[source.state]}</span>
              {source.state === "failed" ? <IconButton icon="refresh" label={`${copy.retryLabel} ${source.title}`} onClick={() => void sourceAction(source.id, "retry")} /> : null}
              <IconButton icon="edit" label={`${copy.renameLabel} ${source.title}`} onClick={() => { setEditingSource(source); setEditTitle(source.title); }} />
              <IconButton icon="delete" label={`${copy.deleteLabel} ${source.title}`} onClick={() => setRemovingSource(source)} />
              {source.state === "failed" && source.failure_reason ? <p className="source-failure-reason">{copy.failureReasonLabels[source.failure_reason]}</p> : null}
            </article>
          ))}
        </div>
      )}

      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent className="source-dialog" closeLabel={copy.closeLabel}>
          <DialogTitle>{copy.addDialogTitle}</DialogTitle>
          <DialogDescription>{copy.addDialogBody}</DialogDescription>
          <input ref={fileInput} className="sr-only" type="file" multiple accept={acceptedSourceFormats} aria-label={copy.chooseFilesLabel} onChange={(event) => void addFiles(event.target.files)} />
          <Button variant="outline" onClick={() => fileInput.current?.click()}><MaterialSymbol name="upload_file" size={19} />{copy.chooseFilesLabel}</Button>
          <p className="source-format-help">{copy.supportedFormatsLabel}</p>
          {uploads.length ? <div className="source-upload-list">{uploads.map((item) => <span key={item.id}>{item.title} · {item.state === "failed" ? copy.sourceFailedLabel : copy.processingLabel}</span>)}</div> : null}
          <div className="source-dialog-divider" />
          <Label htmlFor="source-url">{copy.urlLabel}</Label>
          <div className="source-url-row">
            <Input id="source-url" type="url" value={url} placeholder={copy.urlPlaceholder} onChange={(event) => setURL(event.target.value)} />
            <Button disabled={!url.trim() || addingURL} onClick={() => void addURL()}>{copy.addURLLabel}</Button>
          </div>
        </DialogContent>
      </Dialog>

      <SourceViewer sourceID={viewSourceID} onOpenChange={(open) => !open && setViewSourceID(null)} copy={copy} />

      <Dialog open={Boolean(editingSource)} onOpenChange={(open) => !open && setEditingSource(null)}>
        <DialogContent className="source-dialog" closeLabel={copy.closeLabel}>
          <DialogTitle>{copy.renameDialogTitle}</DialogTitle>
          <Label htmlFor="rename-source-title">{copy.sourceTitleLabel}</Label>
          <Input id="rename-source-title" value={editTitle} onChange={(event) => setEditTitle(event.target.value)} />
          <div className="dialog-actions">
            <Button variant="ghost" onClick={() => setEditingSource(null)}>{copy.cancelLabel}</Button>
            <Button disabled={!editTitle.trim()} onClick={() => void renameSource()}>{copy.saveLabel}</Button>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(removingSource)} onOpenChange={(open) => !open && setRemovingSource(null)}>
        <DialogContent className="source-dialog" closeLabel={copy.closeLabel}>
          <DialogTitle>{copy.removeDialogTitle}</DialogTitle>
          <DialogDescription>{copy.removeDialogBody}</DialogDescription>
          <div className="dialog-actions">
            <Button variant="ghost" onClick={() => setRemovingSource(null)}>{copy.cancelLabel}</Button>
            <Button variant="destructive" onClick={() => {
              const sourceID = removingSource?.id;
              setRemovingSource(null);
              if (sourceID) void sourceAction(sourceID, "delete");
            }}>{copy.removeConfirmLabel}</Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function SourceViewer({ sourceID, onOpenChange, copy }: {
  sourceID: string | null;
  onOpenChange: (open: boolean) => void;
  copy: SourcePanelCopy;
}) {
  const view = useQuery({
    queryKey: ["source-view", sourceID],
    enabled: Boolean(sourceID),
    queryFn: async (): Promise<SourceView> => {
      const response = await memberAPI(`/api/v1/sources/${sourceID}`);
      if (!response.ok) throw new Error(copy.sourceUnavailableLabel);
      return ((await response.json()) as { source: SourceView }).source;
    },
    retry: false
  });
  return (
    <Dialog open={Boolean(sourceID)} onOpenChange={onOpenChange}>
      <DialogContent className="source-viewer-dialog" closeLabel={copy.closeLabel}>
        <DialogTitle>{view.data?.title ?? copy.sourcePreviewLabel}</DialogTitle>
        <DialogDescription>{view.data ? `${view.data.format.toUpperCase()} · ${view.data.revision.coverage.status}` : copy.processingLabel}</DialogDescription>
        {view.isError ? <p role="alert">{copy.sourceUnavailableLabel}</p> : null}
        {view.data?.revision.coverage.gaps.length ? (
          <div className="source-coverage-warning" role="note">
            <strong>{copy.coverageWarningLabel}</strong>
            {view.data.revision.coverage.gaps.map((gap, index) => <p key={index}>{gap.reason} · {gap.impact}</p>)}
          </div>
        ) : null}
        {view.data && isImageFormat(view.data.format) ? (
          <SourceImageViewer
            sourceID={view.data.id}
            title={view.data.title}
            regions={view.data.revision.units.map((unit) => unit.coordinate ?? {}).filter((coordinate) => coordinate.kind === "image_region")}
          />
        ) : null}
        <div className="source-viewer-content">
          {view.data?.revision.units.map((unit) => <section key={unit.id}><small>{unit.kind}</small><p>{unit.text}</p></section>)}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function isImageFormat(format: string) {
  return format === "png" || format === "jpeg" || format === "webp";
}
