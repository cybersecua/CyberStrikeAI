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
