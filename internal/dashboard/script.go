package dashboard

// pageScript is the page's single script. It renders the read-only document the
// plugin's own resource route returns (/api/quota) and never calls the host
// management API: the page has no credential and cannot change anything.
const pageScript = `
(function(){
  'use strict';
  var PLUGIN_ID = "__PLUGIN_ID__";
  var AUTO_STORE_KEY = 'cpa-opencodego-quota:auto';
  var AUTO_INTERVAL_MS = 60000;

  // ------------------------------------------------------------- theme sync
  // The management centre owns the active theme on its own root element and
  // toggles it live, so mirror it and keep following changes.
  function applyTheme(theme){
    var root = document.documentElement;
    if(theme){ root.setAttribute('data-theme', theme); } else { root.removeAttribute('data-theme'); }
  }
  function syncTheme(){
    try{
      if(window.parent === window) return;
      applyTheme(window.parent.document.documentElement.getAttribute('data-theme'));
    }catch(error){ /* cross-origin: prefers-color-scheme takes over */ }
  }
  function watchParent(callback){
    try{
      if(window.parent === window) return;
      new MutationObserver(callback).observe(window.parent.document.documentElement,
        {attributes:true, attributeFilter:['data-theme','class','style']});
    }catch(error){ /* cross-origin: nothing to watch */ }
  }
  syncTheme();

  // ---------------------------------------------------------- host geometry
  // The page lives in an iframe under the management centre's fixed header, so
  // the visible area starts below it. Measure the header and inset the page.
  function hostHeaderHeight(){
    try{
      if(window.parent === window) return 0;
      var parentDocument = window.parent.document;
      var header = parentDocument.querySelector('.main-header');
      if(header){
        var height = header.getBoundingClientRect().height;
        if(height > 0) return Math.round(height);
      }
      var raw = window.parent.getComputedStyle(parentDocument.documentElement).getPropertyValue('--header-height');
      var parsed = parseFloat(raw);
      if(parsed > 0) return Math.round(parsed);
    }catch(error){ /* cross-origin */ }
    return 0;
  }
  var lastHeader = -1;
  function syncHostMetrics(){
    var height = hostHeaderHeight();
    if(height === lastHeader) return;
    lastHeader = height;
    document.documentElement.style.setProperty('--host-header', height + 'px');
  }
  syncHostMetrics();
  watchParent(syncTheme);
  window.addEventListener('resize', syncHostMetrics);
  window.addEventListener('focus', syncHostMetrics);
  if(window.parent !== window){
    try{ window.parent.addEventListener('resize', syncHostMetrics); }catch(error){}
    try{ window.parent.addEventListener('scroll', syncHostMetrics, {passive:true}); }catch(error){}
  }

  // ----------------------------------------------------------------- bases
  function resourceBase(){
    var marker = '/v0/resource/plugins/';
    var path = window.location.pathname;
    var index = path.indexOf(marker);
    var prefix = index >= 0 ? path.slice(0, index) : '';
    return prefix + marker + encodeURIComponent(PLUGIN_ID);
  }
  var BASE = resourceBase();

  // --------------------------------------------------------------- helpers
  function el(id){ return document.getElementById(id); }
  function setText(id, value){ var node = el(id); if(node) node.textContent = value == null ? '' : String(value); }
  function esc(value){
    return String(value == null ? '' : value).replace(/[&<>"']/g, function(character){
      return ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[character];
    });
  }
  function num(value){ var parsed = Number(value); return isFinite(parsed) ? parsed : 0; }
  function clamp(value, low, high){ return Math.min(high, Math.max(low, value)); }
  function fmtPct(value){ return String(Math.round(num(value) * 10) / 10); }
  function fmtTime(value){
    if(!value) return '';
    var date = new Date(value);
    return isNaN(date.getTime()) ? '' : date.toLocaleString();
  }
  function fmtClock(value){
    if(!value) return '';
    var date = new Date(value);
    return isNaN(date.getTime()) ? '' : date.toLocaleTimeString();
  }
  function fmtCountdown(value){
    if(!value) return '—';
    var date = new Date(value);
    if(isNaN(date.getTime())) return '—';
    var diff = date.getTime() - Date.now();
    if(diff <= 0) return '即将重置';
    var minutes = Math.round(diff / 60000);
    if(minutes < 60) return minutes + ' 分钟后重置';
    var hours = Math.round(minutes / 60);
    if(hours < 48) return hours + ' 小时后重置';
    return Math.round(hours / 24) + ' 天后重置';
  }
  function pctClass(value){
    var percent = num(value);
    if(percent >= 85) return 'danger';
    if(percent >= 60) return 'warn';
    return 'ok';
  }
  function proxyLabel(value){
    var raw = String(value || '');
    var match = raw.match(/^[a-z0-9+.-]+:\/\/(.+)$/i);
    return match ? match[1] : raw;
  }

  // ---------------------------------------------------------------- state
  var state = { data: null, loading: false, auto: true, timer: null, groupsByKey: {} };

  // ---------------------------------------------------------------- banner
  function showBanner(kind, message){
    var node = el('banner');
    if(!node) return;
    if(!message){ node.className = 'banner hidden'; node.textContent = ''; return; }
    node.className = 'banner ' + kind;
    node.textContent = message;
  }

  function setBusy(busy){
    var button = el('refresh');
    if(!button) return;
    button.disabled = busy;
    button.textContent = busy ? '刷新中…' : '刷新额度';
  }

  // ---------------------------------------------------------------- loading
  function load(force){
    if(state.loading) return;
    state.loading = true;
    setBusy(true);
    var url = BASE + '/api/quota' + (force ? '?refresh=1' : '');
    fetch(url, {headers:{'Accept':'application/json'}, cache:'no-store'})
      .then(function(response){
        if(!response.ok) throw new Error('HTTP ' + response.status);
        return response.json();
      })
      .then(function(payload){
        state.data = payload;
        render(payload);
      })
      .catch(function(error){
        showBanner('error', '读取额度失败：' + (error && error.message ? error.message : String(error)));
      })
      .then(function(){
        state.loading = false;
        setBusy(false);
      });
  }

  // ---------------------------------------------------------------- render
  function render(payload){
    var groups = payload.groups || [];
    var keys = payload.keys || [];
    var summary = payload.summary || {};
    state.groupsByKey = {};
    for(var index = 0; index < groups.length; index += 1){
      state.groupsByKey[groups[index].key] = groups[index];
    }

    var configPath = payload.config_path || '';
    setText('config-path', configPath || '未找到宿主 config.yaml');
    var configNode = el('config-path');
    if(configNode) configNode.title = configPath || '未找到宿主 config.yaml';

    var banner = '';
    if(payload.config_error){ banner = payload.config_error; }
    if(payload.warnings && payload.warnings.length){ banner = (banner ? banner + '\n' : '') + payload.warnings.join('\n'); }
    if(banner){ showBanner(payload.config_error ? 'error' : 'warn', banner); } else { showBanner('', ''); }

    // summary chips
    var chips = [];
    chips.push(chip('Key 数', summary.keys || keys.length, ''));
    chips.push(chip('路由分组', summary.groups || groups.length, ''));
    chips.push(chip('正常', summary.ok || 0, 'ok'));
    if((summary.failed || 0) > 0){ chips.push(chip('失败', summary.failed, 'danger')); }
    chips.push(chip('最高 5 小时用量', fmtPct(summary.max_rolling_percent) + '%', pctClass(summary.max_rolling_percent)));
    el('summary').innerHTML = chips.join('');

    var generated = payload.generated_at ? '生成于 ' + fmtTime(payload.generated_at) : '';
    var usage = payload.usage_url ? ' · 用量接口 ' + payload.usage_url : '';
    setText('meta', generated + usage);

    // key cards
    var keyHtml = keys.map(cardHtml).join('');
    el('keys').innerHTML = keyHtml || '<div class="empty">没有找到 OpenCode Go 的 Key。检查宿主 config.yaml 的 api-keys 分组，或在插件配置里设置 config-path。</div>';

    // groups
    var rows = groups.map(groupRow).join('');
    el('groups-body').innerHTML = rows || '<tr><td colspan="6" class="tag-muted">没有找到 base-url 指向 OpenCode Go 的 api-keys 分组。</td></tr>';
  }

  function chip(label, value, tone){
    return '<div class="chip"><span class="k">' + esc(label) + '</span><span class="v ' + esc(tone) + '">' + esc(value) + '</span></div>';
  }

  function cardHtml(snapshot){
    var groups = (snapshot.groups || []).map(function(key){
      var group = state.groupsByKey[key];
      return '<span title="' + esc(group ? group.base_url : key) + '">' + esc(group ? group.name : key) + '</span>';
    }).join('');
    var egress = snapshot.egress && snapshot.egress !== 'direct'
      ? '<span class="badge">代理 ' + esc(proxyLabel(snapshot.egress)) + '</span>'
      : '<span class="badge direct">直连</span>';

    var body = '';
    if(snapshot.error){
      body = '<div class="card-error">' + esc(snapshot.error) + '</div>';
    }else if(snapshot.windows && snapshot.windows.length){
      body = '<div class="windows">' + snapshot.windows.map(windowHtml).join('') + '</div>';
    }else{
      body = '<div class="card-error">用量接口未返回窗口数据</div>';
    }

    return '<article class="card">' +
      '<div class="card-head"><code title="' + esc(snapshot.masked) + '">' + esc(snapshot.masked) + '</code>' + egress + '</div>' +
      '<div class="groups-line">' + groups + '</div>' +
      body +
      '</article>';
  }

  function windowHtml(window){
    var percent = clamp(num(window.percent), 0, 100);
    var tone = pctClass(percent);
    var resetLabel = fmtCountdown(window.resets_at);
    var resetTitle = fmtTime(window.resets_at);
    return '<div class="window">' +
      '<div class="window-head"><span>' + esc(window.label || window.key) + '</span>' +
        '<span class="pct">已用 ' + fmtPct(percent) + '%</span></div>' +
      '<div class="track"><div class="fill ' + esc(tone) + '" style="width:' + fmtPct(percent) + '%"></div></div>' +
      '<div class="window-foot"><span class="remain">还剩 ' + fmtPct(100 - percent) + '%</span>' +
        '<span data-reset-at="' + esc(window.resets_at || '') + '" title="' + esc(resetTitle) + '">' + esc(resetLabel) + '</span></div>' +
      '</div>';
  }

  function groupRow(group){
    var models = (group.models || []).map(function(model){
      var label = model.alias || model.name || '';
      var title = model.name + (model.alias ? ' → ' + model.alias : '');
      return '<span title="' + esc(title) + '">' + esc(label) + '</span>';
    }).join('');
    var egress = group.egress === 'direct'
      ? '直连'
      : (group.egress === 'mixed'
        ? '按 Key'
        : (group.egress ? '代理 ' + proxyLabel(group.egress) : '继承全局'));
    var disabled = group.disabled ? ' <span class="tag-muted">已停用</span>' : '';
    return '<tr>' +
      '<td><strong>' + esc(group.name) + '</strong>' + disabled + '</td>' +
      '<td class="nowrap">' + esc(group.family) + '</td>' +
      '<td class="mono">' + esc(group.base_url) + '</td>' +
      '<td class="nowrap">' + esc(egress) + '</td>' +
      '<td class="nowrap">' + esc(group.priority || 0) + '</td>' +
      '<td><div class="models">' + models + '</div></td>' +
      '</tr>';
  }

  // ------------------------------------------------------------ auto refresh
  function readAuto(){
    try{ return window.localStorage.getItem(AUTO_STORE_KEY) !== '0'; }catch(error){ return true; }
  }
  function writeAuto(value){
    try{ window.localStorage.setItem(AUTO_STORE_KEY, value ? '1' : '0'); }catch(error){}
  }
  function schedule(){
    if(state.timer){ window.clearInterval(state.timer); state.timer = null; }
    if(!state.auto) return;
    state.timer = window.setInterval(function(){
      if(document.visibilityState === 'hidden') return;
      load(false);
    }, AUTO_INTERVAL_MS);
  }

  // ------------------------------------------------------------------- boot
  function boot(){
    state.auto = readAuto();
    var autoNode = el('auto');
    if(autoNode){
      autoNode.checked = state.auto;
      autoNode.addEventListener('change', function(){
        state.auto = !!autoNode.checked;
        writeAuto(state.auto);
        schedule();
      });
    }
    var refreshNode = el('refresh');
    if(refreshNode) refreshNode.addEventListener('click', function(){ load(true); });
    document.addEventListener('visibilitychange', function(){
      if(document.visibilityState === 'visible' && state.auto) load(false);
    });
    schedule();
    load(false);
    window.setInterval(function(){
      var nodes = document.querySelectorAll('[data-reset-at]');
      for(var index = 0; index < nodes.length; index += 1){
        nodes[index].textContent = fmtCountdown(nodes[index].getAttribute('data-reset-at'));
      }
    }, 30000);
  }

  if(document.readyState === 'loading'){ document.addEventListener('DOMContentLoaded', boot); } else { boot(); }
})();
`
