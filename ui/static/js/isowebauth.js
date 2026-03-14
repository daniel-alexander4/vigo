// isowebauth.js — Client-side flow for SSH key-based authentication.
// Communicates with the isowebauth desktop app on localhost:7890.

async function handleIsowebauthLogin(e) {
    e.preventDefault();
    const btn = document.getElementById('login-btn');
    const errEl = document.getElementById('login-error');
    const username = document.getElementById('username').value.trim();
    if (!username) return;

    btn.disabled = true;
    btn.textContent = 'Signing...';
    errEl.style.display = 'none';

    try {
        // 1. Get challenge from server.
        const challengeResp = await fetch('/auth/isowebauth/challenge');
        if (!challengeResp.ok) throw new Error('Failed to get challenge');
        const { challenge, namespace } = await challengeResp.json();

        // 2. Ask isowebauth app to sign.
        let signResp;
        try {
            signResp = await fetch('http://127.0.0.1:7890/sign', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ challenge, namespace }),
            });
        } catch (err) {
            throw new Error('Cannot connect to isowebauth app. Is it running?');
        }
        const signData = await signResp.json();
        if (!signData.ok) throw new Error(signData.error || 'Signing refused');

        // 3. Verify signature with server.
        const verifyResp = await fetch('/auth/isowebauth/verify', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ challenge, signature: signData.signature, username }),
        });
        const verifyData = await verifyResp.json();
        if (!verifyResp.ok) throw new Error(verifyData.error || 'Verification failed');

        // Success — redirect to dashboard.
        window.location.href = '/';
    } catch (err) {
        errEl.textContent = err.message;
        errEl.style.display = 'block';
        btn.disabled = false;
        btn.textContent = 'Sign In';
    }
}
