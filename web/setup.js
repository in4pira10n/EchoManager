const i18nReady = window.EchoManagerI18n?.ready || Promise.resolve();
const t = (path) => window.EchoManagerI18n?.t(path) || path;
const form = document.querySelector('#setup-form');
const submitButton = document.querySelector('#setup-submit');
const feedback = document.querySelector('#setup-feedback');
form.addEventListener('submit', async (event) => {
  event.preventDefault();
  await i18nReady;
  if (submitButton.disabled) return;
  submitButton.disabled = true; feedback.textContent = '';
  const username = document.querySelector('#setup-username').value.trim();
  const password = document.querySelector('#setup-password').value;
  try {
    if (form.dataset.hasUsers === 'true') {
      const login = await fetch('/api/auth/login', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({username,password})});
      const result = await login.json(); if (!login.ok) throw new Error(result.error);
    }
    const response = await fetch('/api/setup', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({echokit_dir:document.querySelector('#echokit-dir').value.trim(), username, password, confirm_password:document.querySelector('#setup-confirm')?.value || password})});
    const result = await response.json(); if (!response.ok) throw new Error(result.error || t('setup.initializationFailed'));
    location.assign(result.redirect || '/login');
  } catch(error) { feedback.textContent = error.message || t('common.networkError'); submitButton.disabled = false; }
});
