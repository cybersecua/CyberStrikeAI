// Container fleet management

let _containerDeployCmd = '';
let _buildToolboxSSE = null;

// ── helpers ───────────────────────────────────────────────────────────────────

function esc(s) {
    if (!s) return '';
    return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function i18nC(key) {
    return (window._i18n && window._i18n['containers'] && window._i18n['containers'][key]) || key;
}

// ── container list ────────────────────────────────────────────────────────────

function loadContainersPage() {
    if (typeof apiFetch === 'undefined') return;
    apiFetch('/api/containers')
        .then(function(r) { return r.json(); })
        .then(function(list) { renderContainers(Array.isArray(list) ? list : []); })
        .catch(function(e) {
            console.warn('containers: load failed', e);
            showToast(i18nC('loadFailed'), 'error');
        });
}

function renderContainers(list) {
    var empty = document.getElementById('containers-empty');
    var table = document.getElementById('containers-table');
    var tbody = document.getElementById('containers-tbody');
    if (!tbody) return;
    tbody.innerHTML = '';
    if (!list || list.length === 0) {
        if (empty) empty.style.display = '';
        if (table) table.style.display = 'none';
        return;
    }
    if (empty) empty.style.display = 'none';
    if (table) table.style.display = '';
    list.forEach(function(c) {
        var tr = document.createElement('tr');
        var onlineBadge = c.isOnline
            ? '<span style="color:var(--success-color,#4caf50);font-weight:600">' + i18nC('online') + '</span>'
            : '<span style="color:var(--text-muted)">' + i18nC('offline') + '</span>';
        var lastSeen = c.lastSeenAt ? new Date(c.lastSeenAt).toLocaleString() : '—';
        var created  = c.createdAt  ? new Date(c.createdAt).toLocaleString()  : '—';
        tr.innerHTML =
            '<td><strong>' + esc(c.name) + '</strong></td>' +
            '<td>' + esc(c.hostname || '—') + '</td>' +
            '<td>' + esc(c.ipAddress || '—') + '</td>' +
            '<td>' + onlineBadge + '</td>' +
            '<td style="font-size:12px;color:var(--text-muted)">' + created + '</td>' +
            '<td style="font-size:12px;color:var(--text-muted)">' + lastSeen + '</td>' +
            '<td>' +
                '<button class="btn-secondary" style="font-size:12px;margin-right:4px" onclick="showContainerDeployCmd(\'' + esc(c.id) + '\')">' + i18nC('deployCmd') + '</button>' +
                '<button class="btn-danger" style="font-size:12px" onclick="deleteContainer(\'' + esc(c.id) + '\')">' + i18nC('delete') + '</button>' +
            '</td>';
        tbody.appendChild(tr);
    });
}

// ── new container modal (manual secret / docker run command) ──────────────────

function showNewContainerModal() {
    var modal = document.getElementById('modal-new-container');
    if (!modal) return;
    document.getElementById('new-container-name').value = '';
    document.getElementById('new-container-panel-url').value = window.location.origin;
    document.getElementById('new-container-form-wrap').style.display = '';
    document.getElementById('new-container-result-wrap').style.display = 'none';
    document.getElementById('new-container-submit').style.display = '';
    modal.style.display = 'flex';
}

function closeNewContainerModal() {
    var modal = document.getElementById('modal-new-container');
    if (modal) modal.style.display = 'none';
}

function submitNewContainer() {
    var name = (document.getElementById('new-container-name').value || '').trim() || 'kali-cs';
    var panelUrl = (document.getElementById('new-container-panel-url').value || '').trim();
    var submit = document.getElementById('new-container-submit');
    if (submit) submit.disabled = true;

    apiFetch('/api/containers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: name, panelUrl: panelUrl })
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (submit) submit.disabled = false;
        if (data.error) { showToast(i18nC('createFailed') + ': ' + data.error, 'error'); return; }
        document.getElementById('new-container-docker-run').textContent = data.dockerRun || '';
        document.getElementById('new-container-secret').textContent = (data.container && data.container.gsSecret) || '';
        document.getElementById('new-container-form-wrap').style.display = 'none';
        document.getElementById('new-container-result-wrap').style.display = '';
        if (submit) submit.style.display = 'none';
        _containerDeployCmd = data.dockerRun || '';
        loadContainersPage();
    })
    .catch(function(e) {
        if (submit) submit.disabled = false;
        showToast(i18nC('createFailed'), 'error');
    });
}

function copyContainerDeployCmd() {
    var cmd = document.getElementById('new-container-docker-run');
    if (!cmd) return;
    navigator.clipboard.writeText(cmd.textContent).then(function() {
        var btn = document.getElementById('new-container-copy-btn');
        if (btn) { btn.textContent = i18nC('copiedCmd'); setTimeout(function() { btn.textContent = i18nC('copyCmd'); }, 2000); }
    });
}

// ── deploy-cmd modal (show existing secret / run command) ─────────────────────

function showContainerDeployCmd(id) {
    apiFetch('/api/containers/' + id + '/deploy-cmd?panelUrl=' + encodeURIComponent(window.location.origin))
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var pre = document.getElementById('deploy-modal-cmd');
            if (pre) pre.textContent = data.dockerRun || '';
            _containerDeployCmd = data.dockerRun || '';
            var modal = document.getElementById('modal-container-deploy');
            if (modal) modal.style.display = 'flex';
        });
}

function copyDeployModalCmd() {
    var cmd = document.getElementById('deploy-modal-cmd');
    if (!cmd) return;
    navigator.clipboard.writeText(cmd.textContent);
}

// ── delete ────────────────────────────────────────────────────────────────────

function deleteContainer(id) {
    if (!confirm(i18nC('deleteConfirm'))) return;
    apiFetch('/api/containers/' + id, { method: 'DELETE' })
        .then(function() { loadContainersPage(); })
        .catch(function() { showToast(i18nC('deleteFailed'), 'error'); });
}

// ── Build Deployable Toolbox modal ───────────────────────────────────────────

function showBuildToolboxModal() {
    // Reset state
    if (_buildToolboxSSE) { _buildToolboxSSE.close(); _buildToolboxSSE = null; }

    var modal    = document.getElementById('modal-build-toolbox');
    var formWrap = document.getElementById('build-toolbox-form-wrap');
    var outWrap  = document.getElementById('build-toolbox-output-wrap');
    var submit   = document.getElementById('build-toolbox-submit');
    var log      = document.getElementById('build-toolbox-log');
    var closeBtn = document.getElementById('build-toolbox-close-btn');

    // Pre-fill panel URL with current origin
    var panelInput = document.getElementById('build-toolbox-panel-url');
    if (panelInput && !panelInput.value) panelInput.value = window.location.origin;

    if (formWrap) formWrap.style.display = '';
    if (outWrap)  outWrap.style.display  = 'none';
    if (log)      log.textContent = '';
    if (submit)   { submit.disabled = false; submit.textContent = i18nC('buildAndRun'); }
    if (closeBtn) closeBtn.onclick = closeBuildToolboxModal;

    if (modal) modal.style.display = 'flex';
}

function closeBuildToolboxModal() {
    if (_buildToolboxSSE) { _buildToolboxSSE.close(); _buildToolboxSSE = null; }
    var modal = document.getElementById('modal-build-toolbox');
    if (modal) modal.style.display = 'none';
}

function startBuildToolbox() {
    var name       = (document.getElementById('build-toolbox-name').value || '').trim() || ('kali-' + Date.now().toString().slice(-4));
    var panelUrl   = (document.getElementById('build-toolbox-panel-url').value || '').trim();
    var forceRebuild = document.getElementById('build-toolbox-force').checked;

    var formWrap = document.getElementById('build-toolbox-form-wrap');
    var outWrap  = document.getElementById('build-toolbox-output-wrap');
    var submit   = document.getElementById('build-toolbox-submit');
    var log      = document.getElementById('build-toolbox-log');
    var closeBtn = document.getElementById('build-toolbox-close-btn');

    // Switch to output view
    if (formWrap) formWrap.style.display = 'none';
    if (outWrap)  outWrap.style.display  = '';
    if (log)      log.textContent = '';
    if (submit)   { submit.disabled = true; submit.textContent = i18nC('buildAndRun') + '…'; }

    // Disable close during build (allow but warn)
    if (closeBtn) {
        closeBtn.onclick = function() {
            if (confirm('Cancel the build?')) closeBuildToolboxModal();
        };
    }

    function appendLog(msg, color) {
        if (!log) return;
        var line = document.createElement('div');
        if (color) line.style.color = color;
        line.textContent = msg;
        log.appendChild(line);
        log.scrollTop = log.scrollHeight;
    }

    // POST the build request — server streams SSE back
    if (typeof apiFetch === 'undefined') { appendLog('apiFetch not available', '#f85149'); return; }

    // We need to POST with a body but still read SSE. Use fetch + ReadableStream.
    fetch('/api/containers/build-and-run/stream', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ name: name, panelUrl: panelUrl, forceBuild: forceRebuild })
    }).then(function(resp) {
        if (!resp.ok) {
            return resp.text().then(function(t) { appendLog('Error: ' + t, '#f85149'); });
        }

        var reader = resp.body.getReader();
        var decoder = new TextDecoder();
        var buf = '';

        function pump() {
            return reader.read().then(function(result) {
                if (result.done) {
                    if (submit) { submit.disabled = false; submit.textContent = i18nC('buildAndRun'); }
                    if (closeBtn) closeBtn.onclick = closeBuildToolboxModal;
                    return;
                }
                buf += decoder.decode(result.value, { stream: true });
                var parts = buf.split('\n\n');
                buf = parts.pop();
                parts.forEach(function(part) {
                    var line = part.replace(/^data:\s*/, '').trim();
                    if (!line) return;
                    try {
                        var evt = JSON.parse(line);
                        if (evt.type === 'log') {
                            appendLog(evt.message);
                        } else if (evt.type === 'done') {
                            appendLog('', null);
                            appendLog('✓ ' + evt.message, '#3fb950');
                            if (submit) { submit.disabled = false; submit.textContent = i18nC('buildAndRun'); }
                            if (closeBtn) closeBtn.onclick = closeBuildToolboxModal;
                            loadContainersPage();
                        } else if (evt.type === 'error') {
                            appendLog('✗ ' + evt.message, '#f85149');
                            if (submit) { submit.disabled = false; submit.textContent = i18nC('buildAndRun'); }
                            if (closeBtn) closeBtn.onclick = closeBuildToolboxModal;
                        }
                    } catch (e) { /* ignore malformed lines */ }
                });
                return pump();
            }).catch(function(e) {
                appendLog('Stream error: ' + e.message, '#f85149');
                if (submit) { submit.disabled = false; }
                if (closeBtn) closeBtn.onclick = closeBuildToolboxModal;
            });
        }
        return pump();
    }).catch(function(e) {
        appendLog('Request failed: ' + e.message, '#f85149');
        if (submit) { submit.disabled = false; }
        if (closeBtn) closeBtn.onclick = closeBuildToolboxModal;
    });
}

// ── page router hook ──────────────────────────────────────────────────────────
(function() {
    var _orig = window.switchPage;
    window.switchPage = function(page) {
        if (typeof _orig === 'function') _orig.apply(this, arguments);
        if (page === 'containers') loadContainersPage();
    };
})();
