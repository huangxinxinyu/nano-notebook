import type { ComponentProps, ReactNode } from "react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";
import { ChatPanelContent, type ChatPanelCopy } from "./chat-placeholder-panel";
import { usePrivateChat } from "./private-chat";
import { SourcePanelContent } from "./source-panel";
import { StudioPanelContent } from "./studio-panel";

type WorkspacePanelCopy = ChatPanelCopy & {
  panelsLabel: string;
  sources: string;
  chat: string;
  studio: string;
  addSources: string;
  searchWeb: string;
  web: string;
  fastResearch: string;
  sourcesEmptyTitle: string;
  sourcesEmptyBody: string;
  collapsePanel: string;
  comingSoon: string;
  beta: string;
  studioEmptyTitle: string;
  studioEmptyBody: string;
  addNote: string;
  studioActions: ComponentProps<typeof StudioPanelContent>["actions"];
};

export function NotebookWorkspace({ notebookID, copy }: { notebookID: string; copy: WorkspacePanelCopy }) {
  const chatController = usePrivateChat(notebookID, copy);
  const panels = {
    sources: <SourcePanelContent title={copy.sources} addSourcesLabel={copy.addSources} searchWebLabel={copy.searchWeb} webLabel={copy.web} fastResearchLabel={copy.fastResearch} emptyTitle={copy.sourcesEmptyTitle} emptyBody={copy.sourcesEmptyBody} collapseLabel={copy.collapsePanel} comingSoonMessage={copy.comingSoon} />,
    chat: <ChatPanelContent copy={copy} controller={chatController} />,
    studio: <StudioPanelContent title={copy.studio} actions={copy.studioActions} betaLabel={copy.beta} emptyTitle={copy.studioEmptyTitle} emptyBody={copy.studioEmptyBody} addNoteLabel={copy.addNote} collapseLabel={copy.collapsePanel} comingSoonMessage={copy.comingSoon} />
  };

  return (
    <>
      <div className="workspace-panels" aria-label={copy.panelsLabel}>
        <WorkspaceRegion id="sources" title={copy.sources}>{panels.sources}</WorkspaceRegion>
        <WorkspaceRegion id="chat" title={copy.chat} chatFramework>{panels.chat}</WorkspaceRegion>
        <WorkspaceRegion id="studio" title={copy.studio}>{panels.studio}</WorkspaceRegion>
      </div>
      <Tabs defaultValue="sources" className="workspace-compact-tabs">
        <TabsList className="workspace-tabs" aria-label={copy.panelsLabel}>
          <TabsTrigger value="sources">{copy.sources}</TabsTrigger>
          <TabsTrigger value="chat">{copy.chat}</TabsTrigger>
          <TabsTrigger value="studio">{copy.studio}</TabsTrigger>
        </TabsList>
        <WorkspaceTab value="sources">{panels.sources}</WorkspaceTab>
        <WorkspaceTab value="chat">{panels.chat}</WorkspaceTab>
        <WorkspaceTab value="studio">{panels.studio}</WorkspaceTab>
      </Tabs>
    </>
  );
}

function WorkspaceRegion({ id, title, chatFramework = false, children }: { id: string; title: string; chatFramework?: boolean; children: ReactNode }) {
  const titleID = `workspace-${id}-title`;
  return (
    <section className={`workspace-panel workspace-panel--${id}`} role="region" aria-labelledby={titleID} data-chat-framework={chatFramework ? "@assistant-ui/react" : undefined}>
      <span className="sr-only" id={titleID}>{title}</span>
      {children}
    </section>
  );
}

function WorkspaceTab({ value, children }: { value: string; children: ReactNode }) {
  return <TabsContent className="workspace-panel workspace-panel--compact" value={value}>{children}</TabsContent>;
}
