(function() {
    window.submit_crtsh = async function() {
        const query = document.getElementById('crtsh-query').value.trim();
        if (!query) { showNotification('Enter a domain', 'error'); return; }
        const mode = document.getElementById('crtsh-mode').value;
        const meta = document.getElementById('crtsh-results-meta');
        const body = document.getElementById('crtsh-results-body');
        meta.textContent = 'Searching crt.sh...';
        body.innerHTML = '<p class="muted">Loading (may take 10-30s for uncached domains)...</p>';
        try {
            const resp = await apiFetch('/api/agent-loop', {
                method: 'POST', headers: {'Content-Type':'application/json'},
                body: JSON.stringify({message: `Use crtsh-search tool with query="${query}" mode="${mode}". Return the complete list of subdomains found.`})
            });
            const data = await resp.json();
            meta.textContent = 'Done';
            body.innerHTML = '<pre style="white-space:pre-wrap;font-size:12px;">' + (data.response || JSON.stringify(data, null, 2)) + '</pre>';
        } catch(e) { meta.textContent = 'Error'; body.innerHTML = '<p style="color:red;">' + e.message + '</p>'; }
    };
    window.reset_crtsh = function() {
        document.getElementById('crtsh-query').value = '';
        document.getElementById('crtsh-results-meta').textContent = '-';
        document.getElementById('crtsh-results-body').innerHTML = '<p class="muted">Enter a domain and click Search.</p>';
    };
})();
