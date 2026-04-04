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
  editorPane: 'split',
  currentDir: '',
  treeItems: [],
  currentFile: null,
  envExpanded: {},
  activeJobId: '',
  activeJob: null,
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
    state.currentFile = {
      ...data,
      dirty: false,
    };
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
    payload.env_entries = state.currentFile.env_entries || [];
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
    setToast(data.validation && data.validation.restart_ok === false ? 'error' : 'success', data.validation && data.validation.restart_ok === false ? '文件已保存，但重启校验未通过' : '文件已保存');
    await refreshStatus();
    render();
  } catch (error) {
    setToast('error', error.message || '保存失败');
  }
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
    setToast('error', error.message || '状态刷新失败');
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

  const textEditor = document.getElementById('text-editor');
  if (textEditor && state.currentFile) {
    textEditor.value = state.currentFile.content || '';
    textEditor.addEventListener('input', (event) => {
      state.currentFile.content = event.target.value;
      state.currentFile.dirty = true;
      updateDirtyBadge();
      updatePreview();
    });
    updatePreview();
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
    const expandAll = document.getElementById('env-expand-all');
    if (expandAll) {
      expandAll.addEventListener('click', () => {
        (state.currentFile.env_entries || []).forEach((entry, index) => {
          state.envExpanded[envEntryId(entry, index)] = true;
        });
        render();
      });
    }
    const collapseAll = document.getElementById('env-collapse-all');
    if (collapseAll) {
      collapseAll.addEventListener('click', () => {
        state.envExpanded = {};
        render();
      });
    }
    const tailInput = document.getElementById('env-tail-lines');
    if (tailInput) {
      tailInput.addEventListener('input', (event) => {
        state.currentFile.tail_lines = event.target.value.split('\n');
        state.currentFile.dirty = true;
        updateDirtyBadge();
      });
    }
  }
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
  }
  if (field === 'leading') {
    entry.leading_lines = target.value.split('\n');
  }
  state.currentFile.dirty = true;
  updateDirtyBadge();
}

function updateDirtyBadge() {
  const dirty = document.getElementById('file-dirty-badge');
  if (dirty) {
    dirty.textContent = state.currentFile?.dirty ? '未保存' : '已同步';
    dirty.className = `badge ${state.currentFile?.dirty ? 'warn' : 'ok'}`;
  }
}

function updatePreview() {
  const preview = document.getElementById('syntax-preview');
  if (!preview || !state.currentFile) {
    return;
  }
  preview.innerHTML = highlightText(state.currentFile.content || '', state.currentFile.language || 'text');
}

function loginTemplate() {
  return `
    <section class="login-wrap">
      <div class="login-card material-login-card">
        <div class="brand-mark">M3</div>
        <p class="brand-kicker">openai-compat-proxy</p>
        <h1>管理台</h1>
        <p class="lead">基于 Material 3 层级重构的配置管理界面，用于浏览项目文件、编辑配置、检查运行状态，并执行受控脚本操作。</p>
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
  const drawer = state.sidebarOpen ? `
      <div id="sidebar-backdrop" class="sidebar-backdrop is-open"></div>
      <aside id="navigation-drawer" class="sidebar material-drawer is-open" aria-label="主导航抽屉">
        <div class="material-drawer-topbar">
          <div>
            <div class="brand-kicker">navigation drawer</div>
            <h2 class="material-drawer-title">openai-compat-proxy</h2>
          </div>
          <button id="sidebar-close" class="icon-btn sidebar-close material-icon-button" type="button" aria-label="关闭侧边栏">
            <span></span><span></span>
          </button>
        </div>
        <div class="material-drawer-section">
          <div class="material-label">Destinations</div>
          <div class="nav-list material-nav-list">
            ${renderDrawerNavItem('browser', '文件浏览', '目录树与文件定位', drawerIconFolder())}
            ${renderDrawerNavItem('editor', '文件编辑', state.currentFile ? escapeHtml(state.currentFile.path) : '先从文件浏览选择一个文件', drawerIconEdit())}
            ${renderDrawerNavItem('status', '运行状态', '脚本、日志、健康检查', drawerIconMonitor())}
          </div>
        </div>
        <div class="material-drawer-section material-drawer-supporting">
          <div class="support-card">
            <div class="material-label">Current context</div>
            <div class="support-title">${escapeHtml(pageHeadingByView())}</div>
            <div class="support-copy">${escapeHtml(pageDescriptionByView())}</div>
          </div>
          <div class="support-card compact">
            <div class="support-kv"><span>当前目录</span><strong>${escapeHtml(state.currentDir || '/')}</strong></div>
            <div class="support-kv"><span>当前文件</span><strong>${escapeHtml(state.currentFile?.path || '未选择')}</strong></div>
          </div>
          <button id="logout-button" class="ghost-btn material-outlined-button" type="button">退出登录</button>
        </div>
      </aside>
  ` : '';
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
              <p class="brand-kicker">${escapeHtml(pageTitleByView())}</p>
              <h1>${escapeHtml(pageHeadingByView())}</h1>
              <p class="editor-subtitle">${escapeHtml(pageDescriptionByView())}</p>
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
  const filteredItems = getFilteredTreeItems();
  return `
    <div class="browser-page-grid">
      <section class="panel material-surface-grid">
        <div class="panel-body compact-grid two-up">
          <div class="material-overview-card emphasis">
            <div class="material-label">Browse context</div>
            <h3>项目根目录</h3>
            <p>用一条清晰的文件列表路径浏览仓库，点击文件后进入独立的编辑页。</p>
          </div>
          <div class="material-overview-card compact">
            <div class="overview-pair"><span>当前目录</span><strong>${escapeHtml(state.currentDir || '/')}</strong></div>
            <div class="overview-pair"><span>筛选结果</span><strong>${filteredItems.length}</strong></div>
          </div>
        </div>
      </section>
      <section class="panel">
        <div class="panel-head">
          <h2 class="section-title">目录树</h2>
          <p class="muted">Material 3 list 风格的文件浏览。主操作是定位目标文件，不在这里混入编辑表单。</p>
        </div>
        <div class="panel-body">
          <div class="material-chip-row browser-filter-row">
            ${renderBrowserFilterChip('all', '全部')}
            ${renderBrowserFilterChip('editable', '可编辑')}
            ${renderBrowserFilterChip('dirs', '目录')}
            ${renderBrowserFilterChip('config', '配置')}
          </div>
          <div class="tree-nav">
            <button class="secondary-btn material-tonal-button" type="button" data-tree-open="" data-type="dir">项目根</button>
            ${state.currentDir ? `<button class="secondary-btn material-outlined-button" type="button" data-tree-open="${escapeAttr(parentPath(state.currentDir))}" data-type="dir">返回上级</button>` : ''}
          </div>
          <div class="tree-list">
            ${renderTreeItems(filteredItems)}
          </div>
        </div>
      </section>
    </div>
  `;
}

function renderStatusPage() {
  return `
    <div class="editor-page-grid">
      <section class="panel">
        <div class="panel-head">
          <h2 class="section-title">脚本与运行状态</h2>
          <p class="muted">按 Material 3 的页面级 action hierarchy 组织：状态在上，动作成组，日志独立承载。</p>
        </div>
        <div class="panel-body">
          <div class="status-row material-chip-row" style="margin-bottom: 18px;">
            ${validationBadge()}
            ${healthBadge()}
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
  return `
    <div class="editor-page-grid">
      <section class="panel material-surface-grid">
        <div class="panel-body compact-grid two-up">
          <div class="material-overview-card emphasis">
            <div class="material-label">Editing context</div>
            <h3>${state.currentFile ? escapeHtml(state.currentFile.path) : '等待选择文件'}</h3>
            <p>当前页面只保留编辑与预览，减少任务切换，避免把导航和运行状态塞回同一屏。</p>
          </div>
          <div class="material-overview-card compact">
            <div class="overview-pair"><span>语言模式</span><strong>${state.currentFile ? escapeHtml(labelForLanguage(state.currentFile.language || 'text')) : '—'}</strong></div>
            <div class="overview-pair"><span>保存状态</span><strong>${state.currentFile?.dirty ? '未保存' : '已同步'}</strong></div>
          </div>
        </div>
      </section>
      <section class="panel">
        <div class="panel-head">
          <h2 class="section-title">编辑器</h2>
          <p class="muted">${state.currentFile ? escapeHtml(state.currentFile.path) : '先回到文件浏览页选择一个文件'}</p>
        </div>
        <div class="panel-body">
          <div class="segmented-row">
            ${renderEditorPaneButton('split', '双栏')}
            ${renderEditorPaneButton('edit', '仅编辑')}
            ${renderEditorPaneButton('preview', '仅预览')}
          </div>
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
        <div class="tree-meta">${item.path} · ${item.is_symlink ? '符号链接' : item.is_dir ? '文件夹' : formatBytes(item.size || 0)}</div>
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
    { label: '项目目录', value: status.root_dir || '/', tone: 'info' },
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
        <div class="status-value">${escapeHtml(status.listen_addr || '未知')}</div>
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
      <div class="job-head">
        <div>
          <h3 class="section-title">运行日志尾部</h3>
          <p class="muted job-meta">来自 .proxy.log 的最近输出。</p>
        </div>
      </div>
      <div class="job-body">
        <pre class="runtime-log">${escapeHtml(status.log_tail || '暂无运行日志')}</pre>
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
    return `<div class="empty-state material-empty-state"><p>先去「文件浏览」页选择一个文件。</p><button class="secondary-btn material-tonal-button" type="button" data-view="browser">打开文件浏览</button></div>`;
  }
  if (state.currentFile.mode === 'env') {
    return renderEnvEditor();
  }
  return renderTextEditor();
}

function renderTextEditor() {
  const showEdit = state.editorPane !== 'preview';
  const showPreview = state.editorPane !== 'edit';
  return `
    <div class="text-editor-grid pane-${state.editorPane}">
      ${showEdit ? `<section class="editor-card">
        <div class="editor-head">
          <div>
            <h2>原文编辑</h2>
            <p class="muted">${escapeHtml(labelForLanguage(state.currentFile.language || 'text'))} · Material 3 editor surface</p>
          </div>
        </div>
        <div class="editor-body">
          <textarea id="text-editor" class="text-area" spellcheck="false"></textarea>
        </div>
      </section>` : ''}
      ${showPreview ? `<section class="preview-card">
        <div class="preview-head">
          <div>
            <h2>高亮预览</h2>
            <p class="muted">保存前先确认语法结构。</p>
          </div>
        </div>
        <div class="preview-body">
          <pre id="syntax-preview" class="syntax-preview"></pre>
        </div>
      </section>` : ''}
    </div>
  `;
}

function renderEnvEditor() {
  const entries = state.currentFile.env_entries || [];
  const showEdit = state.editorPane !== 'preview';
  const showPreview = state.editorPane !== 'edit';
  return `
    <div id="env-editor" class="env-list pane-${state.editorPane}">
      ${showEdit ? `<div class="env-toolbar">
        <button id="env-expand-all" class="secondary-btn material-tonal-button" type="button">全部展开</button>
        <button id="env-collapse-all" class="secondary-btn material-outlined-button" type="button">全部折叠</button>
        <span class="helper-text">默认只显示字段；展开后可编辑其前置注释与空行。</span>
      </div>` : ''}
      ${showEdit ? entries.map((entry, index) => renderEnvEntry(entry, index)).join('') : ''}
      ${showEdit ? `<section class="env-card">
        <div class="env-head">
          <div>
            <h2>尾部内容</h2>
            <p class="muted">如果文件末尾还有独立注释或空行，会保存在这里。</p>
          </div>
        </div>
        <div class="env-body">
          <textarea id="env-tail-lines" class="comment-input" spellcheck="false">${escapeHtml((state.currentFile.tail_lines || []).join('\n'))}</textarea>
        </div>
      </section>` : ''}
      ${showPreview ? `<section class="preview-card">
        <div class="preview-head">
          <div>
            <h2>原文预览</h2>
            <p class="muted">方便在保存前确认最终写回的 .env 结构。</p>
          </div>
        </div>
        <div class="preview-body">
          <pre class="syntax-preview">${highlightText(renderEnvRawPreview(), 'env')}</pre>
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
      <div class="env-head">
        <div>
          <h2>${escapeHtml(entry.key || `字段 ${index + 1}`)}</h2>
          <p class="muted">${expanded ? '注释与空行已展开' : '注释已折叠，仅显示字段'}</p>
        </div>
      </div>
      <div class="env-body">
        <div class="env-row">
          <button class="icon-btn env-toggle material-outlined-button" type="button" data-env-toggle="${escapeAttr(entryId)}">${expanded ? '收起' : '展开'}</button>
          <input class="env-key-input" data-index="${index}" data-field="key" value="${escapeAttr(entry.key || '')}">
          <textarea class="env-value-input" data-index="${index}" data-field="value" spellcheck="false">${escapeHtml(entry.value || '')}</textarea>
        </div>
        ${expanded ? `
          <div class="env-extra">
            <div class="helper-text">这些内容会保留在当前字段前方，用于保留原来的中文注释与分隔。</div>
            <textarea class="comment-input" data-index="${index}" data-field="leading" spellcheck="false">${escapeHtml((entry.leading_lines || []).join('\n'))}</textarea>
          </div>
        ` : ''}
      </div>
    </section>
  `;
}

function renderEnvRawPreview() {
  const entries = state.currentFile?.env_entries || [];
  const tailLines = state.currentFile?.tail_lines || [];
  const lines = [];
  entries.forEach((entry) => {
    (entry.leading_lines || []).forEach((line) => {
      lines.push(line);
    });
    lines.push(`${entry.key || ''}=${entry.value || ''}`);
  });
  tailLines.forEach((line) => {
    lines.push(line);
  });
  return `${lines.join('\n')}\n`;
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

function pageTitleByView() {
  if (state.view === 'editor') {
    return '文件编辑';
  }
  if (state.view === 'status') {
    return '运行状态';
  }
  return '文件浏览';
}

function pageHeadingByView() {
  if (state.view === 'editor') {
    return state.currentFile ? `正在编辑 ${state.currentFile.path}` : '文件编辑';
  }
  if (state.view === 'status') {
    return '运行状态与脚本中心';
  }
  return '项目文件浏览';
}

function pageDescriptionByView() {
  if (state.view === 'editor') {
    return '这里只保留当前文件的编辑与预览，不再混入目录树和脚本控制。';
  }
  if (state.view === 'status') {
    return '脚本按钮、健康检查、日志和任务输出只放在这一页。';
  }
  return '这里只做目录树与文件定位，点文件后自动切到编辑页。';
}

function renderBrowserFilterChip(filter, label) {
  const active = state.browserFilter === filter;
  return `<button class="filter-chip ${active ? 'active' : ''}" type="button" data-browser-filter="${filter}">${active ? '✓ ' : ''}${label}</button>`;
}

function renderEditorPaneButton(pane, label) {
  return `<button class="segmented-button ${state.editorPane === pane ? 'active' : ''}" type="button" data-editor-pane="${pane}">${label}</button>`;
}

function getFilteredTreeItems() {
  const items = state.treeItems || [];
  if (state.browserFilter === 'editable') {
    return items.filter((item) => item.editable);
  }
  if (state.browserFilter === 'dirs') {
    return items.filter((item) => item.is_dir);
  }
  if (state.browserFilter === 'config') {
    return items.filter((item) => item.path === '.env' || item.name.endsWith('.env') || item.name.endsWith('.md') || item.name.endsWith('.txt') || item.name.endsWith('.json') || item.name.endsWith('.yaml') || item.name.endsWith('.yml'));
  }
  return items;
}

function renderTopbarTools() {
  if (state.view === 'editor') {
    return `
      <button id="save-file-button" class="save-btn material-filled-button" type="button" ${state.currentFile ? '' : 'disabled'}>保存文件</button>
      <span id="file-dirty-badge" class="badge ${state.currentFile?.dirty ? 'warn' : 'ok'} material-state-chip">${state.currentFile?.dirty ? '未保存' : '已同步'}</span>
    `;
  }
  if (state.view === 'status') {
    return '<span class="badge info material-state-chip">脚本与日志页</span>';
  }
  return '<span class="badge info material-state-chip">点文件后自动进入编辑页</span>';
}

function renderDrawerNavItem(view, title, subtitle, icon) {
  return `
    <button class="nav-button ${state.view === view ? 'active' : ''}" type="button" data-view="${view}">
      <span class="material-nav-indicator">${icon}</span>
      <span class="material-nav-copy">
        <strong>${title}</strong>
        <span>${subtitle}</span>
      </span>
    </button>
  `;
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

function labelForLanguage(language) {
  const map = {
    env: '.env',
    markdown: 'Markdown',
    json: 'JSON',
    go: 'Go',
    shell: 'Shell',
    yaml: 'YAML',
    text: '文本',
  };
  return map[language] || language;
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
