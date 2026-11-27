/* golab docs — progressive enhancement only. The site is fully readable with
   JavaScript disabled: navigation links, prose and diagrams are plain HTML. */
(function () {
  'use strict';

  var STORAGE_KEY = 'golab-docs-theme';
  var root = document.documentElement;

  function stored() {
    try { return localStorage.getItem(STORAGE_KEY); } catch (e) { return null; }
  }
  function save(value) {
    try { localStorage.setItem(STORAGE_KEY, value); } catch (e) { /* private mode */ }
  }

  /* Theme: system (prefers-color-scheme) -> light -> dark -> system. */
  var themeButton = document.getElementById('theme-toggle');
  var themeLabel = document.getElementById('theme-label');
  var modes = ['system', 'light', 'dark'];
  var current = modes.indexOf(stored()) >= 0 ? stored() : 'system';

  function applyTheme(mode) {
    if (mode === 'system') { root.removeAttribute('data-theme'); }
    else { root.setAttribute('data-theme', mode); }
    if (themeLabel) { themeLabel.textContent = mode.charAt(0).toUpperCase() + mode.slice(1); }
    if (themeButton) {
      themeButton.setAttribute('aria-label', 'Colour theme: ' + mode + '. Activate to change.');
      themeButton.setAttribute('title', 'Theme: ' + mode);
    }
  }
  applyTheme(current);
  if (themeButton) {
    themeButton.addEventListener('click', function () {
      current = modes[(modes.indexOf(current) + 1) % modes.length];
      applyTheme(current);
      save(current);
    });
  }

  /* Sidebar filter. */
  var search = document.getElementById('site-search');
  var groups = Array.prototype.slice.call(document.querySelectorAll('.nav-group'));
  var empty = document.getElementById('search-empty');

  function filter() {
    if (!search) { return; }
    var query = search.value.trim().toLowerCase();
    var anyVisible = false;
    groups.forEach(function (group) {
      var title = group.querySelector('.nav-group-title');
      var groupMatch = title && title.textContent.toLowerCase().indexOf(query) >= 0;
      var visible = 0;
      Array.prototype.forEach.call(group.querySelectorAll('.nav-item'), function (item) {
        var show = !query || groupMatch || item.textContent.toLowerCase().indexOf(query) >= 0;
        item.hidden = !show;
        if (show) { visible++; }
      });
      group.hidden = visible === 0;
      if (visible > 0) { anyVisible = true; }
    });
    if (empty) { empty.hidden = anyVisible || !query; }
  }
  if (search) { search.addEventListener('input', filter); }

  /* Keyboard help: "/" focuses the filter, Escape clears it. */
  document.addEventListener('keydown', function (event) {
    var tag = (event.target && event.target.tagName || '').toLowerCase();
    var typing = tag === 'input' || tag === 'textarea' || (event.target && event.target.isContentEditable);
    if (event.key === '/' && !typing && search) {
      event.preventDefault();
      search.focus();
    }
    if (event.key === 'Escape') {
      closeSidebar();
      if (search && document.activeElement === search && search.value) {
        search.value = '';
        filter();
      }
    }
  });

  /* Mobile navigation. */
  var toggle = document.getElementById('sidebar-toggle');
  var overlay = document.querySelector('.sidebar-overlay');
  var sidebar = document.getElementById('site-sidebar');

  function closeSidebar() {
    document.body.classList.remove('sidebar-open');
    if (toggle) { toggle.setAttribute('aria-expanded', 'false'); toggle.setAttribute('aria-label', 'Open navigation'); }
  }
  if (toggle) {
    toggle.addEventListener('click', function () {
      var open = !document.body.classList.contains('sidebar-open');
      document.body.classList.toggle('sidebar-open', open);
      toggle.setAttribute('aria-expanded', String(open));
      toggle.setAttribute('aria-label', open ? 'Close navigation' : 'Open navigation');
      if (open && search) { search.focus(); }
    });
  }
  if (overlay) { overlay.addEventListener('click', closeSidebar); }
  if (sidebar) {
    Array.prototype.forEach.call(sidebar.querySelectorAll('.nav-item a'), function (link) {
      link.addEventListener('click', closeSidebar);
    });
  }
  var wide = window.matchMedia('(min-width: 901px)');
  if (wide.addEventListener) { wide.addEventListener('change', closeSidebar); }
  else if (wide.addListener) { wide.addListener(closeSidebar); }

  /* Mark the current page in the tree using resolved URLs (baseurl-safe). */
  Array.prototype.forEach.call(document.querySelectorAll('.nav-item a'), function (link) {
    try {
      if (new URL(link.href).pathname === window.location.pathname) {
        link.setAttribute('aria-current', 'page');
      }
    } catch (e) { /* relative href edge case: ignore */ }
  });

  /* Copy buttons on code blocks. */
  function legacyCopy(text) {
    var area = document.createElement('textarea');
    area.value = text;
    area.setAttribute('readonly', '');
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.appendChild(area);
    area.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    document.body.removeChild(area);
    return ok;
  }
  Array.prototype.forEach.call(document.querySelectorAll('.prose pre'), function (pre) {
    if (pre.querySelector('.copy-btn')) { return; }
    var code = pre.querySelector('code');
    var button = document.createElement('button');
    button.type = 'button';
    button.className = 'copy-btn';
    button.textContent = 'Copy';
    button.setAttribute('aria-label', 'Copy code block');
    function feedback(done) {
      button.textContent = done ? 'Copied' : 'Select code';
      button.setAttribute('data-state', done ? 'copied' : 'failed');
      setTimeout(function () {
        button.textContent = 'Copy';
        button.removeAttribute('data-state');
      }, 1600);
    }
    button.addEventListener('click', function () {
      var text = code ? code.textContent : pre.textContent;
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(
          function () { feedback(true); },
          function () { feedback(legacyCopy(text)); }
        );
      } else {
        feedback(legacyCopy(text));
      }
    });
    pre.appendChild(button);
  });
}());
