(function () {
  'use strict';
  var key = 'relayproxy-ui-theme';
  var media = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

  function platformName() {
    var value = String((navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || navigator.userAgent || '').toLowerCase();
    if (value.indexOf('mac') >= 0 || value.indexOf('iphone') >= 0 || value.indexOf('ipad') >= 0) return 'macos';
    if (value.indexOf('win') >= 0) return 'windows';
    if (value.indexOf('linux') >= 0 || value.indexOf('x11') >= 0) return 'linux';
    return 'web';
  }

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

  document.documentElement.dataset.platform = platformName();

  window.RelayUITheme = {
    get: function () { return normalize(document.documentElement.dataset.themeMode || stored()); },
    effective: effective,
    set: function (mode) { return apply(mode, true); },
    apply: function (mode, persist) { return apply(mode, persist); },
    platform: function () { return document.documentElement.dataset.platform || 'web'; }
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

  function tablistKeydown(event) {
    var tab = event.target.closest && event.target.closest('[role="tab"]');
    if (!tab) return;
    var list = tab.closest('[role="tablist"]');
    if (!list) return;
    var vertical = list.getAttribute('aria-orientation') === 'vertical';
    var nextKey = vertical ? 'ArrowDown' : 'ArrowRight';
    var prevKey = vertical ? 'ArrowUp' : 'ArrowLeft';
    if (event.key !== nextKey && event.key !== prevKey && event.key !== 'Home' && event.key !== 'End') return;
    var tabs = Array.prototype.filter.call(list.querySelectorAll('[role="tab"]'), function (item) {
      return !item.disabled && item.getAttribute('aria-disabled') !== 'true' && !item.hidden && item.offsetParent !== null;
    });
    if (!tabs.length) return;
    var index = tabs.indexOf(tab);
    var target;
    if (event.key === 'Home') target = tabs[0];
    else if (event.key === 'End') target = tabs[tabs.length - 1];
    else if (event.key === nextKey) target = tabs[(index + 1 + tabs.length) % tabs.length];
    else target = tabs[(index - 1 + tabs.length) % tabs.length];
    event.preventDefault();
    target.focus();
    target.click();
  }
  document.addEventListener('keydown', tablistKeydown);
})();
