(function() {
    window.submit_shodan = async function() {
        const query = document.getElementById('shodan-query').value.trim();
        const command = document.getElementById('shodan-command').value;
        const limit = document.getElementById('shodan-limit').value;

        if (!query && command !== 'validate') {
            showNotification('Enter a Shodan query', 'error');
            return;
        }

        const meta = document.getElementById('shodan-results-meta');
        const body = document.getElementById('shodan-results-body');
        meta.textContent = 'Searching...';
        body.innerHTML = '<p class="muted">Loading...</p>';

        try {
            var toolQuery = query || 'validate';
            var msg = 'Use shodan_search tool with query="' + toolQuery + '" command="' + command + '"';
            if (command === 'search' || command === 'count') {
                msg += ' limit=' + limit;
            }
            msg += '. Return the raw results.';

            const resp = await apiFetch('/api/agent-loop', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({message: msg})
            });
            const data = await resp.json();
            meta.textContent = 'Done';
            body.innerHTML = '<pre style="white-space:pre-wrap;font-size:12px;">' +
                (data.response || JSON.stringify(data, null, 2)) + '</pre>';
        } catch(e) {
            meta.textContent = 'Error';
            body.innerHTML = '<p style="color:red;">' + e.message + '</p>';
        }
    };

    window.reset_shodan = function() {
        document.getElementById('shodan-query').value = '';
        document.getElementById('shodan-command').value = 'search';
        document.getElementById('shodan-limit').value = '50';
        document.getElementById('shodan-results-meta').textContent = '-';
        document.getElementById('shodan-results-body').innerHTML =
            '<p class="muted">Enter a query and click Search.</p>';
    };
})();
