const state = {
  authenticated: false,
  loading: true,
  csrfToken: '',
  actions: [],
  validation: null,
  status: null,
  sidebarOpen: false,
  view: 'browser',
  browserFilter: 'all',
  editorPane: 'edit',
  currentDir: '',
  treeItems: [],
  currentFile: null,
  envExpanded: {},
  activeJobId: '',
  activeJob: null,
  lastSaveFeedback: null,
  toast: null,
};

const app = document.getElementById('app');

init();

async function init() {
  await bootstrap();
}

async function bootstrap() {
  state.loading = true;
  render();
  try {
    const data = await api('/_admin/api/bootstrap');
    state.authenticated = true;
    state.csrfToken = data.csrf_token || '';
    state.actions = data.actions || [];
    state.validation = data.validation || null;
    state.status = data.status || null;
    await loadTree('');
    state.view = 'browser';
    state.loading = false;
    render();
  } catch {
    state.loading = false;
    state.authenticated = false;
    render();
  }
}

async function api(path, options = {}) {
  const requestOptions = {
    method: options.method || 'GET',
    headers: { ...(options.headers || {}) },
    credentials: 'same-origin',
  };
  if (options.body !== undefined) {
    requestOptions.body = JSON.stringify(options.body);
    requestOptions.headers['Content-Type'] = 'application/json';
  }
  if (!['GET', 'HEAD'].includes(requestOptions.method) && state.csrfToken) {
    requestOptions.headers['X-Admin-CSRF'] = state.csrfToken;
  }
  const response = await fetch(path, requestOptions);
  const contentType = response.headers.get('Content-Type') || '';
  let payload = null;
  if (contentType.includes('application/json')) {
    payload = await response.json();
  } else {
    payload = await response.text();
  }
  if (!response.ok) {
    const message = typeof payload === 'object' && payload && payload.error ? payload.error.message : response.statusText;
    if (response.status === 401 && path !== '/_admin/api/login') {
      state.authenticated = false;
      state.csrfToken = '';
      state.currentFile = null;
      state.activeJobId = '';
      state.activeJob = null;
      render();
    }
    throw new Error(message || '请求失败');
  }
  return payload;
}

async function handleLoginSubmit(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const password = form.querySelector('[name="password"]').value;
  const remember = form.querySelector('[name="remember"]').checked;
  try {
    const data = await api('/_admin/api/login', {
      method: 'POST',
      body: { password, remember },
    });
    state.authenticated = true;
    state.csrfToken = data.csrf_token || '';
    setToast('success', '已进入管理台');
    await bootstrap();
  } catch (error) {
    setToast('error', error.message || '登录失败');
  }
}

async function logout() {
  try {
    await api('/_admin/api/logout', { method: 'POST', body: {} });
  } catch {
  }
  state.authenticated = false;
  state.csrfToken = '';
  state.currentFile = null;
  state.activeJobId = '';
  state.activeJob = null;
  render();
}

async function loadTree(path) {
  state.currentDir = path || '';
  const data = await api(`/_admin/api/tree?path=${encodeURIComponent(state.currentDir)}`);
  state.treeItems = data.items || [];
}

async function openDirectory(path) {
  try {
    state.view = 'browser';
    state.sidebarOpen = false;
    await loadTree(path);
    render();
  } catch (error) {
    setToast('error', error.message || '目录读取失败');
  }
}

async function openFile(path) {
  try {
    const data = await api(`/_admin/api/file?path=${encodeURIComponent(path)}`);
    const prepared = data.mode === 'env'
      ? {
          ...data,
          env_entries: (data.env_entries || []).map((entry) => normalizeEnvEntryForDisplay(entry)),
          source_content: renderEnvRaw((data.env_entries || []).map((entry) => normalizeEnvEntryForDisplay(entry)), data.tail_lines || []),
        }
      : data;
    state.currentFile = {
      ...prepared,
      dirty: false,
    };
    state.lastSaveFeedback = null;
    state.view = 'editor';
    state.sidebarOpen = false;
    state.envExpanded = {};
    render();
  } catch (error) {
    setToast('error', error.message || '文件读取失败');
  }
}

async function saveCurrentFile() {
  if (!state.currentFile) {
    return;
  }
  const payload = {
    path: state.currentFile.path,
    mode: state.currentFile.mode,
  };
  if (state.currentFile.mode === 'env') {
    payload.env_entries = serializeEnvEntries(state.currentFile.env_entries || []);
    payload.tail_lines = state.currentFile.tail_lines || [];
  } else {
    payload.content = state.currentFile.content || '';
  }
  try {
    const data = await api('/_admin/api/file', {
      method: 'PUT',
      body: payload,
    });
    state.currentFile.dirty = false;
    state.validation = data.validation || state.validation;
    state.status = state.status || {};
    state.lastSaveFeedback = data.validation && data.validation.restart_ok === false
      ? { tone: 'danger', text: '校验失败' }
      : { tone: 'ok', text: '校验通过' };
    setToast(data.validation && data.validation.restart_ok === false ? 'error' : 'success', data.validation && data.validation.restart_ok === false ? '文件已保存，但重启校验未通过' : '文件已保存');
    await refreshStatus();
    render();
  } catch (error) {
    state.lastSaveFeedback = { tone: 'danger', text: '保存失败' };
    setToast('error', error.message || '保存失败');
  }
}

function closeCurrentFile() {
  state.currentFile = null;
  state.lastSaveFeedback = null;
  state.view = 'browser';
  state.sidebarOpen = false;
  render();
}

async function refreshStatus() {
  try {
    const data = await api('/_admin/api/status');
    state.validation = data.validation || state.validation;
    state.status = data.status || state.status;
    if (data.job) {
      state.activeJob = data.job;
      state.activeJobId = data.job.id || state.activeJobId;
    }
    render();
  } catch (error) {
    const message = error.message === 'Failed to fetch'
      ? '状态刷新失败，服务可能正在重启或已断开'
      : (error.message || '状态刷新失败');
    setToast('error', message);
  }
}

async function runAction(action) {
  if (!confirmAction(action)) {
    return;
  }
  try {
    const data = await api('/_admin/api/action', {
      method: 'POST',
      body: { action },
    });
    state.activeJob = data.job || null;
    state.activeJobId = data.job ? data.job.id : '';
    render();
    setToast('info', `${labelForAction(action)}已开始`);
    pollActiveJob();
  } catch (error) {
    setToast('error', error.message || '任务启动失败');
  }
}

function confirmAction(action) {
  if (action === 'deploy') {
    return window.confirm('确认部署？这会重新编译并重启当前服务。');
  }
  if (action === 'restart') {
    return window.confirm('确认重启？当前服务会短暂中断。');
  }
  if (action === 'stop') {
    return window.confirm('停止服务后，当前页面将很快失去连接。确认继续？');
  }
  if (action === 'uninstall') {
    return window.confirm('卸载会停止服务并删除当前运行产物。确认继续？');
  }
  return true;
}

async function pollActiveJob() {
  if (!state.activeJobId) {
    return;
  }
  let keepPolling = true;
  while (keepPolling && state.activeJobId) {
    try {
      const data = await api(`/_admin/api/jobs?id=${encodeURIComponent(state.activeJobId)}`);
      state.activeJob = data.job || null;
      render();
      if (!state.activeJob || state.activeJob.status !== 'running') {
        keepPolling = false;
        await refreshStatus();
        if (state.activeJob) {
          setToast(state.activeJob.status === 'succeeded' ? 'success' : 'error', `${state.activeJob.label || labelForAction(state.activeJob.action)}已${state.activeJob.status === 'succeeded' ? '完成' : '失败'}`);
        }
        break;
      }
    } catch (error) {
      keepPolling = false;
      setToast('error', error.message || '任务状态查询失败');
      break;
    }
    await delay(1000);
  }
}

function delay(ms) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

function setToast(type, text) {
  state.toast = { type, text };
  renderToast();
  window.clearTimeout(setToast.timer);
  setToast.timer = window.setTimeout(() => {
    state.toast = null;
    renderToast();
  }, 3200);
}

function render() {
  app.innerHTML = state.authenticated ? dashboardTemplate() : loginTemplate();
  bindEvents();
  renderToast();
}

function renderToast() {
  const existing = document.querySelector('.toast');
  if (existing) {
    existing.remove();
  }
  if (!state.toast) {
    return;
  }
  const toast = document.createElement('div');
  toast.className = `toast ${state.toast.type}`;
  toast.textContent = state.toast.text;
  document.body.appendChild(toast);
}

function bindEvents() {
  const loginForm = document.getElementById('login-form');
  if (loginForm) {
    loginForm.addEventListener('submit', handleLoginSubmit);
    return;
  }

  document.querySelectorAll('[data-action-run]').forEach((button) => {
    button.addEventListener('click', () => runAction(button.dataset.actionRun));
  });

  document.querySelectorAll('[data-tree-open]').forEach((button) => {
    button.addEventListener('click', () => {
      if (button.dataset.type === 'dir') {
        openDirectory(button.dataset.treeOpen);
      } else {
        openFile(button.dataset.treeOpen);
      }
    });
  });

  document.querySelectorAll('[data-view]').forEach((button) => {
    button.addEventListener('click', () => {
      state.view = button.dataset.view;
      state.sidebarOpen = false;
      render();
    });
  });

  document.querySelectorAll('[data-browser-filter]').forEach((button) => {
    button.addEventListener('click', () => {
      state.browserFilter = button.dataset.browserFilter;
      render();
    });
  });

  document.querySelectorAll('[data-editor-pane]').forEach((button) => {
    button.addEventListener('click', () => {
      state.editorPane = button.dataset.editorPane;
      if (state.currentFile?.mode === 'env') {
        syncEnvSourceFromStructured();
      }
      render();
    });
  });

  const sidebarToggle = document.getElementById('sidebar-toggle');
  if (sidebarToggle) {
    sidebarToggle.addEventListener('click', () => {
      state.sidebarOpen = !state.sidebarOpen;
      render();
    });
  }

  const sidebarClose = document.getElementById('sidebar-close');
  if (sidebarClose) {
    sidebarClose.addEventListener('click', () => {
      state.sidebarOpen = false;
      render();
    });
  }

  const sidebarBackdrop = document.getElementById('sidebar-backdrop');
  if (sidebarBackdrop) {
    sidebarBackdrop.addEventListener('click', () => {
      state.sidebarOpen = false;
      render();
    });
  }

  const logoutButton = document.getElementById('logout-button');
  if (logoutButton) {
    logoutButton.addEventListener('click', logout);
  }

  const refreshButton = document.getElementById('refresh-status-button');
  if (refreshButton) {
    refreshButton.addEventListener('click', refreshStatus);
  }

  const saveButton = document.getElementById('save-file-button');
  if (saveButton) {
    saveButton.addEventListener('click', saveCurrentFile);
  }

  const cancelButton = document.getElementById('cancel-file-button');
  if (cancelButton) {
    cancelButton.addEventListener('click', closeCurrentFile);
  }

  const fullPathButton = document.getElementById('full-path-button');
  if (fullPathButton && state.currentFile?.path) {
    fullPathButton.addEventListener('click', () => {
      window.alert(state.currentFile.path);
    });
  }

  const textEditor = document.getElementById('text-editor');
  if (textEditor && state.currentFile) {
    textEditor.value = state.currentFile.content || '';
    autoSizeTextarea(textEditor);
    textEditor.addEventListener('input', (event) => {
      state.currentFile.content = event.target.value;
      state.currentFile.dirty = true;
      state.lastSaveFeedback = null;
      autoSizeTextarea(event.target);
      updateDirtyBadge();
    });
  }

  const envContainer = document.getElementById('env-editor');
  if (envContainer && state.currentFile) {
    envContainer.addEventListener('input', handleEnvInput);
    envContainer.querySelectorAll('[data-env-toggle]').forEach((button) => {
      button.addEventListener('click', () => {
        const key = button.dataset.envToggle;
        state.envExpanded[key] = !state.envExpanded[key];
        render();
      });
    });
    const toggleAll = document.getElementById('env-toggle-all');
    if (toggleAll) {
      toggleAll.addEventListener('click', () => {
        const entries = state.currentFile.env_entries || [];
        const allExpanded = entries.length > 0 && entries.every((entry, index) => state.envExpanded[envEntryId(entry, index)]);
        if (allExpanded) {
          state.envExpanded = {};
        } else {
          entries.forEach((entry, index) => {
            state.envExpanded[envEntryId(entry, index)] = true;
          });
        }
        render();
      });
    }
    const tailInput = document.getElementById('env-tail-lines');
    if (tailInput) {
      autoSizeTextarea(tailInput);
      tailInput.addEventListener('input', (event) => {
        state.currentFile.tail_lines = event.target.value.split('\n');
        state.currentFile.dirty = true;
        state.lastSaveFeedback = null;
        syncEnvSourceFromStructured();
        autoSizeTextarea(event.target);
        updateDirtyBadge();
      });
    }
    const envSourceEditor = document.getElementById('env-source-editor');
    if (envSourceEditor) {
      autoSizeTextarea(envSourceEditor);
      envSourceEditor.addEventListener('input', (event) => {
        const parsed = parseEnvSource(event.target.value);
        state.currentFile.source_content = event.target.value;
        state.currentFile.env_entries = parsed.env_entries;
        state.currentFile.tail_lines = parsed.tail_lines;
        state.currentFile.dirty = true;
        state.lastSaveFeedback = null;
        autoSizeTextarea(event.target);
        updateDirtyBadge();
      });
    }
  }

  autoSizeTextareas();
}

function handleEnvInput(event) {
  const target = event.target;
  const index = Number(target.dataset.index);
  const field = target.dataset.field;
  if (!Number.isInteger(index) || !field) {
    return;
  }
  const entry = state.currentFile.env_entries[index];
  if (!entry) {
    return;
  }
  if (field === 'key') {
    entry.key = target.value;
  }
  if (field === 'value') {
    entry.value = target.value;
    syncEnvSourceFromStructured();
  }
  if (field === 'leading') {
    entry.leading_lines = target.value.split('\n');
    syncEnvSourceFromStructured();
  }
  state.currentFile.dirty = true;
  state.lastSaveFeedback = null;
  if (target.tagName === 'TEXTAREA') {
    autoSizeTextarea(target);
  }
  updateDirtyBadge();
}

function updateDirtyBadge() {
  const dirty = document.getElementById('file-dirty-badge');
  if (dirty) {
    dirty.textContent = state.currentFile?.dirty ? '未保存' : '已同步';
    dirty.className = `badge ${state.currentFile?.dirty ? 'warn' : 'ok'}`;
  }
  const saveButton = document.getElementById('save-file-button');
  if (saveButton) {
    saveButton.disabled = !state.currentFile;
  }
  const cancelButton = document.getElementById('cancel-file-button');
  if (cancelButton) {
    cancelButton.disabled = !state.currentFile;
  }
}

function loginTemplate() {
  return `
    <section class="login-wrap">
      <div class="login-card material-login-card">
        <div class="brand-mark">M3</div>
        <p class="brand-kicker">openai-compat-proxy</p>
        <h1>管理台</h1>
        <form id="login-form" class="login-form">
          <label class="form-field">
            <span class="form-label">管理员密码</span>
            <input class="text-input" type="password" name="password" autocomplete="current-password" required>
          </label>
          <label class="checkbox-row">
            <input type="checkbox" name="remember">
            <span>记住我</span>
          </label>
          <button class="primary-btn" type="submit">进入管理台</button>
        </form>
      </div>
    </section>
  `;
}

function dashboardTemplate() {
  const drawer = `
      <div id="sidebar-backdrop" class="sidebar-backdrop ${state.sidebarOpen ? 'is-open' : ''}"></div>
      <aside id="navigation-drawer" class="sidebar material-drawer ${state.sidebarOpen ? 'is-open' : ''}" aria-label="主导航抽屉" aria-hidden="${state.sidebarOpen ? 'false' : 'true'}">
        <div class="material-drawer-topbar">
          <div>
            <h2 class="material-drawer-title">openai-compat-proxy</h2>
          </div>
          <button id="sidebar-close" class="icon-btn sidebar-close material-icon-button" type="button" aria-label="关闭侧边栏">
            <span></span><span></span>
          </button>
        </div>
        <div class="material-drawer-section">
          <div class="nav-list material-nav-list">
            ${renderDrawerNavItem('browser', '文件浏览', drawerIconFolder())}
            ${renderDrawerNavItem('editor', '文件编辑', drawerIconEdit())}
            ${renderDrawerNavItem('status', '运行状态', drawerIconMonitor())}
          </div>
        </div>
        <div class="material-drawer-section material-drawer-supporting">
          <div class="support-card compact">
            <div class="support-kv"><span>当前目录</span><strong>${escapeHtml(state.currentDir || '/')}</strong></div>
            <div class="support-kv"><span>当前文件</span><strong>${escapeHtml(state.currentFile?.path || '未选择')}</strong></div>
          </div>
          <button id="logout-button" class="ghost-btn material-outlined-button" type="button">退出登录</button>
        </div>
      </aside>
  `;
  return `
    <div class="app-shell drawer-layout ${state.sidebarOpen ? 'drawer-open' : ''}">
      ${drawer}
      <div class="content-shell material-content-shell">
        <header class="topbar material-topbar">
          <div class="topbar-line">
            <div class="topbar-leading">
              <button id="sidebar-toggle" class="icon-btn sidebar-toggle material-icon-button" type="button" aria-label="展开侧边栏" aria-controls="navigation-drawer" aria-expanded="${state.sidebarOpen ? 'true' : 'false'}">
                <span></span><span></span><span></span>
              </button>
            </div>
            <div class="topbar-title">
              ${renderTopbarTitle()}
            </div>
            <div class="topbar-meta">
              ${renderTopbarTools()}
            </div>
          </div>
        </header>
        ${state.view === 'browser' ? renderBrowserPage() : state.view === 'status' ? renderStatusPage() : renderEditorPage()}
      </div>
    </div>
  `;
}

function renderBrowserPage() {
  const items = state.treeItems || [];
  return `
    <div class="browser-page-grid page-scene">
      <section class="panel">
        <div class="panel-body">
          <div class="status-row compact-meta-row">
            <span class="badge info material-state-chip">当前目录 ${escapeHtml(state.currentDir || '/')}</span>
            <span class="badge info material-state-chip">项目文件 ${items.length}</span>
          </div>
          <div class="tree-nav">
            <button class="secondary-btn material-tonal-button" type="button" data-tree-open="" data-type="dir">项目根</button>
            ${state.currentDir ? `<button class="secondary-btn material-outlined-button" type="button" data-tree-open="${escapeAttr(parentPath(state.currentDir))}" data-type="dir">返回上级</button>` : ''}
          </div>
          <div class="tree-list">
            ${renderTreeItems(items)}
          </div>
        </div>
      </section>
    </div>
  `;
}

function renderStatusPage() {
  return `
    <div class="editor-page-grid page-scene status-page">
      <section class="panel">
        <div class="panel-body">
          <div class="status-toolbar" style="margin-bottom: 18px;">
            <div class="status-row material-chip-row">
              ${validationBadge()}
              ${healthBadge()}
            </div>
            <button id="refresh-status-button" class="secondary-btn material-outlined-button" type="button">刷新状态</button>
          </div>
          <div class="button-row action-cluster" style="margin-bottom: 18px;">
            ${(state.actions || []).map((action) => `<button class="action-btn material-action-button" type="button" data-action-run="${escapeAttr(action.action)}" data-action="${escapeAttr(action.action)}">${escapeHtml(action.label)}</button>`).join('')}
          </div>
          ${renderStatusSection()}
        </div>
      </section>
    </div>
  `;
}

function renderEditorPage() {
  const showEnvModeSwitch = state.currentFile?.mode === 'env';
  return `
    <div class="editor-page-grid page-scene">
      <section class="panel">
        <div class="panel-body">
          ${showEnvModeSwitch ? `
            <div class="pane-switch" data-pane="${state.editorPane}">
              ${renderEditorPaneButton('edit', '条目式')}
              ${renderEditorPaneButton('preview', '源文式')}
            </div>
            <div class="editor-mode-divider" aria-hidden="true"></div>
          ` : ''}
          ${renderEditorSection()}
        </div>
      </section>
    </div>
  `;
}

function renderTreeItems(items) {
  if (!items || items.length === 0) {
    return `<div class="empty-state">当前目录没有可显示的文件。</div>`;
  }
  return items.map((item) => `
    <button class="tree-item ${state.currentFile?.path === item.path ? 'active' : ''}" type="button" data-tree-open="${escapeAttr(item.path)}" data-type="${item.is_dir ? 'dir' : 'file'}">
      <div class="tree-item-leading">
        <span class="tree-item-icon ${item.is_dir ? 'folder' : 'file'}"></span>
      </div>
      <div class="tree-item-body">
        <div class="tree-item-title">
          <span>${escapeHtml(item.name)}</span>
          <span class="badge ${item.is_dir ? 'info' : item.editable ? 'ok' : 'warn'}">${item.is_dir ? '目录' : item.editable ? '可编辑' : '只读'}</span>
        </div>
        <div class="tree-meta">${escapeHtml(formatTreeMeta(item))}</div>
      </div>
      <div class="tree-item-trailing">›</div>
    </button>
  `).join('');
}

function renderStatusSection() {
  const status = state.status || {};
  const validation = state.validation || {};
  const dashboardCards = [
    { label: '服务状态', value: status.health_ok ? '健康' : '异常', tone: status.health_ok ? 'ok' : 'warn' },
    { label: '配置校验', value: validation.restart_ok ? '可重启' : '需修复', tone: validation.restart_ok ? 'ok' : 'danger' },
    { label: '当前任务', value: state.activeJob ? (state.activeJob.label || labelForAction(state.activeJob.action)) : '空闲', tone: state.activeJob ? 'info' : 'info' },
    { label: '日志目录', value: status.log_dir || '未启用', tone: 'info' },
  ];
  return `
    <div class="dashboard-metric-grid">
      ${dashboardCards.map((card) => `
        <div class="dashboard-metric-card ${card.tone}">
          <div class="status-label">${card.label}</div>
          <div class="dashboard-metric-value">${escapeHtml(card.value)}</div>
        </div>
      `).join('')}
    </div>
    <div class="status-grid material-status-grid">
      <div class="status-card">
        <div class="status-label">监听地址</div>
        <div class="status-value">${escapeHtml(formatListenAddrDisplay(status.listen_addr))}</div>
      </div>
      <div class="status-card">
        <div class="status-label">运行健康</div>
        <div class="status-value">${status.health_ok ? '健康' : '未通过'}</div>
      </div>
      <div class="status-card">
        <div class="status-label">当前 PID</div>
        <div class="status-value">${escapeHtml(status.pid || '暂无')}</div>
      </div>
      <div class="status-card">
        <div class="status-label">配置预检</div>
        <div class="status-value">热加载 ${validation.hot_reload_ok ? '通过' : '失败'} / 重启 ${validation.restart_ok ? '通过' : '失败'}</div>
      </div>
    </div>
    ${renderValidationWarnings(validation)}
    <div class="job-output material-log-card">
      <div class="job-head">
        <div>
          <h3 class="section-title">脚本任务</h3>
          <p class="muted job-meta">${state.activeJob ? escapeHtml(`${state.activeJob.label || labelForAction(state.activeJob.action)} · ${state.activeJob.status}`) : '当前没有正在跟踪的脚本任务'}</p>
        </div>
        ${state.activeJob ? `<span class="badge ${state.activeJob.status === 'succeeded' ? 'ok' : state.activeJob.status === 'running' ? 'info' : 'danger'}">${escapeHtml(state.activeJob.status)}</span>` : ''}
      </div>
      <div class="job-body">
        <pre class="job-log">${escapeHtml(state.activeJob?.output || '暂无脚本输出')}</pre>
      </div>
    </div>
    <div class="job-output material-log-card">
      <div class="job-head compact-head">
        <h3 class="section-title">结构化日志</h3>
      </div>
      <div class="job-body compact-copy">
        当前运行使用新的结构化日志系统，日志目录：<strong>${escapeHtml(status.log_dir || '未启用')}</strong>
      </div>
    </div>
  `;
}

function renderValidationWarnings(validation) {
  if (!validation) {
    return '';
  }
  const items = [];
  if (validation.hot_reload_error) {
    items.push(`<div class="info-card"><div class="info-label">热加载错误</div><div class="info-value">${escapeHtml(validation.hot_reload_error)}</div></div>`);
  }
  if (validation.restart_error) {
    items.push(`<div class="info-card"><div class="info-label">重启错误</div><div class="info-value">${escapeHtml(validation.restart_error)}</div></div>`);
  }
  if (items.length === 0) {
    return '';
  }
  return `<div class="info-grid material-warning-grid" style="margin: 16px 0;">${items.join('')}</div>`;
}

function renderEditorSection() {
  if (!state.currentFile) {
    return `<div class="empty-state material-empty-state"><p>未选择文件</p><button class="secondary-btn material-tonal-button" type="button" data-view="browser">文件浏览</button></div>`;
  }
  if (state.currentFile.mode === 'env') {
    return renderEnvEditor();
  }
  return renderTextEditor();
}

function renderTextEditor() {
  return `
    <div class="text-editor-grid pane-edit">
      <section class="editor-card mode-scene">
        <div class="editor-body">
          <textarea id="text-editor" name="text-editor" class="text-area auto-resize source-mode" spellcheck="false"></textarea>
        </div>
      </section>
    </div>
  `;
}

function renderEnvEditor() {
  const entries = state.currentFile.env_entries || [];
  const allExpanded = entries.length > 0 && entries.every((entry, index) => state.envExpanded[envEntryId(entry, index)]);
  return `
    <div id="env-editor" class="env-list pane-${state.editorPane}">
      ${state.editorPane === 'edit' ? `<div class="env-toolbar">
        <button id="env-toggle-all" class="secondary-btn material-tonal-button" type="button">${allExpanded ? '全部收起' : '全部展开'}</button>
      </div>` : ''}
      ${state.editorPane === 'edit' ? entries.map((entry, index) => renderEnvEntry(entry, index)).join('') : ''}
      ${state.editorPane === 'edit' ? `<section class="env-card mode-scene">
        <div class="env-body">
          <textarea id="env-tail-lines" name="env-tail-lines" class="comment-input auto-resize" spellcheck="false">${escapeHtml((state.currentFile.tail_lines || []).join('\n'))}</textarea>
        </div>
      </section>` : ''}
      ${state.editorPane === 'preview' ? `<section class="editor-card mode-scene">
        <div class="editor-body">
          <textarea id="env-source-editor" name="env-source-editor" class="text-area auto-resize env-source-editor source-mode" spellcheck="false">${escapeHtml(state.currentFile.source_content || renderEnvRawPreview())}</textarea>
        </div>
      </section>` : ''}
    </div>
  `;
}

function renderEnvEntry(entry, index) {
  const entryId = envEntryId(entry, index);
  const expanded = !!state.envExpanded[entryId];
  return `
    <section class="env-card">
      <div class="env-body">
        <div class="env-key-row">
          <div class="env-key-label">${escapeHtml(entry.key || `字段 ${index + 1}`)}</div>
          <button class="env-toggle secondary-btn material-outlined-button" type="button" data-env-toggle="${escapeAttr(entryId)}">${expanded ? '收起' : '说明'}</button>
        </div>
        ${expanded ? `
          <pre class="env-comment-block">${escapeHtml((entry.leading_lines || []).join('\n'))}</pre>
        ` : ''}
        <div class="env-value-row">
          <textarea class="env-value-input auto-resize" name="env-value-${index}" data-index="${index}" data-field="value" spellcheck="false">${escapeHtml(entry.value || '')}</textarea>
        </div>
      </div>
    </section>
  `;
}

function renderEnvRawPreview() {
  const entries = state.currentFile?.env_entries || [];
  const tailLines = state.currentFile?.tail_lines || [];
  return renderEnvRaw(entries, tailLines);
}

function renderEnvRaw(entries, tailLines) {
  const lines = [];
  (entries || []).forEach((entry) => {
    (entry.leading_lines || []).forEach((line) => {
      lines.push(line);
    });
    lines.push(`${entry.key || ''}=${entry.value || ''}`);
  });
  (tailLines || []).forEach((line) => {
    lines.push(line);
  });
  return `${lines.join('\n')}\n`;
}

function syncEnvSourceFromStructured() {
  if (!state.currentFile || state.currentFile.mode !== 'env') {
    return;
  }
  state.currentFile.source_content = renderEnvRaw(state.currentFile.env_entries || [], state.currentFile.tail_lines || []);
}

function parseEnvSource(content) {
  const lines = String(content || '').replace(/\r\n/g, '\n').split('\n');
  if (lines.length > 0 && lines[lines.length - 1] === '') {
    lines.pop();
  }
  const envEntries = [];
  const pending = [];
  lines.forEach((line) => {
    const matched = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (!matched) {
      pending.push(line);
      return;
    }
    envEntries.push({
      key: matched[1],
      value: formatEnvValueForDisplay(matched[1], matched[2]),
      leading_lines: [...pending],
    });
    pending.length = 0;
  });
  return {
    env_entries: envEntries,
    tail_lines: [...pending],
  };
}

function validationBadge() {
  if (!state.validation) {
    return '<span class="badge info material-state-chip">预检中</span>';
  }
  if (state.validation.restart_ok) {
    return '<span class="badge ok material-state-chip">重启校验通过</span>';
  }
  return '<span class="badge danger material-state-chip">重启校验失败</span>';
}

function healthBadge() {
  if (!state.status) {
    return '<span class="badge info material-state-chip">状态读取中</span>';
  }
  return `<span class="badge ${state.status.health_ok ? 'ok' : 'warn'} material-state-chip">${state.status.health_ok ? '服务健康' : '健康检查失败'}</span>`;
}

function labelForAction(action) {
  const actionMap = {
    deploy: '部署',
    restart: '重启',
    stop: '停止',
    uninstall: '卸载',
  };
  return actionMap[action] || action;
}

function pageHeadingByView() {
  if (state.view === 'editor') {
    return state.currentFile ? state.currentFile.path : '文件编辑';
  }
  if (state.view === 'status') {
    return '运行状态';
  }
  return '文件浏览';
}

function renderEditorPaneButton(pane, label) {
  return `<button class="segmented-button ${state.editorPane === pane ? 'active' : ''}" type="button" data-editor-pane="${pane}">${label}</button>`;
}

function renderTopbarTools() {
  if (state.view === 'editor') {
    return `
      <span id="file-dirty-badge" class="badge ${state.currentFile?.dirty ? 'warn' : 'ok'} material-state-chip">${state.currentFile?.dirty ? '未保存' : '已同步'}</span>
      ${state.lastSaveFeedback ? `<span class="badge ${escapeAttr(state.lastSaveFeedback.tone)} material-state-chip">${escapeHtml(state.lastSaveFeedback.text)}</span>` : ''}
      <button id="cancel-file-button" class="secondary-btn material-outlined-button" type="button" ${state.currentFile ? '' : 'disabled'}>取消</button>
      <button id="save-file-button" class="save-btn" type="button" ${state.currentFile ? '' : 'disabled'}>保存</button>
    `;
  }
  if (state.view === 'status') {
    return '';
  }
  return '';
}

function renderDrawerNavItem(view, title, icon) {
  return `
    <button class="nav-button ${state.view === view ? 'active' : ''}" type="button" data-view="${view}">
      <span class="material-nav-indicator">${icon}</span>
      <span class="material-nav-copy">
        <strong>${title}</strong>
      </span>
    </button>
  `;
}

function renderTopbarTitle() {
  if (state.view === 'editor') {
    return `
      <div class="topbar-title-group">
        <span class="title-prefix">正在编辑</span>
        <button id="full-path-button" class="title-file-pill" type="button">${escapeHtml(displayFileName(pageHeadingByView()))}</button>
      </div>
    `;
  }
  return `<h1>${escapeHtml(pageHeadingByView())}</h1>`;
}

function displayFileName(path) {
  const value = String(path || '');
  const parts = value.split('/').filter(Boolean);
  return parts[parts.length - 1] || value || '未选择文件';
}

function formatTreeMeta(item) {
  const modified = formatTimestamp(item.modified);
  const detail = item.is_symlink ? '符号链接' : item.is_dir ? '文件夹' : formatBytes(item.size || 0);
  return `${modified} · ${detail}`;
}

function formatTimestamp(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value || '';
  }
  const parts = [
    date.getFullYear(),
    String(date.getMonth() + 1).padStart(2, '0'),
    String(date.getDate()).padStart(2, '0'),
  ];
  const time = [
    String(date.getHours()).padStart(2, '0'),
    String(date.getMinutes()).padStart(2, '0'),
    String(date.getSeconds()).padStart(2, '0'),
  ];
  return `${parts.join('-')} ${time.join(':')}`;
}

function normalizeEnvEntryForDisplay(entry) {
  return {
    ...entry,
    value: formatEnvValueForDisplay(entry.key, entry.value || ''),
  };
}

function serializeEnvEntries(entries) {
  return entries.map((entry) => ({
    ...entry,
    value: formatEnvValueForSave(entry.key, entry.value || ''),
  }));
}

function formatEnvValueForDisplay(key, value) {
  const trimmed = String(value ?? '').trim();
  if (key === 'LISTEN_ADDR' && /^:\d+$/.test(trimmed)) {
    return trimmed.slice(1);
  }
  return value || '';
}

function formatEnvValueForSave(key, value) {
  const trimmed = String(value ?? '').trim();
  if (key === 'LISTEN_ADDR' && /^:\d+$/.test(trimmed)) {
    return trimmed.slice(1);
  }
  return value || '';
}

function formatListenAddrDisplay(value) {
  const trimmed = String(value ?? '').trim();
  if (/^:\d+$/.test(trimmed)) {
    return trimmed.slice(1);
  }
  return trimmed || '未知';
}

function autoSizeTextareas(root = document) {
  root.querySelectorAll('textarea.auto-resize').forEach((textarea) => {
    autoSizeTextarea(textarea);
  });
}

function autoSizeTextarea(textarea) {
  if (!(textarea instanceof HTMLTextAreaElement)) {
    return;
  }
  textarea.style.height = 'auto';
  textarea.style.height = `${textarea.scrollHeight}px`;
}

function drawerIconFolder() {
  return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6.5A2.5 2.5 0 0 1 5.5 4H9l2 2h7.5A2.5 2.5 0 0 1 21 8.5v9A2.5 2.5 0 0 1 18.5 20h-13A2.5 2.5 0 0 1 3 17.5z" fill="currentColor" opacity="0.92"></path></svg>';
}

function drawerIconEdit() {
  return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 17.25V20h2.75L17.8 8.94l-2.75-2.75z" fill="currentColor"></path><path d="M19.86 7.01a.996.996 0 0 0 0-1.41l-1.46-1.46a.996.996 0 1 0-1.41 1.41l1.46 1.46c.39.39 1.02.39 1.41 0" fill="currentColor" opacity="0.72"></path></svg>';
}

function drawerIconMonitor() {
  return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 5h16a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-6v2h3v2H7v-2h3v-2H4a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2zm0 2v8h16V7zm2 6 2.5-3 2 2 3-4 4.5 5z" fill="currentColor"></path></svg>';
}

function parentPath(path) {
  const parts = (path || '').split('/').filter(Boolean);
  parts.pop();
  return parts.join('/');
}

function envEntryId(entry, index) {
  return `${entry.key || 'entry'}-${index}`;
}

function escapeHtml(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function escapeAttr(value) {
  return escapeHtml(value);
}

function formatBytes(value) {
  const size = Number(value || 0);
  if (size < 1024) {
    return `${size} B`;
  }
  if (size < 1024 * 1024) {
    return `${(size / 1024).toFixed(1)} KB`;
  }
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function highlightText(content, language) {
  if (language === 'env') {
    return highlightEnv(content);
  }
  if (language === 'markdown') {
    return highlightMarkdown(content);
  }
  if (language === 'json') {
    return highlightJSON(content);
  }
  if (language === 'go' || language === 'shell' || language === 'yaml') {
    return highlightKeywords(content, language);
  }
  return escapeHtml(content);
}

function highlightEnv(content) {
  return escapeHtml(content).split('\n').map((line) => {
    if (line.startsWith('#') || line.trim() === '') {
      return `<span class="tok-comment">${line || '&nbsp;'}</span>`;
    }
    const index = line.indexOf('=');
    if (index === -1) {
      return line;
    }
    const key = line.slice(0, index);
    const value = line.slice(index + 1);
    return `<span class="tok-key">${key}</span><span class="tok-op">=</span><span class="tok-string">${value}</span>`;
  }).join('\n');
}

function highlightMarkdown(content) {
  return escapeHtml(content).split('\n').map((line) => {
    if (/^#{1,6}\s/.test(line)) {
      return `<span class="tok-heading">${line}</span>`;
    }
    if (/^[-*+]\s/.test(line) || /^\d+\.\s/.test(line)) {
      return `<span class="tok-list">${line}</span>`;
    }
    if (/^```/.test(line)) {
      return `<span class="tok-code">${line}</span>`;
    }
    if (/^>\s/.test(line)) {
      return `<span class="tok-comment">${line}</span>`;
    }
    return line;
  }).join('\n');
}

function highlightJSON(content) {
  return escapeHtml(content)
    .replace(/("(?:[^"\\]|\\.)*")\s*:/g, '<span class="tok-key">$1</span><span class="tok-op">:</span>')
    .replace(/:\s*("(?:[^"\\]|\\.)*")/g, ': <span class="tok-string">$1</span>')
    .replace(/\b(true|false|null)\b/g, '<span class="tok-bool">$1</span>')
    .replace(/(^|[^\w>])(\d+(?:\.\d+)?)(?![^<]*>)/g, '$1<span class="tok-number">$2</span>');
}

function highlightKeywords(content, language) {
  const keywords = {
    go: ['package', 'func', 'return', 'if', 'else', 'for', 'type', 'struct', 'import', 'var', 'const', 'switch', 'case'],
    shell: ['if', 'then', 'fi', 'for', 'do', 'done', 'case', 'esac', 'function', 'local'],
    yaml: ['true', 'false', 'null'],
  };
  let html = escapeHtml(content);
  html = html.replace(/(^|\n)(\s*#.*)/g, '$1<span class="tok-comment">$2</span>');
  (keywords[language] || []).forEach((keyword) => {
    html = html.replace(new RegExp(`\\b${keyword}\\b`, 'g'), `<span class="tok-key">${keyword}</span>`);
  });
  html = html.replace(/("(?:[^"\\]|\\.)*")/g, '<span class="tok-string">$1</span>');
  html = html.replace(/\b(\d+(?:\.\d+)?)\b/g, '<span class="tok-number">$1</span>');
  return html;
}
