// Debug tab: sessions list + per-session actions + bulk export.
// Loaded after settings.js. Task 22 attaches debugTab.openViewer.
const debugTab = {
    _escape(str) {
        // escapeHtml is a top-level function defined in auth.js (loaded before this file).
        if (typeof escapeHtml === 'function') return escapeHtml(str);
        // Minimal fallback in case auth.js is not loaded.
        return String(str == null ? '' : str)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    },

    async loadSessions() {
        const tbody = document.getElementById('debug-sessions-tbody');
        if (!tbody) return;
        tbody.innerHTML = '';
        let rows;
        try {
            const resp = await fetch('/api/debug/sessions', { credentials: 'same-origin' });
            if (!resp.ok) throw new Error('status ' + resp.status);
            rows = await resp.json();
        } catch (e) {
            tbody.innerHTML = '<tr><td colspan="7">' + debugTab._escape(String(e)) + '</td></tr>';
            return;
        }
        if (!rows || rows.length === 0) {
            const emptyMsg = (typeof window.t === 'function')
                ? window.t('settingsDebug.emptyState')
                : 'No debug sessions yet.';
            tbody.innerHTML = '<tr><td colspan="7">' + debugTab._escape(emptyMsg) + '</td></tr>';
            return;
        }
        for (const r of rows) {
            tbody.appendChild(debugTab.renderRow(r));
        }
    },

    renderRow(r) {
        const tr = document.createElement('tr');
        tr.dataset.conversationId = r.conversationId;

        const started = r.startedAt
            ? new Date(r.startedAt / 1_000_000).toISOString().replace('T', ' ').replace(/\..*/, '')
            : '-';
        const durSecs = r.durationMs ? Math.round(r.durationMs / 1000) : '-';
        const tokens  = (r.promptTokens || 0) + ' / ' + (r.completionTokens || 0);

        const t   = (typeof window.t === 'function') ? window.t.bind(window) : (k) => k.split('.').pop();
        const esc = debugTab._escape.bind(debugTab);

        tr.innerHTML = `
            <td>${esc(started)}</td>
            <td><input class="debug-label-input" type="text" value="${esc(r.label || '')}" placeholder="${esc(t('settingsDebug.labelPlaceholder'))}" /></td>
            <td>${esc(r.outcome || '')}</td>
            <td>${r.iterations || 0}</td>
            <td>${esc(tokens)}</td>
            <td>${esc(String(durSecs))}${typeof durSecs === 'number' ? 's' : ''}</td>
            <td>
                <button class="btn-mini debug-view-btn">${esc(t('settingsDebug.view'))}</button>
                <button class="btn-mini debug-export-raw-btn">${esc(t('settingsDebug.exportRaw'))}</button>
                <button class="btn-mini debug-export-sg-btn">${esc(t('settingsDebug.exportShareGPT'))}</button>
                <button class="btn-mini btn-danger debug-delete-btn">${esc(t('settingsDebug.delete'))}</button>
            </td>
        `;

        const convID = r.conversationId;
        tr.querySelector('.debug-label-input').addEventListener('change', (e) => debugTab.saveLabel(convID, e.target.value));
        tr.querySelector('.debug-view-btn').addEventListener('click', () => {
            if (typeof debugTab.openViewer === 'function') {
                debugTab.openViewer(convID); // stub — Task 22 attaches the viewer
            }
        });
        tr.querySelector('.debug-export-raw-btn').addEventListener('click', () => debugTab.download(convID, 'raw'));
        tr.querySelector('.debug-export-sg-btn').addEventListener('click', () => debugTab.download(convID, 'sharegpt'));
        tr.querySelector('.debug-delete-btn').addEventListener('click', () => debugTab.deleteRow(convID));
        return tr;
    },

    async saveLabel(convID, label) {
        try {
            await fetch('/api/debug/sessions/' + encodeURIComponent(convID), {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json' },
                credentials: 'same-origin',
                body: JSON.stringify({ label }),
            });
        } catch (e) { console.warn('saveLabel failed', e); }
    },

    download(convID, format) {
        const a = document.createElement('a');
        a.href = '/api/conversations/' + encodeURIComponent(convID) + '/export?format=' + encodeURIComponent(format);
        document.body.appendChild(a);
        a.click();
        a.remove();
    },

    downloadBulk() {
        const a = document.createElement('a');
        a.href = '/api/debug/export-bulk?format=sharegpt';
        document.body.appendChild(a);
        a.click();
        a.remove();
    },

    async deleteRow(convID) {
        const t = (typeof window.t === 'function') ? window.t.bind(window) : (k, args) => {
            let s = k.split('.').pop();
            if (args) for (const key in args) s = s.replace('{{' + key + '}}', args[key]);
            return s;
        };
        const msg = t('settingsDebug.deleteConfirm', { id: convID });
        if (!confirm(msg)) return;
        const resp = await fetch('/api/debug/sessions/' + encodeURIComponent(convID), {
            method: 'DELETE',
            credentials: 'same-origin',
        });
        if (resp.status === 204 || resp.status === 404) {
            await debugTab.loadSessions();
        } else {
            alert('delete failed: ' + resp.status);
        }
    },
};

// Wire tab-open trigger + button handlers after DOM is ready.
document.addEventListener('DOMContentLoaded', () => {
    const refreshBtn = document.getElementById('debug-refresh-btn');
    if (refreshBtn) refreshBtn.addEventListener('click', () => debugTab.loadSessions());

    const bulkBtn = document.getElementById('debug-export-bulk-btn');
    if (bulkBtn) bulkBtn.addEventListener('click', () => debugTab.downloadBulk());

    // Load sessions whenever the Debug nav-item is clicked. The nav-item also
    // has an inline onclick="switchSettingsSection('debug')" — both fire fine.
    const debugNavItem = document.querySelector('.settings-nav-item[data-section="debug"]');
    if (debugNavItem) {
        debugNavItem.addEventListener('click', () => debugTab.loadSessions());
    }
});

// Expose on window so Task 22 can attach openViewer.
window.debugTab = debugTab;

debugTab.openViewer = async function(convID) {
    const panel = document.getElementById('debug-viewer-panel');
    const title = document.getElementById('debug-viewer-title');
    const body  = document.getElementById('debug-viewer-body');
    if (!panel || !title || !body) return;

    title.textContent = convID;
    body.innerHTML = '<div style="padding:16px">Loading…</div>';
    panel.hidden = false;

    let data;
    try {
        const resp = await fetch('/api/debug/sessions/' + encodeURIComponent(convID), { credentials: 'same-origin' });
        if (resp.status === 404) {
            body.innerHTML = '<div style="padding:16px">Session was deleted.</div>';
            return;
        }
        if (!resp.ok) throw new Error('status ' + resp.status);
        data = await resp.json();
    } catch (e) {
        body.innerHTML = '<div style="padding:16px">' + debugTab._escape(String(e)) + '</div>';
        return;
    }

    // llmCalls come from LoadLLMCallsExported which returns []LLMCallRow — a Go
    // struct with NO json tags, so gin encodes it with PascalCase field names:
    //   SentAt, AgentID, Iteration, PromptTokens, CompletionTokens,
    //   RequestJSON (string), ResponseJSON (string), Error
    // Events come from LoadEventsExported → rawEventLine → camelCase keys:
    //   startedAt, agentId, eventType, …
    const items = [];
    for (const c of (data.llmCalls || [])) {
        items.push({ kind: 'llm_call', t: c.SentAt || 0, row: c });
    }
    for (const e of (data.events || [])) {
        items.push({ kind: 'event', t: e.startedAt || 0, row: e });
    }
    items.sort((a, b) => a.t - b.t);

    const frag = document.createDocumentFragment();
    const esc  = debugTab._escape;

    for (const it of items) {
        if (it.kind === 'event') {
            const d = document.createElement('div');
            d.className = 'debug-event';
            const when = it.t ? new Date(it.t / 1_000_000).toISOString().replace('T', ' ').replace(/\..*Z$/, '') : '';
            d.textContent = '[' + when + '] ' + (it.row.eventType || '') + ' (agent=' + (it.row.agentId || '-') + ')';
            frag.appendChild(d);
        } else {
            // RequestJSON / ResponseJSON arrive as plain JSON strings (Go string field,
            // not json.RawMessage). Pretty-print them for readability.
            const prettyJSON = (s) => {
                if (!s) return '';
                try { return JSON.stringify(JSON.parse(s), null, 2); } catch (_) { return s; }
            };
            const d = document.createElement('div');
            d.className = 'debug-llmcall';
            const when   = it.t ? new Date(it.t / 1_000_000).toISOString().replace('T', ' ').replace(/\..*Z$/, '') : '';
            const tokens = (it.row.PromptTokens || 0) + '/' + (it.row.CompletionTokens || 0);
            const reqText = prettyJSON(it.row.RequestJSON);
            const resText = prettyJSON(it.row.ResponseJSON);
            const errorBlock = it.row.Error
                ? '<strong>Error:</strong><pre style="max-height:200px;overflow:auto">' + esc(it.row.Error) + '</pre>'
                : '';
            d.innerHTML = `
                <details>
                    <summary>[${esc(when)}] LLM call — iter ${esc(String(it.row.Iteration || 0))}, agent ${esc(it.row.AgentID || '-')}, tokens ${esc(tokens)}</summary>
                    <div style="margin-top:8px">
                        <strong>Request:</strong>
                        <pre style="max-height:400px;overflow:auto">${esc(reqText)}</pre>
                        <strong>Response:</strong>
                        <pre style="max-height:400px;overflow:auto">${esc(resText)}</pre>
                        ${errorBlock}
                    </div>
                </details>
            `;
            frag.appendChild(d);
        }
    }
    body.innerHTML = '';
    body.appendChild(frag);
};

// Close-button handler — registered on DOMContentLoaded so it is wired on the
// same tick as the rest of the debug tab, and remains valid across all
// openViewer calls (the panel is reused, not recreated).
document.addEventListener('DOMContentLoaded', () => {
    const closeBtn = document.getElementById('debug-viewer-close');
    if (closeBtn) {
        closeBtn.addEventListener('click', () => {
            const panel = document.getElementById('debug-viewer-panel');
            if (panel) panel.hidden = true;
        });
    }
});
