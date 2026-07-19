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
import { LibraryToolbar, type LibraryView, type NotebookSort } from "../components/library/library-toolbar";
import { NotebookTable } from "../components/library/notebook-table";
import { LibraryHeader } from "../components/layout/app-header";
import { NotebookWorkspace } from "../components/workspace/notebook-workspace";
import { WorkspaceHeader } from "../components/workspace/workspace-header";
import { TraceDashboard } from "../components/traces/trace-dashboard";
import { queryClient } from "./queryClient";

type Locale = "en" | "zh";
type User = { id: string; email: string };
type Notebook = { id: string; title: string; recent_at?: string };
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
    sourcesEmpty: "Sources are not available in Sprint 1. This area is reserved for later ingestion work.",
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
    delete: "Delete",
    comingSoon: "This feature is coming soon.",
    featuredComingSoon: "Featured notebooks are coming soon.",
    emptyTable: "No notebooks yet.",
    viewAll: "View all",
    gridComingSoon: "Grid view is coming soon.",
    analyze: "Analyze",
    addSources: "Add sources",
    searchWeb: "Search the web for new sources",
    web: "Web",
    fastResearch: "Fast Research",
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
    sourceDisclosure: "Answers use model knowledge and are not based on Notebook Sources.",
    failedLabel: "The answer could not be generated. Try again.",
    stoppedLabel: "Stopped",
    stopLabel: "Stop",
    retryLabel: "Retry",
    beta: "Beta",
    studioEmptyTitle: "Studio output will be saved here",
    studioEmptyBody: "Add sources, then choose an output above when generation becomes available.",
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
    sourcesEmpty: "Sprint 1 尚不支持资料导入，此区域为后续资料流程预留。",
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
    delete: "删除",
    comingSoon: "该功能即将推出",
    featuredComingSoon: "精选笔记本功能即将推出",
    emptyTable: "还没有笔记本。",
    viewAll: "查看全部",
    gridComingSoon: "网格视图即将推出。",
    analyze: "分析",
    addSources: "添加来源",
    searchWeb: "在网络中搜索新来源",
    web: "Web",
    fastResearch: "快速研究",
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
    sourceDisclosure: "回答使用模型知识，不基于笔记本来源。",
    failedLabel: "回答生成失败，请重试。",
    stoppedLabel: "已停止",
    stopLabel: "停止",
    retryLabel: "重试",
    beta: "Beta 版",
    studioEmptyTitle: "Studio 输出将保存在此处",
    studioEmptyBody: "添加来源后，生成能力开放时即可从上方选择输出。",
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
  } else if (traceRoute) {
    shell = <TraceDashboard locale={locale} routePath={route} canRead={capabilities.includes("platform.trace.read")} canReplay={capabilities.includes("platform.trace.replay")} onNavigate={navigate} onLibrary={() => navigate("/")} />;
  } else if (notebookID) {
    shell = <Workspace t={t} onLocale={switchLocale} user={activeUser} notebookID={notebookID} onLibrary={() => navigate("/")} onOpen={(id) => navigate(`/notebooks/${id}`)} onSignedOut={() => {
      clearAuthenticatedQueries();
      setUser(null);
    }} />;
  } else {
    shell = <LibraryScreen t={t} locale={locale} onLocale={switchLocale} user={activeUser} canTrace={capabilities.includes("platform.trace.read")} onTraces={() => navigate("/admin/traces")} onOpen={(id) => navigate(`/notebooks/${id}`)} onSignedOut={() => {
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
  const [dialogOpen, setDialogOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [signOutError, setSignOutError] = useState<string | null>(null);
  const notebooks = useQuery({
    queryKey: ["notebooks", query],
    queryFn: async () => {
      const response = await api(`/api/v1/notebooks?query=${encodeURIComponent(query)}`);
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
          featuredLabel={t.featuredNotebooks}
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
          createAction={createAction}
          onSearchOpen={() => setSearchOpen(true)}
          onSearchClose={() => { setQuery(""); setSearchOpen(false); }}
          onQueryChange={setQuery}
          onViewChange={setView}
          onSortChange={setSort}
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

  const workspaceCopy = {
    panelsLabel: t.notebookPanelsLabel,
    sources: t.sources,
    chat: t.chat,
    studio: t.outputs,
    addSources: t.addSources,
    searchWeb: t.searchWeb,
    web: t.web,
    fastResearch: t.fastResearch,
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
    sourceDisclosure: t.sourceDisclosure,
    failedLabel: t.failedLabel,
    stoppedLabel: t.stoppedLabel,
    stopLabel: t.stopLabel,
    retryLabel: t.retryLabel,
    unavailableLabel: t.unavailableLabel,
    beta: t.beta,
    studioEmptyTitle: t.studioEmptyTitle,
    studioEmptyBody: t.studioEmptyBody,
    addNote: t.addNote,
    studioActions: [
      { icon: "graphic_eq", label: t.audioOverview, tone: "violet" },
      { icon: "view_carousel", label: t.presentation, tone: "amber", beta: true },
      { icon: "slideshow", label: t.videoOverview, tone: "green" },
      { icon: "account_tree", label: t.mindMap, tone: "rose" },
      { icon: "summarize", label: t.report, tone: "amber" },
      { icon: "style", label: t.flashcards, tone: "orange" },
      { icon: "quiz", label: t.quiz, tone: "blue" },
      { icon: "table_view", label: t.dataTable, tone: "slate" },
      { icon: "insert_chart", label: t.infographic, tone: "plum", beta: true }
    ]
  };

  return (
    <main className="workspace-layout">
      {notebook.isLoading ? <div className="workspace-system-state"><p>{t.loading}</p></div> : null}
      {notebook.isError ? <div className="workspace-system-state"><Button variant="ghost" onClick={onLibrary}><MaterialSymbol name="arrow_back" size={20} />{t.back}</Button><RetryableAlert message={notebook.error.message} retryLabel={t.retry} onRetry={() => void notebook.refetch()} /></div> : null}
      {notebook.data ? (
        <>
          <WorkspaceHeader title={notebook.data.title} backLabel={t.back} createAction={createAction} analyzeLabel={t.analyze} shareLabel={t.share} settingsLabel={t.settings} appsLabel={t.apps} email={user.email} openUserMenuLabel={t.openUserMenu} languageLabel={t.languageSwitch} signOutLabel={t.signOut} signingOutLabel={t.signingOut} signingOut={signingOut} comingSoonMessage={t.comingSoon} onBack={onLibrary} onLanguage={onLocale} onSignOut={() => void signOut()} />
          <NotebookWorkspace notebookID={notebookID} copy={workspaceCopy} />
        </>
      ) : null}
    </main>
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
