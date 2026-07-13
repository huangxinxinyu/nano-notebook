import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { ArrowLeft, BookOpen, Languages, Library, LogOut, Plus, Search, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Card, CardContent } from "../components/ui/card";
import { Dialog, DialogClose, DialogContent, DialogTitle, DialogTrigger } from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { Toaster } from "../components/ui/sonner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import { queryClient } from "./queryClient";

type Locale = "en" | "zh";
type User = { id: string; email: string };
type Notebook = { id: string; title: string; recent_at?: string };

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
    outputs: "Outputs",
    sourcesEmpty: "Sources are not available in Sprint 1. This area is reserved for later ingestion work.",
    chatEmpty: "Chat is intentionally empty until source processing and retrieval exist.",
    outputsEmpty: "Outputs are reserved without generation controls in this sprint.",
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
    submitting: "Working..."
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
    outputs: "输出",
    sourcesEmpty: "Sprint 1 尚不支持资料导入，此区域为后续资料流程预留。",
    chatEmpty: "在资料处理和检索完成前，对话区域保持为空。",
    outputsEmpty: "本迭代仅保留输出区域，不提供生成控件。",
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
    submitting: "处理中..."
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
      <Toaster richColors />
    </QueryClientProvider>
  );
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

  const session = useQuery({
    queryKey: ["session"],
    queryFn: async () => {
      const response = await api("/api/v1/session");
      if (response.status === 401) return null;
      if (!response.ok) throw new Error(t.unreachable);
      return ((await response.json()) as { user: User }).user;
    },
    retry: false
  });

  const activeUser = user ?? session.data ?? null;

  function switchLocale() {
    const next = locale === "en" ? "zh" : "en";
    localStorage.setItem("nano-locale", next);
    setLocale(next);
  }

  function navigate(path: string) {
    window.history.pushState(null, "", path);
    setRoute(path);
  }

  if (!activeUser && session.isPending) {
    return <SystemState t={t} onLocale={switchLocale} message={t.loading} />;
  }
  if (!activeUser && session.isError) {
    return <SystemState t={t} onLocale={switchLocale} message={t.unreachable} alert onRetry={() => void session.refetch()} />;
  }
  if (!activeUser) {
    return <AuthScreen t={t} locale={locale} onLocale={switchLocale} onAuthed={setUser} />;
  }
  if (notebookID) {
    return <Workspace t={t} onLocale={switchLocale} user={activeUser} notebookID={notebookID} onLibrary={() => navigate("/")} />;
  }
  return <LibraryScreen t={t} onLocale={switchLocale} user={activeUser} onOpen={(id) => navigate(`/notebooks/${id}`)} onSignedOut={() => {
    queryClient.setQueryData(["session"], null);
    setUser(null);
  }} />;
}

function AuthScreen({ t, locale, onLocale, onAuthed }: { t: typeof strings.en; locale: Locale; onLocale: () => void; onAuthed: (user: User) => void }) {
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
          <BookOpen aria-hidden="true" />
          <h1 id="auth-title">{t.app}</h1>
        </div>
        <p>{t.subtitle}</p>
        <Tabs value={mode} onValueChange={changeMode}>
          <TabsList className="segmented" aria-label="Authentication mode">
            <TabsTrigger value="register">{t.createAccount}</TabsTrigger>
            <TabsTrigger value="sign-in">{t.signIn}</TabsTrigger>
          </TabsList>
        </Tabs>
        <form className="stack" onSubmit={handleSubmit(submit)} noValidate>
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
        <p className="notice"><ShieldCheck aria-hidden="true" /> {locale === "en" ? t.localOnly : t.localOnly}</p>
      </section>
    </main>
  );
}

function LibraryScreen({ t, user, onLocale, onOpen, onSignedOut }: { t: typeof strings.en; user: User; onLocale: () => void; onOpen: (id: string) => void; onSignedOut: () => void }) {
  const [query, setQuery] = useState("");
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

  return (
    <main className="app-layout">
      <header className="topbar">
        <div className="brand-small"><Library aria-hidden="true" /><span>{t.app}</span></div>
        <div className="topbar-actions">
          <LanguageButton label={t.languageSwitch} onClick={onLocale} />
          <Button variant="secondary" className="icon-action" onClick={signOut} disabled={signingOut}><LogOut aria-hidden="true" />{signingOut ? t.signingOut : t.signOut}</Button>
        </div>
      </header>
      {signOutError ? <Alert variant="destructive"><AlertDescription>{signOutError}</AlertDescription></Alert> : null}
      <section className="library-heading">
        <div>
          <h1>{t.library}</h1>
          <p>{user.email}</p>
        </div>
        <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
          <DialogTrigger asChild>
            <Button className="create-notebook-action"><Plus aria-hidden="true" />{t.newNotebook}</Button>
          </DialogTrigger>
          <CreateNotebookDialog t={t} onOpen={onOpen} onClose={() => setDialogOpen(false)} />
        </Dialog>
      </section>
      <label className="search-box">
        <Search aria-hidden="true" />
        <span className="sr-only">{t.search}</span>
        <input placeholder={t.search} value={query} onChange={(event) => setQuery(event.target.value)} />
      </label>
      <section className="notebook-grid" aria-live="polite">
        {notebooks.isLoading ? <p>{t.loading}</p> : null}
        {notebooks.isError ? <RetryableAlert message={t.unreachable} retryLabel={t.retry} onRetry={() => void notebooks.refetch()} /> : null}
        {notebooks.data?.length === 0 && query ? <p className="empty-line">{t.noResults}</p> : null}
        {notebooks.data?.length === 0 && !query ? <EmptyLibrary t={t} /> : null}
        {notebooks.data?.map((notebook) => (
          <Button variant="ghost" className="library-item-action" key={notebook.id} onClick={() => onOpen(notebook.id)}>
            <BookOpen aria-hidden="true" />
            <span>{notebook.title}</span>
          </Button>
        ))}
      </section>
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

function Workspace({ t, notebookID, onLocale, onLibrary }: { t: typeof strings.en; user: User; notebookID: string; onLocale: () => void; onLibrary: () => void }) {
  const notebook = useQuery({
    queryKey: ["notebook", notebookID],
    queryFn: async () => {
      const response = await api(`/api/v1/notebooks/${notebookID}`);
      if (response.status === 404) throw new Error(t.safeNotFound);
      if (!response.ok) throw new Error(t.unreachable);
      return ((await response.json()) as { notebook: Notebook }).notebook;
    }
  });

  return (
    <main className="workspace-layout">
      <header className="topbar">
        <Button variant="secondary" className="icon-action" onClick={onLibrary}><ArrowLeft aria-hidden="true" />{t.back}</Button>
        <LanguageButton label={t.languageSwitch} onClick={onLocale} />
      </header>
      {notebook.isLoading ? <p>{t.loading}</p> : null}
      {notebook.isError ? <RetryableAlert message={notebook.error.message} retryLabel={t.retry} onRetry={() => void notebook.refetch()} /> : null}
      {notebook.data ? (
        <>
          <h1>{notebook.data.title}</h1>
          <Tabs defaultValue="sources" className="workspace-grid">
            <TabsList className="workspace-tabs" aria-label="Notebook panels">
              <TabsTrigger value="sources">{t.sources}</TabsTrigger>
              <TabsTrigger value="chat">{t.chat}</TabsTrigger>
              <TabsTrigger value="outputs">{t.outputs}</TabsTrigger>
            </TabsList>
            <Panel value="sources" title={t.sources} body={t.sourcesEmpty} />
            <Panel value="chat" title={t.chat} body={t.chatEmpty} />
            <Panel value="outputs" title={t.outputs} body={t.outputsEmpty} />
          </Tabs>
        </>
      ) : null}
    </main>
  );
}

function Panel({ value, title, body }: { value: string; title: string; body: string }) {
  return (
    <TabsContent className="workspace-panel" value={value}>
      <h2>{title}</h2>
      <p>{body}</p>
    </TabsContent>
  );
}

function EmptyLibrary({ t }: { t: typeof strings.en }) {
  return (
    <Card className="empty-state">
      <BookOpen aria-hidden="true" />
      <CardContent>
        <h2>{t.emptyTitle}</h2>
        <p>{t.emptyBody}</p>
      </CardContent>
    </Card>
  );
}

function SystemState({ t, onLocale, message, alert = false, onRetry }: { t: typeof strings.en; onLocale: () => void; message: string; alert?: boolean; onRetry?: () => void }) {
  return (
    <main className="auth-layout">
      <LanguageButton label={t.languageSwitch} onClick={onLocale} />
      <section className="auth-panel" aria-labelledby="system-title">
        <div className="brand-lockup">
          <BookOpen aria-hidden="true" />
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
  return <Button variant="secondary" className="icon-action" onClick={onClick} aria-label={label}><Languages aria-hidden="true" />{label}</Button>;
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
