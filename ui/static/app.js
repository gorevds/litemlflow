// LiteMLflow UI — vanilla JS SPA.
//
// Three principles:
// 1. Zero build step (the binary serves these files as-is via go:embed).
// 2. Server-side downsampling for big metric series; the UI just renders.
// 3. No external network calls — all data comes from the same origin.

(function () {
  "use strict";

  const $ = (sel, parent = document) => parent.querySelector(sel);
  const $$ = (sel, parent = document) => Array.from(parent.querySelectorAll(sel));

  // ─── Embed mode ─────────────────────────────────────────────────────────────
  if (new URLSearchParams(location.search).get("embed") === "1") {
    document.body.setAttribute("data-embed", "1");
  }

  // ─── Bulk-select state (survives re-renders) ─────────────────────────────────
  const BulkSelect = {
    _checked: new Set(), // run_id strings
    _expID: null,

    reset(expID) {
      if (this._expID !== expID) {
        this._checked.clear();
        this._expID = expID;
      }
    },

    toggle(id) {
      if (this._checked.has(id)) this._checked.delete(id);
      else this._checked.add(id);
    },

    has(id) { return this._checked.has(id); },
    list()  { return Array.from(this._checked); },
    size()  { return this._checked.size; },
    clear() { this._checked.clear(); },
  };

  // ─── Workspace ───────────────────────────────────────────────────────────────
  const Workspace = {
    _current: getCookie("lmf_workspace") || "default",

    get() { return this._current; },

    set(ws) {
      this._current = ws;
      setCookie("lmf_workspace", ws, 365);
    },

    header() {
      return this._current && this._current !== "default"
        ? { "X-Workspace": this._current }
        : {};
    },
  };

  // ─── Utilities ───────────────────────────────────────────────────────────────
  function getCookie(name) {
    const m = document.cookie.match(new RegExp("(?:^|; )" + name + "=([^;]*)"));
    return m ? decodeURIComponent(m[1]) : "";
  }

  function setCookie(name, value, days) {
    const d = new Date();
    d.setTime(d.getTime() + days * 864e5);
    document.cookie = `${name}=${encodeURIComponent(value)};expires=${d.toUTCString()};path=/;SameSite=Lax`;
  }

  function fetchJSON(url, init) {
    init = init || {};
    init.headers = Object.assign(
      { "Content-Type": "application/json" },
      Workspace.header(),
      init.headers || {}
    );
    return fetch(url, init).then(r => {
      if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
      return r.json();
    });
  }

  function escapeHTML(s) {
    return String(s).replace(/[&<>"']/g, ch => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    })[ch]);
  }

  function formatTime(ms) {
    if (!ms) return "—";
    const d = new Date(ms);
    return d.toISOString().replace("T", " ").slice(0, 19);
  }

  function formatDuration(ms) {
    if (!ms) return "—";
    if (ms < 1000) return ms + "ms";
    const s = ms / 1000;
    if (s < 60) return s.toFixed(1) + "s";
    const m = Math.floor(s / 60), rs = s % 60;
    return m + "m " + rs.toFixed(0) + "s";
  }

  function formatNS(ns) {
    if (ns < 1000) return ns + "ns";
    if (ns < 1e6) return (ns / 1000).toFixed(1) + "µs";
    if (ns < 1e9) return (ns / 1e6).toFixed(1) + "ms";
    return (ns / 1e9).toFixed(2) + "s";
  }

  function debounce(fn, ms) {
    let t;
    return function (...args) {
      clearTimeout(t);
      t = setTimeout(() => fn.apply(this, args), ms);
    };
  }

  // ─── Tiny safe inline Markdown renderer ──────────────────────────────────────
  // Whitelist: bold (**text**), italic (*text*), inline code (`text`),
  // code blocks (```...```), links ([text](url)), unordered lists (- item),
  // and paragraphs. NO raw HTML pass-through.
  function renderMarkdown(md) {
    if (!md) return "";
    const lines = md.split("\n");
    const out = [];
    let inCode = false;
    let inList = false;

    for (let i = 0; i < lines.length; i++) {
      let line = lines[i];

      // Fenced code block toggle
      if (line.startsWith("```")) {
        if (inCode) {
          out.push("</code></pre>");
          inCode = false;
        } else {
          if (inList) { out.push("</ul>"); inList = false; }
          out.push('<pre class="md-code"><code>');
          inCode = true;
        }
        continue;
      }
      if (inCode) {
        out.push(escapeHTML(line) + "\n");
        continue;
      }

      // Unordered list items
      if (line.match(/^[-*+] /)) {
        if (!inList) { out.push("<ul>"); inList = true; }
        out.push("<li>" + inlineMarkdown(line.slice(2)) + "</li>");
        continue;
      }
      if (inList && line.trim() !== "") {
        // continuation indented items
        if (line.match(/^\s+[-*+] /)) {
          out.push("<li>" + inlineMarkdown(line.trimStart().slice(2)) + "</li>");
          continue;
        }
      }
      if (inList) { out.push("</ul>"); inList = false; }

      // Headings
      if (line.startsWith("# "))  { out.push("<h3>" + inlineMarkdown(line.slice(2)) + "</h3>"); continue; }
      if (line.startsWith("## ")) { out.push("<h4>" + inlineMarkdown(line.slice(3)) + "</h4>"); continue; }
      if (line.startsWith("### ")) { out.push("<h5>" + inlineMarkdown(line.slice(4)) + "</h5>"); continue; }

      // Blank line → paragraph break
      if (line.trim() === "") {
        out.push("<br/>");
        continue;
      }

      out.push("<p>" + inlineMarkdown(line) + "</p>");
    }
    if (inList) out.push("</ul>");
    if (inCode) out.push("</code></pre>");
    return out.join("");
  }

  function inlineMarkdown(text) {
    // Process inline code first to prevent double-escaping.
    // Split on backtick spans, escape & render.
    const parts = text.split(/(`[^`]+`)/g);
    return parts.map((part, i) => {
      if (i % 2 === 1) {
        // inline code
        return "<code>" + escapeHTML(part.slice(1, -1)) + "</code>";
      }
      let s = escapeHTML(part);
      // Bold
      s = s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
      // Italic (single star, not already consumed by bold)
      s = s.replace(/\*([^*]+)\*/g, "<em>$1</em>");
      // Links [text](url) — allow only http/https/mailto hrefs
      s = s.replace(/\[([^\]]+)\]\((https?:\/\/[^)]+|mailto:[^)]+)\)/g,
        '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
      return s;
    }).join("");
  }

  // ─── Chart ───────────────────────────────────────────────────────────────────
  function simpleChart(key, points, downsampledFrom) {
    if (!points.length) return "";
    const W = 800, H = 200, pad = 30;
    const vals = points.map(p => p.value);
    const ts = points.map(p => p.timestamp);
    const tmin = Math.min(...ts), tmax = Math.max(...ts);
    const vmin = Math.min(...vals), vmax = Math.max(...vals);
    const tspan = Math.max(tmax - tmin, 1), vspan = Math.max(vmax - vmin, Math.abs(vmax) || 1);
    const xy = points.map(p => {
      const x = pad + ((p.timestamp - tmin) / tspan) * (W - 2 * pad);
      const y = H - pad - ((p.value - vmin) / vspan) * (H - 2 * pad);
      return [x, y];
    });
    const d = xy.map(([x, y], i) => (i === 0 ? "M" : "L") + x.toFixed(1) + "," + y.toFixed(1)).join("");
    let dsNote = "";
    if (downsampledFrom != null && downsampledFrom > points.length) {
      dsNote = ` <span style="color:var(--fg-muted);font-size:0.85em">(showing ${points.length} of ${downsampledFrom} points, LTTB)</span>`;
    }
    return `
      <div class="card" style="padding:8px 12px">
        <strong>${escapeHTML(key)}</strong>${dsNote} <span style="color:var(--fg-muted)">(min ${vmin.toPrecision(4)}, max ${vmax.toPrecision(4)})</span>
        <div class="metric-chart">
          <svg width="100%" height="100%" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">
            <path d="${d}" fill="none" stroke="var(--accent)" stroke-width="1.5" />
          </svg>
        </div>
      </div>`;
  }

  // ─── Keyboard shortcuts ───────────────────────────────────────────────────────
  const Shortcuts = {
    _chordFirst: null,
    _chordTimer: null,

    _isTyping() {
      const el = document.activeElement;
      if (!el) return false;
      const tag = el.tagName.toLowerCase();
      return tag === "input" || tag === "textarea" || tag === "select" || el.isContentEditable;
    },

    init() {
      document.addEventListener("keydown", e => this._handle(e));
    },

    _handle(e) {
      // Ignore modifier-only or when typing (except ctrl/cmd+K which we handle)
      const mod = e.ctrlKey || e.metaKey;
      if (mod && e.key === "k") {
        e.preventDefault();
        CommandPalette.open();
        return;
      }
      if (this._isTyping()) return;

      const key = e.key;

      // Handle chord second
      if (this._chordFirst === "g") {
        clearTimeout(this._chordTimer);
        this._chordFirst = null;
        if (key === "e") { location.hash = "#/experiments"; return; }
        if (key === "p") { location.hash = "#/prompts"; return; }
        if (key === "h") { location.hash = "#/about"; return; }
      }

      switch (key) {
        case "?":
          e.preventDefault();
          ShortcutHelp.toggle();
          break;
        case "g":
          this._chordFirst = "g";
          this._chordTimer = setTimeout(() => { this._chordFirst = null; }, 1000);
          break;
        case "Escape":
          ShortcutHelp.close();
          CommandPalette.close();
          this._clearSelection();
          break;
        case "j":
          e.preventDefault();
          this._moveSelection(1);
          break;
        case "k":
          e.preventDefault();
          this._moveSelection(-1);
          break;
        case "Enter":
          this._activateSelected();
          break;
        case "/":
          e.preventDefault();
          this._focusSearch();
          break;
      }
    },

    _getRows() {
      return $$("[data-row-index]", $("#app"));
    },

    _selectedIndex() {
      const sel = $("[data-selected]", $("#app"));
      if (!sel) return -1;
      return parseInt(sel.getAttribute("data-row-index"), 10);
    },

    _moveSelection(delta) {
      const rows = this._getRows();
      if (!rows.length) return;
      let idx = this._selectedIndex();
      idx = Math.max(0, Math.min(rows.length - 1, idx + delta));
      rows.forEach(r => r.removeAttribute("data-selected"));
      const target = rows.find(r => parseInt(r.getAttribute("data-row-index"), 10) === idx);
      if (target) {
        target.setAttribute("data-selected", "");
        target.scrollIntoView({ block: "nearest" });
      }
    },

    _activateSelected() {
      const sel = $("[data-selected]", $("#app"));
      if (sel) sel.click();
    },

    _clearSelection() {
      $$("[data-selected]", $("#app")).forEach(r => r.removeAttribute("data-selected"));
    },

    _focusSearch() {
      const inp = $("input[type=search], input[type=text]", $("#app"));
      if (inp) inp.focus();
    },
  };

  // ─── Shortcut help overlay ────────────────────────────────────────────────────
  const ShortcutHelp = {
    _el: null,

    _ensure() {
      if (this._el) return;
      const el = document.createElement("div");
      el.className = "shortcut-overlay";
      el.id = "shortcut-overlay";
      el.innerHTML = `
        <div class="shortcut-modal">
          <div class="shortcut-modal-header">
            <strong>Keyboard shortcuts</strong>
            <button class="modal-close" id="shortcut-close">✕</button>
          </div>
          <table class="shortcut-table">
            <tbody>
              <tr><td><kbd>?</kbd></td><td>Show this help</td></tr>
              <tr><td><kbd>g</kbd> <kbd>e</kbd></td><td>Go to Experiments</td></tr>
              <tr><td><kbd>g</kbd> <kbd>p</kbd></td><td>Go to Prompts</td></tr>
              <tr><td><kbd>g</kbd> <kbd>h</kbd></td><td>Go to About</td></tr>
              <tr><td><kbd>j</kbd> / <kbd>k</kbd></td><td>Move selection down / up</td></tr>
              <tr><td><kbd>Enter</kbd></td><td>Open selected row</td></tr>
              <tr><td><kbd>Esc</kbd></td><td>Close modal / clear selection</td></tr>
              <tr><td><kbd>⌘K</kbd> / <kbd>Ctrl+K</kbd></td><td>Open command palette</td></tr>
              <tr><td><kbd>/</kbd></td><td>Focus search</td></tr>
            </tbody>
          </table>
        </div>`;
      document.body.appendChild(el);
      this._el = el;
      el.addEventListener("click", ev => {
        if (ev.target === el || ev.target.id === "shortcut-close") this.close();
      });
    },

    toggle() {
      this._ensure();
      this._el.classList.toggle("open");
    },

    close() {
      if (this._el) this._el.classList.remove("open");
    },
  };

  // ─── Command palette ──────────────────────────────────────────────────────────
  const CommandPalette = {
    _el: null,
    _items: [],
    _cursor: 0,
    _debTimer: null,

    _staticCmds: [
      { label: "Go to: experiments",   action: () => { location.hash = "#/experiments"; } },
      { label: "Go to: prompts",        action: () => { location.hash = "#/prompts"; } },
      { label: "Toggle theme",          action: () => { $("#theme-toggle").click(); } },
      { label: "Open API: /metrics",    action: () => { window.open("/metrics", "_blank"); } },
      { label: "Open API: /version",    action: () => { window.open("/version", "_blank"); } },
      { label: "Copy: tracking URI",    action: () => { navigator.clipboard.writeText(location.origin).catch(() => {}); } },
    ],

    _ensure() {
      if (this._el) return;
      const el = document.createElement("div");
      el.className = "palette-overlay";
      el.id = "palette-overlay";
      el.innerHTML = `
        <div class="palette-modal">
          <input type="text" id="palette-input" placeholder="Search commands or experiments…" autocomplete="off" />
          <ul class="palette-list" id="palette-list"></ul>
        </div>`;
      document.body.appendChild(el);
      this._el = el;

      el.addEventListener("click", ev => { if (ev.target === el) this.close(); });

      const input = $("#palette-input");
      input.addEventListener("keydown", e => {
        if (e.key === "Escape") { this.close(); return; }
        if (e.key === "ArrowDown") { e.preventDefault(); this._moveCursor(1); return; }
        if (e.key === "ArrowUp")   { e.preventDefault(); this._moveCursor(-1); return; }
        if (e.key === "Enter")     { e.preventDefault(); this._activate(); return; }
      });
      input.addEventListener("input", () => {
        clearTimeout(this._debTimer);
        this._debTimer = setTimeout(() => this._search(input.value.trim()), 200);
      });
    },

    open() {
      this._ensure();
      this._el.classList.add("open");
      const input = $("#palette-input");
      input.value = "";
      this._search("");
      setTimeout(() => input.focus(), 30);
    },

    close() {
      if (this._el) this._el.classList.remove("open");
    },

    async _search(query) {
      let items = [...this._staticCmds];

      if (query) {
        // Dynamic: search experiments by name
        try {
          const data = await fetchJSON("/api/2.0/mlflow/experiments/search?max_results=1000");
          const exps = (data.experiments || []).filter(e => e.lifecycle_stage === "active");
          const ql = query.toLowerCase();
          const matched = exps
            .filter(e => e.name.toLowerCase().includes(ql) || e.experiment_id.startsWith(ql))
            .slice(0, 5)
            .map(e => ({
              label: `Search: ${e.name}`,
              action: () => { location.hash = `#/experiments/${e.experiment_id}`; },
            }));
          items = [...matched, ...items];
        } catch {}
      }

      // Filter static by query
      if (query) {
        const ql = query.toLowerCase();
        items = items.filter(i => i.label.toLowerCase().includes(ql));
      }

      this._items = items.slice(0, 10);
      this._cursor = 0;
      this._render();
    },

    _render() {
      const list = $("#palette-list");
      if (!list) return;
      list.innerHTML = this._items.map((item, i) =>
        `<li class="palette-item${i === this._cursor ? " palette-item--active" : ""}" data-idx="${i}">${escapeHTML(item.label)}</li>`
      ).join("");
      $$(".palette-item", list).forEach(li => {
        li.addEventListener("mouseenter", () => {
          this._cursor = parseInt(li.dataset.idx, 10);
          this._render();
        });
        li.addEventListener("click", () => {
          this._cursor = parseInt(li.dataset.idx, 10);
          this._activate();
        });
      });
    },

    _moveCursor(delta) {
      if (!this._items.length) return;
      this._cursor = (this._cursor + delta + this._items.length) % this._items.length;
      this._render();
    },

    _activate() {
      const item = this._items[this._cursor];
      if (item) { this.close(); item.action(); }
    },
  };

  // ─── Workspace selector ───────────────────────────────────────────────────────
  const WorkspaceSelector = {
    async init() {
      let workspaces = [{ id: "default", name: "default" }];
      try {
        const data = await fetchJSON("/api/v1/workspaces");
        if (Array.isArray(data.workspaces) && data.workspaces.length) {
          workspaces = data.workspaces;
        }
      } catch { /* endpoint may not exist yet; graceful fallback */ }

      const cur = Workspace.get();
      const select = document.createElement("select");
      select.id = "workspace-select";
      select.className = "workspace-select";
      select.title = "Active workspace";
      select.innerHTML = workspaces.map(ws =>
        `<option value="${escapeHTML(ws.id)}"${ws.id === cur ? " selected" : ""}>${escapeHTML(ws.name || ws.id)}</option>`
      ).join("");

      // Fallback: make sure current workspace is represented
      if (!workspaces.find(ws => ws.id === cur)) {
        const opt = document.createElement("option");
        opt.value = cur;
        opt.textContent = cur;
        opt.selected = true;
        select.prepend(opt);
      }

      select.addEventListener("change", () => {
        Workspace.set(select.value);
        App.route(); // re-render with new workspace context
      });

      $(".actions").prepend(select);
    },
  };

  // ─── Main App ─────────────────────────────────────────────────────────────────
  const App = {
    cache: { experiments: null, runs: {}, runData: {} },

    init() {
      // Theme
      const stored = localStorage.getItem("litemlflow.theme");
      if (stored) document.documentElement.setAttribute("data-theme", stored);
      $("#theme-toggle").addEventListener("click", () => {
        const cur = document.documentElement.getAttribute("data-theme");
        const next = cur === "dark" ? "light" : "dark";
        document.documentElement.setAttribute("data-theme", next);
        localStorage.setItem("litemlflow.theme", next);
      });

      // Version pill
      fetch("/version").then(r => r.json()).then(v => {
        $("#version").textContent = v.version || "dev";
      });

      // Workspace selector
      WorkspaceSelector.init();

      // Keyboard shortcuts
      Shortcuts.init();

      // Hash-based router
      window.addEventListener("hashchange", () => this.route());
      this.route();
    },

    route() {
      const hash = (window.location.hash || "#/experiments").slice(1);
      const main = $("#app");
      main.innerHTML = '<div class="loading">Loading…</div>';

      // Remove stale bulk bar
      const bar = $("#bulk-action-bar");
      if (bar) bar.remove();

      const expMatch      = hash.match(/^\/experiments\/(\d+)$/);
      const runMatch      = hash.match(/^\/experiments\/(\d+)\/runs\/([0-9a-f]+)$/);
      const cmpMatch      = hash.match(/^\/experiments\/(\d+)\/compare/);
      const promptMatch   = hash.match(/^\/prompts\/(.+)$/);
      const wsMembersMatch = hash.match(/^\/workspaces\/([^/]+)\/members$/);

      if (runMatch)        return this.renderRun(parseInt(runMatch[1], 10), runMatch[2]);
      if (cmpMatch)        return this.renderCompare(parseInt(cmpMatch[1], 10));
      if (expMatch)        return this.renderExperiment(parseInt(expMatch[1], 10));
      if (promptMatch)     return this.renderPromptDetail(promptMatch[1]);
      if (wsMembersMatch)  return this.renderWorkspaceMembers(wsMembersMatch[1]);
      if (hash.startsWith("/workspaces")) return this.renderWorkspaces();
      if (hash.startsWith("/prompts")) return this.renderPrompts();
      if (hash.startsWith("/about"))   return this.renderAbout();
      return this.renderExperiments();
    },

    // ── Experiments list ─────────────────────────────────────────────────────
    async renderExperiments() {
      const main = $("#app");
      try {
        const [data, projectsRes] = await Promise.all([
          fetchJSON("/api/2.0/mlflow/experiments/search?max_results=1000"),
          // Projects endpoint is best-effort — older servers won't have it.
          fetchJSON("/api/v1/projects").catch(() => ({ projects: [] })),
        ]);
        const exps = (data.experiments || []).filter(e => e.lifecycle_stage === "active");
        if (exps.length === 0) {
          main.innerHTML = `
            <h1>Experiments</h1>
            <div class="empty card">No experiments yet. Log one from Python:<br/>
              <pre style="text-align:left">import mlflow
mlflow.set_tracking_uri("${location.origin}")
mlflow.create_experiment("first-experiment")
mlflow.log_metric("loss", 0.42)</pre>
            </div>`;
          return;
        }

        // Pull lmf.project tag out of each experiment for grouping.
        const PROJ_KEY = "lmf.project";
        const projectOf = e => ((e.tags || []).find(t => t.key === PROJ_KEY) || {}).value || "";
        const projects = projectsRes.projects || [];

        // View mode is preserved in localStorage so the user's last choice survives reload.
        const groupKey = "litemlflow.experiments.groupBy";
        let groupBy = localStorage.getItem(groupKey) || (projects.length > 0 ? "project" : "flat");

        const renderRows = (filterFn) => exps.filter(filterFn || (() => true)).map((e, i) => {
          const proj = projectOf(e);
          const otherTags = (e.tags || []).filter(t => t.key !== PROJ_KEY);
          return `
            <tr data-row-index="${i}" onclick="location.hash='#/experiments/${e.experiment_id}'">
              <td class="mono">${e.experiment_id}</td>
              <td><a href="#/experiments/${e.experiment_id}">${escapeHTML(e.name)}</a>${proj ? ` <span class="proj-pill" title="Project">${escapeHTML(proj)}</span>` : ""}</td>
              <td>${formatTime(e.creation_time)}</td>
              <td>${formatTime(e.last_update_time)}</td>
              <td>${otherTags.map(t => `<span class="tag">${escapeHTML(t.key)}=${escapeHTML(t.value)}</span>`).join(" ")}</td>
            </tr>`;
        }).join("");

        // Group sections (one <table> per project) when groupBy === 'project'.
        const renderGrouped = () => {
          // Build buckets from the live exps array (don't trust counts from the
          // /projects endpoint — workspace selector can change between requests).
          const buckets = new Map();
          exps.forEach(e => {
            const p = projectOf(e);
            if (!buckets.has(p)) buckets.set(p, []);
            buckets.get(p).push(e);
          });
          // Stable order: real projects first (alphabetical), then "no project" last.
          const names = Array.from(buckets.keys()).sort((a, b) => {
            if (a === "" && b !== "") return 1;
            if (b === "" && a !== "") return -1;
            return a.localeCompare(b);
          });
          return names.map(name => {
            const list = buckets.get(name);
            const heading = name === "" ? "No project" : name;
            const rows = list.map((e, i) => {
              const otherTags = (e.tags || []).filter(t => t.key !== PROJ_KEY);
              return `
                <tr data-row-index="${i}" onclick="location.hash='#/experiments/${e.experiment_id}'">
                  <td class="mono">${e.experiment_id}</td>
                  <td><a href="#/experiments/${e.experiment_id}">${escapeHTML(e.name)}</a></td>
                  <td>${formatTime(e.creation_time)}</td>
                  <td>${formatTime(e.last_update_time)}</td>
                  <td>${otherTags.map(t => `<span class="tag">${escapeHTML(t.key)}=${escapeHTML(t.value)}</span>`).join(" ")}</td>
                </tr>`;
            }).join("");
            return `
              <h2 class="proj-heading">${escapeHTML(heading)} <span class="proj-count">${list.length}</span></h2>
              <div class="card" style="padding:0; margin-bottom:18px;">
                <table>
                  <thead><tr><th style="width:60px">ID</th><th>Name</th><th>Created</th><th>Updated</th><th>Tags</th></tr></thead>
                  <tbody>${rows}</tbody>
                </table>
              </div>`;
          }).join("");
        };

        main.innerHTML = `
          <div class="toolbar">
            <h1 style="margin:0">Experiments</h1>
            <div class="proj-toggle" role="tablist" aria-label="Group by">
              <button data-mode="project" class="${groupBy === "project" ? "active" : ""}" title="Group by project">By project</button>
              <button data-mode="flat" class="${groupBy === "flat" ? "active" : ""}" title="Flat list">Flat</button>
            </div>
            <input type="search" id="exp-search" placeholder="Filter by name…" />
          </div>
          <div id="exp-list">
            ${groupBy === "project" ? renderGrouped() : `
              <div class="card" style="padding:0">
                <table>
                  <thead><tr><th>ID</th><th>Name</th><th>Created</th><th>Updated</th><th>Tags</th></tr></thead>
                  <tbody id="exp-tbody">${renderRows()}</tbody>
                </table>
              </div>`}
          </div>`;

        // Group toggle
        $$(".proj-toggle button", main).forEach(btn => {
          btn.addEventListener("click", () => {
            const mode = btn.dataset.mode;
            if (mode === groupBy) return;
            localStorage.setItem(groupKey, mode);
            App.renderExperiments();  // re-render
          });
        });

        // Live filter
        $("#exp-search").addEventListener("input", function () {
          const q = this.value.toLowerCase();
          $$("tr[data-row-index]", main).forEach(tr => {
            const name = tr.querySelector("td:nth-child(2)").textContent.toLowerCase();
            tr.style.display = (!q || name.includes(q)) ? "" : "none";
          });
          // Also hide group headings whose siblings are all hidden.
          $$(".proj-heading", main).forEach(h => {
            const card = h.nextElementSibling;
            if (!card) return;
            const visible = $$("tr[data-row-index]", card).some(tr => tr.style.display !== "none");
            h.style.display = visible ? "" : "none";
            card.style.display = visible ? "" : "none";
          });
        });
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    // ── Experiment detail (runs table + bulk select) ──────────────────────────
    async renderExperiment(expID) {
      const main = $("#app");
      BulkSelect.reset(expID);
      const STARRED_TAG = "lmf.starred";
      const starredFirstKey = "litemlflow.runs.starredFirst";
      let starredFirst = localStorage.getItem(starredFirstKey) !== "false";

      try {
        const [exp, runsRes] = await Promise.all([
          fetchJSON(`/api/2.0/mlflow/experiments/get?experiment_id=${expID}`),
          fetchJSON(`/api/2.0/mlflow/runs/search`, {
            method: "POST",
            body: JSON.stringify({ experiment_ids: [String(expID)], max_results: 200 }),
          }),
        ]);
        const e = exp.experiment;
        let runs = runsRes.runs || [];

        const isStarred = r => (r.data && r.data.tags || []).some(t => (t.key || t.Key) === STARRED_TAG && (t.value || t.Value) === "true");

        const sortRuns = list => {
          if (!starredFirst) return list;
          return [...list].sort((a, b) => {
            const as = isStarred(a) ? 0 : 1;
            const bs = isStarred(b) ? 0 : 1;
            return as - bs;
          });
        };

        const renderRows = (list) => sortRuns(list).map((r, i) => {
          const info = r.info, data = r.data;
          const metrics = (data.metrics || []).map(m => `${escapeHTML(m.key)}=${m.value.toPrecision(4)}`).join(", ");
          const checked = BulkSelect.has(info.run_id) ? "checked" : "";
          const starred = isStarred(r);
          return `
            <tr data-row-index="${i}" data-run-id="${info.run_id}">
              <td class="bulk-col"><input type="checkbox" class="bulk-cb" data-run-id="${info.run_id}" ${checked} /></td>
              <td class="mono" onclick="location.hash='#/experiments/${expID}/runs/${info.run_id}'">${info.run_id.slice(0, 8)}</td>
              <td onclick="location.hash='#/experiments/${expID}/runs/${info.run_id}'">
                ${starred ? '<span class="star-icon" title="Starred">&#9733;</span> ' : ""}${escapeHTML(info.run_name || "—")}
              </td>
              <td onclick="location.hash='#/experiments/${expID}/runs/${info.run_id}'"><span class="status-pill status-${info.status}">${info.status}</span></td>
              <td onclick="location.hash='#/experiments/${expID}/runs/${info.run_id}'">${formatTime(info.start_time)}</td>
              <td onclick="location.hash='#/experiments/${expID}/runs/${info.run_id}'">${info.end_time ? formatDuration(info.end_time - info.start_time) : "—"}</td>
              <td class="mono" onclick="location.hash='#/experiments/${expID}/runs/${info.run_id}'">${escapeHTML(metrics)}</td>
            </tr>`;
        }).join("");

        main.innerHTML = `
          <div class="crumbs"><a href="#/experiments">Experiments</a> / ${escapeHTML(e.name)}</div>
          <h1>${escapeHTML(e.name)}</h1>
          <div class="card">
            <div class="kv-table">
              <table>
                <tr><td>ID</td><td class="mono">${e.experiment_id}</td></tr>
                <tr><td>Artifact location</td><td class="mono">${escapeHTML(e.artifact_location)}</td></tr>
                <tr><td>Created</td><td>${formatTime(e.creation_time)}</td></tr>
                <tr><td>Lifecycle</td><td>${e.lifecycle_stage}</td></tr>
              </table>
            </div>
          </div>
          <div style="display:flex;align-items:center;gap:12px;margin-top:16px;margin-bottom:4px">
            <h2 style="margin:0">Runs (${runs.length})</h2>
            <label style="font-size:12px;color:var(--fg-muted);display:flex;align-items:center;gap:4px;cursor:pointer">
              <input type="checkbox" id="starred-first-cb" ${starredFirst ? "checked" : ""}/>
              Starred first
            </label>
          </div>
          <div class="card" style="padding:0">
            <table>
              <thead>
                <tr>
                  <th class="bulk-col"><input type="checkbox" id="bulk-all" title="Select all" /></th>
                  <th>ID</th><th>Name</th><th>Status</th><th>Started</th><th>Duration</th><th>Metrics</th>
                </tr>
              </thead>
              <tbody id="runs-tbody">${renderRows(runs) || `<tr><td colspan="7" class="empty">No runs yet.</td></tr>`}</tbody>
            </table>
          </div>`;

        // Starred-first toggle
        const sfCb = $("#starred-first-cb");
        sfCb.addEventListener("change", () => {
          starredFirst = sfCb.checked;
          localStorage.setItem(starredFirstKey, String(starredFirst));
          const tbody = $("#runs-tbody");
          if (tbody) tbody.innerHTML = renderRows(runs) || `<tr><td colspan="7" class="empty">No runs yet.</td></tr>`;
          // Rewire checkboxes
          $$(".bulk-cb", main).forEach(cb => {
            cb.checked = BulkSelect.has(cb.dataset.runId);
            cb.addEventListener("change", () => {
              BulkSelect.toggle(cb.dataset.runId);
              this._updateBulkBar(expID, runs);
            });
          });
        });

        // Checkbox wiring
        $$(".bulk-cb", main).forEach(cb => {
          cb.addEventListener("change", () => {
            BulkSelect.toggle(cb.dataset.runId);
            this._updateBulkBar(expID, runs);
          });
        });
        // select-all
        const allCb = $("#bulk-all");
        allCb.addEventListener("change", () => {
          const toCheck = allCb.checked;
          $$(".bulk-cb", main).forEach(cb => {
            cb.checked = toCheck;
            const id = cb.dataset.runId;
            if (toCheck) BulkSelect._checked.add(id);
            else BulkSelect._checked.delete(id);
          });
          this._updateBulkBar(expID, runs);
        });

        // Restore checked state (survives re-render)
        $$(".bulk-cb", main).forEach(cb => {
          if (BulkSelect.has(cb.dataset.runId)) cb.checked = true;
        });
        this._updateBulkBar(expID, runs);

      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    _updateBulkBar(expID, runs) {
      let bar = $("#bulk-action-bar");
      if (BulkSelect.size() < 1) {
        if (bar) bar.remove();
        return;
      }
      if (!bar) {
        bar = document.createElement("div");
        bar.id = "bulk-action-bar";
        bar.className = "bulk-action-bar";
        document.body.appendChild(bar);
      }
      const n = BulkSelect.size();
      bar.innerHTML = `
        <span class="bulk-count">${n} run${n === 1 ? "" : "s"} selected</span>
        <button id="bulk-compare">Compare</button>
        <button id="bulk-delete" class="btn-danger">Delete</button>
        <button id="bulk-export">Export JSON</button>
        <button id="bulk-tags">Tags</button>
        <button id="bulk-clear" class="btn-ghost">✕ Clear</button>`;

      $("#bulk-compare").onclick = () => {
        const ids = BulkSelect.list().join(",");
        location.hash = `#/experiments/${expID}/compare?runs=${ids}`;
      };

      $("#bulk-delete").onclick = async () => {
        if (!confirm(`Delete ${n} run(s)? This cannot be undone.`)) return;
        const ids = BulkSelect.list();
        await Promise.all(ids.map(id =>
          fetchJSON("/api/2.0/mlflow/runs/delete", { method: "POST", body: JSON.stringify({ run_id: id }) })
            .catch(() => {})
        ));
        BulkSelect.clear();
        this.renderExperiment(expID);
      };

      $("#bulk-export").onclick = async () => {
        const ids = BulkSelect.list();
        const results = await Promise.all(ids.map(id =>
          fetchJSON(`/api/2.0/mlflow/runs/get?run_id=${id}`).catch(err => ({ error: String(err), run_id: id }))
        ));
        const blob = new Blob([JSON.stringify(results, null, 2)], { type: "application/json" });
        const a = document.createElement("a");
        a.href = URL.createObjectURL(blob);
        a.download = `runs-export-${Date.now()}.json`;
        a.click();
        URL.revokeObjectURL(a.href);
      };

      $("#bulk-tags").onclick = () => {
        this._openBulkTagModal(expID, runs);
      };

      $("#bulk-clear").onclick = () => {
        BulkSelect.clear();
        $$(".bulk-cb", $("#app")).forEach(cb => { cb.checked = false; });
        const allCb = $("#bulk-all");
        if (allCb) allCb.checked = false;
        this._updateBulkBar(expID, runs);
      };
    },

    _openBulkTagModal(expID, runs) {
      // Remove any existing modal
      const old = $("#bulk-tag-modal-overlay");
      if (old) old.remove();

      const overlay = document.createElement("div");
      overlay.id = "bulk-tag-modal-overlay";
      overlay.className = "shortcut-overlay";
      overlay.innerHTML = `
        <div class="shortcut-modal" style="min-width:360px">
          <div class="shortcut-modal-header">
            <strong>Bulk Tag Editor</strong>
            <button class="modal-close" id="bulk-tag-close">✕</button>
          </div>
          <div style="margin-bottom:12px">
            <label style="font-size:12px;color:var(--fg-muted)">Action</label><br/>
            <select id="bulk-tag-action" style="width:100%;margin-top:4px;padding:6px;border:1px solid var(--border);border-radius:6px;background:var(--bg-card);color:var(--fg);font:inherit">
              <option value="add">Add / update tag</option>
              <option value="remove">Remove tag</option>
              <option value="project">Replace project (lmf.project)</option>
            </select>
          </div>
          <div id="bulk-tag-key-row" style="margin-bottom:12px">
            <label style="font-size:12px;color:var(--fg-muted)">Key</label><br/>
            <input id="bulk-tag-key" type="text" placeholder="tag key…" style="width:100%;margin-top:4px" />
          </div>
          <div id="bulk-tag-val-row" style="margin-bottom:16px">
            <label style="font-size:12px;color:var(--fg-muted)">Value</label><br/>
            <input id="bulk-tag-val" type="text" placeholder="tag value…" style="width:100%;margin-top:4px" />
          </div>
          <div id="bulk-tag-progress" style="font-size:12px;color:var(--fg-muted);min-height:18px;margin-bottom:8px"></div>
          <div style="display:flex;gap:8px;justify-content:flex-end">
            <button id="bulk-tag-cancel">Cancel</button>
            <button id="bulk-tag-apply" style="background:var(--accent);color:#fff;border-color:var(--accent)">Apply to ${BulkSelect.size()} runs</button>
          </div>
        </div>`;
      document.body.appendChild(overlay);
      overlay.classList.add("open");

      const actionSel = $("#bulk-tag-action");
      const keyRow = $("#bulk-tag-key-row");
      const valRow = $("#bulk-tag-val-row");
      const keyInp = $("#bulk-tag-key");
      const valInp = $("#bulk-tag-val");
      const progress = $("#bulk-tag-progress");

      const updateVisibility = () => {
        const action = actionSel.value;
        keyRow.style.display = action === "project" ? "none" : "";
        valRow.style.display = action === "remove" ? "none" : "";
      };
      actionSel.addEventListener("change", updateVisibility);
      updateVisibility();

      const close = () => overlay.remove();
      $("#bulk-tag-close").onclick = close;
      $("#bulk-tag-cancel").onclick = close;
      overlay.addEventListener("click", ev => { if (ev.target === overlay) close(); });

      $("#bulk-tag-apply").onclick = async () => {
        const action = actionSel.value;
        const key = action === "project" ? "lmf.project" : keyInp.value.trim();
        const value = valInp.value.trim();

        if (action !== "project" && action !== "remove" && key === "") {
          keyInp.style.borderColor = "var(--error)";
          setTimeout(() => { keyInp.style.borderColor = ""; }, 1500);
          return;
        }

        const ids = BulkSelect.list();
        let done = 0;
        progress.textContent = `Applying to 0 of ${ids.length} runs…`;

        for (const id of ids) {
          try {
            if (action === "remove") {
              await fetchJSON("/api/2.0/mlflow/runs/delete-tag", {
                method: "POST",
                body: JSON.stringify({ run_id: id, key }),
              });
            } else {
              await fetchJSON("/api/2.0/mlflow/runs/set-tag", {
                method: "POST",
                body: JSON.stringify({ run_id: id, key, value }),
              });
            }
          } catch { /* skip failed */ }
          done++;
          progress.textContent = `Applied to ${done} of ${ids.length} runs…`;
        }

        progress.textContent = `Done — applied to ${done} of ${ids.length} runs.`;
        setTimeout(() => {
          close();
          BulkSelect.clear();
          App.renderExperiment(expID);
        }, 800);
      };
    },

    // ── Compare view ─────────────────────────────────────────────────────────
    async renderCompare(expID) {
      const main = $("#app");
      const params = new URLSearchParams(location.hash.split("?")[1] || "");
      const ids = (params.get("runs") || "").split(",").filter(Boolean);
      if (ids.length < 2) {
        main.innerHTML = `<div class="empty">Select at least 2 runs to compare.</div>`;
        return;
      }
      try {
        const runDatas = await Promise.all(ids.map(id =>
          fetchJSON(`/api/v1/runs/${id}/data`).catch(() => null)
        ));
        const valid = runDatas.filter(Boolean);
        if (!valid.length) { main.innerHTML = `<div class="empty">Could not load run data.</div>`; return; }

        // Params comparison
        const allParamKeys = [...new Set(valid.flatMap(r => (r.params || []).map(p => p.Key || p.key)))].sort();
        const paramRows = allParamKeys.map(k => {
          const vals = valid.map(r => {
            const found = (r.params || []).find(p => (p.Key || p.key) === k);
            return found ? (found.Value || found.value) : "—";
          });
          const differ = new Set(vals).size > 1;
          return `<tr class="${differ ? "diff-row" : ""}"><td class="mono">${escapeHTML(k)}</td>${vals.map(v => `<td class="mono">${escapeHTML(v)}</td>`).join("")}</tr>`;
        }).join("");

        // Metrics — small sparklines per common metric
        const allMetricKeys = [...new Set(valid.flatMap(r => (r.metrics || []).map(m => m.Key || m.key)))].sort();
        let charts = "";
        for (const mk of allMetricKeys) {
          const perRun = await Promise.all(valid.map(r =>
            fetchJSON(`/api/2.0/mlflow/metrics/get-history?run_id=${r.id}&metric_key=${encodeURIComponent(mk)}&downsample=200`)
              .then(d => d.metrics || []).catch(() => [])
          ));
          const allPts = perRun.flatMap(pts => pts);
          if (!allPts.length) continue;
          const W = 600, H = 150, pad = 20;
          const colors = ["var(--accent)", "var(--success)", "var(--warning)", "var(--error)"];
          const tmin = Math.min(...allPts.map(p => p.timestamp));
          const tmax = Math.max(...allPts.map(p => p.timestamp));
          const vmin = Math.min(...allPts.map(p => p.value));
          const vmax = Math.max(...allPts.map(p => p.value));
          const tspan = Math.max(tmax - tmin, 1), vspan = Math.max(vmax - vmin, Math.abs(vmax) || 1);
          const paths = perRun.map((pts, ci) => {
            if (!pts.length) return "";
            const d = pts.map((p, i) => {
              const x = pad + ((p.timestamp - tmin) / tspan) * (W - 2 * pad);
              const y = H - pad - ((p.value - vmin) / vspan) * (H - 2 * pad);
              return (i === 0 ? "M" : "L") + x.toFixed(1) + "," + y.toFixed(1);
            }).join("");
            return `<path d="${d}" fill="none" stroke="${colors[ci % colors.length]}" stroke-width="1.5" />`;
          }).join("");

          charts += `
            <div class="card" style="padding:8px 12px">
              <strong>${escapeHTML(mk)}</strong>
              <div class="metric-chart">
                <svg width="100%" height="100%" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">${paths}</svg>
              </div>
            </div>`;
        }

        main.innerHTML = `
          <div class="crumbs"><a href="#/experiments">Experiments</a> / <a href="#/experiments/${expID}">exp ${expID}</a> / Compare</div>
          <h1>Compare Runs</h1>
          <div class="card" style="padding:0;overflow-x:auto">
            <table>
              <thead>
                <tr>
                  <th>Param</th>
                  ${valid.map(r => `<th class="mono">${escapeHTML((r.name || r.id || "").slice(0, 12))}</th>`).join("")}
                </tr>
              </thead>
              <tbody>${paramRows || `<tr><td colspan="${valid.length + 1}" class="empty">No params.</td></tr>`}</tbody>
            </table>
          </div>
          <h2>Metric history</h2>
          ${charts || `<div class="empty">No shared metrics.</div>`}`;
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed: ${escapeHTML(String(err))}</div>`;
      }
    },

    // ── Run detail ───────────────────────────────────────────────────────────
    async renderRun(expID, runID) {
      const main = $("#app");
      const STARRED_TAG = "lmf.starred";

      try {
        const [data, noteRes] = await Promise.all([
          fetchJSON(`/api/v1/runs/${runID}/data`),
          fetchJSON(`/api/v1/runs/${runID}/note`).catch(() => null),
        ]);
        const params = (data.params || []).map(p => `<tr><td>${escapeHTML(p.Key || p.key)}</td><td class="mono">${escapeHTML(p.Value || p.value)}</td></tr>`).join("");
        const allTags = data.tags || [];
        const isStarred = allTags.some(t => (t.Key || t.key) === STARRED_TAG && (t.Value || t.value) === "true");
        const visibleTags = allTags.filter(t => (t.Key || t.key) !== STARRED_TAG);
        const tags = visibleTags.map(t => `<span class="tag">${escapeHTML(t.Key || t.key)}=${escapeHTML(t.Value || t.value)}</span>`).join(" ");
        const metrics = data.metrics || [];
        const isTrace = data.kind === "trace";
        const starIcon = isStarred ? "&#9733;" : "&#9734;";

        main.innerHTML = `
          <div class="crumbs">
            <a href="#/experiments">Experiments</a> /
            <a href="#/experiments/${expID}">exp ${expID}</a> / ${data.id}
          </div>
          <h1 style="display:flex;align-items:center;gap:8px;flex-wrap:wrap">
            <button id="star-btn" class="btn-ghost star-btn" title="${isStarred ? "Unstar" : "Star"} this run" style="font-size:20px;padding:2px 6px">${starIcon}</button>
            <span id="run-name-display" class="run-name-display">${escapeHTML(data.name || "(unnamed run)")}</span>
            <button id="rename-btn" class="btn-ghost" title="Rename run" style="font-size:13px;padding:2px 6px">&#9998;</button>
            <span class="kind-pill">${data.kind}</span>
            <span class="status-pill status-${data.status}">${data.status}</span>
          </h1>
          <div class="card">
            <div class="kv-table">
              <table>
                <tr><td>Run ID</td><td class="mono">${data.id}</td></tr>
                <tr><td>Started</td><td>${formatTime(data.start_time)}</td></tr>
                <tr><td>Duration</td><td>${data.end_time ? formatDuration(data.end_time - data.start_time) : "running"}</td></tr>
                <tr><td>Artifact URI</td><td class="mono">${escapeHTML(data.artifact_uri)}</td></tr>
                <tr><td>Tags</td><td class="tag-list" id="run-tags-cell">${tags || "—"}</td></tr>
              </table>
            </div>
          </div>

          <div class="run-grid">
            <div class="card">
              <h2 style="margin-top:0">Params</h2>
              <table class="kv-table">${params || `<tr><td colspan="2" class="empty">none</td></tr>`}</table>
            </div>
            <div class="card">
              <h2 style="margin-top:0">Latest metrics</h2>
              <table class="kv-table">
                ${metrics.map(m => `<tr><td>${escapeHTML(m.Key || m.key)}</td><td class="numeric">${(m.Value ?? m.value).toPrecision(6)}</td></tr>`).join("") || `<tr><td colspan="2" class="empty">none</td></tr>`}
              </table>
            </div>
          </div>

          ${isTrace ? this.renderSpanWaterfall(data.spans || []) : await this.renderMetricCharts(data.id, metrics)}

          <div class="section" id="notes-section">
            <h2>Notes</h2>
            ${this._renderNoteCard(noteRes)}
          </div>

          <div class="section" id="artifacts-section">
            <h2>Artifacts</h2>
            <div id="artifacts-list"><span style="color:var(--fg-muted);font-size:13px">Loading…</span></div>
          </div>
        `;

        // ── Star toggle ───────────────────────────────────────────────────────
        let currentlyStarred = isStarred;
        const starBtn = $("#star-btn");
        starBtn.addEventListener("click", async () => {
          try {
            if (currentlyStarred) {
              await fetchJSON("/api/2.0/mlflow/runs/delete-tag", {
                method: "POST",
                body: JSON.stringify({ run_id: runID, key: STARRED_TAG }),
              });
              currentlyStarred = false;
            } else {
              await fetchJSON("/api/2.0/mlflow/runs/set-tag", {
                method: "POST",
                body: JSON.stringify({ run_id: runID, key: STARRED_TAG, value: "true" }),
              });
              currentlyStarred = true;
            }
            starBtn.innerHTML = currentlyStarred ? "&#9733;" : "&#9734;";
            starBtn.title = currentlyStarred ? "Unstar this run" : "Star this run";
          } catch (err) {
            alert("Failed to update star: " + err);
          }
        });

        // ── Rename ────────────────────────────────────────────────────────────
        let currentName = data.name || "";
        const nameDisplay = $("#run-name-display");
        const renameBtn = $("#rename-btn");

        const startRename = () => {
          const inp = document.createElement("input");
          inp.type = "text";
          inp.value = currentName;
          inp.className = "run-rename-input";
          inp.style.cssText = "font-size:inherit;font-weight:inherit;border:1px solid var(--accent);border-radius:4px;padding:2px 6px;min-width:200px;background:var(--bg-card);color:var(--fg)";
          nameDisplay.replaceWith(inp);
          inp.focus();
          inp.select();

          const commitRename = async () => {
            const newName = inp.value.trim();
            if (!newName) {
              inp.style.borderColor = "var(--error)";
              return;
            }
            try {
              await fetchJSON("/api/2.0/mlflow/runs/update", {
                method: "POST",
                body: JSON.stringify({ run_id: runID, run_name: newName }),
              });
              currentName = newName;
              const span = document.createElement("span");
              span.id = "run-name-display";
              span.className = "run-name-display";
              span.textContent = newName;
              inp.replaceWith(span);
            } catch (err) {
              alert("Rename failed: " + err);
              const span = document.createElement("span");
              span.id = "run-name-display";
              span.className = "run-name-display";
              span.textContent = currentName;
              inp.replaceWith(span);
            }
          };

          inp.addEventListener("keydown", e => {
            if (e.key === "Enter") { e.preventDefault(); commitRename(); }
            if (e.key === "Escape") {
              const span = document.createElement("span");
              span.id = "run-name-display";
              span.className = "run-name-display";
              span.textContent = currentName;
              inp.replaceWith(span);
            }
          });
          inp.addEventListener("blur", commitRename);
        };

        renameBtn.addEventListener("click", startRename);
        nameDisplay.addEventListener("dblclick", startRename);

        // ── Notes wiring ──────────────────────────────────────────────────────
        this._wireNoteCard(runID, noteRes);

        // ── Artifact preview (lazy) ───────────────────────────────────────────
        this._loadArtifactPreview(runID);

      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    _renderNoteCard(noteRes) {
      const hasNote = noteRes && noteRes.content;
      const md = hasNote ? renderMarkdown(noteRes.content) : "";
      const meta = hasNote
        ? `<div class="note-meta">Last edited${noteRes.updated_by ? " by " + escapeHTML(noteRes.updated_by) : ""} ${formatTime(noteRes.updated_at)}</div>`
        : "";
      return `
        <div class="card note-card" id="note-card">
          <div id="note-view-mode" ${hasNote ? "" : 'style="display:none"'}>
            <div class="note-rendered" id="note-rendered">${md}</div>
            ${meta}
            <button class="btn-ghost" id="note-edit-btn" style="margin-top:8px;font-size:12px">Edit</button>
          </div>
          <div id="note-edit-mode" ${hasNote ? 'style="display:none"' : ""}>
            <textarea id="note-textarea" rows="6" style="width:100%;font-family:var(--mono);font-size:12px;background:var(--bg-card);color:var(--fg);border:1px solid var(--border);border-radius:6px;padding:8px;resize:vertical">${hasNote ? escapeHTML(noteRes.content) : ""}</textarea>
            <div style="display:flex;gap:8px;margin-top:8px">
              <button id="note-save-btn" style="background:var(--accent);color:#fff;border-color:var(--accent)">Save</button>
              <button class="btn-ghost" id="note-cancel-btn">Cancel</button>
            </div>
          </div>
          ${!hasNote ? `<button class="btn-ghost" id="note-add-btn" style="font-size:13px">+ Add note</button>` : ""}
        </div>`;
    },

    _wireNoteCard(runID, noteRes) {
      let currentContent = (noteRes && noteRes.content) || "";

      const viewMode = $("#note-view-mode");
      const editMode = $("#note-edit-mode");
      const rendered = $("#note-rendered");
      const textarea = $("#note-textarea");
      const editBtn = $("#note-edit-btn");
      const saveBtn = $("#note-save-btn");
      const cancelBtn = $("#note-cancel-btn");
      const addBtn = $("#note-add-btn");
      const noteCard = $("#note-card");

      const showEdit = () => {
        if (viewMode) viewMode.style.display = "none";
        editMode.style.display = "";
        if (addBtn) addBtn.style.display = "none";
        textarea.value = currentContent;
        textarea.focus();
      };

      const showView = (newContent) => {
        currentContent = newContent;
        editMode.style.display = "none";
        if (newContent) {
          viewMode.style.display = "";
          if (rendered) rendered.innerHTML = renderMarkdown(newContent);
          if (addBtn) addBtn.style.display = "none";
        } else {
          if (viewMode) viewMode.style.display = "none";
          if (addBtn) addBtn.style.display = "";
        }
      };

      if (editBtn) editBtn.addEventListener("click", showEdit);
      if (addBtn) addBtn.addEventListener("click", showEdit);
      if (cancelBtn) cancelBtn.addEventListener("click", () => showView(currentContent));

      if (saveBtn) {
        saveBtn.addEventListener("click", async () => {
          const newContent = textarea.value;
          try {
            const resp = await fetchJSON(`/api/v1/runs/${runID}/note`, {
              method: "PUT",
              body: JSON.stringify({ content: newContent }),
            });
            // Re-render note card with fresh data
            const noteSection = $("#notes-section");
            if (noteSection) {
              noteSection.innerHTML = "<h2>Notes</h2>" + this._renderNoteCard(newContent ? resp : null);
              this._wireNoteCard(runID, newContent ? resp : null);
            }
          } catch (err) {
            alert("Failed to save note: " + err);
          }
        });
      }
    },

    async _loadArtifactPreview(runID) {
      const container = $("#artifacts-list");
      if (!container) return;
      try {
        const data = await fetchJSON(`/api/2.0/mlflow/artifacts/list?run_id=${runID}`);
        const files = data.files || [];
        if (!files.length) {
          container.innerHTML = `<span style="color:var(--fg-muted);font-size:13px">No artifacts.</span>`;
          return;
        }
        container.innerHTML = files.map(f => {
          const path = f.path || "";
          const size = f.file_size != null ? ` <span style="color:var(--fg-muted);font-size:11px">(${formatBytes(f.file_size)})</span>` : "";
          const ext = path.split(".").pop().toLowerCase();
          const dlURL = `/api/2.0/mlflow-artifacts/artifacts/${encodeURIComponent(runID)}/${encodeURI(path)}`;
          return `
            <details class="artifact-entry" data-path="${escapeHTML(path)}" data-ext="${escapeHTML(ext)}" data-run="${escapeHTML(runID)}">
              <summary style="cursor:pointer;font-family:var(--mono);font-size:12px;padding:4px 0">
                ${escapeHTML(path)}${size}
                <a href="${escapeHTML(dlURL)}" download="${escapeHTML(path.split("/").pop())}" onclick="event.stopPropagation()" style="margin-left:8px;font-size:11px">&#x2B07; download</a>
              </summary>
              <div class="artifact-preview-slot" style="margin-top:4px"></div>
            </details>`;
        }).join("");

        // Lazy preview — only fetch when <details> is opened.
        $$(".artifact-entry", container).forEach(det => {
          det.addEventListener("toggle", async () => {
            if (!det.open) return;
            const slot = $(".artifact-preview-slot", det);
            if (slot.dataset.loaded) return;
            slot.dataset.loaded = "1";
            const ext = det.dataset.ext;
            const path = det.dataset.path;
            const rID = det.dataset.run;
            const previewURL = `/api/2.0/mlflow-artifacts/artifacts/${encodeURIComponent(rID)}/${encodeURI(path)}`;

            if (["png", "jpg", "jpeg", "gif", "svg"].includes(ext)) {
              slot.innerHTML = `<img src="${escapeHTML(previewURL)}" alt="${escapeHTML(path)}" style="max-height:280px;max-width:100%;border-radius:4px;display:block;margin-top:4px" />`;
            } else if (["json", "txt", "md", "log", "yaml", "yml", "csv"].includes(ext)) {
              try {
                // Fetch at most 4 KB
                const resp = await fetch(previewURL, { headers: { Range: "bytes=0-4095" } });
                const text = await resp.text();
                const snippet = text.slice(0, 4096);

                if (ext === "json") {
                  let pretty;
                  try { pretty = JSON.stringify(JSON.parse(snippet), null, 2); } catch { pretty = snippet; }
                  slot.innerHTML = `<pre class="artifact-pre">${escapeHTML(pretty)}</pre>`;
                } else if (ext === "csv") {
                  slot.innerHTML = renderCSVPreview(snippet);
                } else {
                  slot.innerHTML = `<pre class="artifact-pre">${escapeHTML(snippet)}</pre>`;
                }
              } catch (e) {
                slot.innerHTML = `<span style="color:var(--fg-muted);font-size:11px">Preview unavailable.</span>`;
              }
            } else {
              slot.innerHTML = `<span style="color:var(--fg-muted);font-size:11px">No preview for this file type.</span>`;
            }
          });
        });
      } catch {
        container.innerHTML = `<span style="color:var(--fg-muted);font-size:13px">Could not list artifacts.</span>`;
      }
    },

    async renderMetricCharts(runID, metrics) {
      if (!metrics || metrics.length === 0) return "";
      const charts = [];
      for (const m of metrics) {
        const key = m.Key || m.key;
        const hist = await fetchJSON(
          `/api/2.0/mlflow/metrics/get-history?run_id=${runID}&metric_key=${encodeURIComponent(key)}&downsample=1000`
        );
        charts.push(simpleChart(key, hist.metrics || [], hist.downsampled_from));
      }
      return `<h2>Metric history</h2>${charts.join("")}`;
    },

    renderSpanWaterfall(spans) {
      if (!spans.length) return "";
      const t0 = Math.min(...spans.map(s => s.start_time_ns));
      const t1 = Math.max(...spans.map(s => s.end_time_ns || s.start_time_ns));
      const span = Math.max(t1 - t0, 1);
      const rows = spans.map(s => {
        const start = ((s.start_time_ns - t0) / span) * 100;
        const width = (((s.end_time_ns || s.start_time_ns) - s.start_time_ns) / span) * 100;
        return `
          <div class="span-row">
            <div>
              <strong>${escapeHTML(s.name)}</strong>
              ${s.span_kind ? `<span class="kind-pill">${s.span_kind}</span>` : ""}
              <div class="span-bar" style="margin-left:${start.toFixed(2)}%; width:${Math.max(width, 0.3).toFixed(2)}%"></div>
            </div>
            <div class="mono">${formatNS((s.end_time_ns || s.start_time_ns) - s.start_time_ns)}</div>
          </div>`;
      }).join("");
      return `<h2>Trace waterfall</h2><div class="card span-tree">${rows}</div>`;
    },

    // ── Prompts list ─────────────────────────────────────────────────────────
    async renderPrompts() {
      const main = $("#app");

      // Load known prompt names from localStorage
      const stored = localStorage.getItem("litemlflow.knownPrompts");
      let known = [];
      try { known = JSON.parse(stored) || []; } catch {}
      if (!Array.isArray(known)) known = [];

      // Probe each known name
      const results = await Promise.allSettled(
        known.map(name =>
          fetchJSON(`/api/v1/prompts/${encodeURIComponent(name)}`)
            .then(data => ({ name, data }))
        )
      );
      const live = results
        .filter(r => r.status === "fulfilled")
        .map(r => r.value);

      // Remove names that 404'd from registry
      const liveNames = new Set(live.map(l => l.name));
      const pruned = known.filter(n => liveNames.has(n));
      if (pruned.length !== known.length) {
        localStorage.setItem("litemlflow.knownPrompts", JSON.stringify(pruned));
      }

      const rows = live.map((l, i) => `
        <tr data-row-index="${i}" onclick="location.hash='#/prompts/${encodeURIComponent(l.name)}'">
          <td><a href="#/prompts/${encodeURIComponent(l.name)}">${escapeHTML(l.name)}</a></td>
          <td class="mono">${escapeHTML(l.data.latest_version || l.data.version || "—")}</td>
          <td>${escapeHTML(l.data.description || "—")}</td>
        </tr>`).join("");

      main.innerHTML = `
        <h1>Prompts</h1>
        <div class="card" style="margin-bottom:16px;background:var(--bg-alt)">
          <p style="margin:0 0 8px;color:var(--fg-muted);font-size:12px">
            v0.4 limitation: there is no list-all endpoint yet. The UI probes names you've registered locally.
            A <code>GET /api/v1/prompts</code> list endpoint will land in v0.5.
          </p>
          <div style="display:flex;gap:8px;align-items:center">
            <input type="text" id="add-prompt-input" placeholder="Prompt name to add…" style="flex:1" />
            <button id="add-prompt-btn" style="white-space:nowrap">+ Add</button>
          </div>
        </div>
        ${live.length ? `
        <div class="card" style="padding:0">
          <table>
            <thead><tr><th>Name</th><th>Latest version</th><th>Description</th></tr></thead>
            <tbody>${rows}</tbody>
          </table>
        </div>` : `<div class="empty card">No prompts registered yet. Add one above.</div>`}`;

      $("#add-prompt-btn").addEventListener("click", async () => {
        const input = $("#add-prompt-input");
        const name = input.value.trim();
        if (!name) return;
        try {
          await fetchJSON(`/api/v1/prompts/${encodeURIComponent(name)}`);
          // Success — register and navigate
          this._registerPrompt(name);
          input.value = "";
          location.hash = `#/prompts/${encodeURIComponent(name)}`;
        } catch {
          input.style.borderColor = "var(--error)";
          setTimeout(() => { input.style.borderColor = ""; }, 1500);
        }
      });

      $("#add-prompt-input").addEventListener("keydown", e => {
        if (e.key === "Enter") $("#add-prompt-btn").click();
      });
    },

    _registerPrompt(name) {
      const stored = localStorage.getItem("litemlflow.knownPrompts");
      let known = [];
      try { known = JSON.parse(stored) || []; } catch {}
      if (!Array.isArray(known)) known = [];
      if (!known.includes(name)) {
        known.push(name);
        localStorage.setItem("litemlflow.knownPrompts", JSON.stringify(known));
      }
    },

    // ── Prompt detail ────────────────────────────────────────────────────────
    async renderPromptDetail(name) {
      const main = $("#app");
      this._registerPrompt(name);
      try {
        const [promptData, versionsData] = await Promise.all([
          fetchJSON(`/api/v1/prompts/${encodeURIComponent(name)}`),
          fetchJSON(`/api/v1/prompts/${encodeURIComponent(name)}/versions`).catch(() => ({ versions: [] })),
        ]);
        const versions = versionsData.versions || [];

        // Resolve aliases for "production" and "candidate"
        const aliases = {};
        await Promise.all(["production", "candidate"].map(async alias => {
          try {
            const d = await fetchJSON(`/api/v1/prompts/${encodeURIComponent(name)}/aliases/${alias}`);
            aliases[alias] = d.version || null;
          } catch { aliases[alias] = null; }
        }));

        const vOpts = versions.map(v => `<option value="${escapeHTML(v.version)}">${escapeHTML(v.version)}</option>`).join("");
        const aliasBadges = Object.entries(aliases)
          .filter(([, v]) => v != null)
          .map(([a, v]) => `<span class="tag">${escapeHTML(a)}→v${escapeHTML(String(v))}</span>`)
          .join(" ");

        main.innerHTML = `
          <div class="crumbs"><a href="#/prompts">Prompts</a> / ${escapeHTML(name)}</div>
          <h1>${escapeHTML(name)}</h1>
          ${aliasBadges ? `<div class="tag-list" style="margin-bottom:12px">${aliasBadges}</div>` : ""}
          ${promptData.description ? `<p style="color:var(--fg-muted)">${escapeHTML(promptData.description)}</p>` : ""}

          <h2>Version history (${versions.length})</h2>
          <div class="card" style="padding:0;margin-bottom:16px">
            <table>
              <thead><tr><th>Version</th><th>Created</th><th>Author</th><th>Aliases</th></tr></thead>
              <tbody>
                ${versions.map(v => {
                  const vAliases = Object.entries(aliases).filter(([, vv]) => String(vv) === String(v.version)).map(([a]) => `<span class="tag">${escapeHTML(a)}</span>`).join(" ");
                  return `<tr>
                    <td class="mono">v${escapeHTML(String(v.version))}</td>
                    <td>${formatTime(v.creation_timestamp || v.created_at)}</td>
                    <td>${escapeHTML(v.user_id || v.author || "—")}</td>
                    <td>${vAliases || "—"}</td>
                  </tr>`;
                }).join("") || `<tr><td colspan="4" class="empty">No versions yet.</td></tr>`}
              </tbody>
            </table>
          </div>

          ${versions.length >= 2 ? `
          <h2>Diff</h2>
          <div class="toolbar" style="margin-bottom:8px">
            <label>Base: <select id="diff-base">${vOpts}</select></label>
            <label style="margin-left:16px">Compare: <select id="diff-cmp">${vOpts}</select></label>
            <button id="diff-btn" style="margin-left:8px">Show diff</button>
          </div>
          <div id="diff-output" class="card diff-block"></div>
          ` : ""}`;

        if (versions.length >= 2) {
          // Pre-select base = first, cmp = second
          const selBase = $("#diff-base");
          const selCmp  = $("#diff-cmp");
          if (versions.length >= 2) {
            selBase.value = versions[0].version;
            selCmp.value  = versions[1].version;
          }

          const showDiff = async () => {
            const bv = selBase.value, cv = selCmp.value;
            if (bv === cv) {
              $("#diff-output").innerHTML = `<div class="empty">Select two different versions.</div>`;
              return;
            }
            try {
              const [bd, cd] = await Promise.all([
                fetchJSON(`/api/v1/prompts/${encodeURIComponent(name)}/versions`).then(d => {
                  const v = (d.versions || []).find(v => String(v.version) === String(bv));
                  return v ? (v.content || v.template || "") : "";
                }),
                fetchJSON(`/api/v1/prompts/${encodeURIComponent(name)}/versions`).then(d => {
                  const v = (d.versions || []).find(v => String(v.version) === String(cv));
                  return v ? (v.content || v.template || "") : "";
                }),
              ]);
              $("#diff-output").innerHTML = renderLineDiff(bd, cd);
            } catch (err) {
              $("#diff-output").innerHTML = `<div class="empty">Diff error: ${escapeHTML(String(err))}</div>`;
            }
          };

          $("#diff-btn").addEventListener("click", showDiff);
        }
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load prompt: ${escapeHTML(String(err))}</div>`;
      }
    },

    // ── Workspaces list ──────────────────────────────────────────────────────
    async renderWorkspaces() {
      const main = $("#app");
      try {
        const data = await fetchJSON("/api/v1/workspaces");
        const workspaces = data.workspaces || [];

        const rows = workspaces.map((ws, i) => `
          <tr data-row-index="${i}">
            <td class="mono">${escapeHTML(ws.id)}</td>
            <td>${escapeHTML(ws.name || ws.id)}</td>
            <td>${escapeHTML(ws.description || "—")}</td>
            <td>${formatTime(ws.created_at || ws.creation_time)}</td>
            <td>
              <a href="#/workspaces/${encodeURIComponent(ws.id)}/members" class="btn-link">Manage members</a>
            </td>
          </tr>`).join("");

        main.innerHTML = `
          <div class="toolbar">
            <h1 style="margin:0">Workspaces</h1>
          </div>
          <div class="card" style="padding:0">
            <table>
              <thead>
                <tr><th>ID</th><th>Name</th><th>Description</th><th>Created</th><th>Actions</th></tr>
              </thead>
              <tbody>${rows || `<tr><td colspan="5" class="empty">No workspaces found.</td></tr>`}</tbody>
            </table>
          </div>`;
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load workspaces: ${escapeHTML(String(err))}</div>`;
      }
    },

    // ── Workspace members ────────────────────────────────────────────────────
    async renderWorkspaceMembers(wsID) {
      const main = $("#app");
      try {
        const [wsData, membersData] = await Promise.all([
          fetchJSON(`/api/v1/workspaces/${encodeURIComponent(wsID)}`),
          fetchJSON(`/api/v1/workspaces/${encodeURIComponent(wsID)}/members`),
        ]);
        const ws = wsData;
        const members = membersData.members || [];
        this._renderWorkspaceMembersPage(main, wsID, ws, members);
      } catch (err) {
        const status = String(err);
        if (status.includes("403")) {
          main.innerHTML = `
            <div class="crumbs"><a href="#/workspaces">Workspaces</a> / ${escapeHTML(wsID)} / Members</div>
            <div class="empty card" style="margin-top:24px">
              You must be an <strong>admin</strong> of workspace
              <code>${escapeHTML(wsID)}</code> to manage its members.
            </div>`;
          return;
        }
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(status)}</div>`;
      }
    },

    _renderWorkspaceMembersPage(main, wsID, ws, members) {
      const roles = ["viewer", "editor", "admin"];

      const memberRows = members.map(m => `
        <tr data-member-id="${escapeHTML(m.user_id)}">
          <td class="mono">${escapeHTML(m.user_id)}</td>
          <td>
            <select class="member-role-select" data-user-id="${escapeHTML(m.user_id)}">
              ${roles.map(r => `<option value="${r}"${r === m.role ? " selected" : ""}>${r}</option>`).join("")}
            </select>
          </td>
          <td>
            <button class="member-remove-btn btn-danger" data-user-id="${escapeHTML(m.user_id)}">Remove</button>
          </td>
        </tr>`).join("");

      main.innerHTML = `
        <div class="crumbs"><a href="#/workspaces">Workspaces</a> / ${escapeHTML(wsID)} / Members</div>
        <h1>${escapeHTML(ws.name || wsID)} — Members</h1>
        ${ws.description ? `<p style="color:var(--fg-muted);margin:-8px 0 16px">${escapeHTML(ws.description)}</p>` : ""}

        <div class="card" style="padding:0;margin-bottom:20px">
          <table>
            <thead><tr><th>User ID</th><th>Role</th><th>Actions</th></tr></thead>
            <tbody id="members-tbody">
              ${memberRows || `<tr><td colspan="3" class="empty">No members yet.</td></tr>`}
            </tbody>
          </table>
        </div>

        <div class="card" style="margin-bottom:20px">
          <h2 style="margin-top:0">Add member</h2>
          <div class="toolbar" style="flex-wrap:wrap;gap:8px">
            <input type="text" id="new-user-id" placeholder="User ID…" style="flex:1;min-width:140px" />
            <select id="new-user-role" style="padding:6px 10px;border:1px solid var(--border);border-radius:6px;background:var(--bg-card);color:var(--fg);font:inherit">
              ${roles.map(r => `<option value="${r}">${r}</option>`).join("")}
            </select>
            <button id="add-member-btn">+ Add member</button>
          </div>
          <div id="member-msg" style="margin-top:8px;font-size:13px"></div>
        </div>`;

      // Wire role-change dropdowns
      $$(".member-role-select", main).forEach(sel => {
        sel.addEventListener("change", async () => {
          const userID = sel.dataset.userId;
          const role = sel.value;
          try {
            await fetchJSON(`/api/v1/workspaces/${encodeURIComponent(wsID)}/members/${encodeURIComponent(userID)}`, {
              method: "PUT",
              body: JSON.stringify({ role }),
            });
          } catch (err) {
            alert(`Failed to update role: ${err}`);
            // Reload to restore correct state
            this.renderWorkspaceMembers(wsID);
          }
        });
      });

      // Wire remove buttons
      $$(".member-remove-btn", main).forEach(btn => {
        btn.addEventListener("click", async () => {
          const userID = btn.dataset.userId;
          if (!confirm(`Remove ${userID} from workspace ${wsID}?`)) return;
          try {
            await fetch(
              `/api/v1/workspaces/${encodeURIComponent(wsID)}/members/${encodeURIComponent(userID)}`,
              {
                method: "DELETE",
                headers: Object.assign({ "Content-Type": "application/json" }, Workspace.header()),
              }
            );
            // Remove row optimistically
            const row = main.querySelector(`tr[data-member-id="${CSS.escape(userID)}"]`);
            if (row) row.remove();
            // If tbody is now empty, show placeholder
            const tbody = $("#members-tbody");
            if (tbody && !tbody.querySelector("tr[data-member-id]")) {
              tbody.innerHTML = `<tr><td colspan="3" class="empty">No members yet.</td></tr>`;
            }
          } catch (err) {
            alert(`Failed to remove member: ${err}`);
          }
        });
      });

      // Wire add-member form
      const addBtn = $("#add-member-btn");
      const msg = $("#member-msg");

      const doAdd = async () => {
        const userInput = $("#new-user-id");
        const roleSelect = $("#new-user-role");
        const userID = userInput.value.trim();
        const role = roleSelect.value;
        msg.textContent = "";
        if (!userID) {
          msg.style.color = "var(--error)";
          msg.textContent = "User ID is required.";
          return;
        }
        try {
          await fetchJSON(`/api/v1/workspaces/${encodeURIComponent(wsID)}/members/${encodeURIComponent(userID)}`, {
            method: "PUT",
            body: JSON.stringify({ role }),
          });
          msg.style.color = "var(--success)";
          msg.textContent = `Added ${userID} as ${role}.`;
          userInput.value = "";
          // Reload page to show new member in table
          this.renderWorkspaceMembers(wsID);
        } catch (err) {
          msg.style.color = "var(--error)";
          if (String(err).includes("403")) {
            msg.textContent = "You must be admin to add members.";
          } else {
            msg.textContent = `Failed: ${err}`;
          }
        }
      };

      addBtn.addEventListener("click", doAdd);
      $("#new-user-id").addEventListener("keydown", e => { if (e.key === "Enter") doAdd(); });
    },

    // ── About ────────────────────────────────────────────────────────────────
    renderAbout() {
      $("#app").innerHTML = `
        <h1>About LiteMLflow</h1>
        <div class="card">
          <p>Single-binary, MLflow-compatible experiment tracker with first-class LLM trace support.</p>
          <ul>
            <li>Storage: SQLite (WAL) + filesystem artifacts</li>
            <li>API: <code>/api/2.0/mlflow/*</code> compat + <code>/api/v1/*</code> native</li>
            <li>License: Apache 2.0</li>
            <li>Source: see <code>NOTICE</code></li>
          </ul>
          <h2 style="margin-top:16px">Keyboard shortcuts</h2>
          <p>Press <kbd>?</kbd> to see all shortcuts, or <kbd>⌘K</kbd>/<kbd>Ctrl+K</kbd> to open the command palette.</p>
        </div>`;
    },
  };

  // ─── File size formatter ──────────────────────────────────────────────────────
  function formatBytes(n) {
    if (n == null || isNaN(n)) return "?B";
    if (n < 1024) return n + "B";
    if (n < 1048576) return (n / 1024).toFixed(1) + "KB";
    return (n / 1048576).toFixed(1) + "MB";
  }

  // ─── CSV table preview (first 10 rows + header) ───────────────────────────────
  function renderCSVPreview(text) {
    const lines = text.split(/\r?\n/).filter(l => l.trim() !== "");
    const rows = lines.slice(0, 11); // header + 10 data rows
    if (!rows.length) return `<span style="color:var(--fg-muted);font-size:11px">Empty CSV.</span>`;

    const parse = line => {
      // Naive CSV splitter — handles quoted fields with embedded commas.
      const fields = [];
      let cur = "";
      let inQuote = false;
      for (let i = 0; i < line.length; i++) {
        const ch = line[i];
        if (ch === '"' && !inQuote) { inQuote = true; continue; }
        if (ch === '"' && inQuote) {
          if (line[i + 1] === '"') { cur += '"'; i++; } else { inQuote = false; }
          continue;
        }
        if (ch === "," && !inQuote) { fields.push(cur); cur = ""; continue; }
        cur += ch;
      }
      fields.push(cur);
      return fields;
    };

    const header = parse(rows[0]);
    const dataRows = rows.slice(1);
    const thCells = header.map(h => `<th>${escapeHTML(h)}</th>`).join("");
    const bodyRows = dataRows.map(r => {
      const cols = parse(r);
      const tds = header.map((_, i) => `<td class="mono">${escapeHTML(cols[i] || "")}</td>`).join("");
      return `<tr>${tds}</tr>`;
    }).join("");
    const truncNote = lines.length > 11 ? `<p style="font-size:11px;color:var(--fg-muted);margin:4px 0 0">Showing first 10 rows of ${lines.length - 1} total.</p>` : "";
    return `<div style="overflow-x:auto"><table style="font-size:11px"><thead><tr>${thCells}</tr></thead><tbody>${bodyRows}</tbody></table></div>${truncNote}`;
  }

  // ─── Line diff helper ─────────────────────────────────────────────────────────
  function renderLineDiff(textA, textB) {
    const linesA = textA.split("\n");
    const linesB = textB.split("\n");
    // Simple LCS-based unified diff
    const lcs = computeLCS(linesA, linesB);
    const out = [];
    let ia = 0, ib = 0, il = 0;
    while (ia < linesA.length || ib < linesB.length) {
      if (il < lcs.length && ia === lcs[il][0] && ib === lcs[il][1]) {
        out.push(`<div class="diff-ctx">&nbsp;${escapeHTML(linesA[ia])}</div>`);
        ia++; ib++; il++;
      } else if (ib < linesB.length && (il >= lcs.length || ib < lcs[il][1])) {
        out.push(`<div class="diff-add">+${escapeHTML(linesB[ib])}</div>`);
        ib++;
      } else {
        out.push(`<div class="diff-del">-${escapeHTML(linesA[ia])}</div>`);
        ia++;
      }
    }
    return out.join("") || `<div class="empty">No differences.</div>`;
  }

  function computeLCS(a, b) {
    // DP table — keeps track of (ia, ib) pairs in the LCS
    const m = a.length, n = b.length;
    const dp = Array.from({ length: m + 1 }, () => new Int32Array(n + 1));
    for (let i = m - 1; i >= 0; i--) {
      for (let j = n - 1; j >= 0; j--) {
        if (a[i] === b[j]) dp[i][j] = 1 + dp[i + 1][j + 1];
        else dp[i][j] = Math.max(dp[i + 1][j], dp[i][j + 1]);
      }
    }
    const pairs = [];
    let i = 0, j = 0;
    while (i < m && j < n) {
      if (a[i] === b[j]) { pairs.push([i, j]); i++; j++; }
      else if (dp[i + 1][j] >= dp[i][j + 1]) i++;
      else j++;
    }
    return pairs;
  }

  document.addEventListener("DOMContentLoaded", () => App.init());
})();
