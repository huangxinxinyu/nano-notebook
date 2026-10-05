import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { useEffect, useLayoutEffect, useState, type ReactNode } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTitle, DialogTrigger } from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { Toaster } from "../components/ui/sonner";
import { Tabs, TabsList, TabsTrigger } from "../components/ui/tabs";
import { MaterialSymbol } from "../components/icons/material-symbol";
import { FeaturedNotebooks } from "../components/library/featured-notebooks";
import { LibraryToolbar, type LibraryScope, type LibraryView, type NotebookSort } from "../components/library/library-toolbar";
import { NotebookTable } from "../components/library/notebook-table";
import { LibraryHeader } from "../components/layout/app-header";
import { NotebookWorkspace } from "../components/workspace/notebook-workspace";
import { WorkspaceHeader } from "../components/workspace/workspace-header";
import { TraceDashboard } from "../components/traces/trace-dashboard";
import { queryClient } from "./queryClient";

type Locale = "en" | "zh";
type User = { id: string; email: string };
type Notebook = { id: string; title: string; role?: "viewer" | "editor" | "owner"; recent_at?: string };
type SessionState = { status: "anonymous" | "expired" } | { status: "authenticated"; user: User; capabilities: string[] };

const strings = {
  en: {
    languageSwitch: "Switch to 简体中文",
    app: "Nano Notebook",
    subtitle: "A local research notebook foundation for durable source-grounded work.",
    email: "Email",
    password: "Password",
    createAccount: "Create account",
    signIn: "Sign in",
    localOnly: "Local credentials are for development only.",
    library: "Library",
    newNotebook: "New notebook",
    search: "Search notebooks",
    emptyTitle: "Create your first Notebook",
    emptyBody: "Start with a title. Sources, chat, and outputs arrive in later sprints.",
    titleLabel: "Notebook title",
    createNotebook: "Create notebook",
    cancel: "Cancel",
    signOut: "Sign out",
    noResults: "No notebooks match that search.",
    loading: "Loading",
    back: "Back to Library",
    sources: "Sources",
    chat: "Chat",
    outputs: "Studio",
    sourcesEmpty: "Add files, a web page, or a YouTube URL. Each Source is processed independently.",
    chatEmpty: "Chat is intentionally empty until source processing and retrieval exist.",
    outputsEmpty: "Studio outputs are reserved without generation controls in this sprint.",
    unreachable: "Control Plane is unreachable. Retry after starting the local system.",
    validation: "Use a valid email, a 15+ character password, and a notebook title.",
    safeNotFound: "Notebook not found or unavailable.",
    credentialsValidation: "Use a valid email and a 15+ character password.",
    titleValidation: "Enter a notebook title.",
    duplicateEmail: "Email is already registered for this local workspace.",
    invalidCredentials: "Email or password is incorrect.",
    rateLimited: "Too many attempts. Retry shortly.",
    notebookQuota: "Notebook limit reached.",
    notebookCreateFailed: "Notebook could not be created. Retry after checking the local system.",
    retry: "Retry",
    signOutFailed: "Sign out failed. Retry to revoke the server session.",
    signingOut: "Signing out...",
    submitting: "Working...",
    sessionExpired: "Your session expired or was revoked. Sign in again to continue.",
    authModeLabel: "Authentication mode",
    notebookPanelsLabel: "Notebook panels",
    notificationsLabel: "Notifications",
    settings: "Settings",
    apps: "Google apps",
    openUserMenu: "Open user menu",
    allNotebooks: "All",
    featuredNotebooks: "Featured notebooks",
    ownedNotebooks: "Owned by me",
    sharedWithMe: "Shared with me",
    closeSearch: "Close search",
    gridView: "Grid view",
    listView: "List view",
    sortNotebooks: "Sort notebooks",
    recent: "Recent",
    sortTitle: "Title",
    recentlyOpened: "Recently opened notebooks",
    columnTitle: "Title",
    columnSource: "Source",
    creationDate: "Creation date",
    role: "Role",
    owner: "Owner",
    reader: "Reader",
    zeroSources: "0 sources",
    missingDate: "—",
    openNotebook: "Open",
    moreActions: "More actions for",
    rename: "Rename",
    share: "Share",
    manageAccess: "Manage access",
    inviteEmail: "Invite by email",
    invite: "Send invitation",
    viewer: "Viewer",
    editor: "Editor",
    members: "Members",
    pendingInvitations: "Pending invitations",
    leaveNotebook: "Leave notebook",
    invitationSent: "Invitation queued in the local mailbox.",
    acceptInvitation: "Accept invitation",
    invitationUnavailable: "This invitation is unavailable or has expired.",
    invitedAs: "Invited as",
    removeMember: "Remove",
    transferOwner: "Transfer ownership",
    revokeInvitation: "Revoke",
    resendInvitation: "Resend",
    notebookSettings: "Notebook settings",
    deleteNotebook: "Delete notebook permanently",
    deleteNotebookConfirm: "Permanently delete this shared Notebook?",
    delete: "Delete",
    comingSoon: "This feature is coming soon.",
    featuredComingSoon: "Featured notebooks are coming soon.",
    emptyTable: "No notebooks yet.",
    viewAll: "View all",
    gridComingSoon: "Grid view is coming soon.",
    analyze: "Analyze",
    addSources: "Add sources",
    sourceDiscovery: "Source discovery",
    searchWeb: "Search the web for new sources",
    web: "Web",
    researchComplete: "Research completed",
    viewDiscovery: "View",
    moreSources: "Additional sources: {count}",
    sourcesEmptyTitle: "Saved sources will appear here",
    collapsePanel: "Collapse panel",
    unavailableLabel: "Chat is temporarily unavailable.",
    chatEmptyTitle: "Chat will start here",
    chatEmptyBody: "Ask from model knowledge now. Sources can make later answers grounded in your material.",
    composerPlaceholder: "Ask anything…",
    composerLabel: "Message Nano Notebook",
    sendLabel: "Send message",
    waitingLabel: "Waiting to start…",
    generatingLabel: "Generating answer…",
    agentWorkingLabel: "Agent working · {time}",
    agentWorkedLabel: "Worked for {time}",
    analyzingLabel: "Analyzing the question",
    searchingSourcesLabel: "Searching selected sources",
    discoveringSourcesLabel: "Searching the web",
    inspectingSourceLabel: "Inspecting source",
    readingPDFLabel: "Reading PDF",
    readingWebpageLabel: "Reading webpage",
    savingSourceLabel: "Saving source",
    calculatingLabel: "Calculating",
    organizingStepsLabel: "Organizing steps",
    sourceDisclosure: "Answers use model knowledge and are not based on Notebook Sources.",
    selectedSourceDisclosure: "Answers can use the selected Sources ({count}) and include citations.",
    failedLabel: "The answer could not be generated. Try again.",
    stoppedLabel: "Stopped",
    stopLabel: "Stop",
    retryLabel: "Retry",
    chatModeLabel: "Chat",
    researchModeLabel: "Research",
    researchDisclosure: "Research searches and reads the public web, then produces a source-linked report.",
    researchPlanningLabel: "Building a research plan…",
    researchPlanTitle: "Research plan",
    researchPlanHelp: "Review or edit the plan JSON, save any changes, then start the full research run.",
    savePlanLabel: "Save plan",
    startResearchLabel: "Start research",
    savingLabel: "Saving…",
    startingLabel: "Starting…",
    researchProgressLabel: "Research progress",
    discoveredLabel: "Discovered",
    readLabel: "Read",
    failedSourcesLabel: "Unavailable",
    researchCompletedLabel: "Research complete",
    researchFailedLabel: "Research stopped",
    researchQuestionsTitle: "A few questions before planning",
    researchQuestionsHelp: "Your answers shape the research plan. Pick an option or write your own.",
    recommendedLabel: "Recommended",
    otherAnswerLabel: "Or write your own answer",
    submitAnswersLabel: "Submit answers",
    useRecommendedLabel: "Use all recommendations",
    revisePlanLabel: "Ask for changes",
    revisePlanPlaceholder: "Describe what to change, for example: focus on open-source options only.",
    submitRevisionLabel: "Revise plan",
    planInvalidLabel: "The plan is not valid. Keep every required field and use JSON arrays for list fields.",
    addSourceDialogTitle: "Add sources",
    addSourceDialogBody: "Upload supported files or save a web page or YouTube URL as an immutable Source.",
    chooseFiles: "Choose files",
    supportedSourceFormats: "PDF, DOCX, PPTX, text, Markdown, audio, and images · up to 100 MB each",
    sourceURL: "Web page or YouTube URL",
    sourceURLPlaceholder: "https://…",
    addURL: "Add URL",
    webSearchPlaceholder: "Search for sources",
    webSearchAction: "Search",
    webSearching: "Searching…",
    selectAll: "Select all",
    importSelected: "Import selected",
    webSearchFailed: "Web search is temporarily unavailable.",
    noSearchResults: "No relevant sources found.",
    openSearchResult: "Open result",
    sourceImportFailed: "This result could not be imported.",
    sourceReady: "Ready",
    sourceProcessing: "Processing",
    sourceFailed: "Failed",
    sourceFailureLimits: "This Source exceeded its processing limits.",
    sourceFailureUnavailable: "The stored Source is unavailable or changed.",
    sourceFailureUnreadable: "The file content could not be read.",
    sourceFailureIndexing: "The search index could not be verified.",
    sourceFailureRetrievalUnavailable: "Search indexing is not configured. Ask an administrator to activate a Retrieval Index Version.",
    sourceFailureInterrupted: "Processing was interrupted too many times.",
    sourceFailureGeneric: "This Source could not be processed.",
    deleteSource: "Delete",
    useSource: "Use",
    sourceUnavailable: "Sources are temporarily unavailable.",
    sourceUploadFailed: "This Source could not be added.",
    close: "Close",
    backToSources: "Back to Sources",
    sourceOriginal: "Original source",
    sourceOriginalUnavailable: "The original cannot be displayed.",
    sourcePreview: "Source preview",
    renameSource: "Rename source",
    sourceTitle: "Source title",
    save: "Save",
    removeSourceTitle: "Delete source permanently?",
    removeSourceBody: "Its citations will remain visible but can no longer reveal the passage.",
    removeSourceConfirm: "Delete permanently",
    coverageWarning: "Some Source content could not be extracted",
    verificationTitle: "Source verification",
    verificationBody: "This lightweight check runs before retrieval. Web results support identity matching only and are never added as evidence.",
    sourceConfidence: "Confidence",
    sourceSignalCoverage: "Signals checked",
    sourceVerified: "Verified",
    sourceNeedsReview: "Needs review",
    sourceLimitedVerification: "Limited verification",
    sourcePrivate: "Private source",
    sourceApproved: "Approved",
    sourceRejected: "Rejected",
    approveSource: "Approve source",
    rejectSource: "Reject source",
    reviewingSource: "Saving…",
    admissionExtractionComplete: "The extracted content passed the completeness checks.",
    admissionExtractionPartial: "Some content could not be extracted.",
    admissionExactURL: "Web Search found the exact canonical URL.",
    admissionExactIdentifier: "Web Search found the exact DOI, ISBN, or report identifier.",
    admissionExternalReference: "An independent domain also referenced this source.",
    admissionUnavailable: "External verification was temporarily unavailable.",
    admissionNotApplicable: "Public Web verification does not apply to private files.",
    admissionIdentityRequired: "No exact source identity was found.",
    admissionCoverageInsufficient: "Too few verification signals were available.",
    admissionScoreLow: "The confidence score is below the configured threshold.",
    citation: "Citation",
    citationUnavailable: "This citation is no longer available.",
    citationPreview: "Loading citation…",
    beta: "Beta",
    studioEmptyTitle: "Studio output will be saved here",
    studioEmptyBody: "Add sources, then choose an output above when generation becomes available.",
    studioRecent: "Recent",
    studioNoOutputs: "Your generated Studio outputs will appear here.",
    studioNoSource: "Select at least one ready Source first.",
    studioQueued: "Loading outputs…",
    studioGenerating: "Generating…",
    studioFailed: "Generation failed",
    studioDelete: "Delete",
    studioUnavailable: "Studio outputs are temporarily unavailable.",
    studioSource: "Source",
    studioSourceSingular: "source",
    studioSourcePlural: "sources",
    studioPrevious: "Previous card",
    studioNext: "Next card",
    studioFlip: "Flip card",
    studioShuffle: "Shuffle",
    studioRestart: "Restart",
    studioZoomIn: "Zoom in",
    studioZoomOut: "Zoom out",
    studioMissingArtifact: "This output is not ready to view.",
    addNote: "Add note",
    audioOverview: "Audio overview",
    presentation: "Slide deck",
    videoOverview: "Video overview",
    mindMap: "Mind map",
    report: "Report",
    flashcards: "Flashcards",
    quiz: "Quiz",
    dataTable: "Data table",
    infographic: "Infographic",
    traces: "Traces"
  },
  zh: {
    languageSwitch: "切换到 English",
    app: "Nano Notebook",
    subtitle: "面向本地开发的研究笔记基础，用于持久的资料化学习。",
    email: "邮箱",
    password: "密码",
    createAccount: "创建账号",
    signIn: "登录",
    localOnly: "本地凭据仅用于开发。",
    library: "笔记库",
    newNotebook: "新建笔记本",
    search: "搜索笔记本",
    emptyTitle: "创建第一个笔记本",
    emptyBody: "先输入标题。资料、对话和输出会在后续迭代中加入。",
    titleLabel: "笔记本标题",
    createNotebook: "创建笔记本",
    cancel: "取消",
    signOut: "退出登录",
    noResults: "没有匹配的笔记本。",
    loading: "正在加载",
    back: "返回笔记库",
    sources: "资料",
    chat: "对话",
    outputs: "Studio",
    sourcesEmpty: "添加文件、网页或 YouTube 链接；每个资料会独立处理。",
    chatEmpty: "在资料处理和检索完成前，对话区域保持为空。",
    outputsEmpty: "本迭代仅保留 Studio 输出区域，不提供生成控件。",
    unreachable: "无法连接 Control Plane。请先启动本地系统后重试。",
    validation: "请输入有效邮箱、至少 15 个字符的密码，以及笔记本标题。",
    safeNotFound: "笔记本不存在或不可访问。",
    credentialsValidation: "请输入有效邮箱和至少 15 个字符的密码。",
    titleValidation: "请输入笔记本标题。",
    duplicateEmail: "该邮箱已在本地工作区注册。",
    invalidCredentials: "邮箱或密码不正确。",
    rateLimited: "尝试次数过多，请稍后重试。",
    notebookQuota: "笔记本数量已达上限。",
    notebookCreateFailed: "无法创建笔记本。请检查本地系统后重试。",
    retry: "重试",
    signOutFailed: "退出登录失败。请重试以撤销服务器会话。",
    signingOut: "正在退出...",
    submitting: "处理中...",
    sessionExpired: "会话已过期或被撤销。请重新登录以继续。",
    authModeLabel: "认证方式",
    notebookPanelsLabel: "笔记本面板",
    notificationsLabel: "通知",
    settings: "设置",
    apps: "Google 应用",
    openUserMenu: "打开用户菜单",
    allNotebooks: "全部",
    featuredNotebooks: "精选笔记本",
    ownedNotebooks: "我拥有的",
    sharedWithMe: "与我共享",
    closeSearch: "关闭搜索",
    gridView: "网格视图",
    listView: "列表视图",
    sortNotebooks: "排序笔记本",
    recent: "最近",
    sortTitle: "标题",
    recentlyOpened: "最近打开过的笔记本",
    columnTitle: "标题",
    columnSource: "来源",
    creationDate: "创建日期",
    role: "角色",
    owner: "所有者",
    reader: "阅读者",
    zeroSources: "0 个来源",
    missingDate: "—",
    openNotebook: "打开",
    moreActions: "更多操作：",
    rename: "重命名",
    share: "分享",
    manageAccess: "管理访问权限",
    inviteEmail: "通过邮箱邀请",
    invite: "发送邀请",
    viewer: "查看者",
    editor: "编辑者",
    members: "成员",
    pendingInvitations: "待接受邀请",
    leaveNotebook: "退出笔记本",
    invitationSent: "邀请已进入本地邮箱队列。",
    acceptInvitation: "接受邀请",
    invitationUnavailable: "此邀请不可用或已过期。",
    invitedAs: "受邀角色",
    removeMember: "移除",
    transferOwner: "转让所有权",
    revokeInvitation: "撤销",
    resendInvitation: "重新发送",
    notebookSettings: "笔记本设置",
    deleteNotebook: "永久删除笔记本",
    deleteNotebookConfirm: "确定永久删除这个共享笔记本吗？",
    delete: "删除",
    comingSoon: "该功能即将推出",
    featuredComingSoon: "精选笔记本功能即将推出",
    emptyTable: "还没有笔记本。",
    viewAll: "查看全部",
    gridComingSoon: "网格视图即将推出。",
    analyze: "分析",
    addSources: "添加来源",
    sourceDiscovery: "来源发现",
    searchWeb: "在网络中搜索新来源",
    web: "Web",
    researchComplete: "Research 已完成",
    viewDiscovery: "查看",
    moreSources: "另有 {count} 个来源",
    sourcesEmptyTitle: "已保存的来源将显示在此处",
    collapsePanel: "收起面板",
    unavailableLabel: "聊天暂时不可用。",
    chatEmptyTitle: "对话将在这里开始",
    chatEmptyBody: "现在可以基于模型知识提问；后续添加来源可让回答基于你的资料。",
    composerPlaceholder: "输入任何问题…",
    composerLabel: "向 Nano Notebook 发送消息",
    sendLabel: "发送消息",
    waitingLabel: "正在等待开始…",
    generatingLabel: "正在生成回答…",
    agentWorkingLabel: "Agent 已工作 · {time}",
    agentWorkedLabel: "Agent 工作了 {time}",
    analyzingLabel: "正在分析问题",
    searchingSourcesLabel: "正在检索已选资料",
    discoveringSourcesLabel: "正在搜索网络",
    inspectingSourceLabel: "正在查看资料",
    readingPDFLabel: "正在阅读 PDF",
    readingWebpageLabel: "正在阅读网页",
    savingSourceLabel: "正在保存资料",
    calculatingLabel: "正在计算",
    organizingStepsLabel: "正在整理步骤",
    sourceDisclosure: "回答使用模型知识，不基于笔记本来源。",
    selectedSourceDisclosure: "回答可使用已选择的资料（{count} 个），并附带引用。",
    failedLabel: "回答生成失败，请重试。",
    stoppedLabel: "已停止",
    stopLabel: "停止",
    retryLabel: "重试",
    chatModeLabel: "对话",
    researchModeLabel: "深度研究",
    researchDisclosure: "深度研究会搜索并阅读公开网页，经过多轮分析后产出带来源链接的完整报告。",
    researchPlanningLabel: "正在制定研究计划…",
    researchPlanTitle: "研究计划",
    researchPlanHelp: "可直接修改计划 JSON；有改动请先保存，然后开始完整研究。",
    savePlanLabel: "保存计划",
    startResearchLabel: "开始研究",
    savingLabel: "保存中…",
    startingLabel: "启动中…",
    researchProgressLabel: "研究进度",
    discoveredLabel: "已发现",
    readLabel: "已阅读",
    failedSourcesLabel: "不可用",
    researchCompletedLabel: "研究已完成",
    researchFailedLabel: "研究已停止",
    researchQuestionsTitle: "制定计划前想先确认几件事",
    researchQuestionsHelp: "你的回答会决定研究计划。选择一个选项，或填写你自己的答案。",
    recommendedLabel: "推荐",
    otherAnswerLabel: "或者填写你自己的答案",
    submitAnswersLabel: "提交回答",
    useRecommendedLabel: "全部采用推荐",
    revisePlanLabel: "提出修改意见",
    revisePlanPlaceholder: "说明想怎么改，例如：只关注开源方案。",
    submitRevisionLabel: "修改计划",
    planInvalidLabel: "计划格式无效。请保留所有必填字段，并使用 JSON 数组填写列表字段。",
    addSourceDialogTitle: "添加资料",
    addSourceDialogBody: "上传支持的文件，或将网页、YouTube 链接保存为不可变资料。",
    chooseFiles: "选择文件",
    supportedSourceFormats: "PDF、DOCX、PPTX、文本、Markdown、音频和图片；每个不超过 100 MB",
    sourceURL: "网页或 YouTube 链接",
    sourceURLPlaceholder: "https://…",
    addURL: "添加链接",
    webSearchPlaceholder: "搜索资料",
    webSearchAction: "搜索",
    webSearching: "正在搜索…",
    selectAll: "全选",
    importSelected: "导入已选资料",
    webSearchFailed: "网络搜索暂时不可用。",
    noSearchResults: "没有找到相关资料。",
    openSearchResult: "打开搜索结果",
    sourceImportFailed: "无法导入这条结果。",
    sourceReady: "可用",
    sourceProcessing: "处理中",
    sourceFailed: "失败",
    sourceFailureLimits: "此资料超出了处理限制。",
    sourceFailureUnavailable: "已保存的资料不可用或已发生变化。",
    sourceFailureUnreadable: "无法读取此文件的内容。",
    sourceFailureIndexing: "无法验证此资料的检索索引。",
    sourceFailureRetrievalUnavailable: "尚未配置检索索引，请联系管理员激活 Retrieval Index Version。",
    sourceFailureInterrupted: "资料处理被中断的次数过多。",
    sourceFailureGeneric: "无法处理此资料。",
    deleteSource: "删除",
    useSource: "使用",
    sourceUnavailable: "资料暂时不可用。",
    sourceUploadFailed: "无法添加此资料。",
    close: "关闭",
    backToSources: "返回资料",
    sourceOriginal: "原件",
    sourceOriginalUnavailable: "原件无法显示。",
    sourcePreview: "资料预览",
    renameSource: "重命名资料",
    sourceTitle: "资料标题",
    save: "保存",
    removeSourceTitle: "永久删除资料？",
    removeSourceBody: "已有引用标记会保留，但之后无法再显示对应原文。",
    removeSourceConfirm: "永久删除",
    coverageWarning: "部分资料内容未能提取",
    verificationTitle: "资料信源验证",
    verificationBody: "这项轻量检查在进入检索库前完成。网络搜索结果只用于匹配来源身份，不会作为证据写入资料。",
    sourceConfidence: "置信度",
    sourceSignalCoverage: "已覆盖信号",
    sourceVerified: "已验证",
    sourceNeedsReview: "需要复核",
    sourceLimitedVerification: "验证信息有限",
    sourcePrivate: "私有资料",
    sourceApproved: "已批准",
    sourceRejected: "已拒绝",
    approveSource: "批准资料",
    rejectSource: "拒绝资料",
    reviewingSource: "正在保存…",
    admissionExtractionComplete: "提取内容通过完整性检查。",
    admissionExtractionPartial: "部分内容未能提取。",
    admissionExactURL: "网络搜索找到了完全一致的规范链接。",
    admissionExactIdentifier: "网络搜索找到了完全一致的 DOI、ISBN 或报告编号。",
    admissionExternalReference: "另一个独立域名也引用了这份资料。",
    admissionUnavailable: "外部验证暂时不可用。",
    admissionNotApplicable: "私有文件不适用公开网络验证。",
    admissionIdentityRequired: "没有找到完全一致的来源身份。",
    admissionCoverageInsufficient: "可用的验证信号不足。",
    admissionScoreLow: "置信度低于当前阈值。",
    citation: "引用",
    citationUnavailable: "该引用已不可用。",
    citationPreview: "正在加载引用…",
    beta: "Beta 版",
    studioEmptyTitle: "Studio 输出将保存在此处",
    studioEmptyBody: "添加来源后，生成能力开放时即可从上方选择输出。",
    studioRecent: "最近生成",
    studioNoOutputs: "生成后的 Studio 输出会显示在这里。",
    studioNoSource: "请先选择至少一个可用资料。",
    studioQueued: "正在加载输出…",
    studioGenerating: "正在生成…",
    studioFailed: "生成失败",
    studioDelete: "删除",
    studioUnavailable: "Studio 输出暂时不可用。",
    studioSource: "资料",
    studioSourceSingular: "份资料",
    studioSourcePlural: "份资料",
    studioPrevious: "上一张闪卡",
    studioNext: "下一张闪卡",
    studioFlip: "翻转闪卡",
    studioShuffle: "打乱",
    studioRestart: "重新开始",
    studioZoomIn: "放大",
    studioZoomOut: "缩小",
    studioMissingArtifact: "该输出尚未准备好查看。",
    addNote: "添加笔记",
    audioOverview: "音频概览",
    presentation: "演示文稿",
    videoOverview: "视频概览",
    mindMap: "思维导图",
    report: "报告",
    flashcards: "闪卡",
    quiz: "测验",
    dataTable: "数据表格",
    infographic: "信息图",
    traces: "Trace 调试"
  }
} satisfies Record<Locale, Record<string, string>>;

const credentialsSchema = z.object({
  email: z.string().email(),
  password: z.string().min(15).max(128)
});

const notebookSchema = z.object({
  title: z.string().trim().min(1).max(160)
});

type CredentialsForm = z.infer<typeof credentialsSchema>;
type NotebookForm = z.infer<typeof notebookSchema>;

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AppShell />
    </QueryClientProvider>
  );
}

function documentLanguage(locale: Locale) {
  return locale === "zh" ? "zh-CN" : "en";
}

function AppShell() {
  const [locale, setLocale] = useState<Locale>(() => {
    const saved = localStorage.getItem("nano-locale");
    if (saved === "en" || saved === "zh") return saved;
    return navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
  });
  const t = strings[locale];
  const [user, setUser] = useState<User | null>(null);
  const [route, setRoute] = useState(() => window.location.pathname);
  const notebookID = route.startsWith("/notebooks/") ? route.replace("/notebooks/", "") : "";
  const traceRoute = route === "/admin/traces" || route.startsWith("/admin/traces/");
  const invitationRoute = route === "/invitations/accept";

  useLayoutEffect(() => {
    document.documentElement.lang = documentLanguage(locale);
  }, [locale]);

  useEffect(() => {
    const onPopState = () => setRoute(window.location.pathname);
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const session = useQuery({
    queryKey: ["session"],
    queryFn: async () => {
      const response = await api("/api/v1/session");
      if (response.status === 401) {
        const code = await responseErrorCode(response);
        return { status: code === "session_expired" ? "expired" : "anonymous" } satisfies SessionState;
      }
      if (!response.ok) throw new Error(t.unreachable);
      const payload = (await response.json()) as { user: User; platform_capabilities?: string[] };
      return { status: "authenticated", user: payload.user, capabilities: payload.platform_capabilities ?? [] } satisfies SessionState;
    },
    retry: false
  });

  const activeUser = user ?? (session.data?.status === "authenticated" ? session.data.user : null);
  const capabilities = session.data?.status === "authenticated" ? session.data.capabilities : [];
  const sessionNotice = !user && session.data?.status === "expired" ? t.sessionExpired : null;

  function switchLocale() {
    const next = locale === "en" ? "zh" : "en";
    localStorage.setItem("nano-locale", next);
    document.documentElement.lang = documentLanguage(next);
    setLocale(next);
  }

  function navigate(path: string) {
    window.history.pushState(null, "", path);
    setRoute(window.location.pathname);
  }

  let shell: ReactNode;
  if (!activeUser && session.isPending) {
    shell = <SystemState t={t} onLocale={switchLocale} message={t.loading} />;
  } else if (!activeUser && session.isError) {
    shell = <SystemState t={t} onLocale={switchLocale} message={t.unreachable} alert onRetry={() => void session.refetch()} />;
  } else if (!activeUser) {
    shell = <AuthScreen t={t} locale={locale} sessionNotice={sessionNotice} onLocale={switchLocale} onAuthed={(authenticatedUser) => {
      setUser(authenticatedUser);
      void session.refetch();
    }} />;
  } else if (invitationRoute) {
    shell = <InvitationAcceptance t={t} onAccepted={(id) => navigate(`/notebooks/${id}`)} />;
  } else if (traceRoute) {
		shell = <TraceDashboard locale={locale} routePath={route} canRead={capabilities.includes("platform.trace.read")} canReplay={capabilities.includes("platform.trace.replay")} canAnalytics={capabilities.includes("platform.trace.analytics")} onNavigate={navigate} onLibrary={() => navigate("/")} />;
  } else if (notebookID) {
    shell = <Workspace t={t} onLocale={switchLocale} user={activeUser} notebookID={notebookID} onLibrary={() => navigate("/")} onOpen={(id) => navigate(`/notebooks/${id}`)} onSignedOut={() => {
      clearAuthenticatedQueries();
      setUser(null);
    }} />;
  } else {
		shell = <LibraryScreen t={t} locale={locale} onLocale={switchLocale} user={activeUser} canTrace={capabilities.includes("platform.trace.read") || capabilities.includes("platform.trace.analytics")} onTraces={() => navigate(capabilities.includes("platform.trace.analytics") ? "/admin/traces/analytics" : "/admin/traces")} onOpen={(id) => navigate(`/notebooks/${id}`)} onSignedOut={() => {
      clearAuthenticatedQueries();
      setUser(null);
    }} />;
  }

  return (
    <>
      {shell}
      <Toaster richColors containerAriaLabel={t.notificationsLabel} />
    </>
  );
}

function clearAuthenticatedQueries() {
  queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== "session" });
  queryClient.setQueryData(["session"], { status: "anonymous" } satisfies SessionState);
}

function AuthScreen({ t, locale, sessionNotice, onLocale, onAuthed }: { t: typeof strings.en; locale: Locale; sessionNotice: string | null; onLocale: () => void; onAuthed: (user: User) => void }) {
  const [mode, setMode] = useState<"register" | "sign-in">("register");
  const [formError, setFormError] = useState<string | null>(null);
  const { register, handleSubmit, reset, formState } = useForm<CredentialsForm>({
    defaultValues: { email: "", password: "" }
  });
  const busy = formState.isSubmitting;

  async function submit(values: CredentialsForm) {
    const parsed = credentialsSchema.safeParse(values);
    if (!parsed.success) {
      setFormError(t.credentialsValidation);
      return;
    }
    setFormError(null);
    try {
      const response = await api(`/api/v1/auth/${mode === "register" ? "register" : "sign-in"}`, {
        method: "POST",
        body: JSON.stringify(parsed.data)
      });
      if (!response.ok) {
        const message = authErrorMessage(t, mode, await responseErrorCode(response));
        setFormError(message);
        toast.error(message);
        return;
      }
      const payload = (await response.json()) as { user: User };
      onAuthed(payload.user);
    } catch {
      setFormError(t.unreachable);
      toast.error(t.unreachable);
    }
  }

  function changeMode(value: string) {
    setMode(value as "register" | "sign-in");
    setFormError(null);
    reset(undefined, { keepValues: true });
  }

  return (
    <main className="auth-layout">
      <LanguageButton label={t.languageSwitch} onClick={onLocale} />
      <section className="auth-panel" aria-labelledby="auth-title">
        <div className="brand-lockup">
          <span className="auth-brand-icon"><MaterialSymbol name="book_2" size={30} weight={500} /></span>
          <h1 id="auth-title">{t.app}</h1>
        </div>
        <p>{t.subtitle}</p>
        <Tabs value={mode} onValueChange={changeMode}>
          <TabsList className="segmented" aria-label={t.authModeLabel}>
            <TabsTrigger value="register">{t.createAccount}</TabsTrigger>
            <TabsTrigger value="sign-in">{t.signIn}</TabsTrigger>
          </TabsList>
        </Tabs>
        <form className="stack" onSubmit={handleSubmit(submit)} noValidate>
          {sessionNotice ? <Alert variant="destructive"><AlertDescription>{sessionNotice}</AlertDescription></Alert> : null}
          {formError ? <Alert variant="destructive"><AlertDescription>{formError}</AlertDescription></Alert> : null}
          <div className="field">
            <Label htmlFor="email">{t.email}</Label>
            <Input id="email" autoComplete="email" {...register("email")} />
          </div>
          <div className="field">
            <Label htmlFor="password">{t.password}</Label>
            <Input id="password" type="password" autoComplete={mode === "register" ? "new-password" : "current-password"} {...register("password")} />
          </div>
          <Button disabled={busy}>{busy ? t.submitting : mode === "register" ? t.createAccount : t.signIn}</Button>
        </form>
        <p className="notice"><MaterialSymbol name="verified_user" size={19} /> {locale === "en" ? t.localOnly : t.localOnly}</p>
      </section>
    </main>
  );
}

function LibraryScreen({ t, locale, user, canTrace, onTraces, onLocale, onOpen, onSignedOut }: { t: typeof strings.en; locale: Locale; user: User; canTrace: boolean; onTraces: () => void; onLocale: () => void; onOpen: (id: string) => void; onSignedOut: () => void }) {
  const [query, setQuery] = useState("");
  const [searchOpen, setSearchOpen] = useState(false);
  const [view, setView] = useState<LibraryView>("list");
  const [sort, setSort] = useState<NotebookSort>("recent");
  const [scope, setScope] = useState<LibraryScope>("all");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [signOutError, setSignOutError] = useState<string | null>(null);
  const notebooks = useQuery({
    queryKey: ["notebooks", query, scope],
    queryFn: async () => {
      const response = await api(`/api/v1/notebooks?scope=${scope}&query=${encodeURIComponent(query)}`);
      if (!response.ok) throw new Error(t.unreachable);
      return ((await response.json()) as { notebooks: Notebook[] }).notebooks;
    }
  });

  async function signOut() {
    setSigningOut(true);
    setSignOutError(null);
    try {
      const response = await api("/api/v1/auth/sign-out", { method: "POST", headers: { "X-CSRF-Token": csrfToken() } });
      if (!response.ok) throw new Error(t.signOutFailed);
      onSignedOut();
    } catch {
      setSignOutError(t.signOutFailed);
      toast.error(t.signOutFailed);
    } finally {
      setSigningOut(false);
    }
  }

  const createAction = (
    <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
      <DialogTrigger asChild>
        <Button className="create-notebook-action"><MaterialSymbol name="add" size={20} />{t.newNotebook}</Button>
      </DialogTrigger>
      <CreateNotebookDialog t={t} onOpen={onOpen} onClose={() => setDialogOpen(false)} />
    </Dialog>
  );

  return (
    <main className="library-layout">
      <LibraryHeader appName={t.app} email={user.email} settingsLabel={t.settings} appsLabel={t.apps} openUserMenuLabel={t.openUserMenu} languageLabel={t.languageSwitch} signOutLabel={t.signOut} signingOutLabel={t.signingOut} comingSoonMessage={t.comingSoon} traceLabel={t.traces} signingOut={signingOut} onLanguage={onLocale} onSignOut={() => void signOut()} onTraces={canTrace ? onTraces : undefined} />
      <h1 className="sr-only">{t.library}</h1>
      <div className="library-content">
        {signOutError ? <Alert variant="destructive"><AlertDescription>{signOutError}</AlertDescription></Alert> : null}
        <LibraryToolbar
          allLabel={t.allNotebooks}
          featuredLabel={t.ownedNotebooks}
          sharedLabel={t.sharedWithMe}
          searchLabel={t.search}
          closeSearchLabel={t.closeSearch}
          gridLabel={t.gridView}
          listLabel={t.listView}
          sortLabel={t.sortNotebooks}
          recentLabel={t.recent}
          titleLabel={t.sortTitle}
          searchOpen={searchOpen}
          query={query}
          view={view}
          sort={sort}
          scope={scope}
          createAction={createAction}
          onSearchOpen={() => setSearchOpen(true)}
          onSearchClose={() => { setQuery(""); setSearchOpen(false); }}
          onQueryChange={setQuery}
          onViewChange={setView}
          onSortChange={setSort}
          onScopeChange={setScope}
        />
        <section className="library-section" aria-labelledby="recent-notebooks-heading">
          <h2 id="recent-notebooks-heading">{t.recentlyOpened}</h2>
          {view === "list" ? (
            <NotebookTable
              notebooks={notebooks.data ?? []}
              sort={sort}
              label={t.recentlyOpened}
              titleLabel={t.columnTitle}
              sourceLabel={t.columnSource}
              creationDateLabel={t.creationDate}
              roleLabel={t.role}
              ownerLabel={t.owner}
              zeroSourcesLabel={t.zeroSources}
              missingDateLabel={t.missingDate}
              openLabel={(title) => `${t.openNotebook} ${title}`}
              moreLabel={(title) => `${t.moreActions} ${title}`}
              renameLabel={t.rename}
              shareLabel={t.share}
              deleteLabel={t.delete}
              comingSoonMessage={t.comingSoon}
              emptyMessage={query ? t.noResults : t.emptyTable}
              errorMessage={t.unreachable}
              loading={notebooks.isLoading}
              error={notebooks.isError}
              retryLabel={t.retry}
              onOpen={onOpen}
              onRetry={() => void notebooks.refetch()}
            />
          ) : <div className="grid-placeholder" data-placeholder="true">{t.gridComingSoon}</div>}
        </section>
        <section className="library-section featured-section" aria-labelledby="featured-notebooks-heading">
          <h2 id="featured-notebooks-heading">{t.featuredNotebooks}</h2>
          <FeaturedNotebooks locale={locale} label={t.featuredNotebooks} titleLabel={t.columnTitle} sourceLabel={t.columnSource} creationDateLabel={t.creationDate} roleLabel={t.role} readerLabel={t.reader} openLabel={(title) => `${t.openNotebook} ${title}`} comingSoonMessage={t.featuredComingSoon} />
          <Button className="view-all-featured" variant="outline" onClick={() => toast(t.featuredComingSoon)}>{t.viewAll}<MaterialSymbol name="keyboard_arrow_right" size={18} /></Button>
        </section>
      </div>
    </main>
  );
}

function CreateNotebookDialog({ t, onOpen, onClose }: { t: typeof strings.en; onOpen: (id: string) => void; onClose: () => void }) {
  const [formError, setFormError] = useState<string | null>(null);
  const { register, handleSubmit, formState } = useForm<NotebookForm>({
    defaultValues: { title: "" }
  });
  const busy = formState.isSubmitting;

  async function submit(values: NotebookForm) {
    const parsed = notebookSchema.safeParse(values);
    if (!parsed.success) {
      setFormError(t.titleValidation);
      return;
    }
    setFormError(null);
    try {
      const response = await api("/api/v1/notebooks", {
        method: "POST",
        headers: { "Idempotency-Key": crypto.randomUUID(), "X-CSRF-Token": csrfToken() },
        body: JSON.stringify(parsed.data)
      });
      if (!response.ok) {
        const message = notebookErrorMessage(t, await responseErrorCode(response));
        setFormError(message);
        toast.error(message);
        return;
      }
      const payload = (await response.json()) as { notebook: Notebook };
      onClose();
      onOpen(payload.notebook.id);
    } catch {
      setFormError(t.unreachable);
      toast.error(t.unreachable);
    }
  }

  return (
    <DialogContent className="dialog" closeLabel={t.cancel}>
      <DialogTitle>{t.newNotebook}</DialogTitle>
        <form className="stack" onSubmit={handleSubmit(submit)} noValidate>
          {formError ? <Alert variant="destructive"><AlertDescription>{formError}</AlertDescription></Alert> : null}
          <div className="field">
            <Label htmlFor="notebook-title">{t.titleLabel}</Label>
            <Input id="notebook-title" autoFocus {...register("title")} />
          </div>
          <div className="dialog-actions">
            <DialogClose asChild><Button type="button" variant="secondary">{t.cancel}</Button></DialogClose>
            <Button disabled={busy}>{busy ? t.submitting : t.createNotebook}</Button>
          </div>
        </form>
    </DialogContent>
  );
}

function Workspace({ t, user, notebookID, onLocale, onLibrary, onOpen, onSignedOut }: { t: typeof strings.en; user: User; notebookID: string; onLocale: () => void; onLibrary: () => void; onOpen: (id: string) => void; onSignedOut: () => void }) {
  const [dialogOpen, setDialogOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const notebook = useQuery({
    queryKey: ["notebook", notebookID],
    queryFn: async () => {
      const response = await api(`/api/v1/notebooks/${notebookID}`);
      if (response.status === 404) throw new Error(t.safeNotFound);
      if (!response.ok) throw new Error(t.unreachable);
      return ((await response.json()) as { notebook: Notebook }).notebook;
    }
  });

  async function signOut() {
    setSigningOut(true);
    try {
      const response = await api("/api/v1/auth/sign-out", { method: "POST", headers: { "X-CSRF-Token": csrfToken() } });
      if (!response.ok) throw new Error(t.signOutFailed);
      onSignedOut();
    } catch {
      toast.error(t.signOutFailed);
    } finally {
      setSigningOut(false);
    }
  }

  const createAction = (
    <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
      <DialogTrigger asChild>
        <Button className="workspace-create-action"><MaterialSymbol name="add" size={20} />{t.createNotebook}</Button>
      </DialogTrigger>
      <CreateNotebookDialog t={t} onOpen={onOpen} onClose={() => setDialogOpen(false)} />
    </Dialog>
  );

  const shareAction = notebook.data?.role === "owner" || !notebook.data?.role ? (
    <ManageAccess notebookID={notebookID} t={t} onAuthorityChanged={() => void notebook.refetch()} />
  ) : (
    <Button className="workspace-header-pill secondary-workspace-action" variant="outline" onClick={async () => {
      if (!window.confirm(t.leaveNotebook)) return;
      const response = await api(`/api/v1/notebooks/${notebookID}/leave`, { method: "POST", headers: { "X-CSRF-Token": csrfToken() } });
      if (response.ok) onLibrary(); else toast.error(t.safeNotFound);
    }}><MaterialSymbol name="logout" size={19} />{t.leaveNotebook}</Button>
  );
  const settingsAction = notebook.data?.role === "owner" || !notebook.data?.role ? (
    <NotebookSettings notebookID={notebookID} title={notebook.data?.title ?? ""} t={t} onRenamed={() => void notebook.refetch()} onDeleted={onLibrary} />
  ) : undefined;

  const workspaceCopy = {
    panelsLabel: t.notebookPanelsLabel,
    sources: t.sources,
    chat: t.chat,
    studio: t.outputs,
    addSources: t.addSources,
    discoveryTitle: t.sourceDiscovery,
    searchWeb: t.searchWeb,
    web: t.web,
    sourcesEmptyTitle: t.sourcesEmptyTitle,
    sourcesEmptyBody: t.sourcesEmpty,
    collapsePanel: t.collapsePanel,
    comingSoon: t.comingSoon,
    title: t.chat,
    emptyTitle: t.chatEmptyTitle,
    emptyBody: t.chatEmptyBody,
    composerPlaceholder: t.composerPlaceholder,
    composerLabel: t.composerLabel,
    sendLabel: t.sendLabel,
    waitingLabel: t.waitingLabel,
    generatingLabel: t.generatingLabel,
    agentWorkingLabel: t.agentWorkingLabel,
    agentWorkedLabel: t.agentWorkedLabel,
    analyzingLabel: t.analyzingLabel,
    searchingSourcesLabel: t.searchingSourcesLabel,
    discoveringSourcesLabel: t.discoveringSourcesLabel,
    inspectingSourceLabel: t.inspectingSourceLabel,
    readingPDFLabel: t.readingPDFLabel,
    readingWebpageLabel: t.readingWebpageLabel,
    savingSourceLabel: t.savingSourceLabel,
    calculatingLabel: t.calculatingLabel,
    organizingStepsLabel: t.organizingStepsLabel,
    sourceDisclosure: t.sourceDisclosure,
    selectedSourceDisclosure: t.selectedSourceDisclosure,
    failedLabel: t.failedLabel,
    stoppedLabel: t.stoppedLabel,
    stopLabel: t.stopLabel,
    retryLabel: t.retryLabel,
    chatModeLabel: t.chatModeLabel,
    researchModeLabel: t.researchModeLabel,
    researchDisclosure: t.researchDisclosure,
    researchPlanningLabel: t.researchPlanningLabel,
    researchPlanTitle: t.researchPlanTitle,
    researchPlanHelp: t.researchPlanHelp,
    savePlanLabel: t.savePlanLabel,
    startResearchLabel: t.startResearchLabel,
    savingLabel: t.savingLabel,
    startingLabel: t.startingLabel,
    researchProgressLabel: t.researchProgressLabel,
    discoveredLabel: t.discoveredLabel,
    readLabel: t.readLabel,
    failedSourcesLabel: t.failedSourcesLabel,
    researchCompletedLabel: t.researchCompletedLabel,
    researchFailedLabel: t.researchFailedLabel,
    researchQuestionsTitle: t.researchQuestionsTitle,
    researchQuestionsHelp: t.researchQuestionsHelp,
    recommendedLabel: t.recommendedLabel,
    otherAnswerLabel: t.otherAnswerLabel,
    submitAnswersLabel: t.submitAnswersLabel,
    useRecommendedLabel: t.useRecommendedLabel,
    revisePlanLabel: t.revisePlanLabel,
    revisePlanPlaceholder: t.revisePlanPlaceholder,
    submitRevisionLabel: t.submitRevisionLabel,
    planInvalidLabel: t.planInvalidLabel,
    unavailableLabel: t.unavailableLabel,
    citationLabel: t.citation,
    citationUnavailableLabel: t.citationUnavailable,
    citationPreviewLabel: t.citationPreview,
    closeLabel: t.close,
    addDialogTitle: t.addSourceDialogTitle,
    addDialogBody: t.addSourceDialogBody,
    chooseFilesLabel: t.chooseFiles,
    supportedFormatsLabel: t.supportedSourceFormats,
    urlLabel: t.sourceURL,
    urlPlaceholder: t.sourceURLPlaceholder,
    addURLLabel: t.addURL,
    webSearchLabel: t.searchWeb,
    webSearchPlaceholder: t.webSearchPlaceholder,
    webSearchActionLabel: t.webSearchAction,
    webSearchingLabel: t.webSearching,
    selectAllLabel: t.selectAll,
    importSelectedLabel: t.importSelected,
    webSearchFailedLabel: t.webSearchFailed,
    noSearchResultsLabel: t.noSearchResults,
    openSearchResultLabel: t.openSearchResult,
    sourceImportFailedLabel: t.sourceImportFailed,
    researchCompleteLabel: t.researchComplete,
    viewDiscoveryLabel: t.viewDiscovery,
    moreSourcesLabel: t.moreSources,
    readyLabel: t.sourceReady,
    processingLabel: t.sourceProcessing,
    sourceFailedLabel: t.sourceFailed,
    deleteLabel: t.deleteSource,
    renameLabel: t.rename,
    useSourceLabel: t.useSource,
    sourceUnavailableLabel: t.sourceUnavailable,
    uploadFailedLabel: t.sourceUploadFailed,
    backToSourcesLabel: t.backToSources,
    sourceOriginalLabel: t.sourceOriginal,
    sourceOriginalUnavailableLabel: t.sourceOriginalUnavailable,
    sourcePreviewLabel: t.sourcePreview,
    renameDialogTitle: t.renameSource,
    sourceTitleLabel: t.sourceTitle,
    saveLabel: t.save,
    removeDialogTitle: t.removeSourceTitle,
    removeDialogBody: t.removeSourceBody,
    removeConfirmLabel: t.removeSourceConfirm,
    cancelLabel: t.cancel,
    coverageWarningLabel: t.coverageWarning,
    verificationTitle: t.verificationTitle,
    verificationBody: t.verificationBody,
    confidenceLabel: t.sourceConfidence,
    signalCoverageLabel: t.sourceSignalCoverage,
    verifiedLabel: t.sourceVerified,
    needsReviewLabel: t.sourceNeedsReview,
    limitedVerificationLabel: t.sourceLimitedVerification,
    privateSourceLabel: t.sourcePrivate,
    approvedLabel: t.sourceApproved,
    rejectedLabel: t.sourceRejected,
    approveSourceLabel: t.approveSource,
    rejectSourceLabel: t.rejectSource,
    reviewingLabel: t.reviewingSource,
    admissionReasonLabels: {
      extraction_complete: t.admissionExtractionComplete,
      extraction_partial: t.admissionExtractionPartial,
      exact_url_match: t.admissionExactURL,
      exact_identifier_match: t.admissionExactIdentifier,
      external_reference_found: t.admissionExternalReference,
      external_verification_unavailable: t.admissionUnavailable,
      external_verification_not_applicable: t.admissionNotApplicable,
      exact_identity_required: t.admissionIdentityRequired,
      signal_coverage_insufficient: t.admissionCoverageInsufficient,
      score_below_threshold: t.admissionScoreLow
    },
    failureReasonLabels: {
      limits_exceeded: t.sourceFailureLimits,
      source_unavailable: t.sourceFailureUnavailable,
      content_unreadable: t.sourceFailureUnreadable,
      indexing_failed: t.sourceFailureIndexing,
      retrieval_unavailable: t.sourceFailureRetrievalUnavailable,
      processing_interrupted: t.sourceFailureInterrupted,
      processing_failed: t.sourceFailureGeneric
    },
    studioRecent: t.studioRecent,
    studioNoOutputs: t.studioNoOutputs,
    studioNoSource: t.studioNoSource,
    studioQueued: t.studioQueued,
    studioGenerating: t.studioGenerating,
    studioFailed: t.studioFailed,
    studioDelete: t.studioDelete,
    studioUnavailable: t.studioUnavailable,
    studioSource: t.studioSource,
    studioSourceSingular: t.studioSourceSingular,
    studioSourcePlural: t.studioSourcePlural,
    studioPrevious: t.studioPrevious,
    studioNext: t.studioNext,
    studioFlip: t.studioFlip,
    studioShuffle: t.studioShuffle,
    studioRestart: t.studioRestart,
    studioZoomIn: t.studioZoomIn,
    studioZoomOut: t.studioZoomOut,
    studioMissingArtifact: t.studioMissingArtifact,
    studioActions: [
      { kind: "report" as const, icon: "summarize", label: t.report, tone: "amber" },
      { kind: "flashcards" as const, icon: "style", label: t.flashcards, tone: "orange" },
      { kind: "mind_map" as const, icon: "account_tree", label: t.mindMap, tone: "rose" },
      { kind: "data_table" as const, icon: "table_view", label: t.dataTable, tone: "slate" }
    ]
  };

  return (
    <main className="workspace-layout">
      {notebook.isLoading ? <div className="workspace-system-state"><p>{t.loading}</p></div> : null}
      {notebook.isError ? <div className="workspace-system-state"><Button variant="ghost" onClick={onLibrary}><MaterialSymbol name="arrow_back" size={20} />{t.back}</Button><RetryableAlert message={notebook.error.message} retryLabel={t.retry} onRetry={() => void notebook.refetch()} /></div> : null}
      {notebook.data ? (
        <>
          <WorkspaceHeader title={notebook.data.title} backLabel={t.back} createAction={createAction} analyzeLabel={t.analyze} shareLabel={t.share} shareAction={shareAction} settingsLabel={t.settings} settingsAction={settingsAction} appsLabel={t.apps} email={user.email} openUserMenuLabel={t.openUserMenu} languageLabel={t.languageSwitch} signOutLabel={t.signOut} signingOutLabel={t.signingOut} signingOut={signingOut} comingSoonMessage={t.comingSoon} onBack={onLibrary} onLanguage={onLocale} onSignOut={() => void signOut()} />
          <NotebookWorkspace notebookID={notebookID} copy={workspaceCopy} canMaintainSources={notebook.data.role !== "viewer"} />
        </>
      ) : null}
    </main>
  );
}

type AccessMember = { user_id: string; display_email: string; role: "viewer" | "editor" | "owner" };
type AccessInvitation = { id: string; display_email: string; role: "viewer" | "editor"; state: string };

function NotebookSettings({ notebookID, title, t, onRenamed, onDeleted }: { notebookID: string; title: string; t: typeof strings.en; onRenamed: () => void; onDeleted: () => void }) {
  const [open, setOpen] = useState(false);
  const [nextTitle, setNextTitle] = useState(title);
  async function rename() {
    const response = await api(`/api/v1/notebooks/${notebookID}`, { method: "PATCH", headers: { "X-CSRF-Token": csrfToken() }, body: JSON.stringify({ title: nextTitle.trim() }) });
    if (!response.ok) return toast.error(t.safeNotFound);
    setOpen(false);
    onRenamed();
  }
  async function remove() {
    if (!window.confirm(t.deleteNotebookConfirm)) return;
    const locale = document.documentElement.lang === "zh-CN" ? "zh-CN" : "en";
    const response = await api(`/api/v1/notebooks/${notebookID}?locale=${locale}`, { method: "DELETE", headers: { "X-CSRF-Token": csrfToken() } });
    if (!response.ok) return toast.error(t.safeNotFound);
    onDeleted();
  }
  return <Dialog open={open} onOpenChange={setOpen}>
    <DialogTrigger asChild><Button className="workspace-header-pill secondary-workspace-action" variant="outline"><MaterialSymbol name="settings" size={19} />{t.settings}</Button></DialogTrigger>
    <DialogContent className="dialog" closeLabel={t.close}>
      <DialogTitle>{t.notebookSettings}</DialogTitle>
      <Label htmlFor="rename-notebook">{t.titleLabel}</Label>
      <Input id="rename-notebook" value={nextTitle} onChange={(event) => setNextTitle(event.target.value)} />
      <div className="dialog-actions"><Button disabled={!nextTitle.trim()} onClick={() => void rename()}>{t.save}</Button></div>
      <Button variant="destructive" onClick={() => void remove()}>{t.deleteNotebook}</Button>
    </DialogContent>
  </Dialog>;
}

function InvitationAcceptance({ t, onAccepted }: { t: typeof strings.en; onAccepted: (notebookID: string) => void }) {
  const token = new URLSearchParams(window.location.hash.replace(/^#/, "")).get("token") ?? "";
  const preview = useQuery({
    queryKey: ["invitation-preview", token],
    enabled: Boolean(token),
    retry: false,
    queryFn: async () => {
      const response = await api(`/api/v1/invitations/resolve?token=${encodeURIComponent(token)}`);
      if (!response.ok) throw new Error(t.invitationUnavailable);
      return ((await response.json()) as { invitation: { notebook_title: string; role: string; masked_email: string } }).invitation;
    }
  });
  async function accept() {
    const response = await api("/api/v1/invitations/accept", {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID(), "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ token })
    });
    if (!response.ok) {
      toast.error(t.invitationUnavailable);
      return;
    }
    const payload = (await response.json()) as { membership: { notebook_id: string } };
    onAccepted(payload.membership.notebook_id);
  }
  return (
    <main className="auth-layout"><section className="auth-panel">
      <h1>{preview.data?.notebook_title ?? t.acceptInvitation}</h1>
      {preview.isError || !token ? <Alert variant="destructive"><AlertDescription>{t.invitationUnavailable}</AlertDescription></Alert> : null}
      {preview.data ? <><p>{t.invitedAs}: {preview.data.role}</p><p>{preview.data.masked_email}</p><Button onClick={() => void accept()}>{t.acceptInvitation}</Button></> : null}
    </section></main>
  );
}

function ManageAccess({ notebookID, t, onAuthorityChanged }: { notebookID: string; t: typeof strings.en; onAuthorityChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<"viewer" | "editor">("viewer");
  const access = useQuery({
    queryKey: ["notebook-access", notebookID],
    enabled: open,
    queryFn: async () => {
      const [membersResponse, invitationsResponse] = await Promise.all([
        api(`/api/v1/notebooks/${notebookID}/members`), api(`/api/v1/notebooks/${notebookID}/invitations`)
      ]);
      if (!membersResponse.ok || !invitationsResponse.ok) throw new Error(t.unreachable);
      return {
        members: ((await membersResponse.json()) as { members: AccessMember[] }).members,
        invitations: ((await invitationsResponse.json()) as { invitations: AccessInvitation[] }).invitations
      };
    }
  });

  async function invite() {
    if (!email.trim()) return;
    const response = await api(`/api/v1/notebooks/${notebookID}/invitations`, {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID(), "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ email: email.trim(), role, locale: document.documentElement.lang === "zh-CN" ? "zh-CN" : "en" })
    });
    if (!response.ok) {
      toast.error(t.safeNotFound);
      return;
    }
    setEmail("");
    toast.success(t.invitationSent);
    await access.refetch();
  }

  async function command(path: string, method: "POST" | "PATCH" | "DELETE", body?: unknown) {
    const response = await api(path, {
      method,
      headers: { "X-CSRF-Token": csrfToken() },
      body: body === undefined ? undefined : JSON.stringify(body)
    });
    if (!response.ok) {
      toast.error(t.safeNotFound);
      return;
    }
    await access.refetch();
    onAuthorityChanged();
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild><Button className="workspace-header-pill secondary-workspace-action" variant="outline"><MaterialSymbol name="share" size={19} />{t.manageAccess}</Button></DialogTrigger>
      <DialogContent className="dialog" closeLabel={t.close}>
        <DialogTitle>{t.manageAccess}</DialogTitle>
        <Label htmlFor="invite-email">{t.inviteEmail}</Label>
        <Input id="invite-email" type="email" value={email} onChange={(event) => setEmail(event.target.value)} />
        <div className="dialog-actions">
          <select aria-label={t.role} value={role} onChange={(event) => setRole(event.target.value as "viewer" | "editor")}>
            <option value="viewer">{t.viewer}</option><option value="editor">{t.editor}</option>
          </select>
          <Button disabled={!email.trim()} onClick={() => void invite()}>{t.invite}</Button>
        </div>
        <h3>{t.members}</h3>
        <div>{access.data?.members.map((member) => <div className="dialog-actions" key={member.user_id}>
          <span>{member.display_email}</span>
          {member.role === "owner" ? <strong>{t.owner}</strong> : <>
            <select aria-label={`${t.role} ${member.display_email}`} value={member.role} onChange={(event) => void command(`/api/v1/notebooks/${notebookID}/members/${member.user_id}`, "PATCH", { role: event.target.value })}>
              <option value="viewer">{t.viewer}</option><option value="editor">{t.editor}</option>
            </select>
            <Button variant="outline" onClick={() => void command(`/api/v1/notebooks/${notebookID}/members/${member.user_id}`, "DELETE")}>{t.removeMember}</Button>
            <Button variant="outline" onClick={() => void command(`/api/v1/notebooks/${notebookID}/members/${member.user_id}/transfer`, "POST")}>{t.transferOwner}</Button>
          </>}
        </div>)}</div>
        <h3>{t.pendingInvitations}</h3>
        <div>{access.data?.invitations.filter((invitation) => invitation.state !== "accepted").map((invitation) => <div className="dialog-actions" key={invitation.id}>
          <span>{invitation.display_email} · {invitation.role} · {invitation.state}</span>
          {invitation.state === "pending" ? <Button variant="outline" onClick={() => void command(`/api/v1/notebooks/${notebookID}/invitations/${invitation.id}`, "DELETE")}>{t.revokeInvitation}</Button> : null}
          {invitation.state === "expired" ? <Button variant="outline" onClick={() => void command(`/api/v1/notebooks/${notebookID}/invitations/${invitation.id}/resend`, "POST", { locale: document.documentElement.lang === "zh-CN" ? "zh-CN" : "en" })}>{t.resendInvitation}</Button> : null}
        </div>)}</div>
      </DialogContent>
    </Dialog>
  );
}

function SystemState({ t, onLocale, message, alert = false, onRetry }: { t: typeof strings.en; onLocale: () => void; message: string; alert?: boolean; onRetry?: () => void }) {
  return (
    <main className="auth-layout">
      <LanguageButton label={t.languageSwitch} onClick={onLocale} />
      <section className="auth-panel" aria-labelledby="system-title">
        <div className="brand-lockup">
          <span className="auth-brand-icon"><MaterialSymbol name="book_2" size={30} weight={500} /></span>
          <h1 id="system-title">{t.app}</h1>
        </div>
        {alert ? <Alert variant="destructive"><AlertDescription>{message}</AlertDescription></Alert> : <p>{message}</p>}
        {onRetry ? <Button variant="secondary" onClick={onRetry}>{t.retry}</Button> : null}
      </section>
    </main>
  );
}

function RetryableAlert({ message, retryLabel, onRetry }: { message: string; retryLabel: string; onRetry: () => void }) {
  return (
    <Alert variant="destructive" className="retryable-alert">
      <AlertDescription>{message}</AlertDescription>
      <Button variant="secondary" onClick={onRetry}>{retryLabel}</Button>
    </Alert>
  );
}

function LanguageButton({ label, onClick }: { label: string; onClick: () => void }) {
  return <Button variant="secondary" className="icon-action" onClick={onClick} aria-label={label}><MaterialSymbol name="language" size={19} />{label}</Button>;
}

async function api(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  return fetch(path, { credentials: "include", ...init, headers });
}

async function responseErrorCode(response: Response) {
  try {
    const payload = (await response.json()) as { error?: { code?: string } };
    return payload.error?.code ?? "";
  } catch {
    return "";
  }
}

function authErrorMessage(t: typeof strings.en, mode: "register" | "sign-in", code: string) {
  if (mode === "register" && code === "duplicate_email") return t.duplicateEmail;
  if (mode === "sign-in" && (code === "invalid_credentials" || code === "unauthorized")) return t.invalidCredentials;
  if (code === "rate_limited") return t.rateLimited;
  if (code === "validation_failed") return t.credentialsValidation;
  return t.unreachable;
}

function notebookErrorMessage(t: typeof strings.en, code: string) {
  if (code === "quota_reached") return t.notebookQuota;
  if (code === "validation_failed") return t.titleValidation;
  return t.notebookCreateFailed;
}

function csrfToken() {
  return document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith("nn_csrf="))
    ?.slice("nn_csrf=".length) ?? "";
}
