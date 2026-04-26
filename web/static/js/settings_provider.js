// Claude CLI prereq verification panel.
// Wires into the existing #llm-provider <select> (PR #14).
// No separate radio buttons — the provider selector already exists.
const providerSwitch = {
    _escape(s) {
        if (typeof window.escapeHtml === 'function') return window.escapeHtml(s);
        return String(s == null ? '' : s)
            .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    },

    // Last-known check result, used to gate save.
    lastCheck: null,

    init() {
        const providerSelect = document.getElementById('llm-provider');
        const checkBtn = document.getElementById('claude-cli-check-btn');

        if (!providerSelect || !checkBtn) return;

        // Patch toggleProviderFields so switching to claude-cli auto-runs
        // the check once (on first select, lastCheck === null).
        const originalToggle = window.toggleProviderFields;
        window.toggleProviderFields = function () {
            if (typeof originalToggle === 'function') originalToggle();
            if (providerSelect.value === 'claude-cli' && providerSwitch.lastCheck === null) {
                providerSwitch.runCheck();
            }
        };

        checkBtn.addEventListener('click', () => providerSwitch.runCheck());
    },

    async runCheck() {
        const list = document.getElementById('claude-cli-check-results');
        const summary = document.getElementById('claude-cli-status-summary');
        if (!list || !summary) return;

        list.innerHTML = '<li class="pending"><span class="check-name">Checking…</span></li>';
        summary.textContent = '';

        let resp;
        try {
            const r = await fetch('/api/claude-cli/check', {
                method: 'POST',
                credentials: 'same-origin',
            });
            if (!r.ok) throw new Error('status ' + r.status);
            resp = await r.json();
        } catch (e) {
            list.innerHTML = '<li class="fail"><span class="check-name">request</span><span class="check-detail">' + providerSwitch._escape(String(e)) + '</span></li>';
            summary.textContent = 'Probe request failed; cannot enable Claude CLI.';
            summary.style.color = '#f44336';
            providerSwitch.lastCheck = { ok: false };
            return;
        }

        list.innerHTML = '';
        for (const c of (resp.checks || [])) {
            const li = document.createElement('li');
            li.className = c.status === 'ok' ? 'ok' : 'fail';
            const icon = c.status === 'ok' ? '✓' : '✗';
            li.innerHTML = '<span class="check-name">' + icon + ' ' + providerSwitch._escape(c.name) + '</span>'
                + '<span class="check-detail">' + providerSwitch._escape(c.detail || '') + '</span>';
            list.appendChild(li);
        }
        providerSwitch.lastCheck = resp;
        if (resp.ok) {
            summary.textContent = '✓ All checks passed — Claude CLI ready';
            summary.style.color = '#4caf50';
        } else {
            summary.textContent = '✗ Cannot enable Claude CLI until all checks pass';
            summary.style.color = '#f44336';
        }
    },

    // Returns true if save should proceed; false if the user is trying
    // to enable claude-cli but checks haven't passed. Called from applySettings().
    canSave() {
        const providerSelect = document.getElementById('llm-provider');
        if (!providerSelect || providerSelect.value !== 'claude-cli') return true;
        if (providerSwitch.lastCheck && providerSwitch.lastCheck.ok) return true;
        alert('Cannot enable Claude CLI: prereq checks have not all passed. Click "Check Claude CLI prereqs" to retry.');
        return false;
    },
};

document.addEventListener('DOMContentLoaded', () => providerSwitch.init());

if (typeof window !== 'undefined') {
    window.providerSwitch = providerSwitch;
}
