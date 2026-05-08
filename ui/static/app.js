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

      // Hash-based router
      window.addEventListener("hashchange", () => this.route());
      this.route();
    },

    route() {
      const hash = (window.location.hash || "#/experiments").slice(1);
      const main = $("#app");
      main.innerHTML = '<div class="loading">Loading…</div>';

      const expMatch = hash.match(/^\/experiments\/(\d+)$/);
      const runMatch = hash.match(/^\/experiments\/(\d+)\/runs\/([0-9a-f]+)$/);

      if (runMatch) {
        return this.renderRun(parseInt(runMatch[1], 10), runMatch[2]);
      }
      if (expMatch) {
        return this.renderExperiment(parseInt(expMatch[1], 10));
      }
      if (hash.startsWith("/prompts")) {
        return this.renderPrompts();
      }
      if (hash.startsWith("/about")) {
        return this.renderAbout();
      }
      return this.renderExperiments();
    },

    async renderExperiments() {
      const main = $("#app");
      try {
        const data = await fetchJSON("/api/2.0/mlflow/experiments/search?max_results=1000");
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
        const rows = exps.map(e => `
          <tr onclick="location.hash='#/experiments/${e.experiment_id}'">
            <td class="mono">${e.experiment_id}</td>
            <td><a href="#/experiments/${e.experiment_id}">${escapeHTML(e.name)}</a></td>
            <td>${formatTime(e.creation_time)}</td>
            <td>${formatTime(e.last_update_time)}</td>
            <td>${(e.tags || []).map(t => `<span class="tag">${escapeHTML(t.key)}=${escapeHTML(t.value)}</span>`).join(" ")}</td>
          </tr>`).join("");
        main.innerHTML = `
          <h1>Experiments</h1>
          <div class="card" style="padding:0">
            <table>
              <thead><tr><th>ID</th><th>Name</th><th>Created</th><th>Updated</th><th>Tags</th></tr></thead>
              <tbody>${rows}</tbody>
            </table>
          </div>
        `;
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    async renderExperiment(expID) {
      const main = $("#app");
      try {
        const [exp, runsRes] = await Promise.all([
          fetchJSON(`/api/2.0/mlflow/experiments/get?experiment_id=${expID}`),
          fetchJSON(`/api/2.0/mlflow/runs/search`, {
            method: "POST",
            body: JSON.stringify({ experiment_ids: [String(expID)], max_results: 200 }),
          }),
        ]);
        const e = exp.experiment;
        const runs = runsRes.runs || [];
        const rows = runs.map(r => {
          const info = r.info, data = r.data;
          const metrics = (data.metrics || []).map(m => `${escapeHTML(m.key)}=${m.value.toPrecision(4)}`).join(", ");
          return `
            <tr onclick="location.hash='#/experiments/${expID}/runs/${info.run_id}'">
              <td class="mono">${info.run_id.slice(0, 8)}</td>
              <td>${escapeHTML(info.run_name || "—")}</td>
              <td><span class="status-pill status-${info.status}">${info.status}</span></td>
              <td>${formatTime(info.start_time)}</td>
              <td>${info.end_time ? formatDuration(info.end_time - info.start_time) : "—"}</td>
              <td class="mono">${escapeHTML(metrics)}</td>
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
          <h2>Runs (${runs.length})</h2>
          <div class="card" style="padding:0">
            <table>
              <thead><tr><th>ID</th><th>Name</th><th>Status</th><th>Started</th><th>Duration</th><th>Metrics</th></tr></thead>
              <tbody>${rows || `<tr><td colspan="6" class="empty">No runs yet.</td></tr>`}</tbody>
            </table>
          </div>
        `;
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    async renderRun(expID, runID) {
      const main = $("#app");
      try {
        const data = await fetchJSON(`/api/v1/runs/${runID}/data`);
        const params = (data.params || []).map(p => `<tr><td>${escapeHTML(p.Key || p.key)}</td><td class="mono">${escapeHTML(p.Value || p.value)}</td></tr>`).join("");
        const tags = (data.tags || []).map(t => `<span class="tag">${escapeHTML(t.Key || t.key)}=${escapeHTML(t.Value || t.value)}</span>`).join(" ");
        const metrics = data.metrics || [];
        const isTrace = data.kind === "trace";

        main.innerHTML = `
          <div class="crumbs">
            <a href="#/experiments">Experiments</a> /
            <a href="#/experiments/${expID}">exp ${expID}</a> / ${data.id}
          </div>
          <h1>
            ${escapeHTML(data.name || "(unnamed run)")}
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
                <tr><td>Tags</td><td class="tag-list">${tags || "—"}</td></tr>
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
        `;
      } catch (err) {
        main.innerHTML = `<div class="empty">Failed to load: ${escapeHTML(String(err))}</div>`;
      }
    },

    async renderMetricCharts(runID, metrics) {
      if (!metrics || metrics.length === 0) return "";
      const charts = [];
      for (const m of metrics) {
        const key = m.Key || m.key;
        const hist = await fetchJSON(`/api/2.0/mlflow/metrics/get-history?run_id=${runID}&metric_key=${encodeURIComponent(key)}`);
        charts.push(simpleChart(key, hist.metrics || []));
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

    async renderPrompts() {
      $("#app").innerHTML = `
        <h1>Prompts</h1>
        <div class="empty card">
          Prompts are versioned via the native API. v0.1 has no list endpoint;
          a directory page lands in v0.2. Use:
          <pre>POST /api/v1/prompts {"name":"foo","content":"..."}
GET  /api/v1/prompts/foo
GET  /api/v1/prompts/foo/versions</pre>
        </div>`;
    },

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
        </div>`;
    },
  };

  function fetchJSON(url, init) {
    init = init || {};
    init.headers = Object.assign({ "Content-Type": "application/json" }, init.headers || {});
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

  // simpleChart renders an SVG sparkline. Server-side downsampling is added
  // in v0.2; for v0.1 we cap the points client-side.
  function simpleChart(key, points) {
    if (!points.length) return "";
    if (points.length > 1000) {
      // crude downsample
      const stride = Math.ceil(points.length / 1000);
      points = points.filter((_, i) => i % stride === 0);
    }
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
    return `
      <div class="card" style="padding:8px 12px">
        <strong>${escapeHTML(key)}</strong> <span style="color:var(--fg-muted)">(${points.length} points, min ${vmin.toPrecision(4)}, max ${vmax.toPrecision(4)})</span>
        <div class="metric-chart">
          <svg width="100%" height="100%" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">
            <path d="${d}" fill="none" stroke="var(--accent)" stroke-width="1.5" />
          </svg>
        </div>
      </div>`;
  }

  document.addEventListener("DOMContentLoaded", () => App.init());
})();
