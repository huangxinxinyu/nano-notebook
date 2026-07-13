import * as Dialog from "@radix-ui/react-dialog";
import * as Label from "@radix-ui/react-label";
import * as Tabs from "@radix-ui/react-tabs";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { ArrowLeft, BookOpen, Languages, Library, LogOut, Plus, Search, ShieldCheck } from "lucide-react";
import { FormEvent, useState } from "react";
import { Toaster, toast } from "sonner";
import { z } from "zod";

type Locale = "en" | "zh";
type User = { id: string; email: string };
type Notebook = { id: string; title: string; recent_at?: string };

const queryClient = new QueryClient();

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
    safeNotFound: "Notebook not found or unavailable."
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
    safeNotFound: "笔记本不存在或不可访问。"
  }
} satisfies Record<Locale, Record<string, string>>;

const credentialsSchema = z.object({
  email: z.string().email(),
  password: z.string().min(15).max(128)
});

const notebookSchema = z.object({
  title: z.string().trim().min(1).max(160)
});

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
      if (!response.ok) return null;
      return ((await response.json()) as { user: User }).user;
    }
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
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const parsed = credentialsSchema.safeParse({ email, password });
    if (!parsed.success) {
      toast.error(t.validation);
      return;
    }
    setBusy(true);
    try {
      const response = await api(`/api/v1/auth/${mode === "register" ? "register" : "sign-in"}`, {
        method: "POST",
        body: JSON.stringify(parsed.data)
      });
      if (!response.ok) throw new Error(t.validation);
      const payload = (await response.json()) as { user: User };
      onAuthed(payload.user);
    } catch {
      toast.error(t.unreachable);
    } finally {
      setBusy(false);
    }
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
        <Tabs.Root value={mode} onValueChange={(value) => setMode(value as "register" | "sign-in")}>
          <Tabs.List className="segmented" aria-label="Authentication mode">
            <Tabs.Trigger value="register">{t.createAccount}</Tabs.Trigger>
            <Tabs.Trigger value="sign-in">{t.signIn}</Tabs.Trigger>
          </Tabs.List>
        </Tabs.Root>
        <form className="stack" onSubmit={submit}>
          <Field id="email" label={t.email} value={email} onChange={setEmail} autoComplete="email" />
          <Field id="password" label={t.password} value={password} onChange={setPassword} type="password" autoComplete={mode === "register" ? "new-password" : "current-password"} />
          <button className="primary" disabled={busy}>{mode === "register" ? t.createAccount : t.signIn}</button>
        </form>
        <p className="notice"><ShieldCheck aria-hidden="true" /> {locale === "en" ? t.localOnly : t.localOnly}</p>
      </section>
    </main>
  );
}

function LibraryScreen({ t, user, onLocale, onOpen, onSignedOut }: { t: typeof strings.en; user: User; onLocale: () => void; onOpen: (id: string) => void; onSignedOut: () => void }) {
  const [query, setQuery] = useState("");
  const [dialogOpen, setDialogOpen] = useState(false);
  const notebooks = useQuery({
    queryKey: ["notebooks", query],
    queryFn: async () => {
      const response = await api(`/api/v1/notebooks?query=${encodeURIComponent(query)}`);
      if (!response.ok) throw new Error(t.unreachable);
      return ((await response.json()) as { notebooks: Notebook[] }).notebooks;
    }
  });

  async function signOut() {
    await api("/api/v1/auth/sign-out", { method: "POST", headers: { "X-CSRF-Token": csrfToken() } });
    onSignedOut();
  }

  return (
    <main className="app-layout">
      <header className="topbar">
        <div className="brand-small"><Library aria-hidden="true" /><span>{t.app}</span></div>
        <div className="topbar-actions">
          <LanguageButton label={t.languageSwitch} onClick={onLocale} />
          <button className="icon-text" onClick={signOut}><LogOut aria-hidden="true" />{t.signOut}</button>
        </div>
      </header>
      <section className="library-heading">
        <div>
          <h1>{t.library}</h1>
          <p>{user.email}</p>
        </div>
        <Dialog.Root open={dialogOpen} onOpenChange={setDialogOpen}>
          <Dialog.Trigger asChild>
            <button className="primary"><Plus aria-hidden="true" />{t.newNotebook}</button>
          </Dialog.Trigger>
          <CreateNotebookDialog t={t} onOpen={onOpen} onClose={() => setDialogOpen(false)} />
        </Dialog.Root>
      </section>
      <label className="search-box">
        <Search aria-hidden="true" />
        <span className="sr-only">{t.search}</span>
        <input placeholder={t.search} value={query} onChange={(event) => setQuery(event.target.value)} />
      </label>
      <section className="notebook-grid" aria-live="polite">
        {notebooks.isLoading ? <p>{t.loading}</p> : null}
        {notebooks.isError ? <p role="alert">{t.unreachable}</p> : null}
        {notebooks.data?.length === 0 && query ? <p className="empty-line">{t.noResults}</p> : null}
        {notebooks.data?.length === 0 && !query ? <EmptyLibrary t={t} /> : null}
        {notebooks.data?.map((notebook) => (
          <button className="notebook-card" key={notebook.id} onClick={() => onOpen(notebook.id)}>
            <BookOpen aria-hidden="true" />
            <span>{notebook.title}</span>
          </button>
        ))}
      </section>
    </main>
  );
}

function CreateNotebookDialog({ t, onOpen, onClose }: { t: typeof strings.en; onOpen: (id: string) => void; onClose: () => void }) {
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const parsed = notebookSchema.safeParse({ title });
    if (!parsed.success) {
      toast.error(t.validation);
      return;
    }
    setBusy(true);
    try {
      const response = await api("/api/v1/notebooks", {
        method: "POST",
        headers: { "Idempotency-Key": crypto.randomUUID(), "X-CSRF-Token": csrfToken() },
        body: JSON.stringify(parsed.data)
      });
      if (!response.ok) throw new Error(t.unreachable);
      const payload = (await response.json()) as { notebook: Notebook };
      onClose();
      onOpen(payload.notebook.id);
    } catch {
      toast.error(t.unreachable);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog.Portal>
      <Dialog.Overlay className="overlay" />
      <Dialog.Content className="dialog">
        <Dialog.Title>{t.newNotebook}</Dialog.Title>
        <form className="stack" onSubmit={submit}>
          <Field id="notebook-title" label={t.titleLabel} value={title} onChange={setTitle} autoFocus />
          <div className="dialog-actions">
            <Dialog.Close asChild><button type="button" className="secondary">{t.cancel}</button></Dialog.Close>
            <button className="primary" disabled={busy}>{t.createNotebook}</button>
          </div>
        </form>
      </Dialog.Content>
    </Dialog.Portal>
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
        <button className="icon-text" onClick={onLibrary}><ArrowLeft aria-hidden="true" />{t.back}</button>
        <LanguageButton label={t.languageSwitch} onClick={onLocale} />
      </header>
      {notebook.isLoading ? <p>{t.loading}</p> : null}
      {notebook.isError ? <p role="alert">{notebook.error.message}</p> : null}
      {notebook.data ? (
        <>
          <h1>{notebook.data.title}</h1>
          <Tabs.Root defaultValue="sources" className="workspace-grid">
            <Tabs.List className="workspace-tabs" aria-label="Notebook panels">
              <Tabs.Trigger value="sources">{t.sources}</Tabs.Trigger>
              <Tabs.Trigger value="chat">{t.chat}</Tabs.Trigger>
              <Tabs.Trigger value="outputs">{t.outputs}</Tabs.Trigger>
            </Tabs.List>
            <Panel value="sources" title={t.sources} body={t.sourcesEmpty} />
            <Panel value="chat" title={t.chat} body={t.chatEmpty} />
            <Panel value="outputs" title={t.outputs} body={t.outputsEmpty} />
          </Tabs.Root>
        </>
      ) : null}
    </main>
  );
}

function Panel({ value, title, body }: { value: string; title: string; body: string }) {
  return (
    <Tabs.Content className="workspace-panel" value={value}>
      <h2>{title}</h2>
      <p>{body}</p>
    </Tabs.Content>
  );
}

function EmptyLibrary({ t }: { t: typeof strings.en }) {
  return (
    <div className="empty-state">
      <BookOpen aria-hidden="true" />
      <h2>{t.emptyTitle}</h2>
      <p>{t.emptyBody}</p>
    </div>
  );
}

function LanguageButton({ label, onClick }: { label: string; onClick: () => void }) {
  return <button className="icon-text" onClick={onClick} aria-label={label}><Languages aria-hidden="true" />{label}</button>;
}

function Field({ id, label, value, onChange, type = "text", autoComplete, autoFocus }: { id: string; label: string; value: string; onChange: (value: string) => void; type?: string; autoComplete?: string; autoFocus?: boolean }) {
  return (
    <div className="field">
      <Label.Root htmlFor={id}>{label}</Label.Root>
      <input id={id} value={value} type={type} autoComplete={autoComplete} autoFocus={autoFocus} onChange={(event) => onChange(event.target.value)} />
    </div>
  );
}

async function api(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  return fetch(path, { credentials: "include", ...init, headers });
}

function csrfToken() {
  return document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith("nn_csrf="))
    ?.slice("nn_csrf=".length) ?? "";
}
