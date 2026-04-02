(function() {
    window.submit_censys = async function() {
        const query = document.getElementById('censys-query').value.trim();
        if (!query) { showNotification('Enter a Censys query', 'error'); return; }
        const type = document.getElementById('censys-type').value;
        const limit = document.getElementById('censys-limit').value;
        const meta = document.getElementById('censys-results-meta');
        const body = document.getElementById('censys-results-body');
        meta.textContent = 'Searching...';
        body.innerHTML = '<p class="muted">Loading...</p>';
        try {
            const resp = await apiFetch('/api/agent-loop', {
                method: 'POST', headers: {'Content-Type':'application/json'},
                body: JSON.stringify({message: `Use censys-search tool with query="${query}" resource_type="${type}" limit=${limit}. Return the raw results.`})
            });
            const data = await resp.json();
            meta.textContent = 'Done';
            body.innerHTML = '<pre style="white-space:pre-wrap;font-size:12px;">' + (data.response || JSON.stringify(data, null, 2)) + '</pre>';
        } catch(e) { meta.textContent = 'Error'; body.innerHTML = '<p style="color:red;">' + e.message + '</p>'; }
    };
    window.reset_censys = function() {
        document.getElementById('censys-query').value = '';
        document.getElementById('censys-results-meta').textContent = '-';
        document.getElementById('censys-results-body').innerHTML = '<p class="muted">Enter a query and click Search.</p>';
    };
})();
