import { useEffect, useState } from "react";
import { PanelLeft } from "lucide-react";
import { api, openURL } from "../lib/bridge";
import { effortName, tokens } from "../lib/format";
import { shortcut } from "../lib/platform";
import { applyTheme } from "../lib/theme";
import {
  getState,
  inputFrom,
  refreshAccount,
  refreshChat,
  setState,
  signOut,
  toast,
  useStore,
} from "../lib/store";
import type { SettingsInput, Theme } from "../lib/types";
import { McpSection, SkillsSection } from "./Extensions";
import { ModelLogos } from "./ModelLogos";
import { PathPicker } from "./PathPicker";
import { Plans, SignIn } from "./Plans";
import { Select } from "./Select";
import { UsageMeter } from "./Usage";

// Settings is a page in place of the chat; the sidebar stays, and
// opening a chat from it, Escape, or Cancel goes back. The account and
// the plans are always at the top; an admin account has no plans.
export function Settings() {
  const settings = useStore((s) => s.settings)!;
  const version = useStore((s) => s.version);
  const models = useStore((s) => s.catalog);
  const [form, setForm] = useState<SettingsInput>(() => inputFrom(settings));
  const [saving, setSaving] = useState(false);
  // Phone companion goes on and off at once, apart from the form.
  const [companion, setCompanion] = useState(false);
  const workspace = useStore((s) => s.workspace);
  const [picking, setPicking] = useState(false);
  const sidebar = useStore((s) => s.sidebar);
  const admin = useStore((s) => (s.account?.signedIn && s.account.admin) ?? false);

  const set = <K extends keyof SettingsInput>(k: K, v: SettingsInput[K]) => setForm((f) => ({ ...f, [k]: v }));

  const close = () => {
    applyTheme(settings.theme);
    setState(() => ({ settingsOpen: false }));
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && close();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  // The plan and its usage as they are now, not as at launch.
  useEffect(() => {
    refreshAccount();
    api.companion().then(setCompanion, () => {});
  }, []);

  const save = async () => {
    setSaving(true);
    try {
      const view = await api.saveSettings(form);
      setState(() => ({ settings: view, settingsOpen: false }));
      applyTheme(view.theme);
      const id = getState().chatId;
      if (id) await refreshChat(id);
    } catch (e) {
      toast(e);
    } finally {
      setSaving(false);
    }
  };

  const listed = models.some((m) => m.id === form.model);

  return (
    <>
      <header className={`header drag${sidebar ? "" : " inset"}`}>
        {sidebar ? (
          <span className="header-spacer" />
        ) : (
          <button
            className="icon-btn no-drag"
            title={`Show sidebar (${shortcut("\\")})`}
            onClick={() => setState(() => ({ sidebar: true }))}
          >
            <PanelLeft size={16} />
          </button>
        )}
        <div className="header-title" />
        <span className="header-spacer" />
      </header>

      <div className="scroll settings-page">
        <div className="settings-column" role="region" aria-label="Settings">
          <h1>Settings</h1>

          <section className="settings-section">
            <h2>Account</h2>
            <Account />
          </section>

          {/* An admin account has no plan to pick. */}
          {!admin && (
            <section className="settings-section">
              <h2>Plan</h2>
              <div className="row-plans">
                <Plans />
              </div>
            </section>
          )}

          <section className="settings-section">
            <h2>Model</h2>
            <div className="row">
              <div className="row-title">Model for new chats</div>
              <Select
                label="Model for new chats"
                value={form.model}
                placeholder="Loading…"
                onChange={(v) => set("model", v)}
                options={[
                  ...(!listed && form.model ? [{ value: form.model, label: form.model }] : []),
                  ...models.map((m) => ({
                    value: m.id,
                    label: (
                      <>
                        <ModelLogos logos={m.logos} />
                        <span>{m.name || m.id}</span>
                      </>
                    ),
                    aside: m.context ? `${tokens(m.context)} context` : undefined,
                    sub: m.unusable || (m.context ? `${tokens(m.context)} token context` : undefined),
                    disabled: Boolean(m.unusable),
                  })),
                ]}
              />
            </div>

            <div className="row">
              <div className="row-title">Reasoning effort</div>
              <Select
                label="Reasoning effort"
                value={form.effort}
                onChange={(v) => set("effort", v)}
                options={["", ...settings.efforts].map((e) => ({
                  value: e,
                  label: e ? effortName(e) : "Model default",
                }))}
              />
            </div>
          </section>

          <section className="settings-section">
            <h2>MCP servers</h2>
            <McpSection />
          </section>

          <section className="settings-section">
            <h2>Skills</h2>
            <SkillsSection />
          </section>

          <section className="settings-section">
            <h2>General</h2>
            <div className="row">
              <div className="row-head">
                <div>
                  <div className="row-title">Ask permissions in new chats</div>
                  <div className="row-hint">
                    Otherwise new chats bypass every permission and work on their own until the task is done. Each chat
                    switches with the picker under its composer, or ⇧Tab.
                  </div>
                </div>
                <button
                  className={`switch${form.confirm ? " on" : ""}`}
                  role="switch"
                  aria-checked={form.confirm}
                  onClick={() => set("confirm", !form.confirm)}
                />
              </div>
            </div>

            {/* The iPhone app isn't out yet, so only an admin sees the switch,
                or someone who already has it on and may want it off. */}
            {(admin || companion) && (
              <div className="row">
                <div className="row-head">
                  <div>
                    <div className="row-title">Phone companion</div>
                    <div className="row-hint">
                      Follow your open chats in the caveira iPhone app: answer permissions, send follow-ups, and get a
                      notification when a chat needs you or is done. While it's on, open chats pass through
                      caveira.app; turning it off deletes them there.
                    </div>
                  </div>
                  <button
                    className={`switch${companion ? " on" : ""}`}
                    role="switch"
                    aria-checked={companion}
                    onClick={() => {
                      const on = !companion;
                      setCompanion(on);
                      api.setCompanion(on).then(setCompanion, () => setCompanion(!on));
                    }}
                  />
                </div>
              </div>
            )}

            <div className="row">
              <div className="row-head">
                <div>
                  <div className="row-title">Workspace</div>
                  <div className="row-hint">
                    {workspace.path ? (
                      <>
                        Projects open from <span className="mono">{workspace.short}</span>.
                      </>
                    ) : (
                      "Projects open from your home folder."
                    )}
                  </div>
                </div>
                {!picking && (
                  <button className="btn" onClick={() => setPicking(true)}>
                    Change
                  </button>
                )}
              </div>
              {picking && (
                <div className="row-picker">
                  <PathPicker
                    base=""
                    mode="workspace"
                    placeholder="type the folder"
                    initial={workspace.short ? workspace.short + "/" : ""}
                    onPick={async (path) => {
                      try {
                        const ws = await api.setWorkspace(path);
                        setState(() => ({ workspace: ws }));
                        setPicking(false);
                      } catch (e) {
                        toast(e);
                      }
                    }}
                    onCancel={() => setPicking(false)}
                    aside={
                      <button className="btn quiet" onClick={() => setPicking(false)}>
                        Cancel
                      </button>
                    }
                  />
                </div>
              )}
            </div>

            <div className="row">
              <div className="row-head">
                <div>
                  <div className="row-title">Chats from other agents</div>
                  <div className="row-hint">Bring over chats and projects from Claude Code, Codex, OpenCode, and Gemini CLI.</div>
                </div>
                <button className="btn" onClick={() => setState(() => ({ settingsOpen: false, importOpen: true }))}>
                  Import…
                </button>
              </div>
            </div>

            <div className="row">
              <div className="row-head">
                <div className="row-title">Appearance</div>
                <div className="segmented">
                  {(["system", "light", "dark"] as Theme[]).map((t) => (
                    <button
                      key={t}
                      className={form.theme === t ? "on" : ""}
                      onClick={() => {
                        set("theme", t);
                        applyTheme(t);
                      }}
                    >
                      {t[0].toUpperCase() + t.slice(1)}
                    </button>
                  ))}
                </div>
              </div>
            </div>
          </section>
        </div>
      </div>

      <div className="settings-foot">
        <div className="settings-foot-inner">
          <span className="version">caveira {version}</span>
          <div style={{ display: "flex", gap: 8 }}>
            <button className="btn quiet" onClick={close}>
              Cancel
            </button>
            <button className="btn primary" disabled={saving} onClick={save}>
              Save
            </button>
          </div>
        </div>
      </div>
    </>
  );
}

// Account is the caveira account, above the plans. Signed out it is the
// way in; signed in, whose it is, its plan and how much of it is used,
// and the way out. Its plan and billing are managed on the site.
function Account() {
  const account = useStore((s) => s.account);
  if (!account?.signedIn) {
    return (
      <div className="row account">
        <SignIn primary>
          {account?.expired
            ? `The sign-in for ${account.email} ran out. Sign in again to chat.`
            : "Sign in to your caveira account to chat, on Free or a plan."}
        </SignIn>
      </div>
    );
  }
  return (
    <div className="row account">
      <div className="row-head">
        <div>
          <div className="row-title">{account.email}</div>
          <div className="row-hint">{account.planName ? account.planLine || account.planName : "Asking caveira for your plan…"}</div>
        </div>
        <div className="row-actions">
          <button className="btn" onClick={() => openURL(`${account.site}/account`)}>
            Manage
          </button>
          <button className="btn quiet" onClick={signOut}>
            Sign out
          </button>
        </div>
      </div>
      {account.planName && <UsageMeter account={account} />}
    </div>
  );
}
