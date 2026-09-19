(function () {
  'use strict';
  var key = 'relayproxy-ui-theme';
  var media = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

  function normalize(mode) {
    mode = String(mode || '').toLowerCase();
    return mode === 'light' || mode === 'dark' || mode === 'system' ? mode : 'system';
  }
  function stored() {
    try { return normalize(localStorage.getItem(key) || 'system'); }
    catch (_) { return 'system'; }
  }
  function effective(mode) {
    return mode === 'system' ? (media && media.matches ? 'dark' : 'light') : mode;
  }
  function apply(mode, persist) {
    mode = normalize(mode);
    var actual = effective(mode);
    document.documentElement.classList.toggle('dark', actual === 'dark');
    document.documentElement.dataset.theme = actual;
    document.documentElement.dataset.themeMode = mode;
    if (persist !== false) {
      try { localStorage.setItem(key, mode); } catch (_) {}
    }
    document.querySelectorAll('[data-rp-theme]').forEach(function (button) {
      button.classList.toggle('active', button.dataset.rpTheme === mode);
      button.setAttribute('aria-pressed', String(button.dataset.rpTheme === mode));
    });
    window.dispatchEvent(new CustomEvent('relayproxy-theme-change', { detail: { mode: mode, theme: actual } }));
    return actual;
  }

  window.RelayUITheme = {
    get: function () { return normalize(document.documentElement.dataset.themeMode || stored()); },
    effective: effective,
    set: function (mode) { return apply(mode, true); },
    apply: function (mode, persist) { return apply(mode, persist); }
  };

  if (media) {
    var onMedia = function () {
      if (window.RelayUITheme.get() === 'system') apply('system', false);
    };
    if (media.addEventListener) media.addEventListener('change', onMedia);
    else if (media.addListener) media.addListener(onMedia);
  }

  apply(document.documentElement.dataset.themeMode || stored(), false);
  document.addEventListener('click', function (event) {
    var button = event.target.closest && event.target.closest('[data-rp-theme]');
    if (button) window.RelayUITheme.set(button.dataset.rpTheme);
  });
})();
