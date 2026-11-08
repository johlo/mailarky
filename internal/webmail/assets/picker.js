(() => {
  'use strict';
  // Keep bookmarks made before the account picker was introduced working.
  if (window.location.hash) {
    window.location.replace('/alps' + window.location.hash);
    return;
  }
  const list = document.getElementById('accounts');
  const search = document.getElementById('search');
  const refresh = document.getElementById('refresh');
  const error = document.getElementById('error');
  const status = document.getElementById('status');
  const empty = document.getElementById('empty');
  let accounts = [], loaded = false, loading = false, opening = '';

  function element(tag, className, text) {
    const node = document.createElement(tag);
    node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function showError(message) {
    error.textContent = message;
    error.hidden = !message;
  }

  function label(account) {
    return account.name && account.name !== account.id ? account.name : account.username;
  }

  function render() {
    const focused = document.activeElement?.dataset.account;
    const query = search.value.trim().toLocaleLowerCase();
    const filtered = accounts.filter(a => [a.name, a.username, a.id].some(value => value.toLocaleLowerCase().includes(query)));
    const count = document.getElementById('count');
    count.textContent = query ? `${filtered.length} of ${accounts.length} accounts` : `${accounts.length} account${accounts.length === 1 ? '' : 's'}`;
    list.replaceChildren();
    for (const account of filtered) {
      const item = document.createElement('li');
      const button = element('button', 'account');
      button.type = 'button';
      button.dataset.account = account.id;
      button.setAttribute('aria-label', `Open ${label(account)} in Alps`);
      button.setAttribute('aria-busy', String(opening === account.id));
      button.disabled = !!opening;
      const identity = element('span', 'identity');
      const name = element('span', 'name-line');
      const title = element('span', 'name', label(account));
      title.title = label(account);
      name.append(title);
      if (account.id === 'default') name.append(element('span', 'badge', 'Default'));
      identity.append(name, element('span', 'username', account.username));
      const created = element('time', 'created');
      const date = new Date(account.created);
      created.dateTime = account.created;
      if (!Number.isNaN(date.valueOf())) {
        created.textContent = date.toLocaleDateString(undefined, {month: 'short', day: 'numeric'});
        created.title = `Created ${date.toLocaleString()}`;
      }
      const action = element('span', 'open-label');
      action.append(element('span', 'open-text', opening === account.id ? 'Opening…' : 'Open inbox'), element('span', 'arrow', '→'));
      action.setAttribute('aria-hidden', 'true');
      button.append(identity, created, action);
      button.addEventListener('click', () => openAccount(account));
      item.append(button);
      list.append(item);
      if (focused === account.id && !opening) button.focus({preventScroll: true});
    }
    empty.hidden = filtered.length > 0;
    document.getElementById('empty-title').textContent = !loaded ? 'Loading accounts…' : query ? 'No matching accounts' : 'No accounts available';
    document.getElementById('empty-detail').textContent = !loaded ? '' : query ? 'Try another name or username.' : 'Accounts created by your tests will appear here.';
  }

  async function loadAccounts(announce = false) {
    if (loading || opening) return;
    loading = true;
    refresh.setAttribute('aria-busy', 'true');
    list.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch('/mailarky/accounts', {cache: 'no-store', signal: AbortSignal.timeout(15000)});
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'Could not load accounts. Try refreshing.');
      data.sort((a, b) => a.id === 'default' ? -1 : b.id === 'default' ? 1 : b.created.localeCompare(a.created) || label(a).localeCompare(label(b)));
      const changed = JSON.stringify(data) !== JSON.stringify(accounts) || !loaded;
      accounts = data;
      loaded = true;
      showError('');
      if (changed) render();
      if (announce) status.textContent = `Account list refreshed. ${accounts.length} accounts available.`;
    } catch (e) {
      showError(e.name === 'TimeoutError' ? 'Mailarky is taking too long to respond. Try refreshing.' : e.message);
      if (!loaded) {
        document.getElementById('empty-title').textContent = 'Could not load accounts';
        document.getElementById('empty-detail').textContent = 'Check the connection and select Refresh to try again.';
      }
    } finally {
      loading = false;
      refresh.setAttribute('aria-busy', 'false');
      list.setAttribute('aria-busy', 'false');
    }
  }

  async function openAccount(account) {
    if (opening) return;
    opening = account.id;
    showError('');
    status.textContent = `Opening ${label(account)} in Alps…`;
    render();
    try {
      const logout = await fetch('/session', {method: 'DELETE', signal: AbortSignal.timeout(15000)});
      if (!logout.ok) throw new Error('Could not close the previous Alps session. Try again.');
      const response = await fetch(`/mailarky/accounts/${encodeURIComponent(account.id)}/login`, {method: 'POST', signal: AbortSignal.timeout(30000)});
      const data = await response.json();
      if (!response.ok) {
        if (response.status === 404) accounts = accounts.filter(a => a.id !== account.id);
        if (data.error === 'connection_limit') throw new Error('This account has reached its connection limit. Close another mail client and try again.');
        throw new Error(data.error || 'Could not sign in. Check this account’s limits and configured faults.');
      }
      if (!data.ok && !data.requires_2fa) throw new Error('Alps did not complete the login. Try again.');
      window.location.assign(data.requires_2fa ? '/alps#/login/webauthn' : '/alps#/mailbox/INBOX');
    } catch (e) {
      opening = '';
      status.textContent = '';
      showError(e.name === 'TimeoutError' ? 'The login timed out. Check the account’s configured faults and try again.' : e.message);
      render();
      Array.from(list.querySelectorAll('button')).find(button => button.dataset.account === account.id)?.focus();
    }
  }

  search.addEventListener('input', render);
  refresh.addEventListener('click', () => loadAccounts(true));
  window.addEventListener('focus', () => loadAccounts());
  window.addEventListener('pageshow', () => { opening = ''; render(); loadAccounts(); });
  setInterval(() => { if (!document.hidden) loadAccounts(); }, 5000);
  loadAccounts();
})();
