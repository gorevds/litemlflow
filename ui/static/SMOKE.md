# UI v2 Manual Smoke Tests

Run these after every significant UI change. Server must be running:
`make build && ./bin/litemlflow up --data /tmp/lmf-ui`

---

## 1. Existing routes still load

Navigate to each route and verify no blank page / console error:

- `/#/experiments` — experiment list loads (or empty-state card)
- `/#/experiments/1` — experiment detail with runs table (skip if no data)
- `/#/experiments/1/runs/<id>` — run detail with params/metrics (skip if no data)
- `/#/prompts` — prompts page (not the old placeholder)
- `/#/about` — about page

---

## 2. Dark / light theme

1. Click the `◐` theme button in the header.
2. Verify background, text, border, and accent colours all switch.
3. Reload the page — theme should persist (localStorage).
4. Open command palette (`Ctrl+K`), shortcut overlay (`?`), and bulk bar with ≥1 run checked — confirm all three obey the active theme.

---

## 3. Keyboard shortcut chords

1. Press `g` then `e` (within 1 s) — should navigate to `/#/experiments`.
2. Press `g` then `p` — should navigate to `/#/prompts`.
3. Press `g` then `h` — should navigate to `/#/about`.
4. Press `g`, wait >1 s, then press `e` — should NOT navigate (chord expired).

---

## 4. j/k row navigation + Enter

1. Go to `/#/experiments` (ensure ≥1 experiment exists).
2. Press `j` — first row gets `[data-selected]` highlight.
3. Press `j` again — second row highlighted, first un-highlighted.
4. Press `k` — back to first row.
5. Press `Enter` — navigates to that experiment's detail page.

---

## 5. Shortcut help overlay (`?`)

1. Press `?` — overlay appears with shortcut table.
2. Press `Esc` — overlay closes.
3. Press `?` again — overlay reopens.
4. Click outside the modal — overlay closes.

---

## 6. Command palette (`Cmd/Ctrl+K`)

1. Press `Ctrl+K` (or `Cmd+K` on Mac) — palette modal appears.
2. Static commands visible: "Go to: experiments", "Toggle theme", etc.
3. Type `prom` — list filters to commands containing "prom".
4. Use `↓`/`↑` arrows to highlight an item, press `Enter` — navigates/executes.
5. Press `Esc` — palette closes.
6. Select "Toggle theme" via palette — theme switches.
7. Select "Copy: tracking URI" — no error; clipboard should contain `http://localhost:5000`.

---

## 7. Prompts page — add & list

1. Go to `/#/prompts`.
2. Verify the v0.4 limitation notice is shown.
3. In the "+ Add" input, type the name of a prompt that exists on the server, press Enter.
   - If the prompt exists: redirected to `/#/prompts/<name>`.
   - If it doesn't exist: input border turns red briefly; no navigation.
4. Navigate back to `/#/prompts` — the added name appears in the table.

---

## 8. Prompt detail page

1. Navigate to `/#/prompts/<name>` for a prompt with ≥2 versions.
2. Version history table shows rows with version numbers and timestamps.
3. Alias badges ("production", "candidate") appear if resolved.
4. Diff section is visible; change the two version selects and click "Show diff".
5. Diff renders with green `+` lines and red `-` lines. Identical versions show "Select two different versions."

---

## 9. Runs bulk-select

1. Go to `/#/experiments/<id>` with ≥2 runs.
2. Click the checkbox on run 1 — bulk action bar appears at bottom with count "1 run selected".
3. Click checkbox on run 2 — bar shows "2 runs selected", Compare button enabled.
4. Click "Compare" — navigates to `/#/experiments/<id>/compare?runs=id1,id2`.
5. Compare page shows a params table (differing params highlighted amber) and metric sparklines.
6. Back to experiment, re-check same runs — checkboxes are still checked (state preserved across render).
7. Click "✕ Clear" in bulk bar — bar disappears, all checkboxes unchecked.
8. Check ≥1 run, click "Export JSON" — browser downloads a `.json` file.
9. Check ≥1 run, click "Delete" — confirm dialog; run removed from table.

---

## 10. Embed mode

1. Navigate to `http://localhost:5000/ui/?embed=1#/experiments`.
2. Verify the `<header>` and `<footer>` are hidden (`display:none`).
3. Main content still renders correctly.
4. Navigate to a different hash — embed mode persists within the same page load.

---

## 11. Workspace selector

1. Header contains a `<select>` dropdown next to the theme button.
2. Default shows "default" workspace.
3. If `GET /api/v1/workspaces` returns multiple workspaces, they appear in the list.
4. Selecting a non-default workspace sets the `lmf_workspace` cookie and reloads the current view.
5. Reloading the page — the workspace cookie restores the previously selected workspace.

---

## 12. Page weight check

```bash
wc -c ui/static/app.js ui/static/styles.css
```

Total should be under 61 440 bytes (60 KB). Fail the check if over budget.
