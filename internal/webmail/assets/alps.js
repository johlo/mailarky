// Alps emits this after sign-out has finished its request and local cleanup.
// Let its remaining synchronous handlers finish before leaving the application.
window.addEventListener('session-cleared', event => {
  if (event.detail?.reason === 'expired') return;
  queueMicrotask(() => window.location.replace('/'));
});
