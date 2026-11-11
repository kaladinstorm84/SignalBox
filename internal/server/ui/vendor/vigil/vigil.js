/**
 * VIGIL.JS — Live Monitoring Dashboard Framework v0.6.0
 * Declarative polling, data binding, and status mapping.
 */

(function (global) {
  'use strict';

  // ── Constants ──────────────────────────────────────────────
  const ATTR_SRC        = 'data-src';
  const ATTR_POLL       = 'data-poll';
  const ATTR_BIND       = 'data-bind';
  const ATTR_EACH       = 'data-each';
  const ATTR_IF         = 'data-if';
  const ATTR_IF_NOT     = 'data-if-not';
  const ATTR_FORMAT     = 'data-format';
  const ATTR_STATUS_MAP = 'data-status-map';
  const ATTR_ROW_STATUS = 'data-row-status';
  const ATTR_TRANSFORM    = 'data-transform';
  const ATTR_HEADERS      = 'data-headers';
  const ATTR_RESPONSE_MAP = 'data-response-map';
  const ATTR_PROXY        = 'data-proxy';
  const ATTR_BIND_ATTR    = 'data-bind-attr';
  const ATTR_BIND_HTML    = 'data-bind-html';
  const ATTR_BIND_TPL     = 'data-bind-template';
  const ATTR_CLASS        = 'data-class';
  const ATTR_EACH_LIMIT   = 'data-each-limit';
  const ATTR_METHOD       = 'data-method';
  const ATTR_BODY         = 'data-body';

  // ── Global configuration ─────────────────────────────────
  const _config = {
    headers: {},
    proxy: null,
    staleThreshold: 2,
    errorThreshold: 4,
    maxBackoff: 30000,
    onAuthError: null,
    observe: false,
  };

  // ── Response maps (panel-level response transforms) ──────
  const responseMaps = {};

  const STATUS_CLASSES = [
    'vg-dot--success','vg-dot--fail','vg-dot--running','vg-dot--warn',
    'vg-dot--skipped','vg-dot--cancelled','vg-dot--unknown',
    'vg-badge--success','vg-badge--fail','vg-badge--running','vg-badge--warn',
    'vg-badge--skipped','vg-badge--cancelled',
    'vg-row--success','vg-row--fail','vg-row--running','vg-row--warn',
    'vg-row--cancelled',
  ];

  // ── Transforms ─────────────────────────────────────────────
  const transforms = {};

  // ── Formatters ─────────────────────────────────────────────
  const formatters = {
    number(v)   { return Number(v).toLocaleString(); },
    percent(v)  { return Number(v).toFixed(1) + '%'; },
    duration(v) {
      const ms = Number(v);
      if (isNaN(ms)) return v;
      if (ms < 1000) return ms + 'ms';
      const s = Math.floor(ms / 1000);
      if (s < 60)  return s + 's';
      const m = Math.floor(s / 60), rs = s % 60;
      if (m < 60)  return m + 'm ' + rs + 's';
      const h = Math.floor(m / 60), rm = m % 60;
      return h + 'h ' + rm + 'm';
    },
    relative(v) {
      const d = new Date(v);
      if (isNaN(d)) return v;
      const diff = Date.now() - d.getTime();
      if (diff < 5000)  return 'just now';
      if (diff < 60000) return Math.floor(diff / 1000) + 's ago';
      if (diff < 3600000) return Math.floor(diff / 60000) + 'm ago';
      return Math.floor(diff / 3600000) + 'h ago';
    },
    date(v) {
      const d = new Date(v);
      return isNaN(d) ? v : d.toLocaleString();
    },
    short_date(v) {
      const d = new Date(v);
      return isNaN(d) ? v : d.toLocaleDateString();
    },
    successrate(v) {
      const n = Number(v);
      return isNaN(n) ? v : n.toFixed(1) + '%';
    },
    passrate(v) { return formatters.successrate(v); },
    uppercase(v) { return String(v).toUpperCase(); },
    lowercase(v) { return String(v).toLowerCase(); },
  };

  // ── Deep get from dot-path ─────────────────────────────────
  function deepGet(obj, path) {
    if (!path) return obj;
    return path.split('.').reduce((acc, k) => (acc == null ? undefined : acc[k]), obj);
  }

  // ── Apply status class ──────────────────────────────────────
  function applyStatusClass(el, prefix, value) {
    // Remove existing status classes for this prefix
    el.classList.forEach(c => {
      if (c.startsWith(prefix + '--')) el.classList.remove(c);
    });
    if (value) el.classList.add(prefix + '--' + value);
  }

  // ── Render template node with data ─────────────────────────
  function renderNode(root, data) {
    if (!root || !root.getAttribute) return;

    // data-if / data-if-not
    const ifField    = root.getAttribute(ATTR_IF);
    const ifNotField = root.getAttribute(ATTR_IF_NOT);
    if (ifField    && !deepGet(data, ifField))  { root.style.display = 'none'; return; }
    if (ifNotField &&  deepGet(data, ifNotField)) { root.style.display = 'none'; return; }
    root.style.display = '';

    // data-bind: set textContent from a single field
    const bindField = root.getAttribute(ATTR_BIND);
    if (bindField !== null) {
      let val = deepGet(data, bindField);
      const txName = root.getAttribute(ATTR_TRANSFORM);
      if (txName && transforms[txName] && val !== undefined && val !== null) {
        val = transforms[txName](val, data);
      }
      const fmt = root.getAttribute(ATTR_FORMAT);
      if (fmt === 'relative' && val !== undefined && val !== null) {
        root.setAttribute('data-live-value', val);
      }
      if (fmt && formatters[fmt] && val !== undefined && val !== null) {
        val = formatters[fmt](val);
      }

      const rawMap = root.getAttribute(ATTR_STATUS_MAP);
      if (rawMap) {
        try {
          const map  = JSON.parse(rawMap);
          const rawVal = deepGet(data, bindField);
          const mapped = map[rawVal] || map['*'] || rawVal;
          if (root.classList.contains('vg-dot'))   applyStatusClass(root, 'vg-dot', mapped);
          if (root.classList.contains('vg-badge'))  applyStatusClass(root, 'vg-badge', mapped);
          if (!root.classList.contains('vg-dot')) {
            root.textContent = fmt ? val : (rawVal ?? '');
          }
        } catch (e) { /* invalid JSON */ }
        _applyBindAttr(root, data);
        _applyDataClass(root, data);
        return;
      }

      if (root.classList && root.classList.contains('vg-sparkline') && Array.isArray(val)) {
        renderSparkline(root, val, {
          color: root.getAttribute('data-sparkline-color') || undefined,
          type:  root.getAttribute('data-sparkline-type')  || 'line',
        });
        return;
      }

      if (val !== undefined && val !== null) {
        root.textContent = val;
      }
    }

    // data-bind-html: set innerHTML from a field (opt-in, XSS risk)
    const bindHtmlField = root.getAttribute(ATTR_BIND_HTML);
    if (bindHtmlField !== null && bindField === null) {
      let val = deepGet(data, bindHtmlField);
      const txName = root.getAttribute(ATTR_TRANSFORM);
      if (txName && transforms[txName] && val !== undefined && val !== null) {
        val = transforms[txName](val, data);
      }
      if (val !== undefined && val !== null) {
        root.innerHTML = val;
      }
    }

    // data-bind-template: interpolate {field} and {field|format} tokens
    const bindTpl = root.getAttribute(ATTR_BIND_TPL);
    if (bindTpl !== null && bindField === null && bindHtmlField === null) {
      root.textContent = bindTpl.replace(/\{([^}]+)\}/g, function(_, expr) {
        var parts = expr.split('|');
        var field = parts[0].trim();
        var fmt   = parts[1] ? parts[1].trim() : null;
        var val   = deepGet(data, field);
        if (val === undefined || val === null) return '';
        if (fmt && formatters[fmt]) val = formatters[fmt](val);
        return val;
      });
    }

    // data-bind-attr: set element attributes from data
    _applyBindAttr(root, data);

    // data-class: conditionally toggle CSS classes
    _applyDataClass(root, data);

    // data-row-status: map field value to vg-row--* class
    const rowStatus = root.getAttribute(ATTR_ROW_STATUS);
    if (rowStatus) {
      const rawMap = root.getAttribute(ATTR_STATUS_MAP);
      if (rawMap) {
        try {
          const map = JSON.parse(rawMap);
          const rawVal = deepGet(data, rowStatus);
          const mapped = map[rawVal] || map['*'] || rawVal;
          applyStatusClass(root, 'vg-row', mapped);
        } catch (e) {}
      }
    }

    // Recurse into children, handling data-each
    const children = Array.from(root.children || []);
    for (const child of children) {
      const eachField = child.getAttribute && child.getAttribute(ATTR_EACH);
      if (eachField !== null) {
        renderEach(child, deepGet(data, eachField) || []);
      } else {
        renderNode(child, data);
      }
    }
  }

  function _applyBindAttr(el, data) {
    const raw = el.getAttribute(ATTR_BIND_ATTR);
    if (!raw) return;
    raw.split(',').forEach(function(pair) {
      var parts = pair.trim().split(':');
      var attr  = (parts[0] || '').trim();
      var field = (parts[1] || '').trim();
      if (!attr || !field) return;
      var val = deepGet(data, field);
      if (val !== undefined && val !== null) el.setAttribute(attr, val);
    });
  }

  function _applyDataClass(el, data) {
    var raw = el.getAttribute(ATTR_CLASS);
    if (!raw) return;
    raw.split(',').forEach(function(pair) {
      var parts     = pair.trim().split(':');
      var className = (parts[0] || '').trim();
      var field     = (parts[1] || '').trim();
      if (!className || !field) return;
      if (deepGet(data, field)) el.classList.add(className);
      else el.classList.remove(className);
    });
  }

  // ── Render a list container with data-each ──────────────────
  function renderEach(container, items) {
    const tmpl = container.querySelector(':scope > template');
    if (!tmpl) return;

    // data-each-limit: cap rendered items
    const limitAttr = container.getAttribute(ATTR_EACH_LIMIT);
    const limit = limitAttr ? parseInt(limitAttr, 10) : 0;
    container.setAttribute('data-each-total', items.length);
    if (limit > 0 && items.length > limit) items = items.slice(0, limit);

    // Remove previously rendered nodes (not the template)
    Array.from(container.children).forEach(c => {
      if (c !== tmpl) c.remove();
    });

    items.forEach(function(item) {
      const clone = tmpl.content.cloneNode(true);
      // Render each top-level element in the cloned fragment via renderNode,
      // which recursively handles all binding types and nested data-each.
      Array.from(clone.children).forEach(function(el) {
        renderNode(el, item);
      });
      container.appendChild(clone);
    });
  }

  // ── Fetch helpers (headers, proxy, response map) ────────────

  function buildFetchHeaders(el) {
    const merged = Object.assign(
      { 'Accept': 'application/json', 'X-Vigil-Poll': '1' },
      _config.headers
    );
    const perPanel = el.getAttribute(ATTR_HEADERS);
    if (perPanel) {
      try { Object.assign(merged, JSON.parse(perPanel)); }
      catch (e) { console.warn('[Vigil] Invalid data-headers JSON', e); }
    }
    return merged;
  }

  function buildFetchUrl(src, el) {
    const proxy = el.getAttribute(ATTR_PROXY) || _config.proxy;
    if (!proxy) return src;
    const sep = proxy.endsWith('/') ? '' : '/';
    return proxy + sep + src.replace(/^\/+/, '');
  }

  function applyResponseMap(el, data) {
    const mapName = el.getAttribute(ATTR_RESPONSE_MAP);
    if (mapName && responseMaps[mapName]) {
      try { return responseMaps[mapName](data); }
      catch (e) { console.warn('[Vigil] Response map "' + mapName + '" error', e); }
    }
    return data;
  }

  // ── PanelController ─────────────────────────────────────────
  class PanelController {
    constructor(el, options) {
      this.el           = el;
      this.src          = el.getAttribute(ATTR_SRC);
      this.pollInterval = parseInt(el.getAttribute(ATTR_POLL), 10) || 0;
      this.options      = options || {};

      this._timer        = null;
      this._consecutive_errors = 0;
      this._paused       = false;
      this._lastUpdated  = null;

      // Find loading overlay if present
      this._loadingEl = el.querySelector('.vg-panel__loading');
      this._metaEl    = el.querySelector('.vg-panel__meta[data-role="updated"]');
    }

    start() {
      this._fetch(true); // initial load
      if (this.pollInterval > 0) {
        this._timer = setInterval(() => {
          if (!this._paused && !document.hidden) this._fetch();
        }, this.pollInterval);
      }

      // Pause on tab hidden
      document.addEventListener('visibilitychange', () => {
        if (!document.hidden && !this._paused && this.pollInterval > 0) {
          this._fetch(); // immediate refresh on tab focus
        }
      });
    }

    pause()  { this._paused = true; }
    resume() { this._paused = false; this._fetch(); }
    refresh(){ this._fetch(); }

    destroy() {
      if (this._timer) clearInterval(this._timer);
    }

    async _fetch(isInitial = false) {
      if (!this.src) return;
      if (isInitial) this._setLoading(true);

      var staleAt = parseInt(this.el.getAttribute('data-retry-stale'), 10) || _config.staleThreshold;
      var errorAt = parseInt(this.el.getAttribute('data-retry-error'), 10) || _config.errorThreshold;

      try {
        const url     = buildFetchUrl(this.src, this.el);
        const headers = buildFetchHeaders(this.el);
        const method  = (this.el.getAttribute(ATTR_METHOD) || 'GET').toUpperCase();
        const bodyRaw = this.el.getAttribute(ATTR_BODY);
        const fetchOpts = { method: method, headers: headers };
        if (bodyRaw && method !== 'GET' && method !== 'HEAD') {
          fetchOpts.body = bodyRaw;
          if (!headers['Content-Type']) headers['Content-Type'] = 'application/json';
        }
        let res = await fetch(url, fetchOpts);

        // Auth error hook
        if ((res.status === 401 || res.status === 403) && _config.onAuthError) {
          const newHeaders = await _config.onAuthError(res, this);
          if (newHeaders) {
            Object.assign(fetchOpts.headers, newHeaders);
            res = await fetch(url, fetchOpts);
          }
        }

        if (!res.ok) throw new Error('HTTP ' + res.status);
        let data = await res.json();
        data = applyResponseMap(this.el, data);

        this._consecutive_errors = 0;
        this._lastUpdated = new Date();
        this._render(data);
        this._setStale(false);
        this._setError(false);
        this._flashRefresh();
        this._updateMeta();

        this.el.dispatchEvent(new CustomEvent('vigil:update', { detail: data, bubbles: true }));
      } catch (err) {
        this._consecutive_errors++;
        if (this._consecutive_errors >= staleAt) this._setStale(true);
        if (this._consecutive_errors >= errorAt) this._setError(true);
        this.el.dispatchEvent(new CustomEvent('vigil:error', { detail: err, bubbles: true }));
        console.warn('[Vigil] Fetch error for', this.src, err);
      } finally {
        if (isInitial) this._setLoading(false);
      }
    }

    _render(data) {
      const self = this.el;
      const inEach = function(el) { return el.closest('[data-each]'); };

      // data-each containers first
      self.querySelectorAll('[data-each]').forEach(function(container) {
        if (container !== self && container.parentElement.closest('[data-each]')) return;
        var field = container.getAttribute(ATTR_EACH);
        renderEach(container, deepGet(data, field) || []);
      });

      // All binding types outside data-each
      var bindSel = '[data-bind],[data-bind-html],[data-bind-template],[data-bind-attr],[data-class],[data-if],[data-if-not]';
      self.querySelectorAll(bindSel).forEach(function(el) {
        if (inEach(el)) return;
        renderNode(el, data);
      });
    }

    _setLoading(on) {
      this.el.classList.toggle('vg-panel--loading', on);
    }

    _setStale(on) {
      this.el.classList.toggle('vg-panel--stale', on);
    }

    _setError(on) {
      this.el.classList.toggle('vg-panel--error', on);
    }

    _flashRefresh() {
      this.el.classList.remove('vg-panel--refreshed');
      void this.el.offsetWidth; // reflow
      this.el.classList.add('vg-panel--refreshed');
      setTimeout(() => this.el.classList.remove('vg-panel--refreshed'), 500);
    }

    _updateMeta() {
      if (this._metaEl && this._lastUpdated) {
        this._metaEl.textContent = 'Updated ' + formatters.relative(this._lastUpdated);
      }
    }
  }

  // ── KPI Controller (lighter weight) ────────────────────────
  class KpiController {
    constructor(el) {
      this.el           = el;
      this.src          = el.getAttribute(ATTR_SRC);
      this.pollInterval = parseInt(el.getAttribute(ATTR_POLL), 10) || 0;
      this._timer       = null;
      this._paused      = false;
    }

    start() {
      this._fetch();
      if (this.pollInterval > 0) {
        this._timer = setInterval(() => {
          if (!this._paused && !document.hidden) this._fetch();
        }, this.pollInterval);
      }
    }

    pause()  { this._paused = true; }
    resume() { this._paused = false; this._fetch(); }
    refresh(){ this._fetch(); }
    destroy(){ if (this._timer) clearInterval(this._timer); }

    async _fetch() {
      try {
        const url     = buildFetchUrl(this.src, this.el);
        const headers = buildFetchHeaders(this.el);
        const res     = await fetch(url, { headers });
        let data = await res.json();
        data = applyResponseMap(this.el, data);
        this._render(data);
        this.el.dispatchEvent(new CustomEvent('vigil:update', { detail: data, bubbles: true }));
      } catch (err) {
        console.warn('[Vigil] KPI fetch error', this.src, err);
      }
    }

    _render(data) {
      this.el.querySelectorAll('[data-bind]').forEach(el => renderNode(el, data));
      // Update accent colour from data
      const status = deepGet(data, 'status');
      if (status) {
        ['vg-kpi--success','vg-kpi--fail','vg-kpi--warn','vg-kpi--running'].forEach(c =>
          this.el.classList.remove(c));
        if (['success','fail','warn','running'].includes(status)) {
          this.el.classList.add('vg-kpi--' + status);
        }
      }
    }
  }

  // ── Global Refresh Indicator ────────────────────────────────
  class RefreshIndicator {
    constructor(el) {
      this.dot  = el.querySelector('.vg-topbar__refresh-dot');
      this.text = el.querySelector('.vg-topbar__refresh-text');
      this._lastUpdate = null;
      this._timer = setInterval(() => this._tick(), 5000);
    }

    update() {
      this._lastUpdate = new Date();
      if (this.dot) {
        this.dot.className = 'vg-topbar__refresh-dot vg-topbar__refresh-dot--active';
        setTimeout(() => {
          if (this.dot) this.dot.className = 'vg-topbar__refresh-dot';
        }, 800);
      }
      this._tick();
    }

    _tick() {
      if (this.text && this._lastUpdate) {
        this.text.textContent = formatters.relative(this._lastUpdate);
      }
    }

    error() {
      if (this.dot) this.dot.className = 'vg-topbar__refresh-dot vg-topbar__refresh-dot--stale';
    }

    destroy() { clearInterval(this._timer); }
  }

  // ── Debounce helper ─────────────────────────────────────────
  function _debounce(fn, ms) {
    var timer;
    return function() {
      clearTimeout(timer);
      timer = setTimeout(fn, ms);
    };
  }

  // ── FilterGroupController ───────────────────────────────────
  class FilterGroupController {
    constructor(groupEl) {
      this.el        = groupEl;
      this.groupName = groupEl.getAttribute('data-vg-filter-group');
      this._params   = {};
      this._paramEls = [];
      var self = this;

      groupEl.querySelectorAll('[data-vg-src-param]').forEach(el => {
        const paramName = el.getAttribute('data-vg-src-param');
        self._paramEls.push({ el, paramName });
        if (el.value) self._params[paramName] = el.value;

        var isText = el.tagName === 'INPUT' && (el.type === 'text' || el.type === 'search' || el.type === 'number');
        var delay  = parseInt(el.getAttribute('data-vg-debounce'), 10);
        if (isNaN(delay)) delay = isText ? 300 : 0;

        var handler = function() {
          self._params[paramName] = el.value || '';
          self._applyFilters();
        };
        var debounced = delay > 0 ? _debounce(handler, delay) : handler;

        el.addEventListener('change', handler);
        if (isText) el.addEventListener('input', debounced);
      });
    }

    _applyFilters() {
      const controllersInGroup = _controllers.filter(c =>
        this.el.contains(c.el) || c.el.closest('[data-vg-filter-group="' + this.groupName + '"]')
      );

      controllersInGroup.forEach(c => c.pause());

      controllersInGroup.forEach(c => {
        if (!c._baseSrc) c._baseSrc = c.src;
        const url = new URL(c._baseSrc, window.location.origin);
        for (const [k, v] of Object.entries(this._params)) {
          if (v) url.searchParams.set(k, v);
          else url.searchParams.delete(k);
        }
        c.src = url.pathname + url.search;
      });

      controllersInGroup.forEach(c => c.resume());
    }
  }

  // ── SparklineRenderer ──────────────────────────────────────
  function renderSparkline(svgEl, values, options) {
    if (!Array.isArray(values) || values.length < 2) return;

    const nums = values.map(Number).filter(n => !isNaN(n));
    if (nums.length < 2) return;

    const w = svgEl.getAttribute('width')  || svgEl.clientWidth  || 80;
    const h = svgEl.getAttribute('height') || svgEl.clientHeight || 20;
    svgEl.setAttribute('viewBox', '0 0 ' + w + ' ' + h);
    svgEl.setAttribute('preserveAspectRatio', 'none');

    const min   = Math.min(...nums);
    const max   = Math.max(...nums);
    const range = max - min || 1;
    const pad   = 1;

    const points = nums.map((v, i) => {
      const x = (i / (nums.length - 1)) * (w - pad * 2) + pad;
      const y = h - pad - ((v - min) / range) * (h - pad * 2);
      return x.toFixed(1) + ',' + y.toFixed(1);
    });

    const color = options.color || 'var(--vg-accent)';
    const type  = options.type  || 'line';

    while (svgEl.firstChild) svgEl.firstChild.remove();

    if (type === 'area') {
      const area = document.createElementNS('http://www.w3.org/2000/svg', 'polygon');
      const areaPoints = pad + ',' + h + ' ' + points.join(' ') + ' ' + (w - pad) + ',' + h;
      area.setAttribute('points', areaPoints);
      area.setAttribute('fill', color);
      area.setAttribute('opacity', '0.15');
      svgEl.appendChild(area);
    }

    const line = document.createElementNS('http://www.w3.org/2000/svg', 'polyline');
    line.setAttribute('points', points.join(' '));
    line.setAttribute('fill', 'none');
    line.setAttribute('stroke', color);
    line.setAttribute('stroke-width', '1.5');
    line.setAttribute('stroke-linecap', 'round');
    line.setAttribute('stroke-linejoin', 'round');
    line.setAttribute('vector-effect', 'non-scaling-stroke');
    svgEl.appendChild(line);
  }

  // ── WsPanelController ──────────────────────────────────────
  class WsPanelController {
    constructor(el, options) {
      this.el          = el;
      this.wsUrl       = el.getAttribute('data-ws');
      this.wsEvent     = el.getAttribute('data-ws-event') || null;
      this.options     = options || {};

      this._ws         = null;
      this._paused     = false;
      this._destroyed  = false;
      this._retryCount = 0;
      this._retryTimer = null;
      this._lastUpdated = null;

      this._loadingEl = el.querySelector('.vg-panel__loading');
      this._metaEl    = el.querySelector('.vg-panel__meta[data-role="updated"]');
    }

    start() {
      this._setLoading(true);
      this._connect();
    }

    pause()  { this._paused = true; }
    resume() { this._paused = false; }

    refresh() {
      if (this._ws) {
        this._ws.close();
        this._ws = null;
      }
      this._retryCount = 0;
      this._connect();
    }

    destroy() {
      this._destroyed = true;
      if (this._retryTimer) clearTimeout(this._retryTimer);
      if (this._ws) {
        this._ws.close();
        this._ws = null;
      }
    }

    _connect() {
      if (this._destroyed || !this.wsUrl) return;

      try {
        this._ws = new WebSocket(this.wsUrl);
      } catch (err) {
        console.warn('[Vigil] WebSocket connection error', this.wsUrl, err);
        this._scheduleReconnect();
        return;
      }

      this._ws.addEventListener('open', () => {
        this._retryCount = 0;
        this._setLoading(false);
        this.el.classList.remove('vg-panel--stale', 'vg-panel--error');
      });

      this._ws.addEventListener('message', (event) => {
        if (this._paused) return;
        try {
          const msg = JSON.parse(event.data);

          if (this.wsEvent) {
            const eventType = msg.event || msg.type || msg._event;
            if (eventType !== this.wsEvent) return;
          }

          const data = msg.data || msg.payload || msg;
          this._lastUpdated = new Date();
          this._render(data);
          this._updateMeta();

          this.el.dispatchEvent(new CustomEvent('vigil:update', { detail: data, bubbles: true }));
        } catch (err) {
          console.warn('[Vigil] WebSocket message parse error', err);
        }
      });

      this._ws.addEventListener('close', () => {
        if (!this._destroyed) this._scheduleReconnect();
      });

      this._ws.addEventListener('error', () => {
        this.el.classList.add('vg-panel--stale');
        this.el.dispatchEvent(new CustomEvent('vigil:error', { detail: new Error('WebSocket error'), bubbles: true }));
      });
    }

    _scheduleReconnect() {
      if (this._destroyed) return;
      this._retryCount++;
      var maxBack = parseInt(this.el.getAttribute('data-retry-max-backoff'), 10) || _config.maxBackoff;
      var staleAt = parseInt(this.el.getAttribute('data-retry-stale'), 10)      || _config.staleThreshold;
      var errorAt = parseInt(this.el.getAttribute('data-retry-error'), 10)      || _config.errorThreshold;
      const delay = Math.min(1000 * Math.pow(2, this._retryCount - 1), maxBack);
      if (this._retryCount >= staleAt) this.el.classList.add('vg-panel--stale');
      if (this._retryCount >= errorAt) this.el.classList.add('vg-panel--error');
      this._retryTimer = setTimeout(() => this._connect(), delay);
    }

    _render(data) {
      var self = this.el;
      var inEach = function(el) { return el.closest('[data-each]'); };
      self.querySelectorAll('[data-each]').forEach(function(container) {
        if (container !== self && container.parentElement.closest('[data-each]')) return;
        var field = container.getAttribute(ATTR_EACH);
        renderEach(container, deepGet(data, field) || []);
      });
      var bindSel = '[data-bind],[data-bind-html],[data-bind-template],[data-bind-attr],[data-class],[data-if],[data-if-not]';
      self.querySelectorAll(bindSel).forEach(function(el) {
        if (inEach(el)) return;
        renderNode(el, data);
      });
    }

    _setLoading(on) {
      this.el.classList.toggle('vg-panel--loading', on);
    }

    _updateMeta() {
      if (this._metaEl && this._lastUpdated) {
        this._metaEl.textContent = 'Updated ' + formatters.relative(this._lastUpdated);
      }
    }
  }

  // ── Collapsible Panels ─────────────────────────────────────
  function initCollapsiblePanels(root) {
    (root || document).querySelectorAll('[data-vg-collapsible]').forEach(panel => {
      if (panel._vgCollapsible) return;
      panel._vgCollapsible = true;
      panel.classList.add('vg-panel--collapsible');

      const header = panel.querySelector(':scope > .vg-panel__header');
      if (!header) return;

      // Inject chevron if not already present
      if (!header.querySelector('.vg-panel__collapse-icon')) {
        const icon = document.createElement('span');
        icon.className = 'vg-panel__collapse-icon';
        icon.textContent = '▾';
        icon.setAttribute('aria-hidden', 'true');
        header.prepend(icon);
      }

      // Wrap body+footer in a collapsible wrapper for smooth animation
      const body   = panel.querySelector(':scope > .vg-panel__body');
      const footer = panel.querySelector(':scope > .vg-panel__footer');
      if (body && !body.parentElement.classList.contains('vg-panel__collapsible-body')) {
        const wrapper = document.createElement('div');
        wrapper.className = 'vg-panel__collapsible-body';
        wrapper.setAttribute('role', 'region');
        var wid = 'vg-collapse-' + (++_collapseId);
        wrapper.id = wid;
        header.setAttribute('aria-controls', wid);
        panel.insertBefore(wrapper, body);
        wrapper.appendChild(body);
        if (footer) wrapper.appendChild(footer);
        wrapper.style.maxHeight = wrapper.scrollHeight + 'px';
      }
      header.setAttribute('aria-expanded', panel.hasAttribute('data-vg-collapsed') ? 'false' : 'true');

      // Persistence key
      const persistKey = panel.getAttribute('data-vg-collapse-persist');
      const groupName  = panel.getAttribute('data-vg-collapse-group');

      // Restore saved state
      if (persistKey) {
        const saved = localStorage.getItem('vg-collapse:' + persistKey);
        if (saved === 'collapsed') _collapsePanel(panel, false);
      }

      // Start collapsed if attribute says so
      if (panel.hasAttribute('data-vg-collapsed')) {
        _collapsePanel(panel, false);
      }

      header.addEventListener('click', (e) => {
        // Don't collapse when clicking buttons/links/inputs inside header
        if (e.target.closest('button, a, input, select, .vg-btn')) return;

        const isCollapsed = panel.classList.contains('vg-panel--collapsed');

        if (isCollapsed) {
          _expandPanel(panel);
          // Accordion: no action needed on expand for others
        } else {
          _collapsePanel(panel, true);
        }

        // Accordion group: collapse siblings when expanding
        if (isCollapsed && groupName) {
          document.querySelectorAll(
            '[data-vg-collapse-group="' + groupName + '"]'
          ).forEach(sibling => {
            if (sibling !== panel && !sibling.classList.contains('vg-panel--collapsed')) {
              _collapsePanel(sibling, true);
            }
          });
        }

        // Persist state
        if (persistKey) {
          localStorage.setItem(
            'vg-collapse:' + persistKey,
            panel.classList.contains('vg-panel--collapsed') ? 'collapsed' : 'expanded'
          );
        }
      });
    });
  }

  var _collapseId = 0;

  function _collapsePanel(panel, animate) {
    var header = panel.querySelector(':scope > .vg-panel__header');
    if (header) header.setAttribute('aria-expanded', 'false');
    const wrapper = panel.querySelector(':scope > .vg-panel__collapsible-body');
    if (wrapper && animate) {
      wrapper.style.maxHeight = wrapper.scrollHeight + 'px';
      void wrapper.offsetHeight; // force reflow
    }
    panel.classList.add('vg-panel--collapsed');
    if (wrapper && !animate) {
      wrapper.style.maxHeight = '0';
    }
  }

  function _expandPanel(panel) {
    var header = panel.querySelector(':scope > .vg-panel__header');
    if (header) header.setAttribute('aria-expanded', 'true');
    const wrapper = panel.querySelector(':scope > .vg-panel__collapsible-body');
    panel.classList.remove('vg-panel--collapsed');
    if (wrapper) {
      wrapper.style.maxHeight = wrapper.scrollHeight + 'px';
      const onEnd = () => {
        wrapper.style.maxHeight = 'none';
        wrapper.removeEventListener('transitionend', onEnd);
      };
      wrapper.addEventListener('transitionend', onEnd);
    }
  }

  // ── Sidebar collapse toggle ─────────────────────────────────
  function initSidebar() {
    document.querySelectorAll('[data-vg-toggle="sidebar"]').forEach(function(btn) {
      btn.addEventListener('click', function() {
        var shell = document.querySelector('.vg-shell');
        if (shell) shell.classList.toggle('vg-shell--collapsed');
      });
    });
    // Nav group collapsible sections
    document.querySelectorAll('.vg-nav-group__toggle').forEach(function(btn) {
      btn.addEventListener('click', function() {
        btn.closest('.vg-nav-group').classList.toggle('is-collapsed');
      });
    });
  }

  // ── Table sort ─────────────────────────────────────────────
  function initTableSort(root) {
    (root || document).querySelectorAll('.vg-table thead th.vg-col-sort').forEach(function(th) {
      if (th._vgSort) return;
      th._vgSort = true;
      th.setAttribute('aria-sort', 'none');
      th.addEventListener('click', function() {
        var table = th.closest('.vg-table');
        var tbody = table.querySelector('tbody');
        if (!tbody) return;
        var col   = Array.from(th.parentElement.children).indexOf(th);
        var isAsc = th.classList.contains('asc');
        th.parentElement.querySelectorAll('th').forEach(function(t) {
          t.classList.remove('asc', 'desc');
          t.setAttribute('aria-sort', 'none');
        });
        th.classList.add(isAsc ? 'desc' : 'asc');
        th.setAttribute('aria-sort', isAsc ? 'descending' : 'ascending');
        var rows = Array.from(tbody.querySelectorAll('tr'));
        rows.sort(function(a, b) {
          var av = (a.cells[col] || {}).textContent || '';
          var bv = (b.cells[col] || {}).textContent || '';
          var n  = Number(av) - Number(bv);
          if (!isNaN(n)) return isAsc ? -n : n;
          return isAsc ? bv.localeCompare(av) : av.localeCompare(bv);
        });
        rows.forEach(function(r) { tbody.appendChild(r); });
      });
    });
  }

  // ── Table search (Phase 9a) ───────────────────────────────
  function initTableSearch(root) {
    (root || document).querySelectorAll('[data-vg-table-search]').forEach(function(input) {
      if (input._vgSearch) return;
      input._vgSearch = true;
      var tableId = input.getAttribute('data-vg-table-search');
      var table   = document.getElementById(tableId);
      if (!table) return;
      var tbody = table.querySelector('tbody');
      if (!tbody) return;
      var noRow = null;
      input.addEventListener('input', _debounce(function() {
        var q = input.value.toLowerCase().trim();
        var visible = 0;
        Array.from(tbody.querySelectorAll('tr')).forEach(function(tr) {
          if (tr.classList.contains('vg-table__no-results')) return;
          var match = !q || tr.textContent.toLowerCase().indexOf(q) !== -1;
          tr.style.display = match ? '' : 'none';
          if (match) visible++;
        });
        if (!noRow) {
          noRow = document.createElement('tr');
          noRow.className = 'vg-table__no-results';
          noRow.innerHTML = '<td colspan="99" style="text-align:center;padding:var(--vg-s4);" class="vg-text-muted">No results found</td>';
          tbody.appendChild(noRow);
        }
        noRow.style.display = visible === 0 && q ? '' : 'none';
      }, 200));
    });
  }

  // ── Table selection (Phase 9b) ─────────────────────────────
  function initTableSelection(root) {
    (root || document).querySelectorAll('.vg-table--selectable').forEach(function(table) {
      if (table._vgSelect) return;
      table._vgSelect = true;
      var tbody  = table.querySelector('tbody');
      if (!tbody) return;
      var allCb  = table.querySelector('thead .vg-table__select-all');
      var update = function() {
        var rows = Array.from(tbody.querySelectorAll('tr'));
        var checked = rows.filter(function(r) {
          var cb = r.querySelector('.vg-table__row-select');
          return cb && cb.checked;
        });
        rows.forEach(function(r) {
          var cb = r.querySelector('.vg-table__row-select');
          r.classList.toggle('vg-table__row--selected', cb && cb.checked);
        });
        if (allCb) allCb.checked = checked.length === rows.length && rows.length > 0;
        table.dispatchEvent(new CustomEvent('vigil:selection-change', {
          detail: { count: checked.length, rows: checked },
          bubbles: true,
        }));
      };
      if (allCb) {
        allCb.addEventListener('change', function() {
          tbody.querySelectorAll('.vg-table__row-select').forEach(function(cb) {
            cb.checked = allCb.checked;
          });
          update();
        });
      }
      tbody.addEventListener('change', function(e) {
        if (e.target.classList.contains('vg-table__row-select')) update();
      });
    });
  }

  // ── Tabs (Phase 8a) ────────────────────────────────────────
  function initTabs(root) {
    (root || document).querySelectorAll('[data-vg-tabs]').forEach(function(tabGroup) {
      if (tabGroup._vgTabs) return;
      tabGroup._vgTabs = true;
      var tabs   = Array.from(tabGroup.querySelectorAll('.vg-tabs__tab'));
      var panels = Array.from(tabGroup.querySelectorAll('.vg-tabs__panel'));
      var list   = tabGroup.querySelector('.vg-tabs__list');
      var isLazy = tabGroup.hasAttribute('data-vg-tab-lazy');
      var scannedPanels = {};

      if (list) {
        list.setAttribute('role', 'tablist');
        tabs.forEach(function(tab) {
          tab.setAttribute('role', 'tab');
          tab.setAttribute('tabindex', tab.classList.contains('is-active') ? '0' : '-1');
          var target = tab.getAttribute('data-vg-tab-target');
          if (target) tab.setAttribute('aria-controls', target);
          tab.setAttribute('aria-selected', tab.classList.contains('is-active') ? 'true' : 'false');
        });
        panels.forEach(function(p) {
          p.setAttribute('role', 'tabpanel');
        });
      }

      // Lazy: scan the initially-active panel so its data sources start
      if (isLazy) {
        panels.forEach(function(p) {
          if (p.classList.contains('is-active')) {
            Vigil.scan(p);
            scannedPanels[p.id] = true;
          }
        });
      }

      function _pausePanel(panel) {
        _controllers.forEach(function(c) {
          if (panel.contains(c.el)) c.pause();
        });
      }

      function _resumePanel(panel) {
        _controllers.forEach(function(c) {
          if (panel.contains(c.el)) c.resume();
        });
      }

      function activate(tab) {
        // Pause/note outgoing panels
        panels.forEach(function(p) {
          if (p.classList.contains('is-active')) {
            var lazyRefresh = p.getAttribute('data-vg-lazy-refresh');
            if (lazyRefresh === 'false') _pausePanel(p);
          }
        });

        tabs.forEach(function(t) {
          t.classList.remove('is-active');
          t.setAttribute('aria-selected', 'false');
          t.setAttribute('tabindex', '-1');
        });
        panels.forEach(function(p) { p.classList.remove('is-active'); });
        tab.classList.add('is-active');
        tab.setAttribute('aria-selected', 'true');
        tab.setAttribute('tabindex', '0');
        tab.focus();
        var target = tab.getAttribute('data-vg-tab-target');
        if (target) {
          var panel = tabGroup.querySelector('#' + target);
          if (panel) {
            panel.classList.add('is-active');
            // Lazy: scan on first activation
            if (isLazy && !scannedPanels[panel.id]) {
              Vigil.scan(panel);
              scannedPanels[panel.id] = true;
            }
            // Resume paused controllers in the newly-active panel
            var lazyRefresh = panel.getAttribute('data-vg-lazy-refresh');
            if (lazyRefresh === 'false') _resumePanel(panel);
          }
        }
      }
      tabs.forEach(function(tab) {
        tab.addEventListener('click', function() { activate(tab); });
      });
      if (list) {
        list.addEventListener('keydown', function(e) {
          var idx = tabs.indexOf(document.activeElement);
          if (idx < 0) return;
          if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
            e.preventDefault();
            activate(tabs[(idx + 1) % tabs.length]);
          } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
            e.preventDefault();
            activate(tabs[(idx - 1 + tabs.length) % tabs.length]);
          }
        });
      }
    });
  }

  // ── Declarative Navigation ────────────────────────────────
  var _navContainers = [];
  var _navHashChecked = false;

  function initNavigation(root) {
    (root || document).querySelectorAll('[data-vg-nav-container]').forEach(function(container) {
      if (container._vgNav) return;
      container._vgNav = true;
      _navContainers.push(container);
    });

    (root || document).querySelectorAll('[data-vg-nav]').forEach(function(navItem) {
      if (navItem._vgNavBound) return;
      navItem._vgNavBound = true;
      navItem.addEventListener('click', function(e) {
        e.preventDefault();
        var target = navItem.getAttribute('data-vg-nav');
        _navigateTo(target, true);
      });
    });

    if (!_navHashChecked) {
      _navHashChecked = true;
      if (location.hash && location.hash.length > 1) {
        var hashTarget = location.hash.substring(1);
        var matched = document.getElementById(hashTarget);
        if (matched && matched.classList.contains('vg-nav-page')) {
          _navigateTo(hashTarget, false);
        }
      }
    }
  }

  function _navigateTo(pageId, pushHash) {
    var targetEl = document.getElementById(pageId);
    if (!targetEl || !targetEl.classList.contains('vg-nav-page')) return;

    var container = targetEl.closest('[data-vg-nav-container]');
    if (!container) return;

    var pages = Array.from(container.querySelectorAll(':scope > .vg-nav-page'));

    // Pause outgoing page if lazy-refresh is off
    pages.forEach(function(p) {
      if (p.classList.contains('is-active') && p !== targetEl) {
        var lr = p.getAttribute('data-vg-lazy-refresh');
        if (lr === 'false') {
          _controllers.forEach(function(c) { if (p.contains(c.el)) c.pause(); });
        }
      }
    });

    // Toggle pages
    pages.forEach(function(p) { p.classList.remove('is-active'); });
    targetEl.classList.add('is-active');

    // Scan on first activation (lazy)
    if (!targetEl._vgNavScanned) {
      targetEl._vgNavScanned = true;
      Vigil.scan(targetEl);
    }

    // Resume controllers if returning to a paused page
    var lr = targetEl.getAttribute('data-vg-lazy-refresh');
    if (lr === 'false') {
      _controllers.forEach(function(c) { if (targetEl.contains(c.el)) c.resume(); });
    }

    // Toggle nav item active states
    document.querySelectorAll('[data-vg-nav]').forEach(function(item) {
      item.classList.toggle('is-active', item.getAttribute('data-vg-nav') === pageId);
    });

    // Update URL hash
    if (pushHash && history.pushState) {
      history.pushState(null, '', '#' + pageId);
    }

    // Fire custom event
    targetEl.dispatchEvent(new CustomEvent('vigil:navigate', {
      detail: { page: pageId },
      bubbles: true,
    }));
  }

  // Handle browser back/forward
  window.addEventListener('popstate', function() {
    if (location.hash && location.hash.length > 1) {
      _navigateTo(location.hash.substring(1), false);
    }
  });

  // ── Toast / Notifications (Phase 8b) ──────────────────────
  var _toastContainer = null;
  function _getToastContainer() {
    if (_toastContainer && _toastContainer.parentNode) return _toastContainer;
    _toastContainer = document.createElement('div');
    _toastContainer.className = 'vg-toast-container';
    _toastContainer.setAttribute('aria-live', 'polite');
    document.body.appendChild(_toastContainer);
    return _toastContainer;
  }

  function _createToast(message, opts) {
    opts = opts || {};
    var type       = opts.type || 'info';
    var duration   = opts.duration !== undefined ? opts.duration : 4000;
    var dismissible = opts.dismissible !== false;
    var container  = _getToastContainer();

    var toast = document.createElement('div');
    toast.className = 'vg-toast vg-toast--' + type;
    toast.setAttribute('role', 'alert');

    var icons = { success: '✓', fail: '✕', warn: '⚠', info: 'ℹ' };
    toast.innerHTML =
      '<span class="vg-toast__icon">' + (icons[type] || 'ℹ') + '</span>' +
      '<span class="vg-toast__message">' + message + '</span>' +
      (dismissible ? '<button class="vg-toast__close" aria-label="Dismiss">×</button>' : '') +
      (duration > 0 ? '<div class="vg-toast__progress"><div class="vg-toast__progress-bar" style="animation-duration:' + duration + 'ms"></div></div>' : '');

    container.appendChild(toast);
    void toast.offsetHeight;
    toast.classList.add('vg-toast--visible');

    var remove = function() {
      toast.classList.remove('vg-toast--visible');
      toast.classList.add('vg-toast--exit');
      setTimeout(function() { toast.remove(); }, 300);
    };

    if (dismissible) {
      toast.querySelector('.vg-toast__close').addEventListener('click', remove);
    }
    if (duration > 0) setTimeout(remove, duration);

    return { el: toast, dismiss: remove };
  }

  // ── Modal / Dialog (Phase 8c) ─────────────────────────────
  function _createModal(opts) {
    opts = opts || {};
    var sizeClass = opts.size === 'sm' ? ' vg-modal__dialog--sm' :
                    opts.size === 'lg' ? ' vg-modal__dialog--lg' : '';
    var overlay = document.createElement('div');
    overlay.className = 'vg-modal';
    overlay.innerHTML =
      '<div class="vg-modal__backdrop"></div>' +
      '<div class="vg-modal__dialog' + sizeClass + '" role="dialog" aria-modal="true"' +
      (opts.title ? ' aria-labelledby="vg-modal-title-' + _modalId + '"' : '') + '>' +
        (opts.title ? '<div class="vg-modal__header"><h3 class="vg-modal__title" id="vg-modal-title-' + _modalId + '">' + opts.title + '</h3><button class="vg-modal__close" aria-label="Close">×</button></div>' : '') +
        '<div class="vg-modal__body">' + (opts.body || '') + '</div>' +
        (opts.footer ? '<div class="vg-modal__footer">' + opts.footer + '</div>' : '') +
      '</div>';
    _modalId++;

    document.body.appendChild(overlay);
    document.body.classList.add('vg-body--modal-open');
    void overlay.offsetHeight;
    overlay.classList.add('vg-modal--visible');

    var previousFocus = document.activeElement;
    var dialog = overlay.querySelector('.vg-modal__dialog');
    var focusable = dialog.querySelectorAll('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
    if (focusable.length) focusable[0].focus();

    var close = function() {
      overlay.classList.remove('vg-modal--visible');
      document.body.classList.remove('vg-body--modal-open');
      setTimeout(function() { overlay.remove(); }, 300);
      if (previousFocus) previousFocus.focus();
      if (opts.onClose) opts.onClose();
    };

    overlay.querySelector('.vg-modal__backdrop').addEventListener('click', close);
    var closeBtn = overlay.querySelector('.vg-modal__close');
    if (closeBtn) closeBtn.addEventListener('click', close);
    overlay.addEventListener('keydown', function(e) {
      if (e.key === 'Escape') { e.stopPropagation(); close(); }
      // Focus trap
      if (e.key === 'Tab' && focusable.length) {
        var first = focusable[0], last = focusable[focusable.length - 1];
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
      }
    });

    return { el: overlay, dialog: dialog, close: close };
  }
  var _modalId = 0;

  // ── Tooltip (Phase 8d) ────────────────────────────────────
  function initTooltips(root) {
    (root || document).querySelectorAll('[data-vg-tooltip]').forEach(function(el) {
      if (el._vgTooltip) return;
      el._vgTooltip = true;
      el.addEventListener('mouseenter', function() {
        var text = el.getAttribute('data-vg-tooltip');
        var pos  = el.getAttribute('data-vg-tooltip-pos') || 'top';
        var tip  = document.createElement('div');
        tip.className = 'vg-tooltip vg-tooltip--' + pos;
        tip.textContent = text;
        tip.id = 'vg-tip-' + (++_tooltipId);
        el.setAttribute('aria-describedby', tip.id);
        document.body.appendChild(tip);
        var rect = el.getBoundingClientRect();
        var tr   = tip.getBoundingClientRect();
        var top, left;
        if (pos === 'bottom')    { top = rect.bottom + 6; left = rect.left + rect.width / 2 - tr.width / 2; }
        else if (pos === 'left') { top = rect.top + rect.height / 2 - tr.height / 2; left = rect.left - tr.width - 6; }
        else if (pos === 'right'){ top = rect.top + rect.height / 2 - tr.height / 2; left = rect.right + 6; }
        else                     { top = rect.top - tr.height - 6; left = rect.left + rect.width / 2 - tr.width / 2; }
        tip.style.top  = (top + window.scrollY) + 'px';
        tip.style.left = (left + window.scrollX) + 'px';
        void tip.offsetHeight;
        tip.classList.add('vg-tooltip--visible');
        el._vgTipEl = tip;
      });
      el.addEventListener('mouseleave', function() {
        if (el._vgTipEl) { el._vgTipEl.remove(); el._vgTipEl = null; }
        el.removeAttribute('aria-describedby');
      });
    });
  }
  var _tooltipId = 0;

  // ── Dropdown / Popover (Phase 8e) ──────────────────────────
  function initDropdowns(root) {
    (root || document).querySelectorAll('[data-vg-dropdown]').forEach(function(trigger) {
      if (trigger._vgDrop) return;
      trigger._vgDrop = true;
      var menuId = trigger.getAttribute('data-vg-dropdown');
      var menu   = document.getElementById(menuId);
      if (!menu) return;
      menu.classList.add('vg-dropdown__menu');
      trigger.setAttribute('aria-expanded', 'false');
      trigger.setAttribute('aria-haspopup', 'true');

      function toggle(show) {
        var vis = typeof show === 'boolean' ? show : !menu.classList.contains('vg-dropdown__menu--open');
        menu.classList.toggle('vg-dropdown__menu--open', vis);
        trigger.setAttribute('aria-expanded', vis ? 'true' : 'false');
      }
      trigger.addEventListener('click', function(e) { e.stopPropagation(); toggle(); });
      document.addEventListener('click', function() { toggle(false); });
      document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape') toggle(false);
      });
      menu.querySelectorAll('.vg-dropdown__item').forEach(function(item) {
        item.addEventListener('click', function() { toggle(false); });
      });
    });
  }

  // ── Pagination (Phase 8h) ──────────────────────────────────
  function initPagination(root) {
    (root || document).querySelectorAll('[data-vg-paginate]').forEach(function(pager) {
      if (pager._vgPage) return;
      pager._vgPage = true;
      var targetId = pager.getAttribute('data-vg-paginate');
      var target   = document.getElementById(targetId);
      if (!target) return;
      var perPage  = parseInt(pager.getAttribute('data-vg-page-size'), 10) || 10;
      var current  = 1;

      function render() {
        var tbody = target.querySelector('tbody');
        if (!tbody) return;
        var rows  = Array.from(tbody.querySelectorAll('tr:not(.vg-table__no-results)'));
        var total = Math.ceil(rows.length / perPage) || 1;
        if (current > total) current = total;
        rows.forEach(function(r, i) {
          r.style.display = (i >= (current - 1) * perPage && i < current * perPage) ? '' : 'none';
        });
        pager.innerHTML = '';
        var prev = document.createElement('button');
        prev.className = 'vg-pagination__btn'; prev.textContent = '‹'; prev.disabled = current <= 1;
        prev.addEventListener('click', function() { current--; render(); });
        pager.appendChild(prev);
        var start = Math.max(1, current - 2), end = Math.min(total, start + 4);
        if (end - start < 4) start = Math.max(1, end - 4);
        for (var p = start; p <= end; p++) {
          var btn = document.createElement('button');
          btn.className = 'vg-pagination__btn' + (p === current ? ' is-active' : '');
          btn.textContent = p;
          (function(page) { btn.addEventListener('click', function() { current = page; render(); }); })(p);
          pager.appendChild(btn);
        }
        var next = document.createElement('button');
        next.className = 'vg-pagination__btn'; next.textContent = '›'; next.disabled = current >= total;
        next.addEventListener('click', function() { current++; render(); });
        pager.appendChild(next);
        pager.dispatchEvent(new CustomEvent('vigil:page-change', { detail: { page: current, total: total }, bubbles: true }));
      }
      render();
      target.addEventListener('vigil:update', function() { setTimeout(render, 0); });
    });
  }

  // ── Manual refresh button ───────────────────────────────────
  function initRefreshButtons() {
    document.querySelectorAll('[data-vg-action="refresh"]').forEach(function(btn) {
      btn.addEventListener('click', function() { Vigil.refresh(); });
    });
    document.querySelectorAll('[data-vg-action="pause"]').forEach(function(btn) {
      var paused = false;
      btn.addEventListener('click', function() {
        paused = !paused;
        if (paused) { Vigil.pause(); btn.textContent = '▶'; btn.title = 'Resume'; }
        else        { Vigil.resume(); btn.textContent = '⏸'; btn.title = 'Pause'; }
      });
    });
  }

  // ── Global relative-time ticker ────────────────────────────
  function initRelativeTimes() {
    setInterval(function() {
      document.querySelectorAll('[data-format="relative"][data-live-value]').forEach(function(el) {
        el.textContent = formatters.relative(el.getAttribute('data-live-value'));
      });
    }, 10000);
  }

  // ── Row-click detail ──────────────────────────────────────
  function initRowDetail(root) {
    (root || document).querySelectorAll('[data-vg-row-detail]').forEach(function(table) {
      if (table._vgRowDetail) return;
      table._vgRowDetail = true;
      var targetId = table.getAttribute('data-vg-row-detail');
      var detailPanel = document.getElementById(targetId);
      if (!detailPanel) return;

      var tbody = table.querySelector('tbody');
      if (!tbody) return;

      function extractRowData(tr) {
        var data = {};
        if (tr.getAttribute('data-vg-row-id')) data._id = tr.getAttribute('data-vg-row-id');
        Array.from(tr.querySelectorAll('td[data-field]')).forEach(function(td) {
          data[td.getAttribute('data-field')] = td.textContent.trim();
        });
        return data;
      }

      function showDetail(tr) {
        tbody.querySelectorAll('tr').forEach(function(r) { r.classList.remove('vg-table__row--active'); });
        tr.classList.add('vg-table__row--active');
        var data = extractRowData(tr);
        detailPanel.classList.add('is-active');
        detailPanel.setAttribute('aria-hidden', 'false');

        // Populate bindings in the detail panel
        detailPanel.querySelectorAll('[data-bind]').forEach(function(el) {
          var field = el.getAttribute('data-bind');
          if (data[field] !== undefined) el.textContent = data[field];
        });

        // If the panel has a detail-src, fetch full data
        var detailSrc = detailPanel.getAttribute('data-vg-detail-src');
        if (detailSrc && data._id) {
          var url = detailSrc.replace('{id}', encodeURIComponent(data._id));
          fetch(url, { headers: _config.headers }).then(function(res) {
            if (!res.ok) return;
            return res.json();
          }).then(function(fullData) {
            if (!fullData) return;
            renderNode(detailPanel, fullData);
          }).catch(function() {});
        }

        table.dispatchEvent(new CustomEvent('vigil:row-detail', {
          detail: { row: tr, data: data },
          bubbles: true,
        }));
      }

      function hideDetail() {
        tbody.querySelectorAll('tr').forEach(function(r) { r.classList.remove('vg-table__row--active'); });
        detailPanel.classList.remove('is-active');
        detailPanel.setAttribute('aria-hidden', 'true');
      }

      tbody.addEventListener('click', function(e) {
        var tr = e.target.closest('tr');
        if (!tr || !tbody.contains(tr)) return;
        if (tr.classList.contains('vg-table__row--active')) {
          hideDetail();
        } else {
          showDetail(tr);
        }
      });

      detailPanel.querySelectorAll('[data-vg-detail-close]').forEach(function(btn) {
        btn.addEventListener('click', hideDetail);
      });

      // Keyboard: Escape closes
      detailPanel.addEventListener('keydown', function(e) {
        if (e.key === 'Escape') hideDetail();
      });

      detailPanel.setAttribute('aria-hidden', 'true');
    });
  }

  // ── Health badge ─────────────────────────────────────────
  var _healthBadges = [];

  function initHealthBadges(root) {
    (root || document).querySelectorAll('[data-vg-health]').forEach(function(el) {
      if (el._vgHealth) return;
      el._vgHealth = true;

      var url      = el.getAttribute('data-vg-health');
      var interval = parseInt(el.getAttribute('data-vg-health-poll'), 10) || 30;
      var field    = el.getAttribute('data-vg-health-field');
      var expected = el.getAttribute('data-vg-health-value');
      var textEl   = el.querySelector('.vg-health__text');

      el.classList.add('vg-health', 'vg-health--checking');
      el.setAttribute('role', 'status');

      function check() {
        var resolvedUrl = _config.proxy ? _config.proxy + url : url;
        fetch(resolvedUrl, { headers: _config.headers }).then(function(res) {
          if (!res.ok) throw new Error('HTTP ' + res.status);
          return res.json();
        }).then(function(data) {
          var healthy = true;
          if (field && expected) {
            healthy = String(deepGet(data, field)) === expected;
          }
          el.classList.remove('vg-health--checking', 'vg-health--disconnected');
          el.classList.toggle('vg-health--connected', healthy);
          el.classList.toggle('vg-health--disconnected', !healthy);
          if (textEl) textEl.textContent = healthy ? 'Connected' : 'Unhealthy';
        }).catch(function() {
          el.classList.remove('vg-health--checking', 'vg-health--connected');
          el.classList.add('vg-health--disconnected');
          if (textEl) textEl.textContent = 'Disconnected';
        });
      }

      check();
      var badge = { el: el, timer: setInterval(check, interval * 1000), check: check };
      _healthBadges.push(badge);
    });
  }

  // ── Scan helper (used by init and Vigil.scan) ──────────────
  function _isInsideLazyInactivePanel(el) {
    var tabPanel = el.closest('[data-vg-tab-lazy] .vg-tabs__panel');
    if (tabPanel && !tabPanel.classList.contains('is-active')) return true;
    var navPage = el.closest('[data-vg-nav-container] > .vg-nav-page');
    if (navPage && !navPage.classList.contains('is-active')) return true;
    return false;
  }

  function _matchesAndSelf(root, selector) {
    var results = Array.from(root.querySelectorAll(selector));
    if (root.matches && root.matches(selector)) results.unshift(root);
    return results;
  }

  function _scanRoot(root, mountFn) {
    _matchesAndSelf(root, '[data-src]:not([data-vg-mounted])').forEach(function(el) {
      if (_isInsideLazyInactivePanel(el)) return;
      el.setAttribute('data-vg-mounted', '');
      mountFn(el);
    });
    _matchesAndSelf(root, '[data-ws]:not([data-src]):not([data-vg-mounted])').forEach(function(el) {
      if (_isInsideLazyInactivePanel(el)) return;
      el.setAttribute('data-vg-mounted', '');
      mountFn(el);
    });
    root.querySelectorAll('[data-vg-filter-group]').forEach(function(el) {
      if (el._vgFilterGroup) return;
      el._vgFilterGroup = true;
      _filterGroups.push(new FilterGroupController(el));
    });
    initCollapsiblePanels(root);
    initNavigation(root);
    initTabs(root);
    initTooltips(root);
    initDropdowns(root);
    initTableSort(root);
    initTableSearch(root);
    initTableSelection(root);
    initRowDetail(root);
    initPagination(root);
    initHealthBadges(root);
  }

  // ── Public API ─────────────────────────────────────────────
  const _controllers    = [];
  const _filterGroups   = [];
  let   _indicator      = null;

  const Vigil = {
    version: '0.6.0',
    formatters,
    transforms,
    responseMaps,

    configure(opts) {
      if (opts.headers) Object.assign(_config.headers, opts.headers);
      if (opts.proxy !== undefined) _config.proxy = opts.proxy;
      if (opts.staleThreshold !== undefined) _config.staleThreshold = opts.staleThreshold;
      if (opts.errorThreshold !== undefined) _config.errorThreshold = opts.errorThreshold;
      if (opts.maxBackoff !== undefined) _config.maxBackoff = opts.maxBackoff;
      if (opts.onAuthError !== undefined) _config.onAuthError = opts.onAuthError;
      if (opts.observe) {
        _config.observe = true;
        _startObserver();
      }
    },

    registerFormatter(name, fn) { formatters[name] = fn; },
    registerTransform(name, fn) { transforms[name] = fn; },
    registerResponseMap(name, fn) { responseMaps[name] = fn; },

    mount(el, options) {
      var ctrl;
      if (el.getAttribute('data-ws')) {
        ctrl = new WsPanelController(el, options);
      } else {
        var role = el.getAttribute('data-vg-role') || 'panel';
        if (role === 'kpi' || el.classList.contains('vg-kpi')) {
          ctrl = new KpiController(el, options);
        } else {
          ctrl = new PanelController(el, options);
        }
      }
      _controllers.push(ctrl);
      ctrl.start();
      el.addEventListener('vigil:update', function() { if (_indicator) _indicator.update(); });
      el.addEventListener('vigil:error',  function() { if (_indicator) _indicator.error(); });
      return ctrl;
    },

    /** Destroy a controller and remove its element from tracking. */
    unmount(el) {
      for (var i = _controllers.length - 1; i >= 0; i--) {
        if (_controllers[i].el === el) {
          _controllers[i].destroy();
          _controllers.splice(i, 1);
        }
      }
      el.removeAttribute('data-vg-mounted');
    },

    /** Scan a subtree for new Vigil-managed elements. */
    scan(rootEl) {
      _scanRoot(rootEl || document, function(el) { Vigil.mount(el); });
    },

    refresh() { _controllers.forEach(function(c) { c.refresh(); }); },
    pause()   { _controllers.forEach(function(c) { c.pause(); }); },
    resume()  { _controllers.forEach(function(c) { c.resume(); }); },

    collapse(el) {
      if (el.hasAttribute('data-vg-collapsible') && !el.classList.contains('vg-panel--collapsed')) _collapsePanel(el, true);
    },
    expand(el) {
      if (el.hasAttribute('data-vg-collapsible') && el.classList.contains('vg-panel--collapsed')) _expandPanel(el);
    },
    toggleCollapse(el) {
      if (!el.hasAttribute('data-vg-collapsible')) return;
      if (el.classList.contains('vg-panel--collapsed')) _expandPanel(el);
      else _collapsePanel(el, true);
    },

    navigateTo(pageId)   { _navigateTo(pageId, true); },

    toast(message, opts) { return _createToast(message, opts); },
    modal(opts)          { return _createModal(opts); },

    getSelectedRows(tableEl) {
      var selected = [];
      tableEl.querySelectorAll('tbody tr').forEach(function(tr) {
        var cb = tr.querySelector('.vg-table__row-select');
        if (cb && cb.checked) selected.push(tr);
      });
      return selected;
    },

    render(el, data) { renderNode(el, data); },
    format(name, value) { return formatters[name] ? formatters[name](value) : value; },
    sparkline(svgEl, values, options) { renderSparkline(svgEl, values, options || {}); },

    init() {
      var indicatorEl = document.querySelector('[data-vg-role="refresh-indicator"]');
      if (indicatorEl) _indicator = new RefreshIndicator(indicatorEl);

      _scanRoot(document, function(el) { Vigil.mount(el); });
      initSidebar();
      initRefreshButtons();
      initRelativeTimes();
    },
  };

  // ── MutationObserver for auto-scan ─────────────────────────
  var _observer = null;
  function _startObserver() {
    if (_observer) return;
    _observer = new MutationObserver(function(mutations) {
      mutations.forEach(function(m) {
        m.addedNodes.forEach(function(node) {
          if (node.nodeType === 1) Vigil.scan(node);
        });
      });
    });
    _observer.observe(document.body, { childList: true, subtree: true });
  }

  document.addEventListener('DOMContentLoaded', function() { Vigil.init(); });
  global.Vigil = Vigil;

})(window);
