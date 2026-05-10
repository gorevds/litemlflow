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

  // clampInt parses `v` as an integer and clamps to [lo, hi]. Falls back
  // to `dflt` if parse fails. Used by the lineage page's depth/fanout knobs.
  function clampInt(v, lo, hi, dflt) {
    const n = parseInt(v, 10);
    if (Number.isNaN(n)) return dflt;
    return Math.min(hi, Math.max(lo, n));
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

  // ─── Toast notification ───────────────────────────────────────────────────────
  function showToast(msg, durationMs) {
    durationMs = durationMs || 2200;
    let el = document.createElement("div");
    el.className = "lmf-toast";
    el.textContent = msg;
    document.body.appendChild(el);
    // Force reflow so the transition fires.
    el.getBoundingClientRect();
    el.classList.add("lmf-toast--show");
    setTimeout(() => {
      el.classList.remove("lmf-toast--show");
      setTimeout(() => el.remove(), 300);
    }, durationMs);
  }

  // ─── Column picker state ─────────────────────────────────────────────────────
  const ColumnPicker = {
    _default: ["name", "status", "started", "duration"],
    _optional: ["id", "end_time"],

    _key(expID) { return `litemlflow.columns.${expID}`; },

    load(expID, extras) {
      // extras = [{id: "metric.loss", label: "loss", kind: "metric"}, ...]
      let saved;
      try { saved = JSON.parse(localStorage.getItem(this._key(expID))); } catch {}
      if (!Array.isArray(saved)) {
        saved = [...this._default, ...this._optional];
      }
      const all = [
        ...this._default.map(id => ({ id, label: id, kind: "default", locked: true })),
        ...this._optional.map(id => ({ id, label: id.replace("_", " "), kind: "optional" })),
        ...(extras || []),
      ];
      return all.map(c => ({ ...c, enabled: saved.includes(c.id) || c.locked }));
    },

    save(expID, cols) {
      const enabled = cols.filter(c => c.enabled).map(c => c.id);
      localStorage.setItem(this._key(expID), JSON.stringify(enabled));
    },
  };

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
          <label class="palette-fed" title="Search across federated peers (slower)">
            <input type="checkbox" id="palette-federated" /> Federated
          </label>
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
      const fed = $("#palette-federated");
      // Persist the preference so power users don't have to toggle every time.
      fed.checked = localStorage.getItem("litemlflow.federatedSearch") === "1";
      fed.addEventListener("change", () => {
        localStorage.setItem("litemlflow.federatedSearch", fed.checked ? "1" : "0");
        this._search(input.value.trim());
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
        try {
          // Use the new /api/v1/search endpoint for cross-entity search
          const qs = new URLSearchParams({ q: query, kind: "all" });
          // Also pass known prompts so the server can filter them
          let knownPrompts = [];
          try { knownPrompts = JSON.parse(localStorage.getItem("litemlflow.knownPrompts")) || []; } catch {}
          if (knownPrompts.length) qs.set("names", knownPrompts.slice(0, 20).join(","));
          const fedBox = $("#palette-federated");
          if (fedBox && fedBox.checked) qs.set("federated", "1");
          const data = await fetchJSON(`/api/v1/search?${qs}`);
          const tag = inst => inst ? ` <span class="palette-origin">${escapeHTML(inst)}</span>` : "";
          const hits = (data.items || []).map(item => {
            if (item.kind === "experiment") {
              // Federated hits include `instance` so the user knows which
              // server owns the row. Local hits get tagged too when the
              // response is federated, otherwise the pill is suppressed.
              const remote = item.instance && data.federated;
              return {
                label: `Experiment: ${item.name}`,
                instance: item.instance,
                action: () => {
                  if (remote && item.url && item.url.startsWith("http")) { window.open(item.url, "_blank"); return; }
                  location.hash = item.url || `#/experiments/${item.id}`;
                },
                _badge: tag(remote ? item.instance : ""),
              };
            }
            if (item.kind === "run") {
              const remote = item.instance && data.federated;
              return {
                label: `Run: ${item.name || item.id.slice(0, 8)} (${item.subtitle || ""})`,
                action: () => {
                  if (remote && item.url && item.url.startsWith("http")) { window.open(item.url, "_blank"); return; }
                  location.hash = item.url;
                },
                _badge: tag(remote ? item.instance : ""),
              };
            }
            if (item.kind === "prompt") {
              return {
                label: `Prompt: ${item.name}`,
                action: () => { location.hash = item.url; },
                _badge: "",
              };
            }
            return null;
          }).filter(Boolean);
          items = [...hits, ...items];
          if (data.partial) {
            items.unshift({
              label: "⚠︎ Some peers failed to respond — results may be incomplete",
              action: () => { location.hash = "#/federation"; },
              _badge: "",
            });
          }
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
        `<li class="palette-item${i === this._cursor ? " palette-item--active" : ""}" data-idx="${i}">${escapeHTML(item.label)}${item._badge || ""}</li>`
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

  // ─── Server features (T4.22 multi-tenant gate, future flags) ────────────
  // Read once from /version and cached on window for the lifetime of the
  // page. Front-end falls back to "all flags off" if /version hasn't
  // resolved yet — UI degrades gracefully instead of blocking on a fetch.
  const Features = {
    _cache: { multi_tenant: false, _version: "dev" },
    async init() {
      try {
        const data = await fetch("/version").then(r => r.json());
        if (data && typeof data.features === "object") {
          Object.assign(this._cache, data.features);
        }
        if (data && typeof data.version === "string") {
          this._cache._version = data.version;
        }
      } catch {}
    },
    multiTenant() { return !!this._cache.multi_tenant; },
  };

  // ─── Workspace selector ───────────────────────────────────────────────────────
  const WorkspaceSelector = {
    async init() {
      // T4.22: skip entirely if the server hasn't enabled the multi-tenant
      // UI surface. The engine still runs (RBAC + workspace middleware
      // are inert for solo MLE) — this just hides the selector + member
      // pages that would otherwise add visual noise for the hero user.
      if (!Features.multiTenant()) return;

      let workspaces = [];
      try {
        const data = await fetchJSON("/api/v1/workspaces");
        if (Array.isArray(data.workspaces)) workspaces = data.workspaces;
      } catch { /* endpoint may not exist yet; graceful fallback */ }

      // Single-tenant case: only the default workspace exists. Hide the
      // selector entirely — it adds no information and looks like noise.
      const cur = Workspace.get();
      const isSingleDefault = workspaces.length <= 1 &&
        (workspaces.length === 0 || workspaces[0].id === "default") &&
        cur === "default";
      if (isSingleDefault) return;

      // Make sure the current workspace is represented even if it isn't in
      // the list (e.g., user typed a stale ID into localStorage).
      if (!workspaces.find(ws => ws.id === cur)) {
        workspaces = [{ id: cur, name: cur }, ...workspaces];
      }

      const select = document.createElement("select");
      select.id = "workspace-select";
      select.className = "workspace-select";
      select.title = "Active workspace";
      select.innerHTML = workspaces.map(ws =>
        `<option value="${escapeHTML(ws.id)}"${ws.id === cur ? " selected" : ""}>${escapeHTML(ws.name || ws.id)}</option>`
      ).join("");

      select.addEventListener("change", () => {
        Workspace.set(select.value);
        App.route();
      });

      $(".actions").prepend(select);
    },
  };

  // ─── Modal a11y + Confirm/Prompt helpers (Tier 1 / B3) ──────────────────────
  //
  // Existing modals (`_showPromptCreateModal`, `_showNewProjectModal`,
  // `_showMoveProjectModal`, `_showDatasetUploadModal`, `_showAddWidgetModal`)
  // build their own bodies and attach a `.modal-backdrop` div to the DOM. They
  // were not focus-trapped, lacked role=dialog/aria-modal, and tabbing leaked
  // to the page beneath. Rather than rewrite all five callers, this helper
  // elevates EVERY `.modal-backdrop` once it lands in the DOM:
  //
  //   - sets role="dialog" + aria-modal="true" on the inner .modal element
  //   - links aria-labelledby to the first <h2> via auto-generated id
  //   - traps Tab/Shift+Tab inside the backdrop
  //   - moves focus to the first focusable element on open
  //   - restores focus to whatever was active before the modal opened
  //
  // Modal.confirm() and Modal.prompt() are async replacements for the native
  // confirm() / prompt() used in destructive flows (delete run, delete
  // webhook, soft-delete dataset, name a saved analytics query). They build
  // their own .modal-backdrop so the same machinery applies.
  const Modal = (() => {
    const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
    let labelSeq = 0;
    const previousFocus = new WeakMap();

    // Apply a11y attributes + focus trap whenever a .modal-backdrop is added.
    const mo = new MutationObserver((muts) => {
      for (const m of muts) {
        for (const node of m.addedNodes) {
          if (node.nodeType !== 1) continue;
          if (node.classList && node.classList.contains("modal-backdrop")) {
            arm(node);
          }
        }
      }
    });

    const arm = (backdrop) => {
      const card = backdrop.querySelector(".modal");
      if (!card) return;
      card.setAttribute("role", "dialog");
      card.setAttribute("aria-modal", "true");
      // Label the dialog by the first heading inside it.
      const heading = card.querySelector("h2, h1, h3");
      if (heading) {
        const id = "lmf-modal-label-" + (++labelSeq);
        heading.id = heading.id || id;
        card.setAttribute("aria-labelledby", heading.id);
      }
      // Remember the trigger element so we can restore focus on close.
      previousFocus.set(backdrop, document.activeElement);
      // Move focus into the modal — first focusable element, falling back
      // to the modal card itself.
      const firstFocusable = card.querySelector(FOCUSABLE);
      setTimeout(() => {
        (firstFocusable || card).focus();
      }, 0);
      if (!firstFocusable) {
        card.tabIndex = -1; // allow programmatic focus
      }
      // Tab / Shift+Tab loop inside the backdrop. We attach to the
      // backdrop (capture phase) so it runs before any per-input handlers.
      backdrop.addEventListener("keydown", (e) => {
        if (e.key !== "Tab") return;
        const focusables = Array.from(card.querySelectorAll(FOCUSABLE)).filter(el => !el.disabled && el.offsetParent !== null);
        if (focusables.length === 0) {
          e.preventDefault();
          return;
        }
        const first = focusables[0];
        const last = focusables[focusables.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      });
      // Restore focus when the backdrop is removed.
      const removalObserver = new MutationObserver((rmMuts) => {
        for (const rm of rmMuts) {
          for (const removed of rm.removedNodes) {
            if (removed === backdrop) {
              const prev = previousFocus.get(backdrop);
              if (prev && typeof prev.focus === "function") {
                try { prev.focus(); } catch {}
              }
              removalObserver.disconnect();
              return;
            }
          }
        }
      });
      removalObserver.observe(backdrop.parentNode || document.body, { childList: true });
    };

    const init = () => {
      mo.observe(document.body, { childList: true, subtree: false });
    };

    // ----- Confirm / Prompt -----
    //
    // Both return Promises — destructive flows can `await` them rather than
    // using the synchronous, blocking, ugly browser dialogs.

    const confirm = ({ title, message, primaryLabel, danger }) => {
      return new Promise((resolve) => {
        const wrap = document.createElement("div");
        wrap.className = "modal-backdrop";
        wrap.innerHTML = `
          <div class="card modal" style="max-width:440px">
            <h2 style="margin-top:0">${escapeHTML(title || "Confirm")}</h2>
            <p style="color:var(--fg-muted);margin:0 0 16px">${escapeHTML(message || "Are you sure?")}</p>
            <div style="display:flex;gap:8px;justify-content:flex-end">
              <button data-modal-cancel>Cancel</button>
              <button data-modal-ok class="${danger ? "btn-danger" : "btn-primary"}">${escapeHTML(primaryLabel || (danger ? "Delete" : "OK"))}</button>
            </div>
          </div>`;
        document.body.appendChild(wrap);

        const close = (result) => {
          wrap.remove();
          resolve(result);
        };
        wrap.addEventListener("click", (e) => {
          if (e.target === wrap) close(false);
        });
        wrap.querySelector("[data-modal-cancel]").addEventListener("click", () => close(false));
        wrap.querySelector("[data-modal-ok]").addEventListener("click", () => close(true));
        // Wire Escape locally so we can resolve(false) — the global Escape
        // handler in App.init also closes top backdrop, but won't surface
        // a value through our promise.
        wrap.addEventListener("keydown", (e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            close(false);
          } else if (e.key === "Enter" && document.activeElement.tagName !== "TEXTAREA") {
            e.preventDefault();
            close(true);
          }
        });
      });
    };

    const prompt = ({ title, label, placeholder, defaultValue }) => {
      return new Promise((resolve) => {
        const wrap = document.createElement("div");
        wrap.className = "modal-backdrop";
        wrap.innerHTML = `
          <div class="card modal" style="max-width:440px">
            <h2 style="margin-top:0">${escapeHTML(title || "Input")}</h2>
            <table class="form-table">
              <tr>
                <th><label for="lmf-prompt-input">${escapeHTML(label || "Value")}</label></th>
                <td><input type="text" id="lmf-prompt-input" placeholder="${escapeHTML(placeholder || "")}" value="${escapeHTML(defaultValue || "")}" style="width:100%"/></td>
              </tr>
            </table>
            <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px">
              <button data-modal-cancel>Cancel</button>
              <button data-modal-ok class="btn-primary">OK</button>
            </div>
          </div>`;
        document.body.appendChild(wrap);
        const input = wrap.querySelector("#lmf-prompt-input");
        const close = (result) => {
          wrap.remove();
          resolve(result);
        };
        wrap.addEventListener("click", (e) => {
          if (e.target === wrap) close(null);
        });
        wrap.querySelector("[data-modal-cancel]").addEventListener("click", () => close(null));
        wrap.querySelector("[data-modal-ok]").addEventListener("click", () => close(input.value));
        input.addEventListener("keydown", (e) => {
          if (e.key === "Escape") { e.stopPropagation(); close(null); }
          else if (e.key === "Enter") { e.preventDefault(); close(input.value); }
        });
      });
    };

    return { init, confirm, prompt };
  })();

  // ─── Prefs namespace (Tier 1 / B3 follow-up) ────────────────────────────────
  // Centralises localStorage access. Keys follow `litemlflow.<scope>.<key>`.
  // Everywhere across app.js that touches localStorage directly is a candidate
  // for migration (~18 sites at last count) — done piecemeal during T2/T3.
  const Prefs = {
    getJSON(key, def) {
      try {
        const v = localStorage.getItem(key);
        if (v == null) return def;
        return JSON.parse(v);
      } catch { return def; }
    },
    setJSON(key, val) {
      try { localStorage.setItem(key, JSON.stringify(val)); } catch {}
    },
    getString(key, def) {
      const v = localStorage.getItem(key);
      return v == null ? (def || "") : v;
    },
    setString(key, val) {
      try { localStorage.setItem(key, val); } catch {}
    },
    remove(key) {
      try { localStorage.removeItem(key); } catch {}
    },
  };

  // ─── Main App ─────────────────────────────────────────────────────────────────
  const App = {
    cache: { experiments: null, runs: {}, runData: {} },

    init() {
      // Modal a11y observer (focus trap + role=dialog + restore-focus).
      Modal.init();

      // Theme
      const stored = localStorage.getItem("litemlflow.theme");
      if (stored) document.documentElement.setAttribute("data-theme", stored);
      $("#theme-toggle").addEventListener("click", () => {
        const cur = document.documentElement.getAttribute("data-theme");
        const next = cur === "dark" ? "light" : "dark";
        document.documentElement.setAttribute("data-theme", next);
        localStorage.setItem("litemlflow.theme", next);
      });

      // Header search button — opens the command palette (also bound to ⌘K).
      const paletteTrigger = $("#palette-trigger");
      if (paletteTrigger) {
        paletteTrigger.addEventListener("click", () => CommandPalette.open());
      }
      // Footer "Shortcuts (?)" link — opens the keyboard help modal.
      const footerShortcuts = $("#footer-shortcuts");
      if (footerShortcuts) {
        footerShortcuts.addEventListener("click", (e) => {
          e.preventDefault();
          ShortcutHelp.toggle();
        });
      }

      // Global Escape: close any open modal-backdrop. Modal helpers use
      // the same .modal-backdrop class, so one listener serves all of them.
      document.addEventListener("keydown", (e) => {
        if (e.key !== "Escape") return;
        const backdrops = document.querySelectorAll(".modal-backdrop");
        if (backdrops.length) {
          // Close the topmost (last-added) so a nested confirm doesn't
          // wipe the parent.
          backdrops[backdrops.length - 1].remove();
          e.stopPropagation();
        }
      });

      // T4.22 final-review fix: feature flags must resolve BEFORE the
      // workspace selector queries them — otherwise --enable-multi-tenant
      // never lights up on first paint. Single fetch, both consumers.
      Features.init().then(() => {
        $("#version").textContent = (Features._cache._version) || "dev";
        // Re-evaluate workspace nav visibility now that flags are known.
        const wsLink = $('header nav a[href="#/workspaces"]');
        if (wsLink && !Features.multiTenant()) wsLink.style.display = "none";
        // Workspace selector is gated on multi_tenant; init AFTER the
        // flags have resolved so the gate sees the real value.
        WorkspaceSelector.init();
      });

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
      const lineageMatch  = hash.match(/^\/experiments\/(\d+)\/runs\/([0-9a-f]+)\/lineage$/);
      const cmpMatch      = hash.match(/^\/experiments\/(\d+)\/compare/);
      const promptMatch   = hash.match(/^\/prompts\/(.+)$/);
      const wsMembersMatch = hash.match(/^\/workspaces\/([^/]+)\/members$/);
      const dashMatch      = hash.match(/^\/dashboards\/(.+)$/);

      if (lineageMatch)    return this.renderRunLineage(parseInt(lineageMatch[1], 10), lineageMatch[2]);
      if (runMatch)        return this.renderRun(parseInt(runMatch[1], 10), runMatch[2]);
      if (cmpMatch)        return this.renderCompare(parseInt(cmpMatch[1], 10));
      if (expMatch)        return this.renderExperiment(parseInt(expMatch[1], 10));
      if (promptMatch)     return this.renderPromptDetail(promptMatch[1]);
      if (wsMembersMatch)  return this.renderWorkspaceMembers(wsMembersMatch[1]);
      if (dashMatch)       return this.renderDashboard(decodeURIComponent(dashMatch[1]));
      if (hash.startsWith("/workspaces")) return this.renderWorkspaces();
      if (hash.startsWith("/prompts")) return this.renderPrompts();
      if (hash.startsWith("/about"))   return this.renderAbout();
      if (hash.startsWith("/webhooks")) return this.renderWebhooks();
      if (hash.startsWith("/dashboards")) return this.renderDashboardsIndex();
      if (hash.startsWith("/analytics")) return this.renderAnalytics();
      if (hash.startsWith("/federation")) return this.renderFederation();
      const dsDetail = hash.match(/^\/datasets\/(.+)$/);
      if (dsDetail) return this.renderDatasetDetail(decodeURIComponent(dsDetail[1]));
      if (hash.startsWith("/datasets")) return this.renderDatasetsIndex();
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

        // Active-project chip filter (separate from groupBy).
        const filterKey = "litemlflow.experiments.projectFilter";
        let projectFilter = localStorage.getItem(filterKey) || ""; // "" = no filter

        // Build the canonical list of project names from the live exps so it
        // matches the data on screen (the /projects endpoint is best-effort).
        const knownProjects = (() => {
          const set = new Set();
          exps.forEach(e => { const p = projectOf(e); if (p) set.add(p); });
          return Array.from(set).sort();
        })();

        const filteredExps = projectFilter === ""
          ? exps
          : exps.filter(e => projectOf(e) === projectFilter);

        const rowHTML = (e, i) => {
          const proj = projectOf(e);
          const otherTags = (e.tags || []).filter(t => t.key !== PROJ_KEY);
          return `
            <tr data-row-index="${i}" data-exp-id="${e.experiment_id}">
              <td class="mono">${e.experiment_id}</td>
              <td>
                <a href="#/experiments/${e.experiment_id}">${escapeHTML(e.name)}</a>
                ${proj ? ` <span class="proj-pill" title="Project">${escapeHTML(proj)}</span>` : ""}
                <button class="proj-move-btn" data-move-id="${e.experiment_id}" data-current-proj="${escapeHTML(proj)}" title="Assign or change project">${proj ? "Move…" : "+ Project"}</button>
              </td>
              <td>${formatTime(e.creation_time)}</td>
              <td>${formatTime(e.last_update_time)}</td>
              <td>${otherTags.map(t => `<span class="tag">${escapeHTML(t.key)}=${escapeHTML(t.value)}</span>`).join(" ")}</td>
            </tr>`;
        };

        const renderRows = () => filteredExps.map(rowHTML).join("");

        // Group sections (one <table> per project) when groupBy === 'project'.
        // When a filter is active we always render flat (only one bucket would show).
        const renderGrouped = () => {
          const buckets = new Map();
          filteredExps.forEach(e => {
            const p = projectOf(e);
            if (!buckets.has(p)) buckets.set(p, []);
            buckets.get(p).push(e);
          });
          const names = Array.from(buckets.keys()).sort((a, b) => {
            if (a === "" && b !== "") return 1;
            if (b === "" && a !== "") return -1;
            return a.localeCompare(b);
          });
          return names.map(name => {
            const list = buckets.get(name);
            const heading = name === "" ? "No project" : name;
            const rows = list.map(rowHTML).join("");
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

        // Project filter chips: All + each known project + "Unassigned".
        const chipsHTML = (() => {
          const chip = (label, value, count) => `
            <button class="proj-chip ${projectFilter === value ? "active" : ""}" data-proj-filter="${escapeHTML(value)}">
              ${escapeHTML(label)}<span class="proj-chip-count">${count}</span>
            </button>`;
          const all = chip("All", "", exps.length);
          const projChips = knownProjects.map(p =>
            chip(p, p, exps.filter(e => projectOf(e) === p).length)
          ).join("");
          const unassigned = exps.filter(e => projectOf(e) === "").length;
          const unassignedChip = unassigned > 0
            ? chip("Unassigned", "__none__", unassigned) : "";
          return all + projChips + unassignedChip;
        })();

        // Apply __none__ pseudo-filter
        const useFilter = projectFilter === "__none__"
          ? (e => projectOf(e) === "")
          : (projectFilter === "" ? () => true : (e => projectOf(e) === projectFilter));
        const finalExps = exps.filter(useFilter);

        const finalRows = finalExps.map(rowHTML).join("");

        main.innerHTML = `
          <div class="toolbar">
            <h1 style="margin:0">Experiments</h1>
            <button id="new-project-btn" class="btn-primary" title="Create a new project">+ New project</button>
            <div class="proj-toggle" role="tablist" aria-label="Group by">
              <button data-mode="project" class="${groupBy === "project" ? "active" : ""}" title="Group by project">By project</button>
              <button data-mode="flat" class="${groupBy === "flat" ? "active" : ""}" title="Flat list">Flat</button>
            </div>
            <input type="search" id="exp-search" placeholder="Filter by name…" />
          </div>
          <p style="color:var(--fg-muted);font-size:13px;margin:0 0 4px">
            Group experiments into projects with a single click. Each "project" is the
            <code>lmf.project</code> tag on an experiment — assign it via the <strong>Move…</strong>
            or <strong>+ Project</strong> button on each row.
          </p>
          <div class="proj-chip-row">${chipsHTML}</div>
          <div id="exp-list">
            ${(groupBy === "project" && projectFilter === "") ? renderGrouped() : `
              <div class="card" style="padding:0">
                <table>
                  <thead><tr><th>ID</th><th>Name</th><th>Created</th><th>Updated</th><th>Tags</th></tr></thead>
                  <tbody id="exp-tbody">${finalRows}</tbody>
                </table>
              </div>`}
          </div>`;

        // Group toggle
        $$(".proj-toggle button", main).forEach(btn => {
          btn.addEventListener("click", () => {
            const mode = btn.dataset.mode;
            if (mode === groupBy) return;
            localStorage.setItem(groupKey, mode);
            App.renderExperiments();
          });
        });

        // Project chip click → set filter
        $$(".proj-chip", main).forEach(chip => {
          chip.addEventListener("click", () => {
            const v = chip.dataset.projFilter;
            if (v === projectFilter) return;
            localStorage.setItem(filterKey, v);
            App.renderExperiments();
          });
        });

        // New-project button → create modal that lets user create + assign experiments
        $("#new-project-btn").addEventListener("click", () => this._showNewProjectModal(exps, knownProjects));

        // Per-row move-project button
        $$(".proj-move-btn", main).forEach(btn => {
          btn.addEventListener("click", (e) => {
            e.stopPropagation();
            this._showMoveProjectModal(
              btn.dataset.moveId,
              btn.dataset.currentProj || "",
              knownProjects,
            );
          });
        });

        // Row click navigates (but not if click hit a button inside)
        $$("tr[data-exp-id]", main).forEach(tr => {
          tr.style.cursor = "pointer";
          tr.addEventListener("click", e => {
            if (e.target.closest("button") || e.target.tagName === "A") return;
            location.hash = `#/experiments/${tr.dataset.expId}`;
          });
        });

        // Live filter
        $("#exp-search").addEventListener("input", function () {
          const q = this.value.toLowerCase();
          $$("tr[data-exp-id]", main).forEach(tr => {
            const name = tr.querySelector("td:nth-child(2)").textContent.toLowerCase();
            tr.style.display = (!q || name.includes(q)) ? "" : "none";
          });
          $$(".proj-heading", main).forEach(h => {
            const card = h.nextElementSibling;
            if (!card) return;
            const visible = $$("tr[data-exp-id]", card).some(tr => tr.style.display !== "none");
            h.style.display = visible ? "" : "none";
            card.style.display = visible ? "" : "none";
          });
        });
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    // ── Experiment detail (runs table + bulk select + columns picker + share) ──
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

        // Collect all param and metric keys from runs for the column picker
        const paramKeys = [...new Set(runs.flatMap(r => (r.data.params || []).map(p => p.key)))].sort();
        const metricKeys = [...new Set(runs.flatMap(r => (r.data.metrics || []).map(m => m.key)))].sort();
        const extraCols = [
          ...paramKeys.map(k => ({ id: "param." + k, label: k, kind: "param" })),
          ...metricKeys.map(k => ({ id: "metric." + k, label: k, kind: "metric" })),
        ];
        let cols = ColumnPicker.load(expID, extraCols);

        // T2.10: cells render WITHOUT inline `onclick=` attributes. Click
        // handling is delegated to a single listener on #exp-tbody (wired
        // below), which navigates when the click landed on a non-button,
        // non-input cell of a row that carries data-run-id. This drops
        // ~200 strings per render (200 runs × 6+ cols) and removes the
        // string-interpolated event-handler smell.
        const buildRows = () => sortRuns(runs).map((r, i) => {
          const info = r.info, data = r.data;
          const cells = cols.filter(c => c.enabled).map(c => {
            let cell = "";
            switch (c.id) {
              case "id":      cell = `<td class="mono">${info.run_id.slice(0, 8)}</td>`; break;
              case "name":    cell = `<td>${escapeHTML(info.run_name || "—")}</td>`; break;
              case "status":  cell = `<td><span class="status-pill status-${info.status}">${info.status}</span></td>`; break;
              case "started": cell = `<td>${formatTime(info.start_time)}</td>`; break;
              case "duration":cell = `<td>${info.end_time ? formatDuration(info.end_time - info.start_time) : "—"}</td>`; break;
              case "end_time":cell = `<td>${info.end_time ? formatTime(info.end_time) : "—"}</td>`; break;
              default: {
                if (c.id.startsWith("param.")) {
                  const key = c.id.slice(6);
                  const found = (data.params || []).find(p => p.key === key);
                  cell = `<td class="mono">${escapeHTML(found ? found.value : "—")}</td>`;
                } else if (c.id.startsWith("metric.")) {
                  const key = c.id.slice(7);
                  const found = (data.metrics || []).find(m => m.key === key);
                  cell = `<td class="numeric">${found ? found.value.toPrecision(4) : "—"}</td>`;
                } else {
                  cell = `<td>&mdash;</td>`;
                }
              }
            }
            return cell;
          }).join("");
          const checked = BulkSelect.has(info.run_id) ? "checked" : "";
          const starMark = isStarred(r) ? '<span class="star-icon" title="Starred">&#9733;</span> ' : "";
          // Inject star into the rendered "name" cell if the column is enabled.
          const cellsWithStar = cols.filter(c => c.enabled).find(c => c.id === "name")
            ? cells.replace(
                `>${escapeHTML(info.run_name || "—")}</td>`,
                `>${starMark}${escapeHTML(info.run_name || "—")}</td>`
              )
            : cells;
          return `<tr data-row-index="${i}" data-run-id="${info.run_id}" data-href="#/experiments/${expID}/runs/${info.run_id}">
            <td class="bulk-col"><input type="checkbox" class="bulk-cb" data-run-id="${info.run_id}" ${checked} /></td>
            ${cellsWithStar}
          </tr>`;
        }).join("");

        const buildHeader = () => cols.filter(c => c.enabled).map(c => {
          const labels = { id: "ID", name: "Name", status: "Status", started: "Started", duration: "Duration", end_time: "Ended" };
          return `<th>${escapeHTML(labels[c.id] || c.label)}</th>`;
        }).join("");

        const colCount = () => cols.filter(c => c.enabled).length + 1; // +1 for checkbox

        const renderTable = () => {
          const tbody = $("#exp-tbody");
          const thead = $("#exp-thead-cols");
          if (tbody) tbody.innerHTML = buildRows() || `<tr><td colspan="${colCount()}" class="empty">No runs yet.</td></tr>`;
          if (thead) thead.innerHTML = buildHeader();
          // T2.10: install a single delegated click handler that navigates
          // when the click lands on a non-button, non-input cell of a row
          // that carries data-href. Replaces ~200 inline `onclick=` strings
          // (200 runs × 6+ cols) per render.
          if (tbody && !tbody._lmfRowClickWired) {
            tbody.addEventListener("click", (ev) => {
              const t = ev.target;
              // Skip clicks that originated on interactive children — let
              // the native handlers fire (checkbox, links, action buttons).
              if (t.closest("button, a, input")) return;
              const row = t.closest("[data-href]");
              if (!row) return;
              location.hash = row.dataset.href;
            });
            tbody._lmfRowClickWired = true;
          }
        };

        // View mode: "list" (default) or "timeline". Persisted per experiment.
        const viewModeKey = `litemlflow.exp.${expID}.viewMode`;
        let viewMode = localStorage.getItem(viewModeKey) || "list";

        main.innerHTML = `
          <div class="crumbs"><a href="#/experiments">Experiments</a> / ${escapeHTML(e.name)}</div>
          <div style="display:flex;align-items:center;gap:10px;margin-bottom:8px">
            <h1 style="margin:0;flex:1">${escapeHTML(e.name)}</h1>
            <button id="exp-clone-btn" title="Clone this experiment">Clone</button>
            <button id="share-exp-btn" class="btn-ghost" title="Copy link">🔗 Share</button>
          </div>
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
          <div style="display:flex;align-items:center;gap:12px;margin:12px 0 6px">
            <h2 style="margin:0;flex:1">Runs (${runs.length})</h2>
            <div class="proj-toggle" role="tablist" aria-label="View mode">
              <button data-view="list" class="${viewMode === "list" ? "active" : ""}" title="Table view">List</button>
              <button data-view="timeline" class="${viewMode === "timeline" ? "active" : ""}" title="Gantt-style timeline">Timeline</button>
            </div>
            <label style="font-size:12px;color:var(--fg-muted);display:flex;align-items:center;gap:4px;cursor:pointer" id="starred-first-label">
              <input type="checkbox" id="starred-first-cb" ${starredFirst ? "checked" : ""}/>
              Starred first
            </label>
            <div style="position:relative" id="cols-btn-wrap">
              <button id="cols-btn" style="font-size:12px;padding:4px 10px">Columns</button>
              <div id="cols-dropdown" class="cols-dropdown" style="display:none"></div>
            </div>
          </div>
          <div id="runs-list-view" class="card" style="padding:0;${viewMode === "list" ? "" : "display:none"}">
            <table>
              <thead>
                <tr>
                  <th class="bulk-col"><input type="checkbox" id="bulk-all" title="Select all" /></th>
                  <span id="exp-thead-cols"></span>
                </tr>
              </thead>
              <tbody id="exp-tbody"></tbody>
            </table>
          </div>
          <div id="runs-timeline-view" style="${viewMode === "timeline" ? "" : "display:none"}"></div>`;

        const renderTimeline = () => {
          const wrap = $("#runs-timeline-view");
          if (!wrap) return;
          if (runs.length === 0) {
            wrap.innerHTML = `<div class="empty card">No runs yet.</div>`;
            return;
          }
          // Find time bounds across all runs. Guard against degenerate
          // inputs (no positive start times, all-equal times) so the bar
          // computations never produce Infinity / NaN.
          const now = Date.now();
          const sts = runs.map(r => r.info.start_time).filter(t => t > 0);
          if (sts.length === 0) {
            wrap.innerHTML = `<div class="empty card">Runs have no start_time set yet.</div>`;
            return;
          }
          const ets = runs.map(r => r.info.end_time || now);
          const minT = Math.min(...sts);
          const maxT = Math.max(...ets);
          const span = Math.max(1, maxT - minT);
          const bars = sortRuns(runs).map(r => {
            const st = r.info.start_time;
            const et = r.info.end_time || now;
            const left = ((st - minT) / span) * 100;
            const width = Math.max(0.4, ((et - st) / span) * 100);
            const dur = formatDuration(et - st);
            const tip = `${escapeHTML(r.info.run_name || r.info.run_id.slice(0, 8))} • ${r.info.status} • ${dur}`;
            return `
              <div class="timeline-row" data-run-id="${r.info.run_id}" title="${tip}">
                <div class="timeline-name"><a href="#/experiments/${expID}/runs/${r.info.run_id}">${escapeHTML(r.info.run_name || r.info.run_id.slice(0,8))}</a></div>
                <div class="timeline-track">
                  <div class="timeline-bar status-${r.info.status}" style="left:${left}%;width:${width}%"></div>
                </div>
              </div>`;
          }).join("");
          wrap.innerHTML = `
            <div class="timeline-wrap">
              <div class="timeline-axis">
                <div></div>
                <div class="timeline-axis-ticks">
                  <span>${formatTime(minT)}</span>
                  <span>${formatTime((minT + maxT) / 2)}</span>
                  <span>${formatTime(maxT)}</span>
                </div>
              </div>
              ${bars}
            </div>`;
        };
        if (viewMode === "timeline") renderTimeline();

        renderTable();

        // View-mode toggle (List / Timeline)
        $$(".proj-toggle button[data-view]", main).forEach(btn => {
          btn.addEventListener("click", () => {
            const v = btn.dataset.view;
            if (v === viewMode) return;
            viewMode = v;
            localStorage.setItem(viewModeKey, v);
            $$(".proj-toggle button[data-view]", main).forEach(b => {
              b.classList.toggle("active", b.dataset.view === v);
            });
            $("#runs-list-view").style.display = v === "list" ? "" : "none";
            $("#runs-timeline-view").style.display = v === "timeline" ? "" : "none";
            $("#cols-btn-wrap").style.visibility = v === "list" ? "" : "hidden";
            $("#starred-first-label").style.visibility = v === "list" ? "" : "hidden";
            if (v === "timeline") renderTimeline();
          });
        });
        if (viewMode === "timeline") {
          $("#cols-btn-wrap").style.visibility = "hidden";
          $("#starred-first-label").style.visibility = "hidden";
        }

        // Starred-first toggle (re-renders via the same renderTable path)
        const sfCb = $("#starred-first-cb");
        if (sfCb) {
          sfCb.addEventListener("change", () => {
            starredFirst = sfCb.checked;
            localStorage.setItem(starredFirstKey, String(starredFirst));
            renderTable();
            // Rewire checkboxes after table re-render.
            $$(".bulk-cb", main).forEach(cb => {
              cb.checked = BulkSelect.has(cb.dataset.runId);
              cb.addEventListener("change", () => {
                BulkSelect.toggle(cb.dataset.runId);
                App._updateBulkBar(expID, runs);
              });
            });
          });
        }

        // Share button
        const shareBtn = $("#share-exp-btn");
        if (shareBtn) {
          shareBtn.addEventListener("click", () => {
            navigator.clipboard.writeText(location.href).then(() => showToast("Link copied")).catch(() => prompt("Copy:", location.href));
          });
        }

        // Columns dropdown
        const colsBtn = $("#cols-btn");
        const colsDrop = $("#cols-dropdown");
        if (colsBtn && colsDrop) {
          const renderDropdown = () => {
            colsDrop.innerHTML = `<div style="font-size:11px;color:var(--fg-muted);padding:6px 10px 2px;font-weight:600;text-transform:uppercase">Toggle columns</div>` +
              cols.map(c => `<label class="cols-item" style="${c.locked ? "opacity:0.5" : ""}">
                <input type="checkbox" ${c.enabled ? "checked" : ""} ${c.locked ? "disabled" : ""} data-col-id="${escapeHTML(c.id)}">
                ${escapeHTML(c.label)} <span style="font-size:10px;color:var(--fg-muted)">${c.kind}</span>
              </label>`).join("");
            $$(".cols-item input", colsDrop).forEach(cb => {
              cb.addEventListener("change", () => {
                const id = cb.dataset.colId;
                const col = cols.find(c => c.id === id);
                if (col && !col.locked) col.enabled = cb.checked;
                ColumnPicker.save(expID, cols);
                renderTable();
              });
            });
          };
          // Track the outside-click listener so we can detach it on
          // navigation/re-render. Without this each renderExperiment call
          // accumulates a new listener.
          let outsideListener = null;
          const detachOutside = () => {
            if (outsideListener) {
              document.removeEventListener("click", outsideListener);
              outsideListener = null;
            }
          };
          colsBtn.addEventListener("click", (ev) => {
            ev.stopPropagation();
            if (colsDrop.style.display === "none") {
              renderDropdown();
              colsDrop.style.display = "block";
              outsideListener = (e) => {
                if (!colsDrop.contains(e.target) && e.target !== colsBtn) {
                  colsDrop.style.display = "none";
                  detachOutside();
                }
              };
              document.addEventListener("click", outsideListener);
            } else {
              colsDrop.style.display = "none";
              detachOutside();
            }
          });
          // Detach when leaving this view via hashchange.
          window.addEventListener("hashchange", detachOutside, { once: true });
        }

        // Checkbox wiring
        $$(".bulk-cb", main).forEach(cb => {
          cb.addEventListener("change", () => {
            BulkSelect.toggle(cb.dataset.runId);
            this._updateBulkBar(expID, runs);
          });
        });
        const allCb = $("#bulk-all");
        if (allCb) {
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
        }

        // Restore checked state (survives re-render)
        $$(".bulk-cb", main).forEach(cb => {
          if (BulkSelect.has(cb.dataset.runId)) cb.checked = true;
        });
        this._updateBulkBar(expID, runs);

        // Clone experiment button
        $("#exp-clone-btn").addEventListener("click", async () => {
          const btn = $("#exp-clone-btn");
          btn.disabled = true;
          btn.textContent = "Cloning…";
          try {
            const res = await fetchJSON(`/api/v1/experiments/${expID}/clone`, { method: "POST", body: "{}" });
            const newID = res.experiment_id || (res.experiment && res.experiment.experiment_id);
            if (newID) {
              location.hash = `#/experiments/${newID}`;
            } else {
              btn.textContent = "Cloned!";
              setTimeout(() => { btn.disabled = false; btn.textContent = "Clone"; }, 2000);
            }
          } catch (err) {
            alert(`Clone failed: ${err}`);
            btn.disabled = false;
            btn.textContent = "Clone";
          }
        });

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
        if (!await Modal.confirm({
          title: "Delete runs",
          message: `Delete ${n} run(s)? This cannot be undone.`,
          danger: true, primaryLabel: "Delete",
        })) return;
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
      const hashParams = new URLSearchParams(location.hash.split("?")[1] || "");
      const allIds = (hashParams.get("runs") || "").split(",").filter(Boolean);
      const MAX_COMPARE = 6;
      const truncated = allIds.length > MAX_COMPARE;
      const ids = allIds.slice(0, MAX_COMPARE);
      // mode: "diff" shows only differing params; "full" shows side-by-side (default)
      const autoMode = allIds.length === 2 ? "diff" : "full";
      let mode = hashParams.get("mode") || autoMode;

      if (ids.length < 2) {
        main.innerHTML = `<div class="empty">Select at least 2 runs to compare.</div>`;
        return;
      }

      main.innerHTML = `<div class="loading">Loading runs…</div>`;

      try {
        // Fetch experiment name + all runs in parallel
        const [expData, ...runDatas] = await Promise.all([
          fetchJSON(`/api/2.0/mlflow/experiments/get?experiment_id=${expID}`).catch(() => ({ experiment: { name: "exp " + expID } })),
          ...ids.map(id => fetchJSON(`/api/2.0/mlflow/runs/get?run_id=${id}`).catch(() => null)),
        ]);
        const expName = expData.experiment ? expData.experiment.name : "exp " + expID;
        const valid = runDatas.filter(Boolean).map(d => d.run);
        if (!valid.length) { main.innerHTML = `<div class="empty">Could not load run data.</div>`; return; }

        const COLORS = ["#4a90e2","#e2914a","#50c878","#e2504a","#9b59b6","#1abc9c"];
        const runName = r => (r.info.run_name || r.info.run_id.slice(0, 8));

        // ── Section 1: Params ──
        const allParamKeys = [...new Set(valid.flatMap(r => (r.data.params || []).map(p => p.key)))].sort();
        const buildParamRows = (filterDiff) => allParamKeys.map(k => {
          const vals = valid.map(r => {
            const found = (r.data.params || []).find(p => p.key === k);
            return found ? found.value : "—";
          });
          const differ = new Set(vals).size > 1;
          if (filterDiff && !differ) return "";
          const equal = !differ;
          return `<tr class="${differ ? "diff-row" : ""}">
            <td class="mono" style="${equal ? "color:var(--fg-muted)" : ""}">${escapeHTML(k)}</td>
            ${vals.map(v => `<td class="mono" style="${equal ? "color:var(--fg-muted)" : ""}">${escapeHTML(v)}</td>`).join("")}
          </tr>`;
        }).filter(Boolean).join("");

        const diffOnlyCount = allParamKeys.filter(k => {
          const vals = valid.map(r => { const f = (r.data.params||[]).find(p=>p.key===k); return f?f.value:"—"; });
          return new Set(vals).size > 1;
        }).length;

        const paramSection = () => `
          <h2 style="display:flex;align-items:center;gap:12px">
            Parameters
            <span style="font-size:12px;font-weight:400;color:var(--fg-muted)">${diffOnlyCount} differing of ${allParamKeys.length}</span>
            <label style="font-size:12px;font-weight:400;display:flex;align-items:center;gap:4px;margin-left:auto;cursor:pointer">
              <input type="checkbox" id="cmp-diff-only" ${mode === "diff" ? "checked" : ""}> Show only differing
            </label>
          </h2>
          <div class="card" style="padding:0;overflow-x:auto" id="cmp-params-card">
            <table id="cmp-params-table">
              <thead>
                <tr>
                  <th>Param</th>
                  ${valid.map(r => `<th class="mono">${escapeHTML(runName(r).slice(0, 16))}</th>`).join("")}
                </tr>
              </thead>
              <tbody id="cmp-params-tbody">
                ${buildParamRows(mode === "diff") || `<tr><td colspan="${valid.length + 1}" class="empty">${mode === "diff" ? "All params identical." : "No params."}</td></tr>`}
              </tbody>
            </table>
          </div>`;

        // ── Diff mode text (2-run only) ──
        const diffTextSection = () => {
          if (valid.length !== 2) return "";
          const r0 = valid[0], r1 = valid[1];
          const diffKeys = allParamKeys.filter(k => {
            const v0 = (r0.data.params||[]).find(p=>p.key===k)?.value ?? "—";
            const v1 = (r1.data.params||[]).find(p=>p.key===k)?.value ?? "—";
            return v0 !== v1;
          });
          const sameKeys = allParamKeys.filter(k => {
            const v0 = (r0.data.params||[]).find(p=>p.key===k)?.value ?? "—";
            const v1 = (r1.data.params||[]).find(p=>p.key===k)?.value ?? "—";
            return v0 === v1;
          });
          if (!diffKeys.length) return `<div class="card" style="padding:12px;color:var(--fg-muted)">All params identical.</div>`;
          const kw = Math.max(...diffKeys.map(k => k.length), 10) + 2;
          const lines = diffKeys.map(k => {
            const v0 = (r0.data.params||[]).find(p=>p.key===k)?.value ?? "—";
            const v1 = (r1.data.params||[]).find(p=>p.key===k)?.value ?? "—";
            return `<div class="diff-ctx" style="padding:2px 12px"><span style="color:var(--fg-muted);display:inline-block;width:${kw}ch">${escapeHTML(k)}:</span> <span style="color:var(--error)">${escapeHTML(v0)}</span> → <span style="color:var(--success)">${escapeHTML(v1)}</span></div>`;
          }).join("");
          const same = sameKeys.length ? `<div class="diff-ctx" style="padding:4px 12px;color:var(--fg-muted);font-size:12px">(unchanged: ${escapeHTML(sameKeys.slice(0,6).join(", "))}${sameKeys.length>6?" …":""})</div>` : "";
          return `<div class="card diff-block" style="padding:0">${lines}${same}</div>`;
        };

        // ── Section 2: Metrics overlay ──
        const allMetricKeys = [...new Set(valid.flatMap(r => (r.data.metrics || []).map(m => m.key)))].sort();
        let chartsHtml = "";
        const W = 660, H = 160, pad = 22;
        for (const mk of allMetricKeys) {
          const perRun = await Promise.all(valid.map(r =>
            fetchJSON(`/api/2.0/mlflow/metrics/get-history?run_id=${r.info.run_id}&metric_key=${encodeURIComponent(mk)}&downsample=200`)
              .then(d => d.metrics || []).catch(() => [])
          ));
          const allPts = perRun.flatMap(pts => pts);
          if (!allPts.length) continue;
          const presentCount = perRun.filter(pts => pts.length > 0).length;
          const label = presentCount < valid.length ? `<span style="font-size:11px;color:var(--fg-muted)">${presentCount}/${valid.length} runs</span>` : "";
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
            return `<path d="${d}" fill="none" stroke="${COLORS[ci % COLORS.length]}" stroke-width="1.8" />`;
          }).join("");
          const legend = valid.map((r, ci) => perRun[ci].length
            ? `<span style="display:inline-flex;align-items:center;gap:4px;margin-right:10px;font-size:12px"><span style="width:14px;height:3px;background:${COLORS[ci%COLORS.length]};display:inline-block;border-radius:2px"></span>${escapeHTML(runName(r).slice(0,20))}</span>`
            : "").join("");
          chartsHtml += `
            <div class="card" style="padding:8px 12px;margin-bottom:12px">
              <div style="display:flex;align-items:center;gap:8px;margin-bottom:4px">
                <strong>${escapeHTML(mk)}</strong>${label}
              </div>
              <div class="metric-chart">
                <svg width="100%" height="100%" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">${paths}</svg>
              </div>
              <div style="margin-top:4px">${legend}</div>
            </div>`;
        }

        // ── Section 3: Tags ──
        const allTagKeys = [...new Set(valid.flatMap(r => (r.data.tags || []).map(t => t.key)))].sort();
        const tagSection = allTagKeys.length ? `
          <h2>Tags</h2>
          <div class="card" style="padding:0;overflow-x:auto">
            <table>
              <thead><tr><th>Tag</th>${valid.map(r => `<th class="mono">${escapeHTML(runName(r).slice(0,16))}</th>`).join("")}</tr></thead>
              <tbody>
                ${allTagKeys.map(k => {
                  const vals = valid.map(r => { const f=(r.data.tags||[]).find(t=>t.key===k); return f?f.value:"—"; });
                  const differ = new Set(vals).size > 1;
                  return `<tr class="${differ?"diff-row":""}"><td class="mono">${escapeHTML(k)}</td>${vals.map(v=>`<td class="mono">${escapeHTML(v)}</td>`).join("")}</tr>`;
                }).join("")}
              </tbody>
            </table>
          </div>` : "";

        // ── Section 4: Run summary ──
        const summarySection = `
          <h2>Run summary</h2>
          <div class="card" style="padding:0;overflow-x:auto">
            <table>
              <thead><tr><th>Field</th>${valid.map(r=>`<th class="mono">${escapeHTML(runName(r).slice(0,16))}</th>`).join("")}</tr></thead>
              <tbody>
                <tr><td>Status</td>${valid.map(r=>`<td><span class="status-pill status-${r.info.status}">${r.info.status}</span></td>`).join("")}</tr>
                <tr><td>Started</td>${valid.map(r=>`<td>${formatTime(r.info.start_time)}</td>`).join("")}</tr>
                <tr><td>Ended</td>${valid.map(r=>`<td>${r.info.end_time?formatTime(r.info.end_time):"—"}</td>`).join("")}</tr>
                <tr><td>Duration</td>${valid.map(r=>`<td>${r.info.end_time?formatDuration(r.info.end_time-r.info.start_time):"—"}</td>`).join("")}</tr>
                <tr><td>Artifact URI</td>${valid.map(r=>`<td class="mono" style="font-size:11px">${escapeHTML(r.info.artifact_uri||"—")}</td>`).join("")}</tr>
              </tbody>
            </table>
          </div>`;

        const modeSwitcher = `
          <div style="display:flex;gap:8px;margin-bottom:8px;align-items:center">
            <span style="color:var(--fg-muted);font-size:13px">Mode:</span>
            <button id="cmp-mode-full" class="${mode==="full"?"":"btn-ghost"}" style="padding:3px 10px;font-size:12px">Side-by-side</button>
            <button id="cmp-mode-diff" class="${mode==="diff"?"":"btn-ghost"}" style="padding:3px 10px;font-size:12px">Diff</button>
          </div>`;

        main.innerHTML = `
          <div class="crumbs"><a href="#/experiments">Experiments</a> / <a href="#/experiments/${expID}">${escapeHTML(expName)}</a> / Compare</div>
          <div style="display:flex;align-items:center;gap:12px;margin-bottom:12px">
            <h1 style="margin:0">Comparing ${valid.length} run${valid.length===1?"":"s"} from ${escapeHTML(expName)}</h1>
            ${truncated ? `<span class="tag" style="background:rgba(176,123,28,0.12);border-color:var(--warning);color:var(--warning)">Showing first ${MAX_COMPARE} of ${allIds.length}</span>` : ""}
          </div>
          ${modeSwitcher}
          ${mode === "diff" && valid.length === 2 ? diffTextSection() : ""}
          ${paramSection()}
          <h2>Metric history</h2>
          ${chartsHtml || `<div class="empty card">No metrics to compare.</div>`}
          ${tagSection}
          ${summarySection}`;

        // Wire mode toggle buttons
        const setMode = (m) => {
          const hash = location.hash.split("?")[0];
          const p = new URLSearchParams(location.hash.split("?")[1] || "");
          p.set("mode", m);
          p.set("runs", ids.join(","));
          location.hash = hash + "?" + p.toString();
        };
        const btnFull = $("#cmp-mode-full"), btnDiff = $("#cmp-mode-diff");
        if (btnFull) btnFull.addEventListener("click", () => setMode("full"));
        if (btnDiff) btnDiff.addEventListener("click", () => setMode("diff"));

        // Wire "show only differing" checkbox
        const diffCb = $("#cmp-diff-only");
        if (diffCb) {
          diffCb.addEventListener("change", () => {
            const tbody = $("#cmp-params-tbody");
            if (!tbody) return;
            const filterOn = diffCb.checked;
            const rows = buildParamRows(filterOn) || `<tr><td colspan="${valid.length + 1}" class="empty">${filterOn ? "All params identical." : "No params."}</td></tr>`;
            tbody.innerHTML = rows;
            // Sync mode param in URL without re-render
            const hash = location.hash.split("?")[0];
            const p = new URLSearchParams(location.hash.split("?")[1] || "");
            p.set("mode", filterOn ? "diff" : "full");
            p.set("runs", ids.join(","));
            history.replaceState(null, "", location.pathname + location.search + hash + "?" + p.toString());
          });
        }

      } catch (err) {
        main.innerHTML = `<div class="empty">Failed: ${escapeHTML(String(err))}</div>`;
      }
    },

    // ── Run detail ───────────────────────────────────────────────────────────
    async renderRun(expID, runID) {
      const main = $("#app");
      const STARRED_TAG = "lmf.starred";

      try {
        const [data, noteRes, lineageRes] = await Promise.all([
          fetchJSON(`/api/v1/runs/${runID}/data`),
          fetchJSON(`/api/v1/runs/${runID}/note`).catch(() => null),
          fetchJSON(`/api/v1/runs/${runID}/lineage`).catch(() => null),
        ]);
        const params = (data.params || []).map(p => `<tr><td>${escapeHTML(p.Key || p.key)}</td><td class="mono">${escapeHTML(p.Value || p.value)}</td></tr>`).join("");
        const allTags = data.tags || [];
        const isStarred = allTags.some(t => (t.Key || t.key) === STARRED_TAG && (t.Value || t.value) === "true");
        const visibleTags = allTags.filter(t => (t.Key || t.key) !== STARRED_TAG);
        const tags = visibleTags.map(t => `<span class="tag">${escapeHTML(t.Key || t.key)}=${escapeHTML(t.Value || t.value)}</span>`).join(" ");
        const metrics = data.metrics || [];
        const isTrace = data.kind === "trace";
        const starIcon = isStarred ? "&#9733;" : "&#9734;";

        // Build lineage section
        const lineageHTML = this._renderLineage(expID, runID, lineageRes);

        main.innerHTML = `
          <div class="crumbs">
            <a href="#/experiments">Experiments</a> /
            <a href="#/experiments/${expID}">exp ${expID}</a> / ${data.id}
          </div>
          <div style="display:flex;align-items:center;gap:10px;margin-bottom:6px;flex-wrap:wrap">
            <h1 style="margin:0;flex:1;display:flex;align-items:center;gap:8px;flex-wrap:wrap">
              <button id="star-btn" class="btn-ghost star-btn" title="${isStarred ? "Unstar" : "Star"} this run" style="font-size:20px;padding:2px 6px">${starIcon}</button>
              <span id="run-name-display" class="run-name-display">${escapeHTML(data.name || "(unnamed run)")}</span>
              <button id="rename-btn" class="btn-ghost" title="Rename run" style="font-size:13px;padding:2px 6px">&#9998;</button>
              <span class="kind-pill">${data.kind}</span>
              <span class="status-pill status-${data.status}">${data.status}</span>
            </h1>
            <button id="share-btn" class="btn-ghost" style="white-space:nowrap" title="Copy link">🔗 Share</button>
          </div>
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

          ${lineageHTML}

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

        // ── Share button (W7.B) ───────────────────────────────────────────────
        const shareBtn = $("#share-btn");
        if (shareBtn) {
          shareBtn.addEventListener("click", () => {
            navigator.clipboard.writeText(location.href).then(() => {
              showToast("Link copied");
            }).catch(() => {
              prompt("Copy this URL:", location.href);
            });
          });
        }
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

    _renderLineage(expID, runID, lineage) {
      if (!lineage) return "";
      const ancestors = lineage.ancestors || [];
      const descendants = lineage.descendants || [];
      const datasets = lineage.datasets || [];
      if (!ancestors.length && !descendants.length && !datasets.length) return "";

      const runLink = (r) => {
        const rExpID = r.experiment_id || expID;
        const label = escapeHTML(r.name || r.id.slice(0, 8));
        return `<a href="#/experiments/${rExpID}/runs/${r.id}" class="lineage-node">${label} <span class="mono" style="font-size:11px">${r.id.slice(0, 8)}</span></a>`;
      };
      const dsLink = (d) => {
        const label = escapeHTML(d.name) + (d.version ? ` <span class="mono" style="font-size:11px">v${d.version}</span>` : "");
        // Deep-link to the specific version when known, fall back to the
        // dataset detail page (which lands on latest), or the index for
        // legacy v0.3 inputs that have no datasets_v2 mirror.
        let href = "#/datasets";
        if (d.dataset_id && d.version) {
          href = `#/datasets/${encodeURIComponent(d.name)}/v${d.version}`;
        } else if (d.dataset_id) {
          href = `#/datasets/${encodeURIComponent(d.name)}`;
        }
        return `<a href="${href}" class="lineage-node lineage-dataset">${label}</a>`;
      };

      // Build ancestor chain (outermost ancestor first)
      const ancestorChain = ancestors.map(r =>
        `<div class="lineage-item lineage-ancestor">${runLink(r)}</div><div class="lineage-arrow">↓</div>`
      ).join("");

      // Current run node
      const currentNode = `<div class="lineage-item lineage-current"><strong>${escapeHTML(runID.slice(0, 8))}</strong> <span style="color:var(--fg-muted)">(this run)</span></div>`;

      // Children (direct descendants shown as a list)
      const childrenHTML = descendants.length
        ? `<div class="lineage-arrow">↓</div><div class="lineage-children">${descendants.map(r =>
            `<div class="lineage-item lineage-child">${runLink(r)}</div>`
          ).join("")}</div>`
        : "";

      // Inputs strip — datasets the run consumed.
      const datasetsHTML = datasets.length
        ? `<div class="lineage-datasets-strip">
             <span class="u-muted-xs">Inputs:</span>
             ${datasets.map(dsLink).join("")}
           </div>`
        : "";

      return `
        <h2 style="display:flex;align-items:baseline;gap:12px">Lineage
          <a href="#/experiments/${expID}/runs/${runID}/lineage" class="u-muted-xs" style="font-weight:normal;text-decoration:underline">Open full DAG</a>
        </h2>
        <div class="card lineage-tree">
          ${ancestorChain}${currentNode}${childrenHTML}
          ${datasetsHTML}
        </div>`;
    },

    // ── Run lineage DAG (v1.4) ───────────────────────────────────────────────
    //
    // Layered top-down layout: ancestors at top → current → descendants below.
    // Datasets appear as a side strip linked to the current run. Each node
    // is a clickable rect; clicking a run navigates to that run, clicking
    // a dataset opens the dataset detail page.
    async renderRunLineage(expID, runID) {
      const main = $("#app");
      const params = new URLSearchParams(location.hash.split("?")[1] || "");
      const direction = params.get("direction") || "both";
      const depth = clampInt(params.get("depth"), 1, 8, 4);
      const fanout = clampInt(params.get("fanout"), 1, 200, 50);
      let lineage;
      try {
        lineage = await fetchJSON(
          `/api/v1/runs/${runID}/lineage?direction=${direction}&depth=${depth}&fanout=${fanout}`,
        );
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load lineage: ${escapeHTML(String(err))}</div>`;
        return;
      }

      const run = lineage.run || {};
      const ancestors = lineage.ancestors || [];
      const descendants = lineage.descendants || [];
      const datasets = lineage.datasets || [];

      // Build node levels: ancestors top→bottom (deepest ancestor at the
      // top), then current, then descendants grouped by their distance
      // from `run` (BFS order — siblings on the same row).
      // descendants come back flat; reconstruct levels by re-walking the
      // parent_run_id chain via a quick child-of-current map.
      const childrenOf = new Map(); // parentID → [run]
      for (const d of descendants) {
        const arr = childrenOf.get(d.parent_run_id) || [];
        arr.push(d);
        childrenOf.set(d.parent_run_id, arr);
      }
      const descendantLevels = [];
      let frontier = [run.id];
      while (frontier.length) {
        const next = [];
        const levelRuns = [];
        for (const pid of frontier) {
          const kids = childrenOf.get(pid) || [];
          for (const k of kids) {
            levelRuns.push(k);
            next.push(k.id);
          }
        }
        if (!levelRuns.length) break;
        descendantLevels.push(levelRuns);
        frontier = next;
      }

      const levels = [];
      // Ancestors: top is deepest ancestor, bottom-most is run's direct parent.
      const ancestorsTopFirst = ancestors.slice().reverse();
      for (const a of ancestorsTopFirst) levels.push([a]);
      levels.push([run]);
      for (const lvl of descendantLevels) levels.push(lvl);

      // SVG layout
      const nodeW = 180, nodeH = 44, gapX = 24, gapY = 60;
      const widest = Math.max(...levels.map(l => l.length));
      const svgW = Math.max(800, widest * (nodeW + gapX) + gapX);
      const svgH = levels.length * (nodeH + gapY) + 40 + (datasets.length ? 70 : 0);

      // Position lookup: id → {x,y,run}
      const pos = new Map();
      levels.forEach((lvl, i) => {
        const lvlW = lvl.length * nodeW + (lvl.length - 1) * gapX;
        const startX = (svgW - lvlW) / 2;
        const y = 20 + i * (nodeH + gapY);
        lvl.forEach((r, j) => {
          pos.set(r.id, { x: startX + j * (nodeW + gapX), y, run: r });
        });
      });

      // Edges: parent_run_id → child for everything that has both endpoints
      // on the canvas.
      const edges = [];
      for (const [, p] of pos) {
        const r = p.run;
        if (r.parent_run_id && pos.has(r.parent_run_id)) {
          const parent = pos.get(r.parent_run_id);
          edges.push({
            x1: parent.x + nodeW / 2, y1: parent.y + nodeH,
            x2: p.x + nodeW / 2,      y2: p.y,
          });
        }
      }
      // Edges from current run to datasets, drawn as a strip below.
      const dsY = 20 + levels.length * (nodeH + gapY);
      const dsW = 160, dsH = 32, dsGap = 12;
      const dsStripW = datasets.length * dsW + (datasets.length - 1) * dsGap;
      const dsStartX = (svgW - dsStripW) / 2;
      const currentPos = pos.get(run.id);

      // Use the project-wide escapeHTML for both element-content and
      // attribute contexts — it handles &, <, >, ", and ' uniformly.
      const escAttr = s => escapeHTML(s);
      const renderRunNode = (r) => {
        const p = pos.get(r.id);
        const isCurrent = r.id === run.id;
        const label = (r.name || r.id.slice(0, 8));
        const sub = r.id.slice(0, 8);
        const cls = isCurrent ? "lin-node lin-node-current" : "lin-node";
        const targetExp = r.experiment_id || expID;
        return `
          <g class="${cls}" data-href="#/experiments/${targetExp}/runs/${r.id}" tabindex="0" role="link" aria-label="Open run ${escAttr(label)}">
            <rect x="${p.x}" y="${p.y}" width="${nodeW}" height="${nodeH}" rx="6"></rect>
            <text x="${p.x + 12}" y="${p.y + 18}" class="lin-node-label">${escapeHTML(label)}</text>
            <text x="${p.x + 12}" y="${p.y + 34}" class="lin-node-sub">${sub}${isCurrent ? " · this run" : ""}</text>
          </g>`;
      };
      const renderDataset = (d, i) => {
        const x = dsStartX + i * (dsW + dsGap);
        let href = "#/datasets";
        if (d.dataset_id && d.version) {
          href = `#/datasets/${encodeURIComponent(d.name)}/v${d.version}`;
        } else if (d.dataset_id) {
          href = `#/datasets/${encodeURIComponent(d.name)}`;
        }
        const label = d.name + (d.version ? ` v${d.version}` : "");
        return `
          <g class="lin-node lin-node-dataset" data-href="${href}" tabindex="0" role="link" aria-label="Open dataset ${escAttr(d.name)}">
            <rect x="${x}" y="${dsY}" width="${dsW}" height="${dsH}" rx="6"></rect>
            <text x="${x + 10}" y="${dsY + 20}" class="lin-node-label">${escapeHTML(label)}</text>
          </g>`;
      };
      const dsEdges = datasets.map((_, i) => {
        const x = dsStartX + i * (dsW + dsGap) + dsW / 2;
        return `<line class="lin-edge lin-edge-dataset" x1="${currentPos.x + nodeW / 2}" y1="${currentPos.y + nodeH}" x2="${x}" y2="${dsY}" />`;
      }).join("");

      main.innerHTML = `
        <div class="toolbar">
          <h1 class="u-toolbar-title">Lineage · <span class="mono">${runID.slice(0, 8)}</span></h1>
          <a href="#/experiments/${expID}/runs/${runID}" class="btn-ghost">Back to run</a>
        </div>
        <div class="card" style="padding:12px;display:flex;gap:14px;align-items:center;flex-wrap:wrap">
          <label class="u-muted-xs">Direction
            <select id="lin-direction">
              <option value="both"${direction === "both" ? " selected" : ""}>both</option>
              <option value="upstream"${direction === "upstream" ? " selected" : ""}>upstream</option>
              <option value="downstream"${direction === "downstream" ? " selected" : ""}>downstream</option>
            </select>
          </label>
          <label class="u-muted-xs">Depth
            <input id="lin-depth" type="number" min="1" max="8" value="${depth}" style="width:60px"/>
          </label>
          <label class="u-muted-xs" title="Per-level fan-out cap (1..200)">Fanout
            <input id="lin-fanout" type="number" min="1" max="200" value="${fanout}" style="width:70px"/>
          </label>
          ${lineage.truncated ? `<span class="status-pill status-FAILED" title="Increase depth or fanout to see more">truncated</span>` : ""}
          <span class="u-muted-xs" style="margin-left:auto">${ancestors.length} ancestor${ancestors.length === 1 ? "" : "s"} · ${descendants.length} descendant${descendants.length === 1 ? "" : "s"} · ${datasets.length} dataset${datasets.length === 1 ? "" : "s"}</span>
        </div>
        <div class="card" style="margin-top:12px;padding:0;overflow:auto">
          <svg class="lineage-svg" width="${svgW}" height="${svgH}" viewBox="0 0 ${svgW} ${svgH}">
            <defs>
              <marker id="lin-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="6" markerHeight="6" orient="auto">
                <path d="M0,0 L8,4 L0,8 z" fill="currentColor"/>
              </marker>
            </defs>
            ${edges.map(e => `<line class="lin-edge" x1="${e.x1}" y1="${e.y1}" x2="${e.x2}" y2="${e.y2}" marker-end="url(#lin-arrow)"/>`).join("")}
            ${dsEdges}
            ${levels.flat().map(renderRunNode).join("")}
            ${datasets.map(renderDataset).join("")}
          </svg>
        </div>`;

      // Click + keyboard navigation on nodes.
      const navigate = el => {
        const href = el.dataset.href;
        if (href) location.hash = href;
      };
      $$(".lin-node", main).forEach(el => {
        el.addEventListener("click", () => navigate(el));
        el.addEventListener("keydown", e => {
          if (e.key === "Enter" || e.key === " ") { e.preventDefault(); navigate(el); }
        });
      });

      // Direction / depth / fanout controls re-route with new query params.
      const reroute = () => {
        const dir = $("#lin-direction").value;
        const dep = clampInt($("#lin-depth").value, 1, 8, 4);
        const fan = clampInt($("#lin-fanout").value, 1, 200, 50);
        location.hash = `#/experiments/${expID}/runs/${runID}/lineage?direction=${dir}&depth=${dep}&fanout=${fan}`;
      };
      $("#lin-direction").addEventListener("change", reroute);
      $("#lin-depth").addEventListener("change", reroute);
      $("#lin-fanout").addEventListener("change", reroute);
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
      let prompts = [];
      try {
        const data = await fetchJSON("/api/v1/prompts");
        prompts = data.prompts || [];
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load prompts: ${escapeHTML(String(err))}</div>`;
        return;
      }

      const rows = prompts.map((p, i) => `
        <tr data-row-index="${i}" data-prompt-name="${escapeHTML(p.name)}">
          <td><a href="#/prompts/${encodeURIComponent(p.name)}">${escapeHTML(p.name)}</a></td>
          <td class="mono">v${escapeHTML(String(p.version))}</td>
          <td>${escapeHTML(p.description || "—")}</td>
          <td>${formatTime(p.created_at)}</td>
        </tr>`).join("");

      main.innerHTML = `
        <div class="toolbar">
          <h1 style="margin:0">Prompts</h1>
          <button id="new-prompt-btn" class="btn-primary" title="Register a new prompt">+ New prompt</button>
        </div>
        <p style="color:var(--fg-muted);margin-top:0">
          Versioned, content-addressed text snippets. Aliases like <code>production</code> or <code>candidate</code>
          pin a stable version while you iterate.
          <a href="/docs/cookbook.md" target="_blank" rel="noopener">See cookbook</a>.
        </p>
        ${prompts.length ? `
        <div class="card" style="padding:0">
          <table>
            <thead><tr><th>Name</th><th>Latest</th><th>Description</th><th>Created</th></tr></thead>
            <tbody>${rows}</tbody>
          </table>
        </div>` : `
        <div class="empty card">
          <p>No prompts yet. Register one with the <strong>+ New prompt</strong> button, or programmatically:</p>
          <pre>from litemlflow import Client
c = Client("${location.origin}")
c.create_prompt("rag.system", "You are a helpful assistant.", description="seed v1")</pre>
        </div>`}`;

      $("#new-prompt-btn").addEventListener("click", () => this._showPromptCreateModal());
      $$("tr[data-prompt-name]", main).forEach(tr => {
        tr.style.cursor = "pointer";
        tr.addEventListener("click", e => {
          if (e.target.tagName === "A") return;
          location.hash = `#/prompts/${encodeURIComponent(tr.dataset.promptName)}`;
        });
      });
    },

    _showPromptCreateModal(prefillName) {
      const wrap = document.createElement("div");
      wrap.className = "modal-backdrop";
      wrap.innerHTML = `
        <div class="card modal" style="max-width:600px">
          <h2 style="margin-top:0">Register a new prompt</h2>
          <p style="color:var(--fg-muted);font-size:13px;margin-top:0">
            The prompt is content-addressed: re-registering identical content under the same name reuses the version.
          </p>
          <table class="form-table">
            <tr>
              <th><label for="np-name">Name</label></th>
              <td><input type="text" id="np-name" placeholder="e.g. rag.system" style="width:100%" value="${escapeHTML(prefillName || "")}"/></td>
            </tr>
            <tr>
              <th><label for="np-content">Content</label></th>
              <td><textarea id="np-content" rows="8" placeholder="You are a helpful assistant." style="width:100%;font-family:var(--mono);font-size:13px"></textarea></td>
            </tr>
            <tr>
              <th><label for="np-desc">Description (optional)</label></th>
              <td><input type="text" id="np-desc" placeholder="What is this prompt for?" style="width:100%"/></td>
            </tr>
          </table>
          <div id="np-err" style="color:var(--error);min-height:18px;font-size:13px"></div>
          <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px">
            <button id="np-cancel">Cancel</button>
            <button id="np-save" class="btn-primary">Register</button>
          </div>
        </div>`;
      document.body.appendChild(wrap);
      $("#np-name").focus();

      const close = () => wrap.remove();
      wrap.addEventListener("click", e => { if (e.target === wrap) close(); });
      $("#np-cancel").addEventListener("click", close);
      const submit = async () => {
        const name = $("#np-name").value.trim();
        const content = $("#np-content").value;
        const description = $("#np-desc").value.trim();
        if (!name) { $("#np-err").textContent = "Name is required."; return; }
        if (!content) { $("#np-err").textContent = "Content is required."; return; }
        try {
          await fetchJSON("/api/v1/prompts", {
            method: "POST",
            body: JSON.stringify({ name, content, description }),
          });
          showToast(`Registered prompt ${name}`);
          close();
          location.hash = `#/prompts/${encodeURIComponent(name)}`;
        } catch (err) {
          $("#np-err").textContent = String(err);
        }
      };
      $("#np-save").addEventListener("click", submit);
      $("#np-name").addEventListener("keydown", e => { if (e.key === "Enter") $("#np-content").focus(); });
      $("#np-content").addEventListener("keydown", e => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) submit(); });
    },

    _registerPrompt(name) {
      // Legacy localStorage helper kept for renderPromptDetail; with the new
      // list endpoint we no longer need to track names client-side, but the
      // helper is harmless and avoids breaking older bookmarks.
      const stored = localStorage.getItem("litemlflow.knownPrompts");
      let known = [];
      try { known = JSON.parse(stored) || []; } catch {}
      if (!Array.isArray(known)) known = [];
      if (!known.includes(name)) {
        known.push(name);
        localStorage.setItem("litemlflow.knownPrompts", JSON.stringify(known));
      }
    },

    // ── Project management (lmf.project tag) ──────────────────────────────────
    _showNewProjectModal(exps, knownProjects) {
      const wrap = document.createElement("div");
      wrap.className = "modal-backdrop";
      const expOpts = (exps || []).map(e => `
        <label style="display:block;padding:2px 0">
          <input type="checkbox" data-exp-id="${e.experiment_id}" />
          <span class="mono" style="color:var(--fg-muted)">#${e.experiment_id}</span>
          ${escapeHTML(e.name)}
        </label>`).join("");
      wrap.innerHTML = `
        <div class="card modal" style="max-width:520px">
          <h2 style="margin-top:0">New project</h2>
          <p style="color:var(--fg-muted);font-size:13px;margin:0 0 12px">
            A project is just a label that groups experiments. Existing projects: ${
              knownProjects.length ? knownProjects.map(p => `<code>${escapeHTML(p)}</code>`).join(", ") : "<em>none yet</em>"
            }
          </p>
          <table class="form-table">
            <tr>
              <th><label for="np-proj-name">Name</label></th>
              <td><input type="text" id="np-proj-name" placeholder="e.g. Search Quality" style="width:100%"/></td>
            </tr>
            <tr>
              <th>Assign experiments</th>
              <td>
                <div style="max-height:200px;overflow-y:auto;border:1px solid var(--border);padding:8px;border-radius:6px">
                  ${expOpts || `<div class="empty">No experiments to assign yet.</div>`}
                </div>
                <div style="font-size:12px;color:var(--fg-muted);margin-top:4px">
                  You can assign more experiments later via the Move… button on each row.
                </div>
              </td>
            </tr>
          </table>
          <div id="np-proj-err" style="color:var(--error);min-height:18px;font-size:13px"></div>
          <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px">
            <button id="np-proj-cancel">Cancel</button>
            <button id="np-proj-save" class="btn-primary">Create project</button>
          </div>
        </div>`;
      document.body.appendChild(wrap);
      $("#np-proj-name").focus();

      const close = () => wrap.remove();
      wrap.addEventListener("click", e => { if (e.target === wrap) close(); });
      $("#np-proj-cancel").addEventListener("click", close);

      const submit = async () => {
        const name = $("#np-proj-name").value.trim();
        if (!name) { $("#np-proj-err").textContent = "Project name is required."; return; }
        const ids = $$("input[data-exp-id]:checked", wrap).map(cb => cb.dataset.expId);
        try {
          // Set lmf.project tag on each selected experiment.
          for (const id of ids) {
            await this._setExperimentProject(id, name);
          }
          showToast(ids.length
            ? `Created project ${name} with ${ids.length} experiment${ids.length === 1 ? "" : "s"}`
            : `Created project ${name} (no experiments yet — use Move… on a row to add some)`);
          close();
          App.renderExperiments();
        } catch (err) {
          $("#np-proj-err").textContent = String(err);
        }
      };
      $("#np-proj-save").addEventListener("click", submit);
      $("#np-proj-name").addEventListener("keydown", e => { if (e.key === "Enter") submit(); });
    },

    _showMoveProjectModal(expID, currentProj, knownProjects) {
      const wrap = document.createElement("div");
      wrap.className = "modal-backdrop";
      const opts = ['<option value="">— No project —</option>']
        .concat(knownProjects.map(p =>
          `<option value="${escapeHTML(p)}" ${p === currentProj ? "selected" : ""}>${escapeHTML(p)}</option>`))
        .concat(['<option value="__new__">+ Create new project…</option>'])
        .join("");
      wrap.innerHTML = `
        <div class="card modal" style="max-width:420px">
          <h2 style="margin-top:0">Move experiment to project</h2>
          <table class="form-table">
            <tr>
              <th><label for="mv-proj">Project</label></th>
              <td><select id="mv-proj" style="width:100%">${opts}</select></td>
            </tr>
            <tr id="mv-newrow" style="display:none">
              <th><label for="mv-newname">New name</label></th>
              <td><input type="text" id="mv-newname" placeholder="e.g. Sales Forecast" style="width:100%"/></td>
            </tr>
          </table>
          <div id="mv-err" style="color:var(--error);min-height:18px;font-size:13px"></div>
          <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px">
            <button id="mv-cancel">Cancel</button>
            <button id="mv-save" class="btn-primary">Apply</button>
          </div>
        </div>`;
      document.body.appendChild(wrap);

      const close = () => wrap.remove();
      wrap.addEventListener("click", e => { if (e.target === wrap) close(); });
      $("#mv-cancel").addEventListener("click", close);
      $("#mv-proj").addEventListener("change", () => {
        const isNew = $("#mv-proj").value === "__new__";
        $("#mv-newrow").style.display = isNew ? "" : "none";
        if (isNew) $("#mv-newname").focus();
      });
      $("#mv-save").addEventListener("click", async () => {
        let target = $("#mv-proj").value;
        if (target === "__new__") {
          target = $("#mv-newname").value.trim();
          if (!target) { $("#mv-err").textContent = "Enter a name for the new project."; return; }
        }
        try {
          await this._setExperimentProject(expID, target);
          showToast(target ? `Moved to ${target}` : "Removed from project");
          close();
          App.renderExperiments();
        } catch (err) {
          $("#mv-err").textContent = String(err);
        }
      });
    },

    async _setExperimentProject(experimentID, projectName) {
      // MLflow doesn't have a public delete-experiment-tag endpoint; setting
      // the value to "" is treated as unassigned by the rendering code
      // (projectOf falls back to "" when the value is empty).
      return fetchJSON("/api/2.0/mlflow/experiments/set-experiment-tag", {
        method: "POST",
        body: JSON.stringify({
          experiment_id: String(experimentID),
          key: "lmf.project",
          value: projectName || "",
        }),
      });
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
      // T4.22: workspace UI is gated on the multi_tenant feature flag. Solo
      // MLE deploys (the hero use case) get this hidden by default; flip on
      // via --enable-multi-tenant when a real ≥2-workspace setup arrives.
      if (!Features.multiTenant()) {
        main.innerHTML = `
          <h1>Workspaces</h1>
          <div class="empty card">
            <p>The workspace UI is disabled on this deployment.</p>
            <p class="u-muted-sm">Re-run the server with <code>--enable-multi-tenant</code> (or
            <code>LITEMLFLOW_ENABLE_MULTI_TENANT=1</code>) to manage workspaces and members.</p>
          </div>`;
        return;
      }
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
          if (!await Modal.confirm({
            title: "Remove member",
            message: `Remove ${userID} from workspace ${wsID}?`,
            danger: true, primaryLabel: "Remove",
          })) return;
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

    // ── Webhooks ─────────────────────────────────────────────────────────────
    async renderWebhooks() {
      const main = $("#app");
      const ALL_EVENTS = ["run_started", "run_finished", "run_failed", "run_killed"];

      const load = async () => {
        const data = await fetchJSON("/api/v1/webhooks").catch(() => ({ webhooks: [] }));
        return data.webhooks || [];
      };
      const loadEchoLog = async () => {
        try {
          const data = await fetchJSON("/api/v1/webhooks/echo?max=20");
          return data.entries || [];
        } catch { return []; }
      };

      const renderPage = async () => {
        const [webhooks, echoEntries] = await Promise.all([load(), loadEchoLog()]);
        const hasEcho = webhooks.some(w => (w.url || "").startsWith("lmf://echo"));

        const rows = webhooks.map((wh, i) => {
          const evBadges = (wh.events || "").split(",").filter(Boolean)
            .map(e => `<span class="tag">${escapeHTML(e.trim())}</span>`).join(" ");
          const statusClass = wh.last_status >= 200 && wh.last_status < 300
            ? "status-FINISHED" : wh.last_status ? "status-FAILED" : "";
          const statusText = wh.last_status ? String(wh.last_status) : "—";
          return `
            <tr data-row-index="${i}" data-wh-id="${wh.id}">
              <td class="mono">${wh.id}</td>
              <td>${escapeHTML(wh.name)}</td>
              <td class="mono" style="max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${escapeHTML(wh.url)}">${escapeHTML(wh.url)}</td>
              <td>${evBadges || "—"}</td>
              <td><span class="status-pill ${statusClass}">${statusText}</span></td>
              <td>
                <label class="toggle-label">
                  <input type="checkbox" class="wh-enabled" data-wh-id="${wh.id}" ${wh.enabled ? "checked" : ""} />
                  <span>${wh.enabled ? "on" : "off"}</span>
                </label>
              </td>
              <td class="wh-actions">
                <button class="wh-test-btn" data-wh-id="${wh.id}" title="Send test delivery">Test</button>
                <button class="wh-edit-btn" data-wh-id="${wh.id}" title="Edit webhook">Edit</button>
                <button class="wh-del-btn btn-danger" data-wh-id="${wh.id}" title="Delete webhook">Delete</button>
              </td>
            </tr>`;
        }).join("");

        const echoRows = echoEntries.map(e => {
          const t = formatTime(e.timestamp);
          let preview = "";
          try {
            const obj = JSON.parse(e.body);
            preview = obj.run ? `run ${obj.run.id} (${obj.run.status})` : "—";
          } catch { preview = (e.body || "").slice(0, 80); }
          return `
            <div class="wh-deliveries-row">
              <span class="mono">${t}</span>
              <span class="tag">${escapeHTML(e.event)}</span>
              <span class="mono">#${e.webhook_id}</span>
              <span style="color:var(--fg-muted)">${escapeHTML(preview)}</span>
            </div>`;
        }).join("");

        main.innerHTML = `
          <div class="toolbar">
            <h1 style="margin:0">Webhooks</h1>
            <button id="wh-add-btn" class="btn-primary">+ Add webhook</button>
            ${hasEcho ? "" : `<button id="wh-demo-btn" title="Create a built-in echo webhook so you can see deliveries fire end-to-end">+ Try the demo (echo)</button>`}
          </div>
          <p style="color:var(--fg-muted);margin-top:0;font-size:13px">
            Get notified when a run starts, finishes, fails, or is killed. Deliveries are HMAC-SHA256-signed
            (header <code>X-LiteMLflow-Signature</code>) and retried up to 3× with exponential backoff.
            Use <code>lmf://echo</code> as the URL to record deliveries in-process for testing.
          </p>

          <div class="card" style="padding:0;margin-bottom:20px">
            <table>
              <thead>
                <tr>
                  <th style="width:40px">ID</th>
                  <th>Name</th>
                  <th>URL</th>
                  <th>Events</th>
                  <th>Last status</th>
                  <th>Enabled</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>${rows || `<tr><td colspan="7" class="empty">No webhooks configured.</td></tr>`}</tbody>
            </table>
          </div>

          <div class="card" style="margin-bottom:20px">
            <h2 style="margin-top:0;font-size:14px">Recent deliveries to lmf://echo
              <span style="color:var(--fg-muted);font-size:12px;font-weight:normal">(in-process ring buffer; cleared on restart)</span>
              <button id="wh-echo-refresh" style="float:right;font-size:12px">↻ Refresh</button>
            </h2>
            <div id="wh-deliveries-list" class="wh-deliveries">
              ${echoRows || `<div class="empty" style="padding:8px 0">No echo deliveries yet. Click "+ Try the demo" or send a Test from a webhook with URL <code>lmf://echo</code>.</div>`}
            </div>
          </div>

          <div id="wh-form-panel" class="card" style="display:none">
            <h2 id="wh-form-title" style="margin-top:0">Add webhook</h2>
            <div class="kv-table" style="margin-bottom:12px">
              <table>
                <tr>
                  <td>Name</td>
                  <td><input type="text" id="wh-name" placeholder="My webhook" style="width:100%" /></td>
                </tr>
                <tr>
                  <td>URL</td>
                  <td><input type="url" id="wh-url" placeholder="https://hooks.example.com/…" style="width:100%" /></td>
                </tr>
                <tr>
                  <td>Events</td>
                  <td id="wh-events-cell">
                    ${ALL_EVENTS.map(ev =>
                      `<label style="margin-right:12px;display:inline-flex;align-items:center;gap:4px">
                        <input type="checkbox" class="wh-ev-cb" value="${ev}" checked /> ${escapeHTML(ev)}
                      </label>`
                    ).join("")}
                  </td>
                </tr>
                <tr>
                  <td>Secret <span style="color:var(--fg-muted);font-size:11px">(optional)</span></td>
                  <td><input type="password" id="wh-secret" placeholder="HMAC signing secret" style="width:100%" /></td>
                </tr>
                <tr>
                  <td>Enabled</td>
                  <td><input type="checkbox" id="wh-enabled-cb" checked /></td>
                </tr>
              </table>
            </div>
            <div id="wh-form-err" style="color:var(--error);font-size:13px;margin-bottom:8px"></div>
            <div style="display:flex;gap:8px">
              <button id="wh-save-btn">Save</button>
              <button id="wh-cancel-btn" class="btn-ghost">Cancel</button>
            </div>
          </div>`;

        // State for edit mode
        let editID = null;

        const showForm = (wh) => {
          editID = wh ? wh.id : null;
          $("#wh-form-title").textContent = wh ? "Edit webhook" : "Add webhook";
          $("#wh-name").value = wh ? wh.name : "";
          $("#wh-url").value = wh ? wh.url : "";
          $("#wh-secret").value = "";  // never pre-fill secret
          $("#wh-enabled-cb").checked = wh ? wh.enabled : true;
          // Set event checkboxes
          const selectedEvents = new Set((wh ? wh.events : ALL_EVENTS.join(",")).split(",").map(s => s.trim()));
          $$(".wh-ev-cb", main).forEach(cb => {
            cb.checked = selectedEvents.has(cb.value);
          });
          $("#wh-form-err").textContent = "";
          $("#wh-form-panel").style.display = "";
          $("#wh-name").focus();
        };

        const hideForm = () => {
          editID = null;
          $("#wh-form-panel").style.display = "none";
        };

        const saveWebhook = async () => {
          const name = $("#wh-name").value.trim();
          const url = $("#wh-url").value.trim();
          const events = $$(".wh-ev-cb", main).filter(cb => cb.checked).map(cb => cb.value).join(",");
          const secret = $("#wh-secret").value;
          const enabled = $("#wh-enabled-cb").checked;

          if (!name || !url) {
            $("#wh-form-err").textContent = "Name and URL are required.";
            return;
          }
          if (!events) {
            $("#wh-form-err").textContent = "Select at least one event.";
            return;
          }

          const body = { name, url, events, enabled };
          if (secret) body.secret = secret;

          try {
            if (editID != null) {
              await fetchJSON(`/api/v1/webhooks/${editID}`, {
                method: "PATCH",
                body: JSON.stringify(body),
              });
            } else {
              await fetchJSON("/api/v1/webhooks", {
                method: "POST",
                body: JSON.stringify(body),
              });
            }
            hideForm();
            renderPage();
          } catch (err) {
            $("#wh-form-err").textContent = `Error: ${err}`;
          }
        };

        // Wire buttons
        $("#wh-add-btn").addEventListener("click", () => showForm(null));
        $("#wh-save-btn").addEventListener("click", saveWebhook);
        $("#wh-cancel-btn").addEventListener("click", hideForm);
        $("#wh-echo-refresh")?.addEventListener("click", () => renderPage());

        // One-click demo webhook (lmf://echo target)
        const demoBtn = $("#wh-demo-btn");
        if (demoBtn) {
          demoBtn.addEventListener("click", async () => {
            demoBtn.disabled = true;
            demoBtn.textContent = "Creating…";
            try {
              const created = await fetchJSON("/api/v1/webhooks", {
                method: "POST",
                body: JSON.stringify({
                  name: "Demo (echo)",
                  url: "lmf://echo",
                  events: ALL_EVENTS.join(","),
                  enabled: true,
                }),
              });
              // Immediately fire a synthetic delivery so the user sees it.
              await fetchJSON(`/api/v1/webhooks/${created.id}/test`, { method: "POST", body: "{}" });
              showToast("Demo webhook created and a test event fired.");
              renderPage();
            } catch (err) {
              alert(`Failed: ${err}`);
              demoBtn.disabled = false;
              demoBtn.textContent = "+ Try the demo (echo)";
            }
          });
        }

        // Enable/disable toggles
        $$(".wh-enabled", main).forEach(cb => {
          cb.addEventListener("change", async () => {
            const id = cb.dataset.whId;
            const label = cb.nextElementSibling;
            try {
              await fetchJSON(`/api/v1/webhooks/${id}`, {
                method: "PATCH",
                body: JSON.stringify({ enabled: cb.checked }),
              });
              if (label) label.textContent = cb.checked ? "on" : "off";
            } catch (err) {
              cb.checked = !cb.checked;  // revert
              alert(`Failed to update: ${err}`);
            }
          });
        });

        // Test buttons
        $$(".wh-test-btn", main).forEach(btn => {
          btn.addEventListener("click", async () => {
            const id = btn.dataset.whId;
            btn.disabled = true;
            btn.textContent = "…";
            try {
              const res = await fetchJSON(`/api/v1/webhooks/${id}/test`, { method: "POST", body: "{}" });
              btn.textContent = res.status >= 200 && res.status < 300 ? "OK" : `${res.status}`;
              btn.style.color = res.status >= 200 && res.status < 300 ? "var(--success)" : "var(--error)";
            } catch {
              btn.textContent = "err";
              btn.style.color = "var(--error)";
            }
            setTimeout(() => {
              btn.disabled = false;
              btn.textContent = "Test";
              btn.style.color = "";
            }, 3000);
          });
        });

        // Edit buttons — fetch fresh data to populate form
        $$(".wh-edit-btn", main).forEach(btn => {
          btn.addEventListener("click", async () => {
            const id = btn.dataset.whId;
            try {
              const wh = await fetchJSON(`/api/v1/webhooks/${id}`);
              showForm(wh);
            } catch {
              showForm(webhooks.find(w => String(w.id) === String(id)));
            }
          });
        });

        // Delete buttons
        $$(".wh-del-btn", main).forEach(btn => {
          btn.addEventListener("click", async () => {
            const id = btn.dataset.whId;
            if (!await Modal.confirm({
              title: "Delete webhook",
              message: "Delete this webhook? Pending deliveries will be lost.",
              danger: true, primaryLabel: "Delete",
            })) return;
            try {
              await fetch(`/api/v1/webhooks/${id}`, {
                method: "DELETE",
                headers: Object.assign({ "Content-Type": "application/json" }, Workspace.header()),
              });
              renderPage();
            } catch (err) {
              alert(`Failed: ${err}`);
            }
          });
        });
      };

      renderPage();
    },

    // ── Datasets (v1.2) ──────────────────────────────────────────────────────
    async renderDatasetsIndex() {
      const main = $("#app");
      main.innerHTML = `<div class="loading">Loading datasets…</div>`;
      try {
        const data = await fetchJSON("/api/v1/datasets");
        const items = data.datasets || [];
        const rows = items.map(d => `
          <tr data-row-index="${escapeHTML(d.name)}" data-ds-name="${escapeHTML(d.name)}">
            <td><a href="#/datasets/${encodeURIComponent(d.name)}">${escapeHTML(d.name)}</a></td>
            <td class="mono">v${d.version}</td>
            <td class="numeric mono">${formatBytes(d.size_bytes)}</td>
            <td class="mono" style="font-size:11px;color:var(--fg-muted)">${escapeHTML(d.content_hash.slice(0, 12))}…</td>
            <td>${formatTime(d.created_at)}</td>
            <td>${escapeHTML(d.description || "—")}</td>
          </tr>`).join("");
        main.innerHTML = `
          <div class="toolbar">
            <h1 style="margin:0">Datasets</h1>
            <button id="ds-upload-btn" class="btn-primary">+ Upload dataset</button>
          </div>
          <p style="color:var(--fg-muted);margin-top:0;font-size:13px">
            Versioned, content-addressed. Re-uploading the same bytes under any name reuses the existing physical file. Each version has explicit lineage to its parents — useful for tracking joins, splits, and cleaning steps.
          </p>
          ${items.length ? `
          <div class="card" style="padding:0">
            <table>
              <thead><tr><th>Name</th><th>Latest</th><th>Size</th><th>Hash</th><th>Created</th><th>Description</th></tr></thead>
              <tbody>${rows}</tbody>
            </table>
          </div>` : `
          <div class="empty card">
            <p>No datasets yet. Click <strong>+ Upload dataset</strong>, or push from Python:</p>
            <pre>import requests
requests.post("${location.origin}/api/v1/datasets/my-dataset/versions",
              files={"file": open("data.csv", "rb")},
              data={"meta": '{"description": "first upload"}'})</pre>
          </div>`}`;

        $("#ds-upload-btn").addEventListener("click", () => this._showDatasetUploadModal());
        $$("tr[data-ds-name]", main).forEach(tr => {
          tr.style.cursor = "pointer";
          tr.addEventListener("click", e => {
            if (e.target.tagName === "A") return;
            location.hash = `#/datasets/${encodeURIComponent(tr.dataset.dsName)}`;
          });
        });
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    async renderDatasetDetail(name) {
      const main = $("#app");
      main.innerHTML = `<div class="loading">Loading ${escapeHTML(name)}…</div>`;
      try {
        const data = await fetchJSON(`/api/v1/datasets/${encodeURIComponent(name)}`);
        const versions = data.versions || [];
        if (versions.length === 0) {
          main.innerHTML = `
            <div class="crumbs"><a href="#/datasets">Datasets</a> / ${escapeHTML(name)}</div>
            <div class="empty card">No versions for this dataset (it may have been deleted).</div>`;
          return;
        }
        const latest = versions[0];

        // Lineage of the latest version (newer versions on top of the table).
        let lineage = null;
        try {
          lineage = await fetchJSON(`/api/v1/datasets/${encodeURIComponent(name)}/versions/${latest.version}/lineage`);
        } catch { /* tolerate */ }

        const rows = versions.map(v => `
          <tr data-row-index="${v.version}">
            <td class="mono">v${v.version}</td>
            <td><span class="status-pill status-${v.lifecycle_stage === "active" ? "FINISHED" : "KILLED"}">${escapeHTML(v.lifecycle_stage)}</span></td>
            <td class="numeric mono">${formatBytes(v.size_bytes)}</td>
            <td class="mono" style="font-size:11px;color:var(--fg-muted)" title="${escapeHTML(v.content_hash)}">${escapeHTML(v.content_hash.slice(0, 16))}…</td>
            <td>${formatTime(v.created_at)}</td>
            <td>${escapeHTML(v.description || "—")}</td>
            <td>
              ${v.size_bytes > 0 ? `<a href="/api/v1/datasets/${encodeURIComponent(name)}/versions/${v.version}/content" download="${escapeHTML(name)}-v${v.version}">Download</a>` : `<span style="color:var(--fg-muted)">no bytes</span>`}
              ${v.lifecycle_stage === "active" ? ` · <button class="ds-del-btn btn-ghost" data-version="${v.version}" style="font-size:11px;padding:2px 8px">Delete</button>` : ""}
            </td>
          </tr>`).join("");

        const ancestors = (lineage && lineage.ancestors) || [];
        const descendants = (lineage && lineage.descendants) || [];
        const lineageHTML = (ancestors.length || descendants.length) ? `
          <h2>Lineage (latest version v${latest.version})</h2>
          <div class="card" style="padding:14px">
            ${ancestors.length ? `<div><strong>Ancestors (${ancestors.length})</strong>
              <ul style="margin:6px 0">
                ${ancestors.map(a => `<li><a href="#/datasets/${encodeURIComponent(a.name)}">${escapeHTML(a.name)} v${a.version}</a></li>`).join("")}
              </ul></div>` : ""}
            ${descendants.length ? `<div style="margin-top:10px"><strong>Descendants (${descendants.length})</strong>
              <ul style="margin:6px 0">
                ${descendants.map(d => `<li><a href="#/datasets/${encodeURIComponent(d.name)}">${escapeHTML(d.name)} v${d.version}</a></li>`).join("")}
              </ul></div>` : ""}
          </div>` : "";

        main.innerHTML = `
          <div class="crumbs"><a href="#/datasets">Datasets</a> / ${escapeHTML(name)}</div>
          <div class="toolbar">
            <h1 style="margin:0">${escapeHTML(name)}</h1>
            <span style="color:var(--fg-muted);font-size:13px">${versions.length} version${versions.length === 1 ? "" : "s"}</span>
            <button id="ds-upload-new-version" class="btn-primary">+ New version</button>
          </div>
          <div class="card" style="padding:0;margin-bottom:18px">
            <table>
              <thead><tr><th>Version</th><th>Status</th><th>Size</th><th>Hash</th><th>Created</th><th>Description</th><th>Actions</th></tr></thead>
              <tbody>${rows}</tbody>
            </table>
          </div>
          ${lineageHTML}`;

        $("#ds-upload-new-version").addEventListener("click", () => this._showDatasetUploadModal(name));
        $$(".ds-del-btn", main).forEach(b => b.addEventListener("click", async (e) => {
          e.stopPropagation();
          const v = b.dataset.version;
          if (!await Modal.confirm({
            title: "Delete dataset version",
            message: `Soft-delete ${name} v${v}? Content stays in CAS for offline garbage collection.`,
            danger: true, primaryLabel: "Soft-delete",
          })) return;
          try {
            const r = await fetch(`/api/v1/datasets/${encodeURIComponent(name)}/versions/${v}`, { method: "DELETE" });
            if (!r.ok) throw new Error(`HTTP ${r.status}`);
            showToast(`Deleted ${name} v${v}`);
            App.renderDatasetDetail(name);
          } catch (err) {
            alert(`Delete failed: ${err}`);
          }
        }));
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    _showDatasetUploadModal(prefillName) {
      const wrap = document.createElement("div");
      wrap.className = "modal-backdrop";
      wrap.innerHTML = `
        <div class="card modal" style="max-width:520px">
          <h2 style="margin-top:0">Upload dataset version</h2>
          <p style="color:var(--fg-muted);font-size:13px;margin-top:0">
            The server hashes the file as it streams. Identical bytes → one physical file regardless of name.
          </p>
          <table class="form-table">
            <tr>
              <th><label for="du-name">Name</label></th>
              <td><input type="text" id="du-name" placeholder="e.g. wikipedia-2024-q1" value="${escapeHTML(prefillName || "")}" ${prefillName ? "readonly" : ""} style="width:100%"/></td>
            </tr>
            <tr>
              <th><label for="du-file">File</label></th>
              <td><input type="file" id="du-file" style="width:100%"/></td>
            </tr>
            <tr>
              <th><label for="du-desc">Description</label></th>
              <td><input type="text" id="du-desc" placeholder="optional" style="width:100%"/></td>
            </tr>
            <tr>
              <th><label for="du-schema">Schema</label></th>
              <td><textarea id="du-schema" rows="3" placeholder='optional JSON, e.g. {"cols":["a","b"]}' style="width:100%;font-family:var(--mono);font-size:12px"></textarea></td>
            </tr>
          </table>
          <div id="du-progress" style="font-size:12px;color:var(--fg-muted);min-height:18px"></div>
          <div id="du-err" style="color:var(--error);min-height:18px;font-size:13px"></div>
          <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px">
            <button id="du-cancel">Cancel</button>
            <button id="du-save" class="btn-primary">Upload</button>
          </div>
        </div>`;
      document.body.appendChild(wrap);
      $("#du-name").focus();
      const close = () => wrap.remove();
      wrap.addEventListener("click", e => { if (e.target === wrap) close(); });
      $("#du-cancel").addEventListener("click", close);

      $("#du-save").addEventListener("click", async () => {
        const name = $("#du-name").value.trim();
        const fileInput = $("#du-file");
        if (!name) { $("#du-err").textContent = "Name required."; return; }
        if (!fileInput.files[0]) { $("#du-err").textContent = "Pick a file."; return; }

        const meta = {};
        const desc = $("#du-desc").value.trim();
        const schema = $("#du-schema").value.trim();
        if (desc) meta.description = desc;
        if (schema) meta.schema_json = schema;

        const fd = new FormData();
        fd.append("file", fileInput.files[0]);
        fd.append("meta", JSON.stringify(meta));

        const xhr = new XMLHttpRequest();
        xhr.open("POST", `/api/v1/datasets/${encodeURIComponent(name)}/versions`, true);
        xhr.upload.addEventListener("progress", (e) => {
          if (e.lengthComputable) {
            const pct = ((e.loaded / e.total) * 100).toFixed(1);
            $("#du-progress").textContent = `Uploading… ${pct}% (${formatBytes(e.loaded)} / ${formatBytes(e.total)})`;
          }
        });
        xhr.onload = () => {
          if (xhr.status >= 200 && xhr.status < 300) {
            try {
              const r = JSON.parse(xhr.responseText);
              showToast(`Uploaded ${name} v${r.version} (${formatBytes(r.size_bytes)})`);
              close();
              location.hash = `#/datasets/${encodeURIComponent(name)}`;
              App.renderDatasetDetail(name);
            } catch {
              showToast("Uploaded.");
              close();
              App.renderDatasetsIndex();
            }
          } else {
            try {
              const err = JSON.parse(xhr.responseText);
              $("#du-err").textContent = err.message || `HTTP ${xhr.status}`;
            } catch {
              $("#du-err").textContent = `HTTP ${xhr.status}`;
            }
          }
        };
        xhr.onerror = () => { $("#du-err").textContent = "Network error."; };
        $("#du-save").disabled = true;
        xhr.send(fd);
      });
    },

    // ── Federation (v1.3) ────────────────────────────────────────────────────
    async renderFederation() {
      const main = $("#app");
      let data;
      try {
        data = await fetchJSON("/api/v1/federate/peers");
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load peers: ${escapeHTML(String(err))}</div>`;
        return;
      }
      const peers = data.peers || [];
      const rows = peers.map(p => `
        <tr data-peer-id="${p.id}">
          <td>${escapeHTML(p.name)}</td>
          <td class="mono" style="max-width:280px;overflow:hidden;text-overflow:ellipsis" title="${escapeHTML(p.url)}">${escapeHTML(p.url)}</td>
          <td><span class="status-pill status-${p.status === "connected" ? "FINISHED" : (p.status === "error" ? "FAILED" : "RUNNING")}">${escapeHTML(p.status)}</span></td>
          <td>${p.last_seen ? formatTime(p.last_seen) : "—"}</td>
          <td class="u-muted-xs">${escapeHTML(p.last_error || "")}</td>
          <td>
            <button class="fed-echo-btn" data-peer-id="${p.id}" title="Probe connectivity">Echo</button>
            <button class="fed-del-btn btn-danger" data-peer-id="${p.id}">Remove</button>
          </td>
        </tr>`).join("");

      main.innerHTML = `
        <div class="toolbar">
          <h1 class="u-toolbar-title">Federation</h1>
          <button id="fed-add-btn" class="btn-primary">+ Add peer</button>
        </div>
        <p class="u-muted-sm" style="margin-top:0">
          Each peer is another LiteMLflow instance this server can query. Auth is mutual HMAC: both
          servers must store the same 32-byte secret. Add a peer here, copy the secret, paste it
          into the same form on the remote.
        </p>
        ${peers.length ? `
        <div class="card u-pad-0">
          <table>
            <thead><tr><th>Name</th><th>URL</th><th>Status</th><th>Last seen</th><th>Last error</th><th>Actions</th></tr></thead>
            <tbody>${rows}</tbody>
          </table>
        </div>` : `
        <div class="empty card">
          <p>No peers yet. Click <strong>+ Add peer</strong> to register a remote LiteMLflow instance.</p>
          <p class="u-muted-xs">After adding, copy the generated secret to the same form on the remote server. Probe with the <strong>Echo</strong> button — status should flip to <code>connected</code>.</p>
        </div>`}`;

      $("#fed-add-btn").addEventListener("click", () => this._showAddPeerModal());
      $$(".fed-echo-btn", main).forEach(b => b.addEventListener("click", async () => {
        const id = b.dataset.peerId;
        b.disabled = true;
        b.textContent = "…";
        try {
          const r = await fetchJSON(`/api/v1/federate/peers/${id}/echo`, { method: "POST", body: "{}" });
          showToast(r.status === "connected" ? `Connected: ${id}` : `Echo: ${r.error || r.status}`);
          App.renderFederation();
        } catch (err) {
          alert(`Echo failed: ${err}`);
          b.disabled = false;
          b.textContent = "Echo";
        }
      }));
      $$(".fed-del-btn", main).forEach(b => b.addEventListener("click", async () => {
        const id = b.dataset.peerId;
        if (!await Modal.confirm({
          title: "Remove peer",
          message: "This deletes the local row only — the peer's row on its own server stays. Federation will fail for this peer until both sides are removed.",
          danger: true, primaryLabel: "Remove",
        })) return;
        try {
          await fetch(`/api/v1/federate/peers/${id}`, { method: "DELETE" });
          showToast("Peer removed.");
          App.renderFederation();
        } catch (err) {
          alert(`Delete failed: ${err}`);
        }
      }));
    },

    _showAddPeerModal() {
      const wrap = document.createElement("div");
      wrap.className = "modal-backdrop";
      wrap.innerHTML = `
        <div class="card modal" style="max-width:520px">
          <h2 style="margin-top:0">Add federation peer</h2>
          <p class="u-muted-sm" style="margin:0 0 12px">
            Leave the secret blank to have the server generate a fresh one — it will be shown ONCE
            in the response and you'll need to paste it into the peer's form. Pre-fill the secret
            if the remote already shared one.
          </p>
          <table class="form-table">
            <tr>
              <th><label for="ap-name">Name</label></th>
              <td><input type="text" id="ap-name" placeholder="e.g. lmf-team-b" class="u-w-full"/></td>
            </tr>
            <tr>
              <th><label for="ap-url">URL</label></th>
              <td><input type="url" id="ap-url" placeholder="https://lmf.team-b.example" class="u-w-full"/></td>
            </tr>
            <tr>
              <th><label for="ap-secret">Secret (optional)</label></th>
              <td><input type="text" id="ap-secret" placeholder="64-hex-char shared HMAC key" class="u-w-full" maxlength="64"/></td>
            </tr>
          </table>
          <div id="ap-err" style="color:var(--error);min-height:18px;font-size:13px"></div>
          <div class="u-row-end">
            <button data-modal-cancel>Cancel</button>
            <button id="ap-save" class="btn-primary">Add peer</button>
          </div>
        </div>`;
      document.body.appendChild(wrap);
      $("#ap-name").focus();
      const close = () => wrap.remove();
      wrap.addEventListener("click", e => { if (e.target === wrap) close(); });
      wrap.querySelector("[data-modal-cancel]").addEventListener("click", close);
      $("#ap-save").addEventListener("click", async () => {
        const name = $("#ap-name").value.trim();
        const url = $("#ap-url").value.trim();
        const secret = $("#ap-secret").value.trim();
        if (!name || !url) {
          $("#ap-err").textContent = "Name and URL are required.";
          return;
        }
        try {
          const body = { name, url };
          if (secret) body.secret = secret;
          const resp = await fetchJSON("/api/v1/federate/peers", {
            method: "POST",
            body: JSON.stringify(body),
          });
          // Show the secret if the server generated one — copy-to-clipboard
          // affordance so the operator can paste it into the remote.
          close();
          if (!secret && resp.secret) {
            await Modal.confirm({
              title: "Peer created — copy this secret",
              message: `Paste this 64-char HMAC secret into the same form on ${escapeHTML(resp.peer.name)}'s server. It is shown ONLY ONCE.\n\n${resp.secret}`,
              primaryLabel: "I copied it",
            });
            try { await navigator.clipboard.writeText(resp.secret); showToast("Secret copied to clipboard."); } catch {}
          } else {
            showToast(`Peer ${resp.peer.name} added.`);
          }
          App.renderFederation();
        } catch (err) {
          $("#ap-err").textContent = String(err);
        }
      });
    },

    // ── Dashboards ───────────────────────────────────────────────────────────
    async renderDashboardsIndex() {
      const main = $("#app");
      try {
        const projectsRes = await fetchJSON("/api/v1/projects");
        const projects = (projectsRes.projects || []).filter(p => p.name !== "");
        if (projects.length === 0) {
          main.innerHTML = `
            <h1>Dashboards</h1>
            <div class="empty card">
              <p>No projects yet. Create one from the <a href="#/experiments">Experiments</a> page
              with the <strong>+ New project</strong> button. Each project gets its own dashboard
              for trend charts, leaderboards, and widget pinning.</p>
            </div>`;
          return;
        }
        const cards = projects.map(p => `
          <a href="#/dashboards/${encodeURIComponent(p.name)}" class="card" style="text-decoration:none;color:inherit;display:block;padding:18px;cursor:pointer">
            <h3 style="margin:0 0 4px;color:var(--accent)">${escapeHTML(p.name)}</h3>
            <div style="color:var(--fg-muted);font-size:12px">${p.count} experiment${p.count === 1 ? "" : "s"}</div>
          </a>`).join("");
        main.innerHTML = `
          <h1>Dashboards</h1>
          <p style="color:var(--fg-muted)">Pick a project to open its dashboard. Each dashboard is a board of metric-trend, leaderboard, and stat widgets.</p>
          <div class="dashboard-grid">${cards}</div>`;
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    async renderDashboard(project) {
      const main = $("#app");
      // Fetch dashboard config + all runs in the project for client-side
      // widget rendering.
      let dashboard, runsByExp = {}, expList = [];
      try {
        dashboard = await fetchJSON(`/api/v1/dashboards/${encodeURIComponent(project)}`);
        // Find experiments with lmf.project == project.
        const expRes = await fetchJSON("/api/2.0/mlflow/experiments/search?max_results=1000");
        expList = (expRes.experiments || []).filter(e =>
          e.lifecycle_stage === "active" &&
          (e.tags || []).some(t => t.key === "lmf.project" && t.value === project)
        );
        // Pull recent runs for each.
        const runs = await Promise.all(expList.map(e =>
          fetchJSON("/api/2.0/mlflow/runs/search", {
            method: "POST",
            body: JSON.stringify({ experiment_ids: [String(e.experiment_id)], max_results: 100 }),
          }).then(r => r.runs || []).catch(() => [])
        ));
        expList.forEach((e, i) => { runsByExp[e.experiment_id] = runs[i]; });
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load dashboard: ${escapeHTML(String(err))}</div>`;
        return;
      }

      const allRuns = Object.values(runsByExp).flat();
      let widgets;
      try { widgets = JSON.parse(dashboard.widgets || "[]"); } catch { widgets = []; }
      let editMode = false;

      const allMetricKeys = [...new Set(allRuns.flatMap(r => (r.data?.metrics || []).map(m => m.key)))].sort();
      const allParamKeys  = [...new Set(allRuns.flatMap(r => (r.data?.params  || []).map(p => p.key)))].sort();

      const renderWidgetBody = (w) => {
        switch (w.type) {
          case "run_count": {
            const total = allRuns.length;
            const finished = allRuns.filter(r => r.info.status === "FINISHED").length;
            const running = allRuns.filter(r => r.info.status === "RUNNING").length;
            const failed = allRuns.filter(r => r.info.status === "FAILED").length;
            return `
              <div class="widget-big-num">${total}</div>
              <div class="widget-row"><span>Finished</span><span class="mono">${finished}</span></div>
              <div class="widget-row"><span>Running</span><span class="mono">${running}</span></div>
              <div class="widget-row"><span>Failed</span><span class="mono">${failed}</span></div>`;
          }
          case "latest_best": {
            const metric = (w.config && w.config.metric) || "";
            const dir = (w.config && w.config.direction) || "max";
            const candidates = allRuns
              .map(r => ({
                r,
                v: (r.data?.metrics || []).find(m => m.key === metric)?.value,
              }))
              .filter(x => x.v != null);
            if (candidates.length === 0) {
              return `<div class="widget-row" style="color:var(--fg-muted)">No runs with metric <code>${escapeHTML(metric)}</code>.</div>`;
            }
            candidates.sort((a, b) => dir === "min" ? a.v - b.v : b.v - a.v);
            const best = candidates[0];
            return `
              <div class="widget-big-num">${best.v.toPrecision(5)}</div>
              <div class="widget-subtitle">best ${escapeHTML(dir)}imum of <code>${escapeHTML(metric)}</code></div>
              <div class="widget-row">
                <a href="#/experiments/${best.r.info.experiment_id}/runs/${best.r.info.run_id}">${escapeHTML(best.r.info.run_name || best.r.info.run_id.slice(0,8))}</a>
                <span class="mono">${best.r.info.status}</span>
              </div>`;
          }
          case "param_leaderboard": {
            const sortMetric = (w.config && w.config.sort_metric) || "";
            const dir = (w.config && w.config.direction) || "max";
            const limit = (w.config && w.config.limit) || 5;
            const ranked = allRuns
              .map(r => ({
                r,
                v: (r.data?.metrics || []).find(m => m.key === sortMetric)?.value,
              }))
              .filter(x => x.v != null);
            ranked.sort((a, b) => dir === "min" ? a.v - b.v : b.v - a.v);
            const top = ranked.slice(0, limit);
            if (top.length === 0) {
              return `<div class="widget-row" style="color:var(--fg-muted)">No runs with metric <code>${escapeHTML(sortMetric)}</code>.</div>`;
            }
            return top.map(({ r, v }) => `
              <div class="widget-row">
                <a href="#/experiments/${r.info.experiment_id}/runs/${r.info.run_id}">${escapeHTML(r.info.run_name || r.info.run_id.slice(0,8))}</a>
                <span class="mono">${v.toPrecision(4)}</span>
              </div>`).join("");
          }
          case "metric_trend": {
            const metric = (w.config && w.config.metric) || "";
            const points = allRuns
              .map(r => {
                const m = (r.data?.metrics || []).find(x => x.key === metric);
                return m ? { x: r.info.start_time, y: m.value, name: r.info.run_name || r.info.run_id.slice(0, 8), id: r.info.run_id, expID: r.info.experiment_id } : null;
              })
              .filter(Boolean)
              .sort((a, b) => a.x - b.x);
            if (points.length === 0) {
              return `<div class="widget-row" style="color:var(--fg-muted)">No runs with metric <code>${escapeHTML(metric)}</code>.</div>`;
            }
            const w0 = 280, h0 = 100, pad = 8;
            const xs = points.map(p => p.x), ys = points.map(p => p.y);
            // Filter NaN/Infinity defensively — log_metric is float so a
            // bad row from a custom client could otherwise blow up the SVG.
            const finiteYs = ys.filter(Number.isFinite);
            if (finiteYs.length === 0) {
              return `<div class="widget-row" style="color:var(--fg-muted)">All values are non-finite.</div>`;
            }
            const xMin = Math.min(...xs), xMax = Math.max(...xs);
            const yMin = Math.min(...finiteYs), yMax = Math.max(...finiteYs);
            const xSpan = Math.max(1, xMax - xMin);
            const ySpan = Math.max(1e-9, yMax - yMin);
            const sx = x => pad + ((x - xMin) / xSpan) * (w0 - 2 * pad);
            const sy = y => h0 - pad - ((y - yMin) / ySpan) * (h0 - 2 * pad);
            const path = points.map((p, i) => `${i === 0 ? "M" : "L"}${sx(p.x).toFixed(1)},${sy(p.y).toFixed(1)}`).join(" ");
            const circles = points.map(p => `<circle cx="${sx(p.x).toFixed(1)}" cy="${sy(p.y).toFixed(1)}" r="2" fill="var(--accent)"/>`).join("");
            return `
              <svg viewBox="0 0 ${w0} ${h0}" style="width:100%;height:${h0}px">
                <path d="${path}" stroke="var(--accent)" stroke-width="1.5" fill="none"/>
                ${circles}
              </svg>
              <div class="widget-subtitle">${points.length} runs · range ${yMin.toPrecision(3)} → ${yMax.toPrecision(3)}</div>`;
          }
          default:
            return `<div class="widget-row" style="color:var(--fg-muted)">Unknown widget type: ${escapeHTML(w.type)}</div>`;
        }
      };

      const renderBoard = () => {
        const cards = widgets.map((w, i) => `
          <div class="widget${editMode ? " widget-draggable" : ""}" data-widget-idx="${i}"${editMode ? ' draggable="true"' : ""}>
            ${editMode ? `<div class="widget-actions" style="display:flex">
              <span class="widget-handle" title="Drag to reorder" aria-hidden="true">⋮⋮</span>
              <button class="widget-up" data-idx="${i}" title="Move up">↑</button>
              <button class="widget-down" data-idx="${i}" title="Move down">↓</button>
              <button class="widget-rm btn-danger" data-idx="${i}" title="Remove">✕</button>
            </div>` : ""}
            <h3 class="widget-title">${escapeHTML(w.title || w.type)}</h3>
            ${renderWidgetBody(w)}
          </div>`).join("");

        main.innerHTML = `
          <div class="crumbs"><a href="#/dashboards">Dashboards</a> / ${escapeHTML(project)}</div>
          <div class="toolbar">
            <h1 style="margin:0">${escapeHTML(project)}</h1>
            <span style="color:var(--fg-muted);font-size:13px">
              ${expList.length} experiment${expList.length === 1 ? "" : "s"} · ${allRuns.length} run${allRuns.length === 1 ? "" : "s"}
            </span>
            <button id="dash-edit-btn">${editMode ? "Done" : "Edit"}</button>
            ${editMode ? `<button id="dash-add-btn" class="btn-primary">+ Add widget</button>` : ""}
          </div>
          ${widgets.length === 0 ? `
            <div class="empty card">
              <p>This project has no widgets yet. Click <strong>Edit → + Add widget</strong> to pin a metric chart, leaderboard, or run-count tile.</p>
            </div>` : `<div class="dashboard-grid">${cards}</div>`}`;

        $("#dash-edit-btn").addEventListener("click", () => {
          if (editMode) {
            // Save and exit edit mode.
            saveWidgets().then(() => { editMode = false; renderBoard(); });
          } else {
            editMode = true;
            renderBoard();
          }
        });
        const addBtn = $("#dash-add-btn");
        if (addBtn) addBtn.addEventListener("click", () => this._showAddWidgetModal(allMetricKeys, allParamKeys, (cfg) => {
          widgets.push(cfg);
          renderBoard();
        }));

        if (editMode) {
          $$(".widget-rm", main).forEach(b => b.addEventListener("click", (e) => {
            e.stopPropagation();
            const idx = Number(b.dataset.idx);
            widgets.splice(idx, 1);
            renderBoard();
          }));
          $$(".widget-up", main).forEach(b => b.addEventListener("click", (e) => {
            e.stopPropagation();
            const i = Number(b.dataset.idx);
            if (i > 0) [widgets[i - 1], widgets[i]] = [widgets[i], widgets[i - 1]];
            renderBoard();
          }));
          $$(".widget-down", main).forEach(b => b.addEventListener("click", (e) => {
            e.stopPropagation();
            const i = Number(b.dataset.idx);
            if (i < widgets.length - 1) [widgets[i + 1], widgets[i]] = [widgets[i], widgets[i + 1]];
            renderBoard();
          }));

          // HTML5 drag-and-drop for visual reordering. Indices in the DOM
          // dataset are stable across the array swap because we re-render
          // after every drop.
          let dragSrc = -1;
          $$(".widget-draggable", main).forEach(el => {
            el.addEventListener("dragstart", (e) => {
              dragSrc = Number(el.dataset.widgetIdx);
              el.classList.add("widget-dragging");
              if (e.dataTransfer) {
                e.dataTransfer.effectAllowed = "move";
                e.dataTransfer.setData("text/plain", String(dragSrc));
              }
            });
            el.addEventListener("dragend", () => {
              el.classList.remove("widget-dragging");
              $$(".widget-drop-target", main).forEach(t => t.classList.remove("widget-drop-target"));
              dragSrc = -1;
            });
            el.addEventListener("dragover", (e) => {
              if (dragSrc < 0) return;
              e.preventDefault();
              if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
              el.classList.add("widget-drop-target");
            });
            el.addEventListener("dragleave", () => {
              el.classList.remove("widget-drop-target");
            });
            el.addEventListener("drop", (e) => {
              e.preventDefault();
              const dst = Number(el.dataset.widgetIdx);
              if (dragSrc < 0 || dragSrc === dst) return;
              const [moved] = widgets.splice(dragSrc, 1);
              widgets.splice(dst, 0, moved);
              renderBoard();
            });
          });
        }
      };

      const saveWidgets = async () => {
        try {
          await fetchJSON(`/api/v1/dashboards/${encodeURIComponent(project)}`, {
            method: "PUT",
            // The server stores `widgets` as a JSON array (text column), so we
            // pass the array directly and let the server preserve it verbatim.
            body: JSON.stringify({ widgets }),
          });
          showToast("Dashboard saved");
        } catch (err) {
          alert(`Failed to save: ${err}`);
        }
      };

      renderBoard();
    },

    _showAddWidgetModal(metricKeys, paramKeys, onAdd) {
      const wrap = document.createElement("div");
      wrap.className = "modal-backdrop";
      const metricOpts = metricKeys.map(k => `<option value="${escapeHTML(k)}">${escapeHTML(k)}</option>`).join("");
      wrap.innerHTML = `
        <div class="card modal" style="max-width:520px">
          <h2 style="margin-top:0">Add widget</h2>
          <table class="form-table">
            <tr>
              <th><label for="aw-type">Type</label></th>
              <td>
                <select id="aw-type" style="width:100%">
                  <option value="run_count">Run count tile</option>
                  <option value="latest_best">Latest best run</option>
                  <option value="param_leaderboard">Run leaderboard</option>
                  <option value="metric_trend">Metric trend chart</option>
                </select>
              </td>
            </tr>
            <tr>
              <th><label for="aw-title">Title</label></th>
              <td><input type="text" id="aw-title" placeholder="e.g. Best loss" style="width:100%"/></td>
            </tr>
            <tr id="aw-metric-row">
              <th><label for="aw-metric">Metric</label></th>
              <td>
                <select id="aw-metric" style="width:100%">
                  ${metricOpts || '<option value="">— no metrics yet —</option>'}
                </select>
              </td>
            </tr>
            <tr id="aw-dir-row">
              <th><label for="aw-dir">Direction</label></th>
              <td>
                <select id="aw-dir" style="width:100%">
                  <option value="max">Higher is better</option>
                  <option value="min">Lower is better</option>
                </select>
              </td>
            </tr>
            <tr id="aw-limit-row" style="display:none">
              <th><label for="aw-limit">Top N</label></th>
              <td><input type="number" id="aw-limit" value="5" min="1" max="20" style="width:80px"/></td>
            </tr>
          </table>
          <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px">
            <button id="aw-cancel">Cancel</button>
            <button id="aw-save" class="btn-primary">Add</button>
          </div>
        </div>`;
      document.body.appendChild(wrap);

      const close = () => wrap.remove();
      wrap.addEventListener("click", e => { if (e.target === wrap) close(); });
      $("#aw-cancel").addEventListener("click", close);

      const updateRows = () => {
        const t = $("#aw-type").value;
        $("#aw-metric-row").style.display = (t === "run_count") ? "none" : "";
        $("#aw-dir-row").style.display = (t === "metric_trend" || t === "run_count") ? "none" : "";
        $("#aw-limit-row").style.display = (t === "param_leaderboard") ? "" : "none";
      };
      $("#aw-type").addEventListener("change", updateRows);
      updateRows();

      $("#aw-save").addEventListener("click", () => {
        const type = $("#aw-type").value;
        const title = $("#aw-title").value.trim() || ({
          run_count: "Run count",
          latest_best: "Latest best",
          param_leaderboard: "Leaderboard",
          metric_trend: "Metric trend",
        })[type];
        const cfg = { type, title, config: {} };
        if (type !== "run_count") cfg.config.metric = $("#aw-metric").value;
        if (type === "latest_best" || type === "param_leaderboard") {
          cfg.config.direction = $("#aw-dir").value;
        }
        if (type === "param_leaderboard") {
          cfg.config.sort_metric = $("#aw-metric").value;
          cfg.config.limit = parseInt($("#aw-limit").value, 10) || 5;
        }
        onAdd(cfg);
        close();
      });
    },

    // ── Analytics (v1.1) ─────────────────────────────────────────────────────
    async renderAnalytics() {
      const main = $("#app");

      // Persisted query and saved-queries list (per-workspace).
      const wsKey = `litemlflow.analytics.${Workspace.get() || "default"}.last`;
      const savedKey = `litemlflow.analytics.${Workspace.get() || "default"}.saved`;
      const loadJSON = (k, def) => { try { return JSON.parse(localStorage.getItem(k)) || def; } catch { return def; } };
      const saveJSON = (k, v) => localStorage.setItem(k, JSON.stringify(v));

      let query = loadJSON(wsKey, {
        metric: "",
        agg: "max",
        group_by: "",
        where: { lifecycle: "active" },
        order_by: "value_desc",
        limit: 100,
      });
      const savedQueries = loadJSON(savedKey, []); // [{name, query}]

      // Fetch experiments (for the experiment-id picker) + project tags.
      let experiments = [];
      try {
        const data = await fetchJSON("/api/2.0/mlflow/experiments/search?max_results=500");
        experiments = (data.experiments || []).filter(e => e.lifecycle_stage === "active");
      } catch {}

      // Pull a sample of metric/param/tag keys via a probe run from each
      // experiment (best effort — keys are autocomplete hints, not required).
      const sampleSize = Math.min(experiments.length, 8);
      const sampleRuns = await Promise.all(
        experiments.slice(0, sampleSize).map(e =>
          fetchJSON("/api/2.0/mlflow/runs/search", {
            method: "POST",
            body: JSON.stringify({ experiment_ids: [String(e.experiment_id)], max_results: 5 }),
          }).then(r => r.runs || []).catch(() => [])
        )
      );
      const allRuns = sampleRuns.flat();
      const metricKeys = [...new Set(allRuns.flatMap(r => (r.data?.metrics || []).map(m => m.key)))].sort();
      const paramKeys  = [...new Set(allRuns.flatMap(r => (r.data?.params  || []).map(p => p.key)))].sort();
      const tagKeys    = [...new Set(allRuns.flatMap(r => (r.data?.tags    || []).map(t => t.key).filter(k => !k.startsWith("mlflow.") && !k.startsWith("lmf."))))].sort();

      const metricOpts = metricKeys.length
        ? metricKeys.map(k => `<option value="${escapeHTML(k)}">${escapeHTML(k)}</option>`).join("")
        : `<option value="">(no metrics seen — type a key)</option>`;
      const groupOpts = `
        <option value="">(no grouping)</option>
        <option value="experiment_id">experiment_id</option>
        <option value="status">status</option>
        ${paramKeys.map(k => `<option value="params.${escapeHTML(k)}">params.${escapeHTML(k)}</option>`).join("")}
        ${tagKeys.map(k => `<option value="tags.${escapeHTML(k)}">tags.${escapeHTML(k)}</option>`).join("")}
      `;

      // Helpers: time-window presets
      const windowPresets = [
        { label: "All time", value: 0 },
        { label: "Last 24 hours", value: 24 * 3600 * 1000 },
        { label: "Last 7 days", value: 7 * 24 * 3600 * 1000 },
        { label: "Last 30 days", value: 30 * 24 * 3600 * 1000 },
        { label: "Last 90 days", value: 90 * 24 * 3600 * 1000 },
      ];

      const buildToolbar = () => `
        <div class="toolbar" style="align-items:flex-end;flex-wrap:wrap;gap:12px">
          <label class="aq-field">
            <span>Metric</span>
            <input list="aq-metric-list" id="aq-metric" placeholder="e.g. eval/f1" value="${escapeHTML(query.metric || "")}" />
            <datalist id="aq-metric-list">${metricOpts}</datalist>
          </label>
          <label class="aq-field">
            <span>Agg</span>
            <select id="aq-agg">
              <option value="max"${query.agg === "max" ? " selected" : ""}>max</option>
              <option value="min"${query.agg === "min" ? " selected" : ""}>min</option>
              <option value="avg"${query.agg === "avg" ? " selected" : ""}>avg</option>
            </select>
          </label>
          <label class="aq-field">
            <span>Group by</span>
            <select id="aq-group">${groupOpts.replace(`value="${escapeHTML(query.group_by || "")}"`, `value="${escapeHTML(query.group_by || "")}" selected`)}</select>
          </label>
          <label class="aq-field">
            <span>Time window</span>
            <select id="aq-window">
              ${windowPresets.map(w => `<option value="${w.value}"${(query._window === w.value || (w.value === 0 && !query._window)) ? " selected" : ""}>${escapeHTML(w.label)}</option>`).join("")}
            </select>
          </label>
          <label class="aq-field">
            <span>Status</span>
            <select id="aq-status" multiple size="1" style="min-width:140px">
              <option value="FINISHED"${(query.where.status||[]).includes("FINISHED") ? " selected" : ""}>FINISHED</option>
              <option value="FAILED"${(query.where.status||[]).includes("FAILED") ? " selected" : ""}>FAILED</option>
              <option value="RUNNING"${(query.where.status||[]).includes("RUNNING") ? " selected" : ""}>RUNNING</option>
              <option value="KILLED"${(query.where.status||[]).includes("KILLED") ? " selected" : ""}>KILLED</option>
            </select>
          </label>
          <label class="aq-field">
            <span>Order</span>
            <select id="aq-order">
              <option value="value_desc"${query.order_by === "value_desc" ? " selected" : ""}>value desc</option>
              <option value="value_asc"${query.order_by === "value_asc" ? " selected" : ""}>value asc</option>
              <option value="count_desc"${query.order_by === "count_desc" ? " selected" : ""}>run count desc</option>
              <option value="group_asc"${query.order_by === "group_asc" ? " selected" : ""}>group asc</option>
            </select>
          </label>
          <label class="aq-field">
            <span>Limit</span>
            <input type="number" id="aq-limit" min="1" max="1000" value="${query.limit || 100}" style="width:80px;min-width:0"/>
          </label>
          <button id="aq-run" class="btn-primary">Run query</button>
          <button id="aq-save">Save</button>
          <button id="aq-clear" class="btn-ghost">Clear</button>
        </div>`;

      const buildSavedList = () => savedQueries.length === 0 ? "" : `
        <div class="saved-queries">
          <strong>Saved:</strong>
          ${savedQueries.map((s, i) => `
            <button class="saved-q" data-saved-idx="${i}" title="Load this query">${escapeHTML(s.name)}</button>
            <button class="saved-q-rm btn-ghost" data-saved-idx="${i}" title="Delete">✕</button>
          `).join("")}
        </div>`;

      const renderResultRows = (result) => {
        const rows = (result.rows || []).map(r => `
          <tr>
            <td>${r.group != null && r.group !== "" ? escapeHTML(String(r.group)) : `<span style="color:var(--fg-muted)">— total —</span>`}</td>
            <td class="numeric mono">${r.agg_value.toPrecision(5)}</td>
            <td class="numeric">${r.run_count}</td>
            <td>${r.best_run_id ? `<a href="#/experiments/${r.best_experiment_id}/runs/${r.best_run_id}">${escapeHTML(r.best_run_name || r.best_run_id.slice(0,8))}</a>` : "—"}</td>
          </tr>`).join("");
        return rows || `<tr><td colspan="4" class="empty">No matching runs.</td></tr>`;
      };

      const renderResultChart = (result) => {
        const rows = (result.rows || []).filter(r => r.group != null && r.group !== "");
        if (rows.length < 2) return "";
        const w0 = 720, h0 = 200, padL = 80, padR = 12, padT = 12, padB = 36;
        const innerH = h0 - padT - padB;
        const innerW = w0 - padL - padR;
        const vals = rows.map(r => r.agg_value);
        const vMax = Math.max(...vals, 0);
        const vMin = Math.min(...vals, 0);
        const vSpan = Math.max(1e-9, vMax - vMin);
        const barW = Math.max(8, innerW / rows.length - 6);
        const bars = rows.map((r, i) => {
          const x = padL + i * (innerW / rows.length) + 3;
          const yTop = padT + (1 - (r.agg_value - vMin) / vSpan) * innerH;
          const barH = innerH - (yTop - padT);
          return `
            <rect x="${x.toFixed(1)}" y="${yTop.toFixed(1)}" width="${barW.toFixed(1)}" height="${Math.max(2, barH).toFixed(1)}" fill="var(--accent)" opacity="0.85" />
            <text x="${(x + barW / 2).toFixed(1)}" y="${h0 - padB + 14}" text-anchor="middle" fill="var(--fg-muted)" font-size="10">${escapeHTML(String(r.group).slice(0, 14))}</text>
            <text x="${(x + barW / 2).toFixed(1)}" y="${(yTop - 3).toFixed(1)}" text-anchor="middle" fill="var(--fg)" font-size="10">${r.agg_value.toPrecision(3)}</text>`;
        }).join("");
        return `
          <h3 style="margin-bottom:6px">Distribution</h3>
          <svg viewBox="0 0 ${w0} ${h0}" style="width:100%;height:auto;max-height:240px;background:var(--bg-card);border:1px solid var(--border);border-radius:8px;padding:8px">
            <line x1="${padL}" y1="${padT + innerH}" x2="${w0 - padR}" y2="${padT + innerH}" stroke="var(--border)" />
            <text x="${padL - 4}" y="${padT + 4}" text-anchor="end" fill="var(--fg-muted)" font-size="10">${vMax.toPrecision(3)}</text>
            <text x="${padL - 4}" y="${padT + innerH}" text-anchor="end" fill="var(--fg-muted)" font-size="10">${vMin.toPrecision(3)}</text>
            ${bars}
          </svg>`;
      };

      const renderResults = (result) => {
        const meta = `<div style="color:var(--fg-muted);font-size:12px;margin-bottom:8px">
          ${(result.rows || []).length} groups · ${result.total_runs_scanned} runs scanned · ${result.execution_ms}ms
        </div>`;
        return `
          ${meta}
          ${renderResultChart(result)}
          <div class="card" style="padding:0;margin-top:10px">
            <table>
              <thead>
                <tr><th>Group</th><th class="numeric">${escapeHTML(query.agg.toUpperCase())} ${escapeHTML(query.metric)}</th><th class="numeric">Runs</th><th>Best run</th></tr>
              </thead>
              <tbody>${renderResultRows(result)}</tbody>
            </table>
          </div>`;
      };

      const collectQuery = () => {
        const statusEl = $("#aq-status");
        const selected = Array.from(statusEl.options).filter(o => o.selected).map(o => o.value);
        const win = parseInt($("#aq-window").value, 10) || 0;
        return {
          metric: $("#aq-metric").value.trim(),
          agg: $("#aq-agg").value,
          group_by: $("#aq-group").value,
          order_by: $("#aq-order").value,
          limit: parseInt($("#aq-limit").value, 10) || 100,
          where: {
            lifecycle: "active",
            status: selected,
            time_after: win > 0 ? Date.now() - win : 0,
          },
          _window: win, // UI-only, not sent
        };
      };

      const runQuery = async () => {
        const q = collectQuery();
        if (!q.metric) {
          $("#aq-results").innerHTML = `<div class="empty">Enter a metric key (e.g. <code>eval/f1</code>, <code>loss</code>) and run.</div>`;
          return;
        }
        $("#aq-results").innerHTML = `<div class="loading">Running query…</div>`;
        const body = { ...q };
        delete body._window;
        body.where = { ...body.where };
        if (!body.where.status || !body.where.status.length) delete body.where.status;
        if (!body.where.time_after) delete body.where.time_after;
        try {
          const result = await fetchJSON("/api/v1/analytics/query", {
            method: "POST",
            body: JSON.stringify(body),
          });
          query = q;
          saveJSON(wsKey, query);
          $("#aq-results").innerHTML = renderResults(result);
        } catch (err) {
          $("#aq-results").innerHTML = `<div class="empty">Query failed: ${escapeHTML(String(err))}</div>`;
        }
      };

      main.innerHTML = `
        <div class="toolbar">
          <h1 style="margin:0">Analytics</h1>
          <span style="color:var(--fg-muted);font-size:13px">Cross-experiment OLAP — find best runs, group by params/tags, filter by status & time.</span>
        </div>
        ${buildToolbar()}
        ${buildSavedList()}
        <div id="aq-results" style="margin-top:14px">
          <div class="empty">Configure a query above, then <strong>Run query</strong>. Results render as a table + bar chart for grouped queries.</div>
        </div>`;

      // Wire main run button
      $("#aq-run").addEventListener("click", runQuery);
      $("#aq-clear").addEventListener("click", () => {
        localStorage.removeItem(wsKey);
        App.renderAnalytics();
      });
      $("#aq-save").addEventListener("click", async () => {
        const name = await Modal.prompt({
          title: "Save query",
          label: "Name",
          placeholder: "e.g. F1 by optimizer (last 30 days)",
        });
        if (!name) return;
        const q = collectQuery();
        savedQueries.push({ name, query: q });
        saveJSON(savedKey, savedQueries);
        App.renderAnalytics();
      });

      // Saved-query buttons
      $$(".saved-q", main).forEach(b => b.addEventListener("click", () => {
        const idx = parseInt(b.dataset.savedIdx, 10);
        const q = savedQueries[idx]?.query;
        if (!q) return;
        query = q;
        saveJSON(wsKey, query);
        App.renderAnalytics();
      }));
      $$(".saved-q-rm", main).forEach(b => b.addEventListener("click", (e) => {
        e.stopPropagation();
        const idx = parseInt(b.dataset.savedIdx, 10);
        savedQueries.splice(idx, 1);
        saveJSON(savedKey, savedQueries);
        App.renderAnalytics();
      }));

      // Auto-run if we have a stored query with a metric.
      if (query.metric) {
        runQuery();
      }
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
