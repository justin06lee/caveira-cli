import { useEffect, useState } from "react";
import { api } from "../lib/bridge";
import { toast, useStore } from "../lib/store";
import type { McpFound, McpServer, RegistryServer, Skill } from "../lib/types";

// The MCP servers and skills sections of Settings. Servers are the ones
// set up for you (~/.caveira/mcp.json) and for the open project; each
// change is saved at once, like the switches elsewhere on the page.

const statusText: Record<McpServer["status"], string> = {
  starting: "Starting…",
  ready: "",
  failed: "Failed to start",
  disabled: "Off",
};

function serverHint(s: McpServer): string {
  if (s.status === "ready")
    return [`${s.tools.length} ${s.tools.length === 1 ? "tool" : "tools"}`, s.deferred && "loaded on demand", s.summary]
      .filter(Boolean)
      .join(" · ");
  return [statusText[s.status], s.summary].filter(Boolean).join(" · ");
}

export function McpSection() {
  const dir = useStore((s) => s.project?.path ?? "");
  const [servers, setServers] = useState<McpServer[] | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [mode, setMode] = useState<"" | "add" | "import">("");

  const load = () =>
    api
      .mcpServers(dir)
      .then((l) => setServers(l ?? []))
      .catch(toast);

  useEffect(() => {
    load();
    // Servers that are starting say how they got on a moment later.
    const t = window.setInterval(load, 3000);
    return () => window.clearInterval(t);
  }, [dir]);

  const run = (p: Promise<McpServer[] | null>) =>
    p.then((l) => setServers(l ?? [])).catch(toast);

  return (
    <>
      {servers?.length === 0 && mode === "" && (
        <div className="row">
          <div className="row-hint">
            No MCP servers yet. Add one from the MCP Registry, or bring over the ones set up in Claude Code, Claude
            Desktop or Cursor. Chats can call their tools, asking first unless permissions are bypassed.
          </div>
        </div>
      )}
      {servers?.map((s) => (
        <div className="row" key={s.name}>
          <div className="row-head">
            <button className="ext-name" onClick={() => setOpen(open === s.name ? null : s.name)}>
              <div className={`row-title${s.status === "failed" ? " ext-failed" : ""}`}>
                {s.name}
                {s.scope === "project" && <span className="ext-tag">project</span>}
              </div>
              <div className="row-hint mono ext-hint">{serverHint(s)}</div>
            </button>
            <div className="row-actions">
              <button className="btn quiet" onClick={() => run(api.removeMcp(dir, s.name))}>
                Remove
              </button>
              <button
                className={`switch${s.status !== "disabled" ? " on" : ""}`}
                role="switch"
                aria-checked={s.status !== "disabled"}
                aria-label={`${s.name} on`}
                onClick={() => run(api.setMcpEnabled(dir, s.name, s.status === "disabled"))}
              />
            </div>
          </div>
          {open === s.name && (
            <div className="ext-detail">
              {s.error && <div className="ext-error">{s.error}</div>}
              {s.tools.map((t) => (
                <div key={t.name} className="ext-tool">
                  <span className="mono">{t.name}</span>
                  <span>{t.description}</span>
                </div>
              ))}
              {!s.error && s.tools.length === 0 && <div className="row-hint">No tools yet.</div>}
            </div>
          )}
        </div>
      ))}
      {mode === "add" && <AddFromRegistry dir={dir} done={() => (setMode(""), load())} />}
      {mode === "import" && <ImportServers dir={dir} done={() => (setMode(""), load())} />}
      {mode === "" && (
        <div className="row">
          <div className="row-head">
            <div className="row-hint">From the MCP Registry, or from your other apps.</div>
            <div className="row-actions">
              <button className="btn" onClick={() => setMode("import")}>
                Import…
              </button>
              <button className="btn" onClick={() => setMode("add")}>
                Add server…
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function AddFromRegistry({ dir, done }: { dir: string; done: () => void }) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<RegistryServer[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [pick, setPick] = useState<RegistryServer | null>(null);
  const [way, setWay] = useState(0);
  const [values, setValues] = useState<Record<string, string>>({});

  const search = async () => {
    if (!query.trim()) return;
    setBusy(true);
    try {
      setResults((await api.searchMcp(query)) ?? []);
    } catch (e) {
      toast(e);
    } finally {
      setBusy(false);
    }
  };

  const add = async () => {
    if (!pick) return;
    setBusy(true);
    try {
      const name = await api.addMcp(dir, pick.name, way, values);
      toast(`Added ${name}. Its tools come with your next message.`);
      done();
    } catch (e) {
      toast(e);
    } finally {
      setBusy(false);
    }
  };

  if (pick) {
    const needs = pick.options[way]?.needs ?? [];
    return (
      <div className="row">
        <div className="row-title">{pick.title || pick.short}</div>
        <div className="row-hint">{pick.description}</div>
        {pick.options.length > 1 && (
          <div className="segmented ext-ways">
            {pick.options.map((o, i) => (
              <button key={o.label} className={i === way ? "on" : ""} onClick={() => setWay(i)}>
                {o.label.split(" · ")[0]}
              </button>
            ))}
          </div>
        )}
        <div className="row-hint mono">{pick.options[way]?.label}</div>
        {needs.map((n) => (
          <div key={n.name}>
            <input
              className="field mono"
              type={n.isSecret ? "password" : "text"}
              placeholder={n.name}
              value={values[n.name] ?? ""}
              onChange={(e) => setValues((v) => ({ ...v, [n.name]: e.target.value }))}
            />
            {n.description && <div className="row-hint">{n.description}</div>}
          </div>
        ))}
        <div className="ext-buttons">
          <button className="btn quiet" onClick={() => setPick(null)}>
            Back
          </button>
          <button
            className="btn primary"
            disabled={busy || needs.some((n) => !values[n.name]?.trim())}
            onClick={add}
          >
            Add
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="row">
      <div className="row-head">
        <div className="row-title">Add from the MCP Registry</div>
        <button className="btn quiet" onClick={done}>
          Cancel
        </button>
      </div>
      <input
        className="field"
        autoFocus
        placeholder="Search servers: github, postgres, browser…"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && search()}
      />
      {busy && <div className="row-hint">Searching…</div>}
      {results?.length === 0 && <div className="row-hint">Nothing caveira can run matches that.</div>}
      <div className="ext-results">
        {results?.map((r) => (
          <button
            key={r.name}
            className="ext-result"
            onClick={() => {
              setPick(r);
              setWay(0);
              setValues({});
            }}
          >
            <span className="row-title">{r.title || r.short}</span>
            <span className="row-hint">{r.description}</span>
            <span className="row-hint mono">{r.name}</span>
          </button>
        ))}
      </div>
    </div>
  );
}

function ImportServers({ dir, done }: { dir: string; done: () => void }) {
  const [found, setFound] = useState<McpFound[] | null>(null);
  const [picked, setPicked] = useState<number[]>([]);

  useEffect(() => {
    api
      .detectMcp(dir)
      .then((f) => setFound(f ?? []))
      .catch(toast);
  }, [dir]);

  const copy = async () => {
    try {
      await api.importMcp(dir, picked);
      toast(`Copied ${picked.length} ${picked.length === 1 ? "server" : "servers"} in.`);
      done();
    } catch (e) {
      toast(e);
    }
  };

  return (
    <div className="row">
      <div className="row-head">
        <div>
          <div className="row-title">Import MCP servers</div>
          <div className="row-hint">Set up in Claude Code, Claude Desktop and Cursor. Nothing is copied until you say.</div>
        </div>
      </div>
      {found?.length === 0 && <div className="row-hint">None found.</div>}
      {found?.map((f, i) => (
        <div className="row-head ext-import" key={`${f.source}-${f.name}`}>
          <div>
            <div className="row-title">{f.name}</div>
            <div className="row-hint">
              {f.source}
              {f.have ? " · replaces yours" : ""}
            </div>
          </div>
          <button
            className={`switch${picked.includes(i) ? " on" : ""}`}
            role="switch"
            aria-checked={picked.includes(i)}
            aria-label={`Import ${f.name}`}
            onClick={() => setPicked((p) => (p.includes(i) ? p.filter((x) => x !== i) : [...p, i]))}
          />
        </div>
      ))}
      <div className="ext-buttons">
        <button className="btn quiet" onClick={done}>
          Cancel
        </button>
        <button className="btn primary" disabled={picked.length === 0} onClick={copy}>
          Import
        </button>
      </div>
    </div>
  );
}

export function SkillsSection() {
  const dir = useStore((s) => s.project?.path ?? "");
  const [list, setList] = useState<Skill[] | null>(null);
  const [source, setSource] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api
      .skills(dir)
      .then((l) => setList(l ?? []))
      .catch(toast);
  }, [dir]);

  const run = (p: Promise<Skill[] | null>) => p.then((l) => setList(l ?? [])).catch(toast);

  const install = async () => {
    if (!source.trim()) return;
    setBusy(true);
    try {
      setList((await api.installSkill(dir, source)) ?? []);
      setSource("");
    } catch (e) {
      toast(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      {list?.length === 0 && (
        <div className="row">
          <div className="row-hint">
            No skills yet. Skills in ~/.claude/skills and a project's .claude/skills are picked up as they are.
          </div>
        </div>
      )}
      {list?.map((s) => (
        <div className="row" key={s.name}>
          <div className="row-head">
            <div>
              <div className="row-title">{s.name}</div>
              <div className="row-hint">{s.description}</div>
              <div className="row-hint mono">{s.where}</div>
            </div>
            <div className="row-actions">
              {s.installed && (
                <button className="btn quiet" onClick={() => run(api.removeSkill(dir, s.name))}>
                  Remove
                </button>
              )}
              <button
                className={`switch${s.enabled ? " on" : ""}`}
                role="switch"
                aria-checked={s.enabled}
                aria-label={`${s.name} on`}
                onClick={() => run(api.setSkillEnabled(dir, s.name, !s.enabled))}
              />
            </div>
          </div>
        </div>
      ))}
      <div className="row">
        <div className="row-title">Install from GitHub</div>
        <div className="row-hint">A repository, a folder in one, or owner/repo/path.</div>
        <div className="ext-install">
          <input
            className="field mono"
            placeholder="https://github.com/anthropics/skills/tree/main/skills/pdf"
            value={source}
            onChange={(e) => setSource(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && install()}
          />
          <button className="btn" disabled={busy || !source.trim()} onClick={install}>
            {busy ? "Installing…" : "Install"}
          </button>
        </div>
      </div>
    </>
  );
}
