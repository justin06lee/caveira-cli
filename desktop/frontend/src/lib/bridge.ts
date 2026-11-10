// The Go side, as Wails exposes it on window.go and window.runtime. Called
// directly rather than through generated bindings, so the frontend builds
// on its own and the types stay the ones in types.ts.

import type {
  AccountView,
  Boot,
  ChatEvent,
  ChatHeader,
  ChatView,
  ImportApp,
  ImportPick,
  ImportProgress,
  ImportResult,
  Listing,
  McpFound,
  McpServer,
  RegistryServer,
  Skill,
  ModelOption,
  Project,
  SettingsInput,
  SettingsView,
  UpdateView,
  Workspace,
} from "./types";

declare global {
  interface Window {
    go?: { main: { App: Record<string, (...args: unknown[]) => Promise<unknown>> } };
    runtime?: {
      EventsOn(name: string, cb: (...data: unknown[]) => void): () => void;
      ClipboardSetText(text: string): Promise<boolean>;
      BrowserOpenURL(url: string): void;
    };
  }
}

function call<T>(method: string, ...args: unknown[]): Promise<T> {
  const app = window.go?.main.App;
  if (!app) return Promise.reject(new Error("not running inside the caveira app"));
  return app[method](...args) as Promise<T>;
}

export const api = {
  boot: () => call<Boot>("Boot"),
  openProject: (dir: string) => call<Project>("OpenProject", dir),
  chooseProject: () => call<Project>("ChooseProject"),
  forgetProject: (dir: string) => call<void>("ForgetProject", dir),
  reveal: (dir: string) => call<void>("Reveal", dir),
  projects: () => call<Project[] | null>("Projects"),
  listDir: (base: string, rel: string) => call<Listing>("ListDir", base, rel),
  makeFolder: (base: string, rel: string) => call<Project>("MakeFolder", base, rel),
  setWorkspace: (dir: string) => call<Workspace>("SetWorkspace", dir),
  suggestWorkspace: () => call<Workspace>("SuggestWorkspace"),
  finishOnboarding: () => call<void>("FinishOnboarding"),
  scanImports: () => call<ImportApp[] | null>("ScanImports"),
  importChats: (picks: ImportPick[]) => call<ImportResult>("Import", picks),
  settings: () => call<SettingsView>("Settings"),
  saveSettings: (s: SettingsInput) => call<SettingsView>("SaveSettings", s),
  models: () => call<ModelOption[] | null>("Models"),
  newChat: (dir: string) => call<ChatView>("NewChat", dir),
  openChat: (id: string) => call<ChatView>("OpenChat", id),
  chats: (dir: string) => call<ChatHeader[] | null>("Chats", dir),
  deleteChat: (id: string) => call<void>("DeleteChat", id),
  renameChat: (id: string, title: string) => call<void>("RenameChat", id, title),
  // send says whether a turn started; one the account cannot run is held.
  send: (id: string, text: string) => call<boolean>("Send", id, text),
  stop: (id: string) => call<void>("Stop", id),
  // retry takes up a turn that gave up waiting out a busy model.
  retry: (id: string) => call<boolean>("Retry", id),
  answer: (id: string, callId: string, decision: "allow" | "always" | "deny") =>
    call<void>("Answer", id, callId, decision),
  setModel: (id: string, model: string, window: number, effort: string) =>
    call<ChatView>("SetModel", id, model, window, effort),
  // subscribe opens the plan's checkout on the caveira site, in the browser.
  subscribe: (plan: string) => call<void>("Subscribe", plan),
  // account asks the site about the account; signIn opens its sign-in
  // page, and "account" events say how it went.
  account: () => call<AccountView>("Account"),
  signIn: () => call<AccountView>("SignIn"),
  cancelSignIn: () => call<AccountView>("CancelSignIn"),
  signOut: () => call<AccountView>("SignOut"),
  setPermissions: (id: string, ask: boolean) => call<ChatView>("SetPermissions", id, ask),
  titleBarDoubleClick: () => call<void>("TitleBarDoubleClick"),
  companion: () => call<boolean>("Companion"),
  setCompanion: (on: boolean) => call<boolean>("SetCompanion", on),
  // MCP servers and skills, for the project at dir ("" for none).
  mcpServers: (dir: string) => call<McpServer[] | null>("MCPServers", dir),
  setMcpEnabled: (dir: string, name: string, on: boolean) => call<McpServer[] | null>("SetMCPEnabled", dir, name, on),
  removeMcp: (dir: string, name: string) => call<McpServer[] | null>("RemoveMCP", dir, name),
  searchMcp: (query: string) => call<RegistryServer[] | null>("SearchMCP", query),
  addMcp: (dir: string, name: string, way: number, values: Record<string, string>) =>
    call<string>("AddMCP", dir, name, way, values),
  detectMcp: (dir: string) => call<McpFound[] | null>("DetectMCP", dir),
  importMcp: (dir: string, picks: number[]) => call<McpServer[] | null>("ImportMCP", dir, picks),
  skills: (dir: string) => call<Skill[] | null>("Skills", dir),
  setSkillEnabled: (dir: string, name: string, on: boolean) => call<Skill[] | null>("SetSkillEnabled", dir, name, on),
  removeSkill: (dir: string, name: string) => call<Skill[] | null>("RemoveSkill", dir, name),
  installSkill: (dir: string, source: string) => call<Skill[] | null>("InstallSkill", dir, source),
  // installUpdate puts the newer release in place and reopens the app.
  installUpdate: () => call<void>("InstallUpdate"),
  // restartForUpdate opens the version an update at launch put in place.
  restartForUpdate: () => call<void>("RestartForUpdate"),
};

export function onChatEvent(cb: (e: ChatEvent) => void): () => void {
  return window.runtime?.EventsOn("chat", (e) => cb(e as ChatEvent)) ?? (() => {});
}

export function onAccount(cb: (a: AccountView) => void): () => void {
  return window.runtime?.EventsOn("account", (a) => cb(a as AccountView)) ?? (() => {});
}

export function onImport(cb: (p: ImportProgress) => void): () => void {
  return window.runtime?.EventsOn("import", (p) => cb(p as ImportProgress)) ?? (() => {});
}

export function onUpdate(cb: (u: UpdateView) => void): () => void {
  return window.runtime?.EventsOn("update", (u) => cb(u as UpdateView)) ?? (() => {});
}

export function onMenu(cb: (action: string) => void): () => void {
  return window.runtime?.EventsOn("menu", (a) => cb(a as string)) ?? (() => {});
}

export async function copyText(text: string): Promise<void> {
  if (window.runtime) {
    await window.runtime.ClipboardSetText(text);
    return;
  }
  await navigator.clipboard.writeText(text);
}

export function openURL(url: string): void {
  if (window.runtime) window.runtime.BrowserOpenURL(url);
  else window.open(url, "_blank", "noopener");
}

export function errorText(e: unknown): string {
  if (e instanceof Error) return e.message;
  return String(e);
}
