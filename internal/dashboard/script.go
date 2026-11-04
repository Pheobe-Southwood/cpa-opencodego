package dashboard

// pageScript is the page's single script. It keeps three things apart:
//   - a read model built from the plugin's own credential-free resource routes
//     (/api/models, /api/quota), which is what every pane renders;
//   - a patch of what the operator changed (edits), never a full copy of the
//     configuration, so a save can be merged into the host configuration
//     without the page ever having read the stored key secrets;
//   - the host management credential, which is only needed at write time and is
//     asked for in a dialog when the management centre did not store one.
const pageScript = `
(function(){
  'use strict';
  var PLUGIN_ID = "__PLUGIN_ID__";
  var ROLE = "__ROLE__";
  var FAMILIES = ['chat','responses','messages','gemini'];
  var FAMILY_LABEL = {chat:'chat/completions', responses:'responses', messages:'messages', gemini:'gemini'};
  var PROXY_MODES = ['inherit','proxy','direct'];
  var TABS = ['accounts','stats','requests','models','status','settings'];
  var REPO_URL = '__REPO_URL__';           // injected from internal/brand
  var CRED_STORE = 'cli-proxy-auth';        // management centre credential store
  var CRED_LEGACY = 'managementKey';        // older management centre builds
  var CRED_SESSION = 'cpa-opencodego:webui-key'; // typed password, this tab only
  var TOP_LABELS = {
    'base-url':'Base URL', 'session-mode':'Session 模式', 'session':'Session 值', 'proxy-url':'插件代理 URL',
    'default-proxy':'默认代理模式', 'request-timeout-seconds':'请求超时',
    'state-dir':'状态目录', 'debug':'调试日志', 'plan':'套餐',
    'endpoint-layout':'端点布局', 'developer-role':'developer 角色',
    'reasoning-replay':'reasoning 回放', 'request-log':'请求日志'
  };
  var MODEL_LABELS = {enabled:'启用', family:'协议', proxy:'代理', aliases:'别名'};

  // ---------------------------------------------------------------- theme
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

  // ------------------------------------------------------- host geometry
  // The plugin page lives in an iframe placed under the management centre's
  // fixed header, so the visible area starts below it. The header height is
  // read from the parent document (same origin) and applied as a top inset;
  // when the parent is not reachable the inset stays 0 and the page simply
  // starts at the top of the iframe.
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
  function routeBases(){
    var marker = '/v0/resource/plugins/';
    var path = window.location.pathname;
    var index = path.indexOf(marker);
    var prefix = index >= 0 ? path.slice(0, index) : '';
    return { resource: prefix + marker + encodeURIComponent(PLUGIN_ID), management: prefix };
  }
  var BASES = routeBases();
  var CONFIG_PATH = '/config/plugins/configs/' + encodeURIComponent(PLUGIN_ID);

  // -------------------------------------------------------------- helpers
  function el(id){ return document.getElementById(id); }
  function text(node, value){ if(node) node.textContent = value == null ? '' : String(value); }
  function esc(value){
    return String(value == null ? '' : value).replace(/[&<>"']/g, function(c){
      return ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c];
    });
  }
  function clone(value){ return JSON.parse(JSON.stringify(value)); }
  function sameText(a, b){ return String(a == null ? '' : a) === String(b == null ? '' : b); }
  function sameList(a, b){
    var left = (a || []).slice().sort().join('\\u0000');
    var right = (b || []).slice().sort().join('\\u0000');
    return left === right;
  }
  function fmtTime(value){
    if(!value) return '';
    var date = new Date(value);
    return isNaN(date.getTime()) ? '' : date.toLocaleString();
  }
  function relTime(value){
    if(!value) return '';
    var date = new Date(value);
    if(isNaN(date.getTime())) return '';
    var diff = date.getTime() - Date.now();
    if(diff <= 0) return '即将重置';
    var minutes = Math.round(diff / 60000);
    if(minutes < 60) return minutes + ' 分钟后重置';
    var hours = Math.round(minutes / 60);
    if(hours < 48) return hours + ' 小时后重置';
    return Math.round(hours / 24) + ' 天后重置';
  }
  function relPast(value){
    if(!value) return '';
    var date = new Date(value);
    if(isNaN(date.getTime())) return '';
    var seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
    if(seconds < 60) return seconds + ' 秒前';
    if(seconds < 3600) return Math.round(seconds / 60) + ' 分钟前';
    return Math.round(seconds / 3600) + ' 小时前';
  }
  function uptime(value){
    if(!value) return '';
    var date = new Date(value);
    if(isNaN(date.getTime())) return '';
    var seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
    var days = Math.floor(seconds / 86400);
    var hours = Math.floor((seconds % 86400) / 3600);
    var minutes = Math.floor((seconds % 3600) / 60);
    if(days) return days + ' 天 ' + hours + ' 小时';
    if(hours) return hours + ' 小时 ' + minutes + ' 分钟';
    if(seconds < 60) return '不到 1 分钟';
    return minutes + ' 分钟';
  }
  function shorten(value, keep){
    var trimmed = String(value == null ? '' : value);
    keep = keep || 26;
    if(trimmed.length <= keep * 2 + 3) return trimmed;
    return trimmed.slice(0, keep) + '…' + trimmed.slice(-keep);
  }
  function maskSecret(secret){
    var trimmed = String(secret || '');
    if(trimmed.length <= 12) return new Array(Math.max(trimmed.length, 1) + 1).join('*');
    return trimmed.slice(0, 8) + '…' + trimmed.slice(-4);
  }

  // ------------------------------------------------------ host credential
  // The management centre stores the credential it authenticated with in its
  // own browser storage when the operator ticked 记住密码. The page reads it
  // from there; when it is absent the operator is asked once, at write time.
  function deobfuscate(raw){
    var prefix = 'enc::v1::';
    if(!raw) return null;
    if(raw.indexOf(prefix) !== 0) return raw;
    try{
      var material = new TextEncoder().encode('cli-proxy-api-webui::secure-storage|' + window.location.host + '|' + navigator.userAgent);
      var binary = atob(raw.slice(prefix.length));
      var bytes = new Uint8Array(binary.length);
      for(var i=0;i<binary.length;i++){ bytes[i] = binary.charCodeAt(i) ^ material[i % material.length]; }
      return new TextDecoder().decode(bytes);
    }catch(error){ return null; }
  }
  function decodeJSON(raw){
    var decoded = deobfuscate(raw);
    if(!decoded) return null;
    try{ return JSON.parse(decoded); }catch(error){ return decoded; }
  }
  function hostCredential(){
    try{
      var stored = decodeJSON(localStorage.getItem(CRED_STORE));
      if(stored && typeof stored === 'object'){
        var state = stored.state && typeof stored.state === 'object' ? stored.state : stored;
        var key = state.managementKey;
        if(typeof key === 'string' && key.trim()) return key.trim();
      }
      var legacy = decodeJSON(localStorage.getItem(CRED_LEGACY));
      if(typeof legacy === 'string' && legacy.trim()) return legacy.trim();
    }catch(error){ /* storage unavailable */ }
    return null;
  }
  function sessionCredential(){
    try{
      var stored = sessionStorage.getItem(CRED_SESSION);
      if(stored && stored.trim()) return stored.trim();
    }catch(error){ /* storage unavailable */ }
    return null;
  }
  var hostCredentialRejected = false;
  function knownCredential(){
    var session = sessionCredential();
    if(session) return session;
    if(hostCredentialRejected) return null;
    return hostCredential();
  }

  // ------------------------------------------------- credential dialog
  var credentialAsk = null;
  var credentialError = '';
  var credentialHint = '';
  function askCredential(hint, errorMessage){
    if(credentialAsk) return credentialAsk.promise;
    var pending = {};
    pending.promise = new Promise(function(resolve, reject){ pending.resolve = resolve; pending.reject = reject; });
    credentialAsk = pending;
    credentialError = errorMessage || '';
    credentialHint = hint || '本页没有读到管理中心已保存的凭证（登录时未勾选「记住密码」）。输入 WebUI 密码即可保存；插件本身不保存任何密码。';
    text(el('credHint'), credentialHint);
    var error = el('credError');
    if(credentialError){ text(error, credentialError); error.hidden = false; } else { text(error, ''); error.hidden = true; }
    el('credInput').value = '';
    el('credModal').hidden = false;
    setTimeout(function(){ try{ el('credInput').focus(); }catch(ignore){} }, 30);
    return pending.promise;
  }
  function closeCredential(){
    el('credModal').hidden = true;
    el('credInput').value = '';
    var pending = credentialAsk;
    credentialAsk = null;
    return pending;
  }
  function submitCredential(){
    var value = (el('credInput').value || '').trim();
    var error = el('credError');
    if(!value){
      text(error, '请输入 WebUI 密码（管理密钥）');
      error.hidden = false;
      el('credInput').focus();
      return;
    }
    if(el('credRemember').checked){
      try{ sessionStorage.setItem(CRED_SESSION, value); }catch(ignore){}
    }
    var pending = closeCredential();
    credentialError = '';
    error.hidden = true;
    if(pending) pending.resolve(value);
  }
  function cancelCredential(){
    var pending = closeCredential();
    credentialError = '';
    if(pending) pending.reject(new Error('cancelled'));
  }
  function ensureCredential(hint){
    var known = knownCredential();
    if(known) return Promise.resolve(known);
    return askCredential(hint, '');
  }

  // -------------------------------------------------------- management API
  function authMessage(status, body){
    var detail = '';
    try{
      var parsed = JSON.parse(body);
      if(parsed && parsed.error) detail = String(parsed.error);
    }catch(error){}
    if(/banned/i.test(detail)) return '当前 IP 因多次输错密码被临时封禁，请稍后再试。';
    if(status === 403) return detail ? '管理接口拒绝了这次请求：' + detail : '管理接口拒绝了这次请求（HTTP 403）。';
    return 'WebUI 密码不正确（HTTP 401）。连续输错 5 次会临时封禁当前 IP，请确认后再试。';
  }
  function managementRequest(credential, path, options){
    options = options || {};
    var init = { method: options.method || 'GET', headers: { 'Authorization': 'Bearer ' + credential } };
    if(options.body !== undefined){
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(options.body);
    }
    var url = BASES.management + '/v8/management' + path;
    return fetch(url, init).then(function(response){
      // Older builds only expose the v0 prefix.
      if(response.status === 404 && options.noFallback !== true){
        return fetch(BASES.management + '/v0/management' + path, init);
      }
      return response;
    }).then(function(response){
      if(response.status === 401 || response.status === 403){
        return response.text().then(function(body){
          var error = new Error(authMessage(response.status, body));
          error.auth = true;
          error.status = response.status;
          throw error;
        });
      }
      if(response.status === 404 && options.allowNotFound) return {};
      return response.text().then(function(body){
        if(!response.ok){ throw new Error('HTTP ' + response.status + '：' + String(body || '').slice(0, 200)); }
        return body ? JSON.parse(body) : {};
      });
    });
  }

  // ---------------------------------------------------------------- state
  var state = null;        // read model from /api/models
  var upstream = null;     // upstream summary of the last model list fetch
  var quota = null;        // {keys:[…], at:<ms>} from /api/quota
  var stats = null;        // /api/stats read model
  var statsRange = 'month';
  var statsKey = '';
  var statsBusy = false;
  var statsErrorText = '';
  var statsCalendarRetry = false;

  // 请求日志 tab state. The filter lives in the page (not the URL) and the
  // timer only runs while the tab is visible.
  var requests = null;          // /api/requests read model
  var requestsBusy = false;
  var requestsErrorText = '';
  var requestsRequest = 0;
  var requestsTimer = null;
  var requestsFilter = { family:'', key:'', model:'', failed:false, limit:200, auto:true };
  var expanded = {};
  var probes = {};
  var tab = 'accounts';
  var saveBusy = false;
  var authAttempts = 0;
  var edits = emptyEdits();

  function emptyEdits(){ return { top:{}, models:{}, keys:{ add:{}, del:{}, set:{}, rename:{} } }; }
  function rows(){ return (state && state.models) || []; }
  function rowByID(id){
    var all = rows();
    for(var i=0;i<all.length;i++){ if(all[i].id === id) return all[i]; }
    return null;
  }
  function poolKeys(){ return (state && state.keys) || []; }
  function poolKey(name){
    var all = poolKeys();
    for(var i=0;i<all.length;i++){ if(all[i].name === name) return all[i]; }
    return null;
  }
  function quotaFor(name){
    var all = (quota && quota.keys) || [];
    for(var i=0;i<all.length;i++){ if(all[i].key_name === name) return all[i]; }
    return null;
  }
  // accounts is the plugin's projection of the keys onto host auth files. CPA's
  // AuthManager schedules those accounts; the dashboard shows which file
  // represents each key so an operator can find it on the credentials page.
  function accountsInfo(){ return (state && state.accounts) || null; }
  function accountFileOf(name){
    var info = accountsInfo();
    if(!info || !info.files) return '';
    return info.files[name] || '';
  }

  // ------------------------------------------------------- pending changes
  function countEdits(){
    var total = Object.keys(edits.top).length;
    Object.keys(edits.models).forEach(function(id){
      total += edits.models[id] === null ? 1 : Object.keys(edits.models[id]).length;
    });
    total += keyEditCount();
    return total;
  }
  function modelEditCount(){ return Object.keys(edits.models).length; }
  // A rename is one edit too: without it a page whose only pending change is a
  // new name would keep the save button disabled until something else changes.
  function keyEditCount(){
    var total = Object.keys(edits.keys.add).length + Object.keys(edits.keys.del).length;
    Object.keys(edits.keys.set).forEach(function(name){ total += Object.keys(edits.keys.set[name]).length; });
    total += Object.keys(edits.keys.rename).length;
    return total;
  }
  function topEditCount(){ return Object.keys(edits.top).length; }
  function isDirty(){ return countEdits() > 0; }
  function clearEdits(){ edits = emptyEdits(); }
  function markDirty(){
    renderChrome();
    renderStats();
    renderPending();
  }
  function show(value){ return String(value == null || value === '' ? '(空)' : value); }
  function pendingLines(){
    var lines = [];
    Object.keys(edits.top).forEach(function(field){
      var value = edits.top[field];
      if(field === 'debug') value = value ? '开启' : '关闭';
      if(field === 'plan') value = value === 'go-plus' ? 'Go Plus' : 'Go';
      lines.push({group:'设置', text:(TOP_LABELS[field] || field) + '：' + show(value)});
    });
    Object.keys(edits.models).forEach(function(id){
      var edit = edits.models[id];
      if(edit === null){ lines.push({group:'模型', text:id + '：恢复默认（删除配置项）'}); return; }
      Object.keys(edit).forEach(function(field){
        var value = edit[field];
        if(field === 'enabled'){
          lines.push({group:'模型', text:id + '：切换为' + (value ? '启用' : '关闭')});
          return;
        }
        if(field === 'aliases') value = (value || []).join(', ');
        lines.push({group:'模型', text:id + '：' + (MODEL_LABELS[field] || field) + ' → ' + show(value)});
      });
    });
    Object.keys(edits.keys.add).forEach(function(name){
      lines.push({group:'Key', text:'新增 ' + name + '（优先级 ' + edits.keys.add[name].priority + '）'});
    });
    Object.keys(edits.keys.del).forEach(function(name){ lines.push({group:'Key', text:'移除 ' + name}); });
    Object.keys(edits.keys.set).forEach(function(name){
      var set = edits.keys.set[name];
      var label = edits.keys.rename[name] || name;
      Object.keys(set).forEach(function(field){
        var value = set[field];
        if(field === 'enabled') value = value ? '启用' : '停用';
        lines.push({group:'Key', text:label + '：' + (field === 'priority' ? '优先级' : '状态') + ' → ' + show(value)});
      });
    });
    Object.keys(edits.keys.rename).forEach(function(name){
      lines.push({group:'Key', text:name + ' → ' + edits.keys.rename[name]});
    });
    return lines;
  }

  // ---------------------------------------------------------------- chrome
  function renderChrome(){
    var count = countEdits();
    var save = el('save');
    save.disabled = saveBusy || count === 0;
    if(count > 0 && !saveBusy){ save.classList.add('is-dirty'); } else { save.classList.remove('is-dirty'); }
    text(el('saveLabel'), saveBusy ? '保存中…' : '保存');
    var badge = el('saveCount');
    badge.hidden = count === 0;
    text(badge, String(count));
    var chip = el('saveState');
    if(saveBusy){ chip.className = 'pill warn'; text(chip, '保存中…'); }
    else if(count){ chip.className = 'pill warn'; text(chip, count + ' 项未保存'); }
    else { chip.className = 'pill ok'; text(chip, '已与配置同步'); }
    TABS.forEach(function(name){
      var dot = document.querySelector('#tab-' + name + ' .dot');
      if(!dot) return;
      var editsHere = name === 'accounts' ? keyEditCount() : (name === 'models' ? modelEditCount() : (name === 'settings' ? topEditCount() : 0));
      dot.hidden = editsHere === 0;
    });
  }

  var toastTimer = null;
  function notify(message, kind){
    var node = el('toast');
    text(node, message);
    node.className = 'toast' + (kind ? ' ' + kind : '');
    node.hidden = false;
    if(toastTimer) clearTimeout(toastTimer);
    toastTimer = setTimeout(function(){ node.hidden = true; }, kind === 'error' ? 8000 : 3600);
  }

  // ------------------------------------------------------------------ tabs
  function showTab(name, keepScroll){
    if(TABS.indexOf(name) < 0) name = 'accounts';
    tab = name;
    TABS.forEach(function(item){
      var button = el('tab-' + item);
      var pane = el('pane-' + item);
      var active = item === name;
      button.setAttribute('aria-selected', active ? 'true' : 'false');
      if(active){ pane.removeAttribute('hidden'); pane.classList.add('active'); }
      else { pane.setAttribute('hidden', ''); pane.classList.remove('active'); }
    });
    try{ history.replaceState(null, '', '#' + name); }catch(error){}
    if(name === 'settings') renderSettings();
    if(name === 'status') renderStatus();
    if(name === 'models') renderModels();
    if(name === 'accounts') renderAccounts();
    if(name === 'requests'){
      if(!requests && !requestsBusy) loadRequests();
      else renderRequests();
      scheduleRequestsTimer();
    } else {
      stopRequestsTimer();
    }
    if(name === 'stats'){
      initStatsFold();
      if(!stats && !statsBusy) loadUsageStats();
      else renderUsageStats();
      // Opening the tab is also a model-list check; the upstream cache keeps
      // this to one request per five minutes. loadModels refreshes the stats
      // read model when its model-list check is older than the fetch.
      loadModels(false);
    }
    if(!keepScroll) el('panes').scrollTop = 0;
  }
  function currentTabFromHash(){
    var name = String(window.location.hash || '').replace('#', '');
    return TABS.indexOf(name) >= 0 ? name : 'accounts';
  }

  // --------------------------------------------------------- accounts pane
  function keyViews(){
    var out = [];
    poolKeys().forEach(function(key){
      if(edits.keys.del[key.name]) return;
      var set = edits.keys.set[key.name] || {};
      out.push({
        name: key.name,
        label: edits.keys.rename[key.name] || key.name,
        masked: key.masked,
        state: key,
        priority: 'priority' in set ? set.priority : (key.priority != null ? key.priority : 1),
        enabled: 'enabled' in set ? set.enabled : key.enabled,
        isNew: false
      });
    });
    Object.keys(edits.keys.add).forEach(function(name){
      var added = edits.keys.add[name];
      out.push({
        name: name,
        label: name,
        masked: maskSecret(added.secret),
        state: null,
        priority: added.priority,
        enabled: added.enabled,
        isNew: true
      });
    });
    return out;
  }
  function keyFormHint(message, isError){
    var node = el('keyFormHint');
    text(node, message);
    node.className = isError ? 'hint err-text' : 'hint';
  }
  function keyHealth(key){
    if(!key) return '<span class="pill">未生效</span>';
    if(!key.enabled) return '<span class="pill">已停用</span>';
    // Scheduling and cooldown belong to CPA's AuthManager; the plugin only
    // configures the key and projects it into an auth file.
    return '<span class="pill ok">CPA 调度</span>';
  }
  function quotaRow(label, percent, resetsAt){
    var used = Number(percent);
    if(!isFinite(used)) used = 0;
    used = Math.max(0, Math.min(100, used));
    var left = 100 - used;
    var level = left < 10 ? 'err' : (left < 30 ? 'warn' : '');
    return '<div class="quota-row">' +
      '<div class="quota-top"><span class="label">' + esc(label) + '</span>' +
      '<span class="value' + (level === 'err' ? ' err-text' : (level === 'warn' ? ' warn-text' : '')) + '">剩余 ' + left.toFixed(0) + '%</span></div>' +
      '<div class="bar ' + level + '"><span style="width:' + left + '%"></span></div>' +
      '<div class="hint">已用 ' + used.toFixed(0) + '%' + (resetsAt ? ' · ' + esc(relTime(resetsAt)) : '') + '</div>' +
    '</div>';
  }
  function quotaBlock(view){
    if(view.isNew) return '<div class="hint">保存并生效后开始统计余量。</div>';
    var entry = quotaFor(view.name);
    if(!entry) return '<div class="hint">' + (quota === null ? '正在读取余量…' : (view.enabled ? '上游没有返回这个 Key 的窗口数据。' : '已停用的 Key 不参与轮询。')) + '</div>';
    if(entry.error) return '<div class="err-text">' + esc(entry.error) + '</div>';
    var windows = entry.windows || [];
    if(!windows.length) return '<div class="hint">上游没有返回窗口数据。</div>';
    return windows.map(function(window){ return quotaRow(window.label || window.key, window.percent, window.resets_at); }).join('');
  }
  function renderKeyCard(view){
    var key = view.state;
    var meta = [];
    var authFile = accountFileOf(view.name);
    if(authFile){
      var info = accountsInfo() || {};
      meta.push('<span class="hint">CPA 认证文件 ' + esc(authFile) + ' · 共 ' + (info.total || 0) + ' 个协议面文件</span>');
    } else if(accountsInfo() && accountsInfo().error){
      meta.push('<span class="hint">认证投影失败：' + esc(accountsInfo().error) + '</span>');
    }
    // Only persisted keys are renameable in place. A pending addition is
    // renamed in the add form, which keeps the add map and the rename map from
    // ever sharing a key.
    var nameField = view.isNew
      ? '<span class="key-name">' + esc(view.label) + '</span>'
      : '<input class="key-name-input" type="text" data-key-rename="' + esc(view.name) + '" value="' + esc(view.label) + '" ' +
        'aria-label="Key 名称" title="点击修改名称，回车或失焦生效；保存后同步重命名 CPA 认证文件" ' +
        'autocomplete="off" spellcheck="false" size="' + Math.max(6, Math.min(32, String(view.label).length + 1)) + '">';
    return '<div class="key-card' + (view.enabled ? '' : ' is-off') + (view.isNew ? ' is-new' : '') + '" data-key-card="' + esc(view.name) + '">' +
      '<div class="key-head">' +
        nameField +
        '<code class="key-mask">' + esc(view.masked || '(未配置)') + '</code>' +
        keyHealth(key) +
        (view.isNew ? '<span class="pill accent">待新增</span>' : '') +
        '<span class="spacer"></span>' +
        '<span class="key-actions">' +
          '<span class="key-priority"><span class="hint">优先级</span>' +
            '<input type="number" min="0" step="1" inputmode="numeric" data-key-priority="' + esc(view.name) + '" value="' + esc(view.priority) + '"></span>' +
          '<label class="switch"><input type="checkbox" data-key-enabled="' + esc(view.name) + '"' + (view.enabled ? ' checked' : '') + '><span class="hint">' + (view.enabled ? '启用' : '停用') + '</span></label>' +
          '<button class="danger small" type="button" data-key-remove="' + esc(view.name) + '">移除</button>' +
        '</span>' +
      '</div>' +
      '<div class="key-quota">' + quotaBlock(view) + '</div>' +
      '<div class="key-meta">' + meta.join('') + '</div>' +
    '</div>';
  }
  function renderKeyList(){
    var views = keyViews();
    if(!views.length){
      el('keyList').innerHTML = '<div class="empty-state">还没有配置 Key。展开下面的「添加 Key」填入 OpenCode Go 凭据（oc_sk_…），再点右上角「保存」。</div>';
      return;
    }
    el('keyList').innerHTML = views.map(renderKeyCard).join('');
  }
  function renderAccountStats(){
    var views = keyViews();
    var enabled = views.filter(function(view){ return view.enabled; }).length;
    var worst = null;
    views.forEach(function(view){
      var entry = quotaFor(view.name);
      if(!entry || !entry.windows) return;
      entry.windows.forEach(function(window){
        var used = Number(window.percent);
        if(!isFinite(used)) return;
        var left = Math.max(0, Math.min(100, 100 - used));
        if(!worst || left < worst.left) worst = {left:left, key:view.name, label:window.label || window.key};
      });
    });
    var accounts = accountsInfo();
    var stats = [
      ['Key', enabled + ' 启用 / ' + views.length],
      ['最低剩余', worst ? worst.left.toFixed(0) + '%' : '—', worst ? worst.key + ' · ' + worst.label : '尚无窗口数据'],
      ['调度方式', 'CPA AuthManager', '按认证文件 priority + 宿主 routing 选择'],
      ['CPA 认证文件', accounts ? accounts.total + ' 个' : '—', accounts ? (accounts.synced ? '已同步；每 Key 4 个协议面文件' : (accounts.error || '等待宿主 auth 目录')) : ''],
      ['协议面', (state && state.siblings ? state.siblings.length : 0) + ' 个', '四个协议族共用这份配置'],
      ['余量更新', quota && quota.at ? new Date(quota.at).toLocaleTimeString() : '—']
    ];
    el('accountStats').innerHTML = stats.map(function(stat){
      return '<div class="stat"><span class="k">' + esc(stat[0]) + '</span><span class="v">' + esc(stat[1]) + '</span>' +
        (stat[2] ? '<span class="hint">' + esc(stat[2]) + '</span>' : '') + '</div>';
    }).join('');
    text(el('quotaHint'), quota === null ? '正在读取余量…' : '余量来自上游 /usage 接口 · Key 的调度/冷却由 CPA AuthManager 负责');
  }
  function renderRotationGrid(){
    var settings = [
      ['路由策略', '跟随宿主网络配置', 'CPA routing.strategy（轮询/填满优先等）'],
      ['Key 调度', '按优先级降序', '数字越大越优先；同层与 429 冷却后的切换由 CPA 决定'],
      ['请求超时', (state && state.timeout_seconds ? state.timeout_seconds : 900) + ' 秒', '上游单个请求的最长等待时间'],
      ['插件代理', state ? (state.proxy_url || (state.host_proxy_url ? '继承 CPA：' + state.host_proxy_url : '未设置')) : '—', '可在「设置」里修改']
    ];
    el('rotationGrid').innerHTML = settings.map(function(item){
      return '<div class="kv"><span class="k">' + esc(item[0]) + '</span><span class="v">' + esc(shorten(item[1], 22)) + '</span><span class="hint">' + esc(item[2]) + '</span></div>';
    }).join('');
  }
  function renderAccounts(){
    renderAccountStats();
    renderKeyList();
    renderRotationGrid();
  }

  // ----------------------------------------------------------- model pane
  function modelBase(row){
    return {
      enabled: !!row.enabled,
      family: row.family,
      proxy: row.proxy,
      aliases: (row.aliases || []).slice()
    };
  }
  function modelEffective(row){
    var base = modelBase(row);
    var edit = edits.models[row.id];
    if(edit === undefined) return base;
    if(edit === null){
      // The configuration entry will be removed, so the catalogue default applies.
      base.enabled = !!row.catalogued;
      base.proxy = (state && state.models_default_proxy) || 'inherit';
      base.aliases = [];
      base.reset = true;
      return base;
    }
    if('enabled' in edit) base.enabled = edit.enabled;
    if('family' in edit) base.family = edit.family;
    if('proxy' in edit) base.proxy = edit.proxy;
    if('aliases' in edit) base.aliases = edit.aliases;
    base.edited = true;
    return base;
  }
  function setModelField(id, field, value){
    var row = rowByID(id);
    if(!row) return;
    var base = modelBase(row);
    if(edits.models[id] === null) delete edits.models[id];
    var entry = edits.models[id] && typeof edits.models[id] === 'object' ? edits.models[id] : {};
    var unchanged = field === 'aliases' ? sameList(value, base.aliases) : sameText(value, base[field]);
    if(unchanged){ delete entry[field]; } else { entry[field] = value; }
    if(Object.keys(entry).length){ edits.models[id] = entry; } else { delete edits.models[id]; }
    markDirty();
  }
  function resetModel(id){
    if(edits.models[id] === null){ delete edits.models[id]; } else { edits.models[id] = null; }
    markDirty();
    renderModels();
  }
  function matchesFilter(row, current){
    var filter = (el('modelFilter').value || '').trim().toLowerCase();
    var family = el('familyFilter').value;
    var stateFilter = el('stateFilter').value;
    if(family && current.family !== family) return false;
    if(filter){
      var haystack = (row.id + ' ' + (row.display_name || '') + ' ' + (row.aliases || []).join(' ') + ' ' +
        (row.client_ids || []).join(' ') + ' ' + (row.upstream || '')).toLowerCase();
      if(haystack.indexOf(filter) < 0) return false;
    }
    switch(stateFilter){
      case 'served': return current.enabled && current.family === ROLE;
      case 'disabled': return !current.enabled;
      case 'discovered': return !!row.discovered;
      case 'missing': return row.upstream_available === false;
      case 'alias': return !!row.aliased;
      case 'inferred': return row.family_source === 'inferred';
      case 'edited': return edits.models[row.id] !== undefined;
      default: return true;
    }
  }
  function filteredRows(){
    var all = rows();
    return all.filter(function(row){ return matchesFilter(row, modelEffective(row)); });
  }
  function setEnabledForFiltered(enabled){
    var selected = filteredRows();
    if(!selected.length){ notify('当前筛选没有可操作的模型', 'error'); return; }
    selected.forEach(function(row){
      setModelField(row.id, 'enabled', enabled);
      if(enabled && row.discovered && !modelEffective(row).family){
        setModelField(row.id, 'family', el('familyFilter').value || row.family || 'chat');
      }
    });
    renderModels();
  }
  function badgeList(row, current){
    var badges = [];
    // The family pill is green only when this provider build actually serves the
    // model; a model owned by another protocol family is labelled as such.
    var servedHere = current.enabled && current.family === ROLE;
    var familyLabel = FAMILY_LABEL[current.family] || current.family;
    if(current.family !== ROLE) familyLabel += ' · 其他实例';
    badges.push('<span class="pill' + (servedHere ? ' ok' : '') + '">' + esc(familyLabel) + '</span>');
    if(row.family_source === 'inferred') badges.push('<span class="pill warn" title="按模型 ID 前缀推测，可用「探测协议」确认">协议推测</span>');
    if(row.discovered) badges.push('<span class="pill accent">上游新增</span>');
    if(row.aliased) badges.push('<span class="pill accent" title="客户端只能看到这些 id；原名已不再提供">对外 ' + esc((row.client_ids || []).join(', ')) + '</span>');
    if(row.upstream_available === false) badges.push('<span class="pill err">上游已下线</span>');
    if(row.alias_shadowed) badges.push('<span class="pill err" title="与真实模型 id 冲突，已忽略：' + esc((row.shadowed_aliases || []).join(', ')) + '">别名被占用</span>');
    if(!row.aliased && current.aliases && current.aliases.length) badges.push('<span class="pill err" title="别名未生效，该模型仍用自己的原名">别名未生效</span>');
    if(current.proxy && current.proxy !== 'inherit') badges.push('<span class="pill accent">' + esc(current.proxy === 'proxy' ? '走代理' : '直连') + '</span>');
    if(current.reset) badges.push('<span class="pill warn">待恢复默认</span>');
    else if(edits.models[row.id] !== undefined) badges.push('<span class="pill warn">本次已修改</span>');
    if(row.note) badges.push('<span class="pill warn">注意</span>');
    return badges;
  }
  function renderProbe(row){
    var result = probes[row.id];
    if(!result) return '';
    if(result === 'loading') return '<div class="probe-result hint">正在逐个协议族探测…</div>';
    if(result.error) return '<div class="probe-result err-text">探测失败：' + esc(result.error) + '</div>';
    var items = (result.results || []).map(function(item){
      var pill = item.ok ? '<span class="pill ok">可用' + (item.status ? ' ' + item.status : '') + '</span>' : '<span class="pill">不可用</span>';
      var detail = item.error ? '<span class="hint">' + esc(String(item.error).slice(0, 90)) + '</span>' : '';
      var use = item.ok ? '<button class="small" type="button" data-probe-use="' + esc(row.id) + '" data-probe-family="' + esc(item.family) + '">使用 ' + esc(item.family) + '</button>' : '';
      return '<span class="probe-result">' + pill + '<code>' + esc(FAMILY_LABEL[item.family] || item.family) + '</code>' + detail + use + '</span>';
    });
    var summary = result.best ? '探测结果：' + esc(result.best) + ' 可用' : '探测结果：没有协议族接受这个模型 id';
    return '<div class="model-note hint">' + summary + '</div><div class="probe-result">' + items.join('') + '</div>';
  }
  function renderModelRow(row){
    var current = modelEffective(row);
    var isOpen = !!expanded[row.id];
    var aliasValue = (current.aliases || []).join(', ');
    var head = '<div class="model-top">' +
        '<span class="model-name">' + esc(row.display_name || row.id) + '</span>' +
        '<span class="model-badges">' + badgeList(row, current).join(' ') + '</span>' +
        '<label class="switch" title="是否由本插件提供该模型">' +
          '<input type="checkbox" data-model-enabled="' + esc(row.id) + '"' + (current.enabled ? ' checked' : '') + '>' +
          '<span class="hint">' + (current.enabled ? '启用' : '关闭') + '</span>' +
        '</label>' +
        '<button class="ghost small settings-toggle" type="button" data-model-settings="' + esc(row.id) + '" aria-expanded="' + (isOpen ? 'true' : 'false') + '">' + (isOpen ? '收起设置' : '设置') + '</button>' +
      '</div>' +
      '<div class="model-id">' + esc(row.id) +
        (row.aliased ? ' · 对外 id ' + esc((row.client_ids || []).join(', ')) : '') +
        (row.upstream && row.upstream !== row.id ? ' → ' + esc(row.upstream) : '') +
      '</div>';

    var panel = '';
    if(isOpen){
      panel = '<div class="settings-panel">' +
        '<label class="field"><span>别名（逗号分隔，填写后原名不再对外提供）</span>' +
          '<input type="text" data-model-alias="' + esc(row.id) + '" value="' + esc(aliasValue) + '" placeholder="例如 DeepSeek-V4.1-Flash" autocomplete="off" spellcheck="false">' +
        '</label>' +
        '<label class="field"><span>上游协议' + (row.family_source === 'inferred' ? '（推测）' : '') + '</span>' +
          '<select data-model-family="' + esc(row.id) + '">' +
          FAMILIES.map(function(f){
            return '<option value="' + f + '"' + (current.family === f ? ' selected' : '') + '>' + esc(FAMILY_LABEL[f]) + '</option>';
          }).join('') +
          '</select></label>' +
        '<label class="field"><span>代理</span>' +
          '<select data-model-proxy="' + esc(row.id) + '">' +
          PROXY_MODES.map(function(mode){
            return '<option value="' + mode + '"' + (current.proxy === mode ? ' selected' : '') + '>' + mode + '</option>';
          }).join('') +
          '</select></label>' +
        '<div class="field actions">' +
          '<button class="small" type="button" data-model-probe="' + esc(row.id) + '" title="探测需要管理中心密码，会弹出输入框">探测协议</button>' +
          '<button class="ghost small" type="button" data-model-reset="' + esc(row.id) + '">恢复默认</button>' +
        '</div>' +
      '</div>' + renderProbe(row);
    }

    return '<div class="model-card' + (current.enabled ? '' : ' off') + (edits.models[row.id] !== undefined ? ' edited' : '') + '" data-model-card="' + esc(row.id) + '">' +
      head + panel +
      (row.note ? '<div class="model-note hint warn-text">' + esc(row.note) + '</div>' : '') +
    '</div>';
  }
  function renderModels(){
    var all = rows();
    var selected = filteredRows();
    if(!selected.length){
      el('modelList').innerHTML = '<div class="empty-state">没有匹配的模型。换个筛选条件，或点「从上游刷新」重新拉取模型列表。</div>';
      text(el('modelHint'), '显示 0 / 共 ' + all.length + ' 个 id');
      renderModelStats();
      return;
    }
    el('modelList').innerHTML = selected.map(renderModelRow).join('');
    var served = all.filter(function(row){ var current = modelEffective(row); return current.enabled && current.family === ROLE; }).length;
    var hint = '显示 ' + selected.length + ' / 共 ' + all.length + ' 个 id · 本实例提供 ' + served + ' 个';
    if(upstream && upstream.count){ hint += ' · 上游 ' + upstream.count + ' 个'; }
    if(upstream && upstream.error){ hint += ' · 上游拉取失败：' + upstream.error; }
    text(el('modelHint'), hint);
    renderModelStats();
  }
  function renderModelStats(){
    var all = rows();
    var current = el('stateFilter').value;
    var served = 0, disabled = 0, discovered = 0, missing = 0, aliased = 0;
    all.forEach(function(row){
      var effective = modelEffective(row);
      if(effective.enabled && effective.family === ROLE) served++;
      if(!effective.enabled) disabled++;
      if(row.discovered) discovered++;
      if(row.upstream_available === false) missing++;
      if(row.aliased) aliased++;
    });
    var stats = [
      ['模型 id', all.length, ''],
      ['本实例提供', served, 'served'],
      ['已关闭', disabled, 'disabled'],
      ['上游新增', discovered, 'discovered'],
      ['已下线', missing, 'missing'],
      ['已修改', modelEditCount(), 'edited']
    ];
    el('modelStats').innerHTML = stats.map(function(stat){
      var filter = stat[2];
      var pressed = filter && current === filter ? ' aria-pressed="true"' : '';
      if(!filter) return '<div class="stat"><span class="k">' + esc(stat[0]) + '</span><span class="v">' + esc(stat[1]) + '</span></div>';
      return '<button class="stat" type="button" data-stat-filter="' + esc(filter) + '"' + pressed + '><span class="k">' + esc(stat[0]) +
        '</span><span class="v">' + esc(stat[1]) + '</span></button>';
    }).join('');
  }

  // ---------------------------------------------------------- status pane
  function renderStatus(){
    if(!state) return;
    var rows = [
      ['Provider', state.provider],
      ['插件 ID', state.plugin_id],
      ['版本', state.version],
      ['协议族', state.role],
      ['Base URL', state.base_url],
      ['端点布局', state.endpoint_layout + (state.endpoint_layout === 'auto' ? '（自动）' : '')],
      ['模型列表', state.models_url],
      ['用量接口', state.usage_url],
      ['developer 角色', state.developer_role],
      ['reasoning 回放', state.reasoning_replay],
      ['请求日志', state.request_log ? '开启' : '关闭'],
      ['Session', state.session_mode + (state.session_mode === 'fixed' ? ' / ' + state.session : '')],
      ['默认代理', state.default_proxy],
      ['插件代理', state.proxy_url || '(未设置)'],
      ['CPA 全局代理', state.host_proxy_url || '(未暴露)'],
      ['状态文件', state.state_path],
      ['启动时间', fmtTime(state.started_at) + (uptime(state.started_at) ? '（已运行 ' + uptime(state.started_at) + '）' : '')]
    ];
    if(state.migration) rows.push(['升级迁移', state.migration]);
    var extra = Object.keys(state.extra_headers || {});
    rows.push(['额外请求头', extra.length ? extra.join(', ') : '(无)']);
    el('statusGrid').innerHTML = rows.map(function(row){
      return '<div class="kv"><span class="k">' + esc(row[0]) + '</span><span class="v">' + esc(shorten(row[1] || '', 26)) + '</span></div>';
    }).join('');
    var served = (state.models || []).filter(function(row){ return row.served; }).length;
    var total = (state.models || []).length;
    text(el('statusSummary'), '提供 ' + served + ' / ' + total + ' 个模型 id');
    var errorBox = el('statusError');
    if(state.config_error){ text(errorBox, state.config_error); errorBox.hidden = false; }
    else { errorBox.hidden = true; }
    var upstreamRows = [
      ['上游模型数', upstream && upstream.count ? String(upstream.count) : '—'],
      ['本次拉取', upstream && upstream.fetched_at ? fmtTime(upstream.fetched_at) + '（' + relPast(upstream.fetched_at) + '）' : '—'],
      ['拉取结果', upstream && upstream.error ? '失败：' + upstream.error : '正常']
    ];
    el('upstreamGrid').innerHTML = upstreamRows.map(function(row){
      return '<div class="kv"><span class="k">' + esc(row[0]) + '</span><span class="v">' + esc(shorten(row[1], 26)) + '</span></div>';
    }).join('');
    el('siblingGrid').innerHTML = (state.siblings || []).map(renderFace).join('');
    renderAbout();
  }
  // renderFace draws one protocol face. The host lists one plugin row per
  // shared library; this is where the four rows become one managed product:
  // what the face speaks, which plugin id carries it, which models depend on
  // it, and whether it is the face that owns the configuration.
  function renderFace(face){
    var formats = (face.formats || []).join(' + ');
    var models = face.models || [];
    var count = Number(face.model_count) || 0;
    var chips = models.map(function(id){ return '<span class="chip">' + esc(id) + '</span>'; }).join('');
    if(count > models.length){
      chips += '<span class="chip chip-more">+' + (count - models.length) + '</span>';
    }
    return '<div class="face' + (face.main ? ' face-main' : '') + '">' +
      '<div class="face-head">' +
        '<span class="face-name">' + esc(face.label || face.role) + '</span>' +
        '<span class="face-tag">' + (face.main ? '配置入口' : '零配置') + '</span>' +
      '</div>' +
      '<div class="hint">' + esc(face.protocol || face.role) + '</div>' +
      '<div class="kv"><span class="k">协议</span><span class="v"><code>' + esc(formats) + '</code></span></div>' +
      '<div class="kv"><span class="k">上游端点</span><span class="v"><code>' + esc(face.endpoint || '') + '</code></span></div>' +
      (face.resolved_endpoint ? '<div class="kv"><span class="k">实际请求</span><span class="v"><code>' + esc(face.resolved_endpoint) + '</code></span></div>' : '') +
      '<div class="kv"><span class="k">插件 ID</span><span class="v"><code>' + esc(face.plugin_id) + '</code></span></div>' +
      '<div class="kv"><span class="k">provider</span><span class="v"><code>' + esc(face.provider) + '</code></span></div>' +
      '<div class="kv"><span class="k">承载模型</span><span class="v">' + count + ' 个</span></div>' +
      (chips ? '<div class="face-models">' + chips + '</div>' : '') +
      '<div class="hint face-note">' + (face.main
        ? '本面承载侧边栏页面与全部配置：Key、模型、设置都在这里改。'
        : '无需配置：由 <code>cpa-opencodego</code> 统一维护，插件管理里只控制启用。') +
      '</div>' +
    '</div>';
  }
  function aboutRow(label, value){
    return '<div class="about-row"><span class="k">' + esc(label) + '</span><span>' + value + '</span></div>';
  }
  function renderAbout(){
    var version = state ? state.version : '';
    el('aboutBox').innerHTML = [
      aboutRow('版本', '插件 <code>' + esc(version) + '</code> · 主实例 <code>' + esc(PLUGIN_ID) + '</code>'),
      aboutRow('协议面', '一个主插件 + 三个协议面插件（responses / messages / gemini），每个 .so 只声明一种上游协议，协议翻译由 CPA 完成。四个 .so 必须同版本安装，只有主插件需要配置，四个面在「状态」里逐面说明。'),
      aboutRow('别名', '在「模型」里为模型填写的别名会替代原名对外提供；清空别名即恢复原名，多个别名等于多个对外 id。'),
      aboutRow('Key 调度', '插件把每个 Key 投影成 4 个 CPA 认证文件（chat / responses / messages / gemini 各一个，带 <code>managed_by: cpa-opencodego</code>，权限 0600），由 CPA AuthManager 按 <code>priority</code> 与宿主 <code>routing.strategy</code> 选择、轮询、冷却并在 429 时自动切换；插件自身不实现 Key 轮询。投影是单向的：在 CPA 认证文件页改启停/优先级会被下一次同步覆盖，请在「账号管理」页修改。'),
      aboutRow('余量', '「账号管理」的余量来自上游 <code>/usage</code> 接口，按 Key 统计 5 小时 / 本周 / 本月三个窗口，只查询上游、不消耗额度。Key 的调度状态与冷却看 CPA 认证文件页；额度与 Key 配置看本页。'),
      aboutRow('统计', '「统计」按模型汇总 token、刊例价、月额度百分比、整体 TPS、生成 TPS 与 TTFT。token、失败与请求总延迟来自宿主 usage 事件；整体 TPS 含首字，生成 TPS 只算插件测到的首内容到末内容，TTFT 由插件读流自测。v1.7 起可回放旧 Token Usage Tracker 中 opencode-go 的历史。'),
      aboutRow('保存', '保存写回 CPA 的 config.yaml，由 CPA 热加载给插件，不需要重启；未勾选「记住密码」时会先弹出一次密码输入框。'),
      aboutRow('请求日志', '「请求日志」逐条列出每次上游尝试：时间、输入/输出/合计 token、TTFT、生成 TPS 与端到端 TPS，以及状态与错误。数据由插件在转发时自测，和「统计」的宿主 usage 口径略有差异；可在设置里关闭，默认保留 2000 条 / 7 天。'),
      aboutRow('协议', '插件同时支持旧版单根 /v1 端点和新版 /openai、/anthropic、/google 分族端点：base-url 以 /v1 结尾视为旧布局，以 Go 根结尾视为新布局，也可用 endpoint-layout 显式指定。只额外发送 x-opencode-session，User-Agent 跟随客户端透传。'),
      aboutRow('思考回放', 'DeepSeek 思考模式要求历史 assistant 消息回传 reasoning_content；客户端丢弃旧思维链时会偶发上游 400。reasoning-replay=auto（默认）只在思考请求上给缺失字段补占位文本，已有内容原样保留；同一 400 还会自动重试一次，因为该检查在部分网关节点上是间歇性的。'),
      aboutRow('热重载', '替换插件文件同样热生效：把新版本 .so 放进 plugins/linux/amd64 后原地改一次 config.yaml 即可（不要用会替换 inode 的方式写文件）。'),
      aboutRow('残留', '插件只在 CPA 目录下写 plugins/、data/cpa-opencodego/（shared.json、stats-&lt;role&gt;.json、requests-&lt;role&gt;.jsonl，可选 debug.log）和 auth 目录里的 <code>opencode-go-*.json</code> 认证文件；认证文件在插件被卸载（删配置块 / 禁用 / 停服）时自动删除，<code>scripts/uninstall.sh</code> 也会按 <code>managed_by</code> 标记兜底清理。'),
      aboutRow('仓库', '<a href="' + REPO_URL + '" target="_blank" rel="noreferrer noopener">' + esc(REPO_URL.replace('https://', '')) + '</a>')
    ].join('');
  }

  // -------------------------------------------------------- settings pane
  function topBase(field){
    if(!state) return undefined;
    switch(field){
      case 'base-url': return state.base_url || '';
      case 'session-mode': return state.session_mode || 'auto';
      case 'session': return state.session || '';
      case 'proxy-url': return state.proxy_url || '';
      case 'default-proxy': return state.default_proxy || 'inherit';
      case 'request-timeout-seconds': return state.timeout_seconds || 900;
      case 'state-dir': return state.state_dir || '';
      case 'plan': return state.plan || 'go';
      case 'endpoint-layout': return state.endpoint_layout || 'auto';
      case 'developer-role': return state.developer_role || 'retry-only';
      case 'reasoning-replay': return state.reasoning_replay || 'retry-only';
      case 'request-log': return !!state.request_log;
      case 'debug': return !!state.debug;
      default: return undefined;
    }
  }
  function renderSettings(){
    if(!state) return;
    function value(field){
      if(field in edits.top) return edits.top[field];
      var base = topBase(field);
      return base === undefined ? '' : base;
    }
    el('cfgBaseURL').value = value('base-url');
    el('cfgSessionMode').value = value('session-mode');
    el('cfgSession').value = value('session');
    el('cfgProxyURL').value = value('proxy-url');
    el('cfgDefaultProxy').value = value('default-proxy');
    el('cfgTimeout').value = value('request-timeout-seconds');
    el('cfgStateDir').value = value('state-dir');
    el('cfgPlan').value = value('plan');
    el('cfgEndpointLayout').value = value('endpoint-layout');
    el('cfgDeveloperRole').value = value('developer-role');
    el('cfgReasoningReplay').value = value('reasoning-replay');
    el('cfgRequestLog').checked = !!value('request-log');
    el('cfgDebug').checked = !!value('debug');
    text(el('stateDirHint'), '实际状态文件：' + (state.state_path || '(未解析)'));
    text(el('endpointHint'), '当前布局 ' + (state.endpoint_layout || 'auto') + ' · base-url ' + (state.base_url || '') +
      ' · 模型列表 ' + (state.models_url || '') + ' · 用量 ' + (state.usage_url || ''));
  }
  function renderPending(){
    var lines = pendingLines();
    if(!lines.length){
      el('pendingBox').innerHTML = '<div class="hint">当前没有未保存的修改。</div>';
      return;
    }
    el('pendingBox').innerHTML = '<ul class="pending">' + lines.map(function(line){
      return '<li><span class="group">' + esc(line.group) + '</span><span>' + esc(line.text) + '</span></li>';
    }).join('') + '</ul>';
  }

  // ------------------------------------------------------------------ save
  function buildPayload(base){
    var payload = base && typeof base === 'object' ? clone(base) : {};
    Object.keys(edits.top).forEach(function(field){ payload[field] = edits.top[field]; });
    payload.enabled = true;
    var modelIDs = Object.keys(edits.models);
    if(modelIDs.length){
      if(!payload.models || typeof payload.models !== 'object') payload.models = {};
      modelIDs.forEach(function(id){
        var edit = edits.models[id];
        if(edit === null){ delete payload.models[id]; return; }
        var entry = payload.models[id];
        if(!entry || typeof entry !== 'object') entry = payload.models[id] = {};
        Object.keys(edit).forEach(function(field){
          var value = edit[field];
          if(value === undefined || value === null || (field === 'aliases' && (!value || !value.length))){ delete entry[field]; return; }
          entry[field] = value;
        });
        if(!Object.keys(entry).length) delete payload.models[id];
      });
    }
    var keyTouched = keyEditCount() > 0;
    if(keyTouched || Array.isArray(payload.keys)){
      var keys = Array.isArray(payload.keys) ? payload.keys.slice() : [];
      var index = {};
      keys.forEach(function(item, position){ if(item && item.name) index[item.name] = position; });
      keys = keys.filter(function(item){ return !(item && edits.keys.del[item.name]); });
      index = {};
      keys.forEach(function(item, position){ if(item && item.name) index[item.name] = position; });
      Object.keys(edits.keys.set).forEach(function(name){
        if(!(name in index)) return;
        var set = edits.keys.set[name];
        var target = keys[index[name]];
        if('enabled' in set) target.enabled = set.enabled;
        if('priority' in set) target.priority = set.priority;
      });
      // Rename after removals and per-key edits (both are keyed by the name the
      // page read) and before additions. Rebuilding the index here is what lets
      // "rename A to B, then add a new A" produce two distinct keys in one save.
      Object.keys(edits.keys.rename).forEach(function(name){
        if(!(name in index)) return;
        keys[index[name]].name = edits.keys.rename[name];
      });
      index = {};
      keys.forEach(function(item, position){ if(item && item.name) index[item.name] = position; });
      Object.keys(edits.keys.add).forEach(function(name){
        var added = edits.keys.add[name];
        if(name in index){
          var existing = keys[index[name]];
          existing.key = added.secret;
          existing.priority = added.priority;
          existing.enabled = added.enabled;
          return;
        }
        keys.push({ name:name, key:added.secret, priority:added.priority, enabled:added.enabled });
      });
      payload.keys = keys;
    }
    return payload;
  }
  function saveWith(credential){
    return managementRequest(credential, CONFIG_PATH, { allowNotFound:true }).then(function(base){
      var payload = buildPayload(base);
      return managementRequest(credential, CONFIG_PATH, { method:'PUT', body: payload });
    }).then(function(){
      var saved = countEdits();
      authAttempts = 0;
      hostCredentialRejected = false;
      clearEdits();
      renderChrome();
      renderPending();
      notify('已保存 ' + saved + ' 项修改，正在重新读取…', 'ok');
      // The host reconfigures the plugin before answering, but its live state is
      // only published once the new configuration is applied, so read again.
      setTimeout(function(){ loadModels(false); loadQuota(); if(stats) loadUsageStats(true); }, 400);
    }).catch(function(error){
      if(error && error.auth){
        // The credential the page used is not accepted: drop what the page
        // cached and ask for it again instead of leaving a dead write path.
        if(error.status === 401 || error.status === 403){
          hostCredentialRejected = true;
          try{ sessionStorage.removeItem(CRED_SESSION); }catch(ignore){}
        }
      }
      throw error;
    });
  }
  function save(){
    var count = countEdits();
    if(!count || saveBusy) return;
    saveBusy = true;
    renderChrome();
    ensureCredential('保存需要管理中心密码。本页没有读到管理中心已保存的凭证（登录时未勾选「记住密码」），输入一次即可保存；插件不保存任何密码。')
      .then(function(credential){ return saveWith(credential); })
      .catch(function(error){
        if(error && error.message === 'cancelled') return;
        if(error && error.auth && authAttempts < 1){
          authAttempts++;
          return askCredential('保存需要管理中心密码，请重新输入。', error.message)
            .then(function(credential){ return saveWith(credential); });
        }
        notify('保存失败：' + error.message, 'error');
      })
      .then(function(){ saveBusy = false; renderChrome(); });
  }

  // ----------------------------------------------------------- data loading
  function loadModels(force){
    var url = BASES.resource + '/api/models' + (force ? '?refresh=1' : '');
    if(force) text(el('modelHint'), '正在从上游拉取…');
    return fetch(url).then(function(response){ return response.json(); }).then(function(payload){
      state = payload;
      upstream = payload.upstream || null;
      renderAll();
      // /api/models records the model-list diff used by the price reminder.
      // Refresh the stats read model when its check is older than this fetch,
      // so a new/removed model can trigger the banner immediately.
      if(tab === 'stats' && upstream && upstream.fetched_at &&
         (!stats || stats.model_list_checked_at !== upstream.fetched_at)){
        loadUsageStats(true);
      }
    }).catch(function(error){
      notify('读取运行状态失败：' + error.message, 'error');
    });
  }
  function loadQuota(){
    text(el('quotaHint'), '正在读取余量…');
    return fetch(BASES.resource + '/api/quota').then(function(response){ return response.json(); }).then(function(payload){
      quota = { keys: payload.keys || [], at: Date.now() };
      renderAccounts();
      if(tab === 'stats') renderUsageStats();
      // The first statistics load may have fallen back to the calendar month
      // because quota had not arrived yet. Once the reset time is known, ask
      // for the real monthly window once.
      if(stats && statsRange === 'month' && stats.range && stats.range.since_source === 'calendar' &&
         !statsCalendarRetry && statsSinceISO()){
        statsCalendarRetry = true;
        loadUsageStats(true);
      }
    }).catch(function(error){
      quota = { keys: [], at: 0, error: error.message };
      renderAccounts();
      notify('读取额度失败：' + error.message, 'error');
    });
  }
  function renderAll(){
    renderChrome();
    if(tab === 'accounts') renderAccounts();
    if(tab === 'models') renderModels();
    if(tab === 'status') renderStatus();
    if(tab === 'settings') renderSettings();
    if(tab === 'stats') renderUsageStats();
    if(tab === 'requests') renderRequests();
    renderStats();
    renderPending();
  }
  function renderStats(){
    renderAccountStats();
    renderModelStats();
  }

  // ------------------------------------------------------------- stats pane
  // The 统计 tab is deliberately layered for a phone: the summary strip is
  // always visible, the per-model table lives in a <details> that starts folded
  // below 640px, and the methodology is always folded. This keeps the first
  // screenful to a few numbers instead of a wall of token counts.
  var statsFoldReady = false;
  var statsRequest = 0;
  function initStatsFold(){
    if(statsFoldReady) return;
    statsFoldReady = true;
    var fold = el('statsModelsFold');
    if(fold && window.matchMedia && window.matchMedia('(max-width: 640px)').matches){
      fold.open = false;
    }
  }
  function statsPlanLabel(){
    if(stats && stats.plan_label) return stats.plan_label;
    if(state && state.plan === 'go-plus') return 'Go Plus';
    return 'Go';
  }
  function monthlyQuota(){
    var best = null;
    var latestReset = null;
    var keyName = '';
    ((quota && quota.keys) || []).forEach(function(entry){
      (entry.windows || []).forEach(function(window){
        if(window.key !== 'monthly') return;
        var percent = Number(window.percent);
        if(!isFinite(percent)) percent = 0;
        if(!best || percent > best.percent){ best = {percent:percent}; keyName = entry.key_name || ''; }
        var reset = window.resets_at ? new Date(window.resets_at) : null;
        if(reset && !isNaN(reset.getTime()) && (!latestReset || reset > latestReset)) latestReset = reset;
      });
    });
    if(!best) return null;
    return {percent:best.percent, reset:latestReset, key:keyName};
  }
  function monthBefore(date){
    // Subtracting one month from the 31st with setUTCMonth alone overflows into
    // the following month (e.g. Mar 31 -> Mar 3); clamp to the target month's
    // last day instead so the window really starts one month earlier.
    var result = new Date(date.getTime());
    var day = result.getUTCDate();
    result.setUTCDate(1);
    result.setUTCMonth(result.getUTCMonth() - 1);
    var lastDay = new Date(Date.UTC(result.getUTCFullYear(), result.getUTCMonth() + 1, 0)).getUTCDate();
    result.setUTCDate(Math.min(day, lastDay));
    return result;
  }
  function statsSinceISO(){
    var info = monthlyQuota();
    if(!info || !info.reset) return null;
    return monthBefore(info.reset).toISOString();
  }
  function loadUsageStats(force){
    if(statsBusy && !force) return Promise.resolve();
    var request = ++statsRequest;
    statsBusy = true;
    statsErrorText = '';
    renderUsageStats();
    var params = '?range=' + encodeURIComponent(statsRange);
    if(statsKey) params += '&key=' + encodeURIComponent(statsKey);
    var since = statsRange === 'month' ? statsSinceISO() : null;
    if(since) params += '&since=' + encodeURIComponent(since);
    return fetch(BASES.resource + '/api/stats' + params)
      .then(function(response){
        if(!response.ok) throw new Error('HTTP ' + response.status);
        return response.json();
      })
      .then(function(payload){
        if(request !== statsRequest) return;
        stats = payload || null;
        statsErrorText = '';
        // If the quota request had not finished when this read started, the
        // server used the calendar month. One retry with the now-known reset
        // gives the real monthly window instead of leaving the page stale.
        if(stats && statsRange === 'month' && stats.range && stats.range.since_source === 'calendar' &&
           !statsCalendarRetry && statsSinceISO()){
          statsCalendarRetry = true;
          loadUsageStats(true);
        }
      })
      .catch(function(error){
        if(request !== statsRequest) return;
        statsErrorText = '读取统计失败：' + error.message;
      })
      .then(function(){
        if(request !== statsRequest) return;
        statsBusy = false;
        renderUsageStats();
      });
  }
  function renderStatsKeyFilter(){
    var select = el('statsKey');
    if(!select) return;
    var current = statsKey;
    var keys = (stats && stats.keys) || [];
    var html = '<option value="">全部 Key</option>';
    keys.forEach(function(k){
      var label = k.name + ' (优先级 ' + (k.priority || 0) + ')';
      html += '<option value="' + esc(k.name) + '"' + (k.name === current ? ' selected' : '') + '>' + esc(label) + '</option>';
    });
    select.innerHTML = html;
    select.value = current;
  }
  function renderUsageStats(){
    if(!el('statsSummary')) return;
    renderStatsKeyFilter();
    renderPriceFreshness();
    renderUsageSummary();
    renderUsageTable();
    renderUsageMeta();
  }
  function priceAgeText(days){
    if(days <= 0) return '今天';
    if(days === 1) return '昨天';
    return days + ' 天前';
  }
  function formatModelIDs(ids){
    ids = (ids || []).filter(function(id){ return !!id; });
    if(!ids.length) return '';
    var head = ids.slice(0, 4).join('、');
    return ids.length > 4 ? head + ' 等 ' + ids.length + ' 个' : head;
  }
  function renderPriceFreshness(){
    var badge = el('statsPriceBadge');
    var hint = el('statsPriceFreshness');
    var line = el('statsPriceFreshLine');
    var notice = el('statsPriceNotice');
    if(!stats){
      if(badge){ badge.className = 'pill'; text(badge, '价目表 —'); }
      text(hint, statsBusy ? '正在读取…' : '统计读取后才能判断价目表是否需要更新');
      if(notice) notice.hidden = true;
      if(line) line.hidden = false;
      return;
    }
    var revision = stats.price_book_revision || '未知';
    var url = stats.price_book_url || 'https://opencode.ai/docs/go/';
    var checked = stats.price_book_checked_at || '';
    var dueAt = stats.price_book_due_at || '';
    var refresh = Number(stats.price_book_refresh_after_days) || 30;
    var age = Number(stats.price_book_age_days);
    if(!isFinite(age) || age < 0) age = 0;
    var due = !!stats.price_book_update_due;
    var modelChecked = !!stats.model_list_checked_at;
    var modelChanged = !!stats.model_list_changed;
    var needsUpdate = due || modelChanged;
    if(badge){ badge.className = 'pill' + (needsUpdate ? ' warn' : ' ok'); text(badge, '价目表 ' + revision); }
    var checkedText = checked ? ('核对于 ' + checked + '（' + priceAgeText(age) + '）') : '核对日期未知';
    var dueText = dueAt ? (due ? ' · 提醒日期 ' + dueAt + '（已到期）' : ' · 下次提醒 ' + dueAt) : '';
    var modelText = modelChecked ? (modelChanged ? ' · 模型列表有变更' : ' · 模型列表无变化') : '';
    text(hint, checkedText + dueText + modelText + ' · ' + refresh + ' 天周期');
    [el('statsPriceLink'), el('statsPriceNoticeLink')].forEach(function(link){ if(link) link.href = url; });
    if(!needsUpdate){
      if(notice) notice.hidden = true;
      if(line) line.hidden = false;
      return;
    }
    // The banner already carries the checked date and the official link. Hide
    // the one-line status so a phone does not show the same reminder twice.
    if(line) line.hidden = true;
    var parts = [];
    if(due){
      var duePrefix = checked ? ('内嵌价目表（' + revision + '）核对于 ' + checked + '，') : ('内嵌价目表（' + revision + '）');
      parts.push(duePrefix + '已到 ' + refresh + ' 天核对周期。');
    }
    if(modelChanged){
      var changes = [];
      if((stats.model_list_added || []).length) changes.push('新增 ' + formatModelIDs(stats.model_list_added));
      if((stats.model_list_removed || []).length) changes.push('移除 ' + formatModelIDs(stats.model_list_removed));
      if(changes.length){
        var snapshotLabel = checked ? (' ' + checked + ' 快照') : '内嵌快照';
        parts.push('上游模型列表与' + snapshotLabel + '不一致：' + changes.join('；') + '。');
      } else {
        parts.push('上游模型列表与内嵌快照不一致。');
      }
    }
    parts.push('OpenCode Go 可能已经调整价格、月额度或模型列表；当前统计仍按旧表折算。');
    var badgeText;
    if(due && modelChanged) badgeText = '价目到期 · 模型变更';
    else if(due) badgeText = age + ' 天未核对';
    else badgeText = '模型列表有变更';
    text(el('statsPriceNoticeBadge'), badgeText);
    text(el('statsPriceNoticeText'), parts.join(''));
    if(notice) notice.hidden = false;
  }
  function renderUsageSummary(){
    var totals = stats ? stats.totals : null;
    var info = monthlyQuota();
    var quotaValue = '—';
    var quotaHint = '正在读取余量…';
    if(quota && quota.error){ quotaHint = '余量读取失败'; }
    else if(quota){
      if(info){ quotaValue = formatPercent(info.percent); quotaHint = info.reset ? '重置 ' + fmtTime(info.reset) : '上游 /usage'; }
      else { quotaHint = '上游没有返回月度窗口'; }
    }
    var tiles = [
      ['本月额度', quotaValue, quotaHint],
      ['刊例价', totals ? formatUSD(totals.cost_usd) : '—', stats ? (stats.range.kind === 'month' ? '本月' : '累计') + ' · 按价目表折算' : '正在读取…'],
      ['Token', totals ? formatTokens(totals.total_tokens) : '—', totals ? '输入 ' + formatTokens(totals.input_tokens) + ' · 输出 ' + formatTokens(totals.output_tokens) : ''],
      ['平均 TPS', totals && totals.tps_samples ? formatTPS(totals.avg_tps) : '—', totals && totals.tps_samples ? totals.tps_samples + ' 个成功请求 · 整体' : '成功请求 · 含首字'],
      ['生成 TPS', totals && totals.gen_tps_samples ? formatTPS(totals.avg_gen_tps) : '—', totals && totals.gen_tps_samples ? totals.gen_tps_samples + ' 个插件实测样本' : '首内容后 · 插件实测'],
      ['平均 TTFT', totals && totals.ttft_samples ? formatTTFT(totals.avg_ttft_ms) : '—', totals && totals.ttft_samples ? totals.ttft_samples + ' 个成功流式' : '仅成功流式'],
      ['有用量模型', totals ? String(totals.models_used) : '—', '套餐 ' + statsPlanLabel()]
    ];
    el('statsSummary').innerHTML = tiles.map(function(tile){
      return '<div class="stat"><span class="k">' + esc(tile[0]) + '</span><span class="v">' + esc(tile[1]) + '</span>' +
        (tile[2] ? '<span class="hint">' + esc(tile[2]) + '</span>' : '') + '</div>';
    }).join('');
  }
  function formatTokens(value){
    var number = Number(value) || 0;
    if(number >= 1e9) return (number / 1e9).toFixed(2).replace('.00', '') + 'B';
    if(number >= 1e6) return (number / 1e6).toFixed(2).replace('.00', '') + 'M';
    if(number >= 1e4) return (number / 1e3).toFixed(1).replace('.0', '') + 'K';
    return number.toLocaleString();
  }
  function formatUSD(value){
    var number = Number(value);
    if(!isFinite(number)) return '—';
    if(number === 0) return '$0';
    if(number < 0.001) return '$' + number.toFixed(5);
    if(number < 0.01) return '$' + number.toFixed(4);
    if(number < 1) return '$' + number.toFixed(3);
    if(number < 100) return '$' + number.toFixed(2);
    return '$' + Math.round(number).toLocaleString();
  }
  function formatPercent(value){
    if(value == null) return '—';
    var number = Number(value);
    if(!isFinite(number)) return '—';
    if(number >= 100) return number.toFixed(0) + '%';
    if(number >= 10) return number.toFixed(1) + '%';
    return number.toFixed(2) + '%';
  }
  function formatTPS(value){
    var number = Number(value);
    if(!isFinite(number) || number <= 0) return '—';
    if(number >= 100) return number.toFixed(0) + ' tok/s';
    if(number >= 10) return number.toFixed(1) + ' tok/s';
    return number.toFixed(2) + ' tok/s';
  }
  function formatTTFT(value){
    var number = Number(value);
    if(!isFinite(number) || number <= 0) return '—';
    if(number < 1000) return Math.round(number) + ' ms';
    return (number / 1000).toFixed(2) + ' s';
  }
  function usageRow(model){
    var aliases = (model.client_ids || []).filter(function(id){ return id !== model.id; });
    var badges = '';
    if(model.free) badges += '<span class="pill ok">免费</span>';
    else if(!model.priced) badges += '<span class="pill">未定价</span>';
    if(model.family) badges += '<span class="pill">' + esc(FAMILY_LABEL[model.family] || model.family) + '</span>';
    if(!model.configured) badges += '<span class="pill warn">已不在配置中</span>';
    var modelCell = '<div class="stats-model-name">' + esc(model.display_name || model.id) + '</div>' +
      '<div class="stats-model-id"><code>' + esc(model.id) + '</code>' +
      (aliases.length ? ' · 对外 ' + esc(aliases.join(', ')) : '') + '</div>' +
      (badges ? '<div class="stats-model-badges">' + badges + '</div>' : '');

    var requestSub = [];
    if(model.stream_requests) requestSub.push(model.stream_requests + ' 流式');
    if(model.failed_requests) requestSub.push(model.failed_requests + ' 失败');
    if(model.canceled_requests) requestSub.push(model.canceled_requests + ' 取消');
    if(model.unmetered_requests) requestSub.push(model.unmetered_requests + ' 未上报');
    var requests = '<span class="stats-cell-main">' + Number(model.requests || 0).toLocaleString() + '</span>' +
      (requestSub.length ? '<span class="stats-sub">' + esc(requestSub.join(' · ')) + '</span>' : '');

    var input = '<span class="stats-cell-main">' + formatTokens(model.input_tokens) + '</span>' +
      (model.cached_tokens ? '<span class="stats-sub">缓存 ' + formatTokens(model.cached_tokens) + '</span>' : '');
    var output = '<span class="stats-cell-main">' + formatTokens(model.output_tokens) + '</span>' +
      (model.reasoning_tokens ? '<span class="stats-sub">推理 ' + formatTokens(model.reasoning_tokens) + '</span>' : '');
    var cache = (model.cache_read_tokens || model.cache_creation_tokens)
      ? '<span class="stats-cell-main">' + formatTokens(model.cache_read_tokens) + ' / ' + formatTokens(model.cache_creation_tokens) + '</span><span class="stats-sub">读 / 写</span>'
      : '<span class="hint">—</span>';

    var cost = model.free ? '<span class="pill ok">免费</span>'
      : (!model.priced ? '<span class="pill">未定价</span>' : '<span class="stats-cell-main">' + formatUSD(model.cost_usd) + '</span>');
    if(model.priced && model.unpriced_requests) cost += '<span class="stats-sub">' + model.unpriced_requests + ' 次未计价</span>';

    var monthly = '<span class="hint">—</span>';
    if(stats.range && stats.range.kind !== 'month') monthly = '<span class="hint">仅本月</span>';
    else if(model.free) monthly = '<span class="pill ok">不限</span>';
    else if(model.monthly_limit_usd > 0){
      var used = Math.max(0, Number(model.monthly_percent) || 0);
      var width = Math.min(100, used);
      var level = used >= 100 ? 'err' : (used >= 80 ? 'warn' : '');
      var mainBar = '<div class="stats-percent">' +
        '<div class="stats-percent-head">' +
          '<span class="v ' + (level === 'err' ? 'err-text' : level === 'warn' ? 'warn-text' : '') + '">' + formatPercent(used) + '</span>' +
          '<span class="stats-sub">' + formatUSD(model.cost_usd) + ' / ' + formatUSD(model.monthly_limit_usd) + '</span>' +
        '</div>' +
        '<div class="bar ' + level + '"><span style="width:' + width + '%"></span></div>' +
      '</div>';
      var keyPills = '';
      if(model.by_key && model.by_key.length > 0){
        keyPills = '<div class="stats-key-remainders">' + model.by_key.map(function(bk){
          var remUSD = formatUSD(bk.remaining_usd);
          var remPct = bk.remaining_percent != null ? bk.remaining_percent.toFixed(0) + '%' : '';
          var bkLevel = bk.remaining_usd <= 0 ? 'err' : (bk.used_percent >= 80 ? 'warn' : '');
          return '<div class="stats-key-rem-pill ' + bkLevel + '" title="' + esc(bk.key_name) + '：已用 ' + formatUSD(bk.cost_usd) + ' / 限额 ' + formatUSD(model.monthly_limit_usd) + '">' +
            '<span class="k-name">' + esc(bk.key_name) + '</span>' +
            '<span class="k-val">余 ' + remUSD + (remPct ? ' (' + remPct + ')' : '') + '</span>' +
          '</div>';
        }).join('') + '</div>';
      }
      monthly = '<div class="stats-quota-cell">' + mainBar + keyPills + '</div>';
    } else if(model.priced) monthly = '<span class="hint">限额未知</span>';

    var tps = model.tps_samples
      ? '<span class="stats-cell-main">' + formatTPS(model.avg_tps) + '</span><span class="stats-sub">' + model.tps_samples + ' 成功请求 · 整体</span>'
      : '<span class="hint">—</span>';
    var genTps = model.gen_tps_samples
      ? '<span class="stats-cell-main">' + formatTPS(model.avg_gen_tps) + '</span><span class="stats-sub">' + model.gen_tps_samples + ' 插件实测</span>'
      : '<span class="hint">—</span>';
    var ttft = model.ttft_samples
      ? '<span class="stats-cell-main">' + formatTTFT(model.avg_ttft_ms) + '</span><span class="stats-sub">' + model.ttft_samples + ' 成功流式</span>'
      : '<span class="hint">—</span>';
    var last = model.last_used ? esc(relPast(model.last_used)) : '<span class="hint">—</span>';

    return '<tr>' +
      '<td class="stats-model-cell" data-label="模型">' + modelCell + '</td>' +
      '<td class="num" data-label="请求">' + requests + '</td>' +
      '<td class="num" data-label="输入">' + input + '</td>' +
      '<td class="num" data-label="输出">' + output + '</td>' +
      '<td class="num" data-label="缓存读/写">' + cache + '</td>' +
      '<td class="num" data-label="刊例价">' + cost + '</td>' +
      '<td data-label="本月额度">' + monthly + '</td>' +
      '<td class="num" data-label="平均 TPS">' + tps + '</td>' +
      '<td class="num" data-label="生成 TPS">' + genTps + '</td>' +
      '<td class="num" data-label="平均 TTFT">' + ttft + '</td>' +
      '<td data-label="最近使用">' + last + '</td>' +
    '</tr>';
  }
  function renderUsageTable(){
    var box = el('usageStatsTable');
    if(!stats){
      box.innerHTML = '<div class="hint">' + (statsBusy ? '正在读取统计…' : (statsErrorText ? esc(statsErrorText) : '还没有统计数据。')) + '</div>';
      return;
    }
    if(!stats.models || !stats.models.length){
      box.innerHTML = '<div class="empty-state">还没有用量数据。发一次请求后点「刷新」；v1.7 起可从旧 Token Usage Tracker 回放历史；若已有请求但这里仍为空，留意上方的诊断提示。</div>';
      return;
    }
    var headers = ['模型','请求','输入','输出','缓存读/写','刊例价','本月额度','平均 TPS','生成 TPS','平均 TTFT','最近使用'];
    var head = headers.map(function(label, index){
      var numeric = index >= 1 && index <= 5 || index === 7 || index === 8;
      return '<th' + (numeric ? ' class="num"' : '') + '>' + esc(label) + '</th>';
    }).join('');
    box.innerHTML = '<table class="responsive stats-table"><thead><tr>' + head + '</tr></thead><tbody>' +
      stats.models.map(usageRow).join('') + '</tbody></table>';
  }
  function renderUsageMeta(){
    var errorBox = el('statsError');
    if(statsErrorText){ text(errorBox, statsErrorText); errorBox.hidden = false; }
    else { errorBox.hidden = true; }
    var warnBox = el('statsWarnings');
    var warnings = (stats && stats.warnings) || [];
    if(warnings.length){
      warnBox.innerHTML = warnings.map(function(message){ return esc(message); }).join('<br>');
      warnBox.hidden = false;
    } else { warnBox.hidden = true; }
    text(el('statsPlanHint'), '当前套餐：' + statsPlanLabel() + ' · 逐模型月额度为按刊例价折算的估算');
    text(el('statsRangeHint'), statsBusy ? '读取中…' : '每 5 秒落盘');
    var count = stats && stats.models ? stats.models.length : 0;
    text(el('statsModelsHint'), stats ? (count + ' 个模型 · ' + (stats.range.kind === 'month' ? '本月' : '累计') + ' ' + formatUSD(stats.totals.cost_usd)) : '正在读取…');
    if(stats && stats.price_book_revision){
      text(el('statsPriceRevision'), '价目表 ' + stats.price_book_revision + (stats.price_book_checked_at ? ' · 核对于 ' + stats.price_book_checked_at : ''));
      if(stats.price_book_checked_at) text(el('statsPriceCheckedAt'), stats.price_book_checked_at);
    }
    var notes = [];
    if(stats && stats.range){
      if(stats.range.since_source === 'calendar') notes.push('未取得额度重置时间，按 UTC 自然月统计。');
      if(stats.range.clamped) notes.push('起点超出 40 天保留期，已截断。');
    }
    notes.push('v1.7 起可从旧 Token Usage Tracker 回放 opencode-go 历史；新请求最多 5 秒可见。金额为刊例价估算，不是 OpenCode 账单。');
    text(el('statsFootnote'), notes.join(' '));
  }
  function load(){
    loadModels(false);
    loadQuota();
    if(tab === 'stats') loadUsageStats();
  }

  // -------------------------------------------------------- requests pane
  // The 请求日志 tab is fed by /api/requests, which merges the four protocol
  // faces' JSONL files. It is read-only and never touches the configuration.
  var requestsSearchTimer = null;
  function stopRequestsTimer(){
    if(requestsTimer){ clearTimeout(requestsTimer); requestsTimer = null; }
  }
  function scheduleRequestsTimer(){
    stopRequestsTimer();
    if(tab !== 'requests' || !requestsFilter.auto || document.hidden) return;
    requestsTimer = setTimeout(function(){
      loadRequests().then(scheduleRequestsTimer);
    }, 5000);
  }
  function requestQueryString(){
    var parts = ['limit=' + encodeURIComponent(requestsFilter.limit)];
    if(requestsFilter.family) parts.push('family=' + encodeURIComponent(requestsFilter.family));
    if(requestsFilter.key) parts.push('key=' + encodeURIComponent(requestsFilter.key));
    if(requestsFilter.model) parts.push('model=' + encodeURIComponent(requestsFilter.model));
    if(requestsFilter.failed) parts.push('failed=1');
    return parts.join('&');
  }
  function loadRequests(force){
    if(requestsBusy && !force) return Promise.resolve(requests);
    var request = ++requestsRequest;
    requestsBusy = true;
    requestsErrorText = '';
    renderRequests();
    return fetch(BASES.resource + '/api/requests?' + requestQueryString())
      .then(function(response){
        if(!response.ok) throw new Error('HTTP ' + response.status);
        return response.json();
      })
      .then(function(payload){
        if(request !== requestsRequest) return;
        requests = payload || null;
        requestsErrorText = '';
      })
      .catch(function(error){
        if(request !== requestsRequest) return;
        requestsErrorText = '读取请求日志失败：' + error.message;
      })
      .then(function(){
        if(request !== requestsRequest) return;
        requestsBusy = false;
        renderRequests();
      });
  }
  function requestKeyNames(){
    var seen = {};
    var names = [];
    function add(name){
      name = String(name == null ? '' : name).trim();
      if(!name || seen[name]) return;
      seen[name] = true;
      names.push(name);
    }
    ((state && state.keys) || []).forEach(function(key){ add(key.name); });
    ((quota && quota.keys) || []).forEach(function(entry){ add(entry.key_name); });
    ((requests && requests.entries) || []).forEach(function(entry){ add(entry.key); });
    names.sort();
    return names;
  }
  function renderRequestsFilters(){
    var family = el('requestsFamily');
    if(family) family.value = requestsFilter.family;
    var limit = el('requestsLimit');
    if(limit) limit.value = String(requestsFilter.limit);
    var failed = el('requestsFailed');
    if(failed) failed.checked = requestsFilter.failed;
    var auto = el('requestsAuto');
    if(auto) auto.checked = requestsFilter.auto;
    var model = el('requestsModel');
    if(model && document.activeElement !== model) model.value = requestsFilter.model;
    var select = el('requestsKey');
    if(!select) return;
    var html = '<option value="">全部 Key</option>';
    requestKeyNames().forEach(function(name){
      html += '<option value="' + esc(name) + '"' + (name === requestsFilter.key ? ' selected' : '') + '>' + esc(name) + '</option>';
    });
    select.innerHTML = html;
    select.value = requestsFilter.key;
  }
  function renderRequestsMeta(){
    var errorBox = el('requestsError');
    if(requestsErrorText){ text(errorBox, requestsErrorText); errorBox.hidden = false; }
    else { errorBox.hidden = true; }
    var warnBox = el('requestsWarnings');
    var warnings = (requests && requests.warnings) || [];
    if(warnings.length){
      warnBox.innerHTML = '<div class="notice-title"><strong>请求日志提示</strong></div>' +
        warnings.map(function(message){ return '<div class="hint">' + esc(message) + '</div>'; }).join('');
      warnBox.hidden = false;
    } else { warnBox.hidden = true; }
    var hint = el('requestsHint');
    if(!requests) text(hint, requestsBusy ? '正在读取…' : '');
    else if(!requests.enabled) text(hint, '已在设置中关闭');
    else text(hint, '更新于 ' + (requests.generated_at ? fmtTime(requests.generated_at) : '—') +
      ' · 保留 ' + (requests.retention_hours ? Math.round(requests.retention_hours / 24) : 7) + ' 天');
  }
  function renderRequestsSummary(){
    var box = el('requestSummary');
    if(!box) return;
    var totals = (requests && requests.totals) || {};
    var outcome = [];
    if(totals.failed) outcome.push(totals.failed + ' 失败');
    if(totals.canceled) outcome.push(totals.canceled + ' 取消');
    var stats = [
      ['请求', Number(totals.requests || 0).toLocaleString(), outcome.length ? outcome.join(' · ') : '全部成功'],
      ['输入 token', formatTokens(totals.input_tokens), '本次筛选范围'],
      ['输出 token', formatTokens(totals.output_tokens), '本次筛选范围'],
      ['平均 TTFT', totals.ttft_samples ? formatTTFT(totals.avg_ttft_ms) : '—', (totals.ttft_samples || 0) + ' 个样本'],
      ['平均生成 TPS', totals.gen_tps_samples ? formatTPS(totals.avg_gen_tps) : '—', (totals.gen_tps_samples || 0) + ' 个样本'],
      ['平均端到端 TPS', totals.e2e_tps_samples ? formatTPS(totals.avg_e2e_tps) : '—', (totals.e2e_tps_samples || 0) + ' 个样本']
    ];
    box.innerHTML = stats.map(function(stat){
      return '<div class="stat"><span class="k">' + esc(stat[0]) + '</span><span class="v">' + esc(stat[1]) + '</span>' +
        (stat[2] ? '<span class="hint">' + esc(stat[2]) + '</span>' : '') + '</div>';
    }).join('');
  }
  function requestStatusCell(entry){
    var level = entry.failed ? 'err' : (entry.canceled ? 'warn' : (entry.status >= 200 && entry.status < 300 ? 'ok' : ''));
    var label = entry.status ? String(entry.status) : (entry.error ? '失败' : '—');
    var html = '<span class="pill ' + level + '">' + esc(label) + '</span>';
    if(entry.canceled) html += '<span class="pill warn" title="下游客户端在响应完成前断开，不计为失败">取消</span>';
    if(entry.incomplete) html += '<span class="pill warn" title="上游流式在返回 finish_reason 前意外结束">截断</span>';
    if(entry.repairs && entry.repairs.length) html += '<span class="pill accent" title="触发兼容修复: ' + esc(entry.repairs.join(', ')) + '">修复</span>';
    if(entry.error) html += '<span class="stats-sub err-text" title="' + esc(entry.error) + '">' + esc(shorten(entry.error, 40)) + '</span>';
    if(entry.log_id) html += '<span class="stats-sub" title="OpenCode 日志 ID">' + esc(entry.log_id) + '</span>';
    return html;
  }
  function requestRow(entry){
    var aliases = (entry.client_ids || []).filter(function(id){ return id !== entry.model; });
    var badges = '<span class="pill">' + esc(FAMILY_LABEL[entry.role] || entry.role) + '</span>';
    badges += entry.stream ? '<span class="pill accent">流式</span>' : '<span class="pill">非流式</span>';
    var modelCell = '<div class="stats-model-name">' + esc(entry.display_name || entry.model) + '</div>' +
      '<div class="stats-model-id"><code>' + esc(entry.model) + '</code>' +
      (aliases.length ? ' · 对外 ' + esc(aliases.join(', ')) : '') + '</div>' +
      '<div class="stats-model-badges">' + badges + '</div>';
    var timeCell = '<span class="stats-cell-main">' + esc(fmtTime(entry.at)) + '</span><span class="stats-sub">' + esc(relPast(entry.at)) + '</span>';
    var session = entry.session ? '<span class="stats-sub">会话 ' + esc(entry.session) + '</span>' : '';
    var input = '<span class="stats-cell-main">' + formatTokens(entry.input_tokens) + '</span>';
    var output = '<span class="stats-cell-main">' + formatTokens(entry.output_tokens) + '</span>' +
      (entry.reasoning_tokens ? '<span class="stats-sub">推理 ' + formatTokens(entry.reasoning_tokens) + '</span>' : '');
    var total = '<span class="stats-cell-main">' + formatTokens(entry.total_tokens) + '</span>' +
      ((entry.cache_read_tokens || entry.cache_creation_tokens)
        ? '<span class="stats-sub">缓存 ' + formatTokens(entry.cache_read_tokens) + ' / ' + formatTokens(entry.cache_creation_tokens) + '</span>'
        : '');
    var ttft = entry.ttft_ms ? '<span class="stats-cell-main">' + formatTTFT(entry.ttft_ms) + '</span>' : '<span class="hint">—</span>';
    var gen = entry.gen_tps ? '<span class="stats-cell-main">' + formatTPS(entry.gen_tps) + '</span><span class="stats-sub">' + formatTTFT(entry.generation_ms) + '</span>' : '<span class="hint">—</span>';
    var e2e = entry.e2e_tps ? '<span class="stats-cell-main">' + formatTPS(entry.e2e_tps) + '</span><span class="stats-sub">总耗时 ' + formatTTFT(entry.total_ms) + '</span>' : '<span class="hint">—</span>';
    return '<tr>' +
      '<td data-label="时间">' + timeCell + session + '</td>' +
      '<td class="stats-model-cell" data-label="模型">' + modelCell + '</td>' +
      '<td data-label="Key">' + (entry.key ? esc(entry.key) : '<span class="hint">—</span>') + '</td>' +
      '<td class="num" data-label="输入">' + input + '</td>' +
      '<td class="num" data-label="输出">' + output + '</td>' +
      '<td class="num" data-label="合计">' + total + '</td>' +
      '<td class="num" data-label="TTFT">' + ttft + '</td>' +
      '<td class="num" data-label="生成 TPS">' + gen + '</td>' +
      '<td class="num" data-label="端到端 TPS">' + e2e + '</td>' +
      '<td data-label="状态">' + requestStatusCell(entry) + '</td>' +
    '</tr>';
  }
  function renderRequestsTable(){
    var box = el('requestTable');
    if(!box) return;
    if(!requests){
      box.innerHTML = '<div class="hint">' + (requestsBusy ? '正在读取请求日志…' : '还没有数据。发一次请求后点「刷新」。') + '</div>';
      return;
    }
    if(!requests.enabled){
      box.innerHTML = '<div class="empty-state">请求日志已在「设置 → 状态与调试」中关闭。打开后新请求会出现在这里。</div>';
      return;
    }
    if(!requests.entries || !requests.entries.length){
      box.innerHTML = '<div class="empty-state">没有符合条件的请求。调整筛选或发一次请求后刷新；每个协议面最多保留 2000 条 / 7 天。</div>';
      return;
    }
    var headers = ['时间','模型','Key','输入','输出','合计','TTFT','生成 TPS','端到端 TPS','状态'];
    var head = headers.map(function(label, index){
      var numeric = index >= 3 && index <= 8;
      return '<th' + (numeric ? ' class="num"' : '') + '>' + esc(label) + '</th>';
    }).join('');
    box.innerHTML = '<table class="responsive stats-table"><thead><tr>' + head + '</tr></thead><tbody>' +
      requests.entries.map(requestRow).join('') + '</tbody></table>';
  }
  function renderRequests(){
    if(!el('requestSummary')) return;
    renderRequestsFilters();
    renderRequestsMeta();
    renderRequestsSummary();
    renderRequestsTable();
  }
  function initRequestsPane(){
    var family = el('requestsFamily');
    if(family) family.addEventListener('change', function(){
      requestsFilter.family = family.value || '';
      loadRequests(true).then(scheduleRequestsTimer);
    });
    var key = el('requestsKey');
    if(key) key.addEventListener('change', function(){
      requestsFilter.key = key.value || '';
      loadRequests(true).then(scheduleRequestsTimer);
    });
    var model = el('requestsModel');
    if(model) model.addEventListener('input', function(){
      requestsFilter.model = (model.value || '').trim();
      if(requestsSearchTimer) clearTimeout(requestsSearchTimer);
      requestsSearchTimer = setTimeout(function(){ loadRequests(true).then(scheduleRequestsTimer); }, 300);
    });
    var failed = el('requestsFailed');
    if(failed) failed.addEventListener('change', function(){
      requestsFilter.failed = !!failed.checked;
      loadRequests(true).then(scheduleRequestsTimer);
    });
    var limit = el('requestsLimit');
    if(limit) limit.addEventListener('change', function(){
      var parsed = parseInt(limit.value, 10);
      requestsFilter.limit = isNaN(parsed) ? 200 : parsed;
      loadRequests(true).then(scheduleRequestsTimer);
    });
    var auto = el('requestsAuto');
    if(auto) auto.addEventListener('change', function(){
      requestsFilter.auto = !!auto.checked;
      scheduleRequestsTimer();
    });
    var refresh = el('refreshRequests');
    if(refresh) refresh.addEventListener('click', function(){
      loadRequests(true).then(scheduleRequestsTimer);
    });
    document.addEventListener('visibilitychange', function(){
      if(document.hidden){ stopRequestsTimer(); return; }
      if(tab === 'requests') loadRequests().then(scheduleRequestsTimer);
    });
  }

  // ------------------------------------------------------------ key edits
  // Key names are the identity of a pool entry, so a rename has to be checked
  // against the names the configuration will end up with: the rename targets of
  // the other keys, the entries marked for removal (those names are freed), and
  // the pending additions. Without the last check "add key X" after "rename X"
  // would create two entries the host cannot tell apart.
  function keyNameTaken(name, ignore){
    var taken = false;
    poolKeys().forEach(function(key){
      if(taken || edits.keys.del[key.name] || key.name === ignore) return;
      if((edits.keys.rename[key.name] || key.name) === name) taken = true;
    });
    Object.keys(edits.keys.add).forEach(function(added){
      if(!taken && added === name) taken = true;
    });
    return taken;
  }
  // setKeyRename records or drops one pending rename. It returns ok/label so the
  // caller can keep the input and the pending list in sync even when the name is
  // rejected; an invalid draft is never written to the save payload.
  function setKeyRename(original, raw){
    var target = String(raw == null ? '' : raw).trim();
    if(!target){ delete edits.keys.rename[original]; return {ok:false, reason:'empty', label:original}; }
    if(target === original){ delete edits.keys.rename[original]; return {ok:true, label:original}; }
    if(keyNameTaken(target, original)){ delete edits.keys.rename[original]; return {ok:false, reason:'taken', label:original, target:target}; }
    edits.keys.rename[original] = target;
    return {ok:true, label:target};
  }
  function setKeyEnabled(name, enabled){
    var key = poolKey(name);
    var added = edits.keys.add[name];
    if(added){ added.enabled = enabled; }
    else if(key){
      var set = edits.keys.set[name] || {};
      if(enabled === key.enabled){ delete set.enabled; } else { set.enabled = enabled; }
      if(Object.keys(set).length){ edits.keys.set[name] = set; } else { delete edits.keys.set[name]; }
    }
    markDirty();
  }
  function setKeyPriority(name, priority){
    var key = poolKey(name);
    var added = edits.keys.add[name];
    if(added){ added.priority = priority; }
    else if(key){
      var set = edits.keys.set[name] || {};
      if(priority === key.priority){ delete set.priority; } else { set.priority = priority; }
      if(Object.keys(set).length){ edits.keys.set[name] = set; } else { delete edits.keys.set[name]; }
    }
    markDirty();
  }
  function removeKey(name){
    if(edits.keys.add[name]){ delete edits.keys.add[name]; }
    else { edits.keys.del[name] = true; }
    delete edits.keys.set[name];
    delete edits.keys.rename[name];
    markDirty();
    renderAccounts();
  }
  function addKeyFromForm(){
    var name = (el('keyName').value || '').trim();
    var secret = (el('keySecret').value || '').trim();
    var priority = parseInt(el('keyPriority').value, 10);
    if(!name){ keyFormHint('Key 名称不能为空', true); return; }
    if(!secret){ keyFormHint('Key 内容不能为空', true); return; }
    if(isNaN(priority) || priority < 0) priority = 1;
    if(keyNameTaken(name, null)){ keyFormHint('已存在同名 Key：' + name, true); return; }
    delete edits.keys.del[name];
    edits.keys.add[name] = { secret: secret, priority: priority, enabled: true };
    el('keyName').value = ''; el('keySecret').value = ''; el('keyPriority').value = '1';
    keyFormHint('已加入待保存列表，点右上角「保存」写入配置。', false);
    el('keyAdder').open = true;
    markDirty();
    renderAccounts();
  }

  // ------------------------------------------------------------- listeners
  TABS.forEach(function(name){
    el('tab-' + name).addEventListener('click', function(){ showTab(name); });
  });
  window.addEventListener('hashchange', function(){ showTab(currentTabFromHash(), true); });

  el('save').addEventListener('click', save);
  el('reload').addEventListener('click', function(){
    if(isDirty() && !window.confirm('有 ' + countEdits() + ' 项未保存的修改，重载会丢弃它们。继续？')) return;
    clearEdits();
    renderAll();
    load();
  });
  el('discard').addEventListener('click', function(){
    if(!isDirty()){ notify('没有未保存的修改'); return; }
    if(!window.confirm('放弃全部未保存的修改？')) return;
    clearEdits();
    renderAll();
    notify('已放弃未保存的修改');
  });

  el('credSubmit').addEventListener('click', submitCredential);
  el('credCancel').addEventListener('click', cancelCredential);
  el('credInput').addEventListener('keydown', function(event){
    if(event.key === 'Enter'){ event.preventDefault(); submitCredential(); }
  });
  el('credModal').addEventListener('keydown', function(event){
    if(event.key === 'Escape'){ event.preventDefault(); cancelCredential(); }
  });
  el('toast').addEventListener('click', function(){ el('toast').hidden = true; });

  el('refreshQuota').addEventListener('click', loadQuota);
  el('refreshStats').addEventListener('click', function(){ loadUsageStats(true); });
  el('statsRange').addEventListener('change', function(){
    statsRange = el('statsRange').value === 'all' ? 'all' : 'month';
    stats = null;
    loadUsageStats(true);
  });
  el('statsKey').addEventListener('change', function(){
    statsKey = el('statsKey').value;
    stats = null;
    loadUsageStats(true);
  });
  el('statsPlanBtn').addEventListener('click', function(){
    showTab('settings');
    var plan = el('cfgPlan');
    if(plan) plan.focus();
  });
  el('refreshUpstream').addEventListener('click', function(){ loadModels(true); });
  el('refreshModels').addEventListener('click', function(){ loadModels(true); });
  el('collapseModels').addEventListener('click', function(){ expanded = {}; renderModels(); });
  // Only the text box re-renders on every keystroke: re-rendering from a change
  // event would rebuild the list while a click is in flight (mousedown blurs the
  // box, the change event replaces the node, and the click never lands).
  el('modelFilter').addEventListener('input', renderModels);
  ['familyFilter','stateFilter'].forEach(function(id){
    el(id).addEventListener('change', renderModels);
  });
  el('addKey').addEventListener('click', addKeyFromForm);
  el('keySecret').addEventListener('keydown', function(event){
    if(event.key === 'Enter'){ event.preventDefault(); addKeyFromForm(); }
  });

  el('modelStats').addEventListener('click', function(event){
    var button = event.target.closest ? event.target.closest('button[data-stat-filter]') : null;
    if(!button) return;
    var filter = button.getAttribute('data-stat-filter');
    el('stateFilter').value = el('stateFilter').value === filter ? '' : filter;
    renderModels();
  });
  el('keyList').addEventListener('change', function(event){
    var target = event.target;
    if(target.hasAttribute('data-key-rename')){
      var original = target.getAttribute('data-key-rename');
      var verdict = setKeyRename(original, target.value);
      if(!verdict.ok){
        notify(verdict.reason === 'empty' ? 'Key 名称不能为空' : ('已存在同名 Key：' + verdict.target), 'error');
      }
      target.value = verdict.label;
      target.classList.remove('invalid');
      markDirty();
      renderAccounts();
      return;
    }
    if(target.hasAttribute('data-key-enabled')){
      setKeyEnabled(target.getAttribute('data-key-enabled'), target.checked);
      renderAccounts();
      return;
    }
    if(target.hasAttribute('data-key-priority') || target.hasAttribute('data-key-weight')){
      var priority = parseInt(target.value, 10);
      if(isNaN(priority) || priority < 0) priority = 0;
      var keyName = target.getAttribute('data-key-priority') || target.getAttribute('data-key-weight');
      setKeyPriority(keyName, priority);
      target.value = priority;
    }
  });
  el('keyList').addEventListener('input', function(event){
    var target = event.target;
    if(target.hasAttribute('data-key-rename')){
      // Record on every keystroke so a page whose only pending change is the
      // name enables the save button before the click blurs the input.
      var verdict = setKeyRename(target.getAttribute('data-key-rename'), target.value);
      target.classList.toggle('invalid', !verdict.ok);
      target.size = Math.max(6, Math.min(32, String(target.value).length + 1));
      markDirty();
      return;
    }
    if(!target.hasAttribute('data-key-priority') && !target.hasAttribute('data-key-weight')) return;
    var priority = parseInt(target.value, 10);
    if(isNaN(priority) || priority < 0) return;
    var keyName = target.getAttribute('data-key-priority') || target.getAttribute('data-key-weight');
    setKeyPriority(keyName, priority);
  });
  el('keyList').addEventListener('keydown', function(event){
    var target = event.target;
    if(!target.hasAttribute('data-key-rename')) return;
    if(event.key === 'Enter'){ event.preventDefault(); target.blur(); return; }
    if(event.key === 'Escape'){
      event.preventDefault();
      delete edits.keys.rename[target.getAttribute('data-key-rename')];
      target.value = target.getAttribute('data-key-rename');
      target.classList.remove('invalid');
      markDirty();
      target.blur();
    }
  });
  el('keyList').addEventListener('click', function(event){
    var button = event.target.closest ? event.target.closest('button[data-key-remove]') : null;
    if(!button) return;
    removeKey(button.getAttribute('data-key-remove'));
  });
  el('modelList').addEventListener('change', function(event){
    var target = event.target;
    if(target.hasAttribute('data-model-enabled')){
      setModelField(target.getAttribute('data-model-enabled'), 'enabled', target.checked);
      renderModels();
      return;
    }
    if(target.hasAttribute('data-model-family')){
      setModelField(target.getAttribute('data-model-family'), 'family', target.value);
      renderModels();
      return;
    }
    if(target.hasAttribute('data-model-proxy')){
      setModelField(target.getAttribute('data-model-proxy'), 'proxy', target.value);
      renderModels();
    }
  });
  el('modelList').addEventListener('input', function(event){
    var target = event.target;
    if(!target.hasAttribute('data-model-alias')) return;
    var aliases = target.value.split(',').map(function(part){ return part.trim(); }).filter(Boolean);
    setModelField(target.getAttribute('data-model-alias'), 'aliases', aliases);
  });
  el('modelList').addEventListener('click', function(event){
    var target = event.target;
    var settings = target.closest ? target.closest('button[data-model-settings]') : null;
    if(settings){
      var id = settings.getAttribute('data-model-settings');
      if(expanded[id]){ delete expanded[id]; } else { expanded[id] = {}; }
      renderModels();
      return;
    }
    var reset = target.closest ? target.closest('button[data-model-reset]') : null;
    if(reset){ resetModel(reset.getAttribute('data-model-reset')); return; }
    var probe = target.closest ? target.closest('button[data-model-probe]') : null;
    if(probe){ runProbe(probe.getAttribute('data-model-probe')); return; }
    var use = target.closest ? target.closest('button[data-probe-use]') : null;
    if(use){
      var probeID = use.getAttribute('data-probe-use');
      setModelField(probeID, 'family', use.getAttribute('data-probe-family'));
      delete probes[probeID];
      renderModels();
    }
  });
  function runProbe(id){
    probes[id] = 'loading';
    renderModels();
    ensureCredential('探测模型协议需要管理中心密码（一次输入即可，可在弹窗里选择记住到本标签页）。')
      .then(function(credential){
        return managementRequest(credential, '/opencode-go/probe?model=' + encodeURIComponent(id));
      })
      .then(function(payload){ probes[id] = payload || {}; })
      .catch(function(error){
        if(error.message === 'cancelled'){ delete probes[id]; notify('已取消探测'); }
        else { probes[id] = { error: error.message }; }
      })
      .then(function(){ renderModels(); });
  }
  ['cfgBaseURL','cfgSession','cfgProxyURL','cfgStateDir','cfgSessionMode','cfgDefaultProxy','cfgTimeout','cfgPlan','cfgEndpointLayout','cfgDeveloperRole','cfgReasoningReplay','cfgRequestLog','cfgDebug'].forEach(function(id){
    var field = {
      cfgBaseURL:'base-url', cfgSession:'session', cfgProxyURL:'proxy-url', cfgStateDir:'state-dir',
      cfgSessionMode:'session-mode', cfgDefaultProxy:'default-proxy', cfgTimeout:'request-timeout-seconds',
      cfgPlan:'plan', cfgEndpointLayout:'endpoint-layout', cfgDeveloperRole:'developer-role', cfgReasoningReplay:'reasoning-replay',
      cfgRequestLog:'request-log', cfgDebug:'debug'
    }[id];
    var node = el(id);
    function record(){
      var raw;
      if(node.type === 'checkbox'){ raw = node.checked; }
      else if(node.type === 'number'){
        var parsed = parseInt(node.value, 10);
        if(isNaN(parsed)) return;
        if(parsed < 0) parsed = 0;
        if(field === 'request-timeout-seconds' && parsed < 1) parsed = 1;
        raw = parsed;
      } else { raw = (node.value || '').trim(); }
      if(sameText(raw, topBase(field))){ delete edits.top[field]; } else { edits.top[field] = raw; }
      markDirty();
    }
    node.addEventListener('input', record);
    node.addEventListener('change', record);
  });

  document.addEventListener('keydown', function(event){
    if(event.key !== '/' || event.metaKey || event.ctrlKey || event.altKey) return;
    var target = event.target || {};
    var tag = String(target.tagName || '').toLowerCase();
    if(tag === 'input' || tag === 'select' || tag === 'textarea') return;
    event.preventDefault();
    showTab('models');
    var filter = el('modelFilter');
    filter.focus();
    filter.select();
  });
  window.addEventListener('storage', function(event){
    // The management centre also writes its credential when it saves it from
    // another tab; nothing to do here beyond dropping a stale rejection.
    if(event.key !== CRED_STORE && event.key !== CRED_LEGACY) return;
    hostCredentialRejected = false;
  });
  initRequestsPane();
  showTab(currentTabFromHash(), true);
  renderChrome();
  renderPending();
  load();
})();
`
