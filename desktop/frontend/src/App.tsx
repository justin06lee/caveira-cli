import { lazy, Suspense, useEffect } from "react";
import { X } from "lucide-react";
import { api, onMenu } from "./lib/bridge";
import { menuAction } from "./lib/platform";
import { applyTheme } from "./lib/theme";
import { boot, getState, listen, newChat, refreshAccount, setState, toast, useStore } from "./lib/store";
import { Sidebar } from "./components/Sidebar";
import { ChatPane } from "./components/ChatPane";
import { Welcome } from "./components/Welcome";
import { loadMarkdown } from "./components/Markdown";

// Screens opened now and then load on their own, so the first frame only waits for the chat window.
const loadSettings = () => import("./components/Settings");
const loadOnboarding = () => import("./components/Onboarding");
const loadOpenProject = () => import("./components/OpenProject");
const loadImportSheet = () => import("./components/ImportSheet");
const Settings = lazy(() => loadSettings().then((m) => ({ default: m.Settings })));
const Onboarding = lazy(() => loadOnboarding().then((m) => ({ default: m.Onboarding })));
const OpenProject = lazy(() => loadOpenProject().then((m) => ({ default: m.OpenProject })));
const ImportSheet = lazy(() => loadImportSheet().then((m) => ({ default: m.ImportSheet })));

// Fetched once the window has drawn, so opening any of them later shows no blank frame.
function preload() {
  for (const load of [loadMarkdown, loadSettings, loadOnboarding, loadOpenProject, loadImportSheet]) load().catch(() => {});
}

export function App() {
  const ready = useStore((s) => s.ready);
  const project = useStore((s) => s.project);
  const sidebar = useStore((s) => s.sidebar);
  const settingsOpen = useStore((s) => s.settingsOpen);
  const theme = useStore((s) => s.settings?.theme ?? "system");
  const message = useStore((s) => s.toast);
  const onboarded = useStore((s) => s.onboarded);
  const pickerOpen = useStore((s) => s.pickerOpen);
  const importOpen = useStore((s) => s.importOpen);

  useEffect(() => {
    const stopEvents = listen();
    const run = (action: string) => {
      // The first run finishes before the menu does anything.
      if (!getState().onboarded) return;
      switch (action) {
        case "new-chat":
          newChat();
          break;
        case "open-project":
          setState(() => ({ pickerOpen: true, settingsOpen: false }));
          break;
        case "import":
          setState(() => ({ importOpen: true, settingsOpen: false }));
          break;
        case "settings":
          setState(() => ({ settingsOpen: true }));
          break;
        case "toggle-sidebar":
          setState((s) => ({ sidebar: !s.sidebar }));
          break;
      }
    };
    const stopMenu = onMenu(run);
    // Linux has no menu, so the page takes its shortcuts.
    const onKey = (e: KeyboardEvent) => {
      const action = menuAction(e);
      if (action) {
        e.preventDefault();
        run(action);
      }
    };
    window.addEventListener("keydown", onKey);
    // A double-click on the title bar zooms the window, as in any window.
    // Wails starts no drag on the second click, so it reaches the page.
    const onDown = (e: MouseEvent) => {
      if (e.detail !== 2 || e.button !== 0 || !(e.target instanceof Element)) return;
      if (getComputedStyle(e.target).getPropertyValue("--wails-draggable").trim() === "drag")
        api.titleBarDoubleClick().catch(() => {});
    };
    window.addEventListener("mousedown", onDown);
    // Back from the browser, a plan bought or a sign-in made there shows.
    const onFocus = () => {
      if (getState().ready) refreshAccount();
    };
    window.addEventListener("focus", onFocus);
    boot().catch(toast);
    return () => {
      stopEvents();
      stopMenu();
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("focus", onFocus);
    };
  }, []);

  useEffect(() => applyTheme(theme), [theme]);

  useEffect(() => {
    if (ready) window.setTimeout(preload, 0);
  }, [ready]);

  if (!ready) return <div className="app drag" />;

  return (
    <div className="app">
      {onboarded && <Sidebar hidden={!sidebar || !project} />}
      <main className="main">
        <Suspense fallback={null}>
          {!onboarded ? <Onboarding /> : settingsOpen ? <Settings /> : project ? <ChatPane /> : <Welcome />}
        </Suspense>
      </main>
      <Suspense fallback={null}>
        {pickerOpen && <OpenProject />}
        {importOpen && <ImportSheet />}
      </Suspense>
      {message && (
        <div className="toast" role="status">
          <span>{message}</span>
          <button className="no-drag" onClick={() => setState(() => ({ toast: null }))} aria-label="Dismiss">
            <X size={14} />
          </button>
        </div>
      )}
    </div>
  );
}
