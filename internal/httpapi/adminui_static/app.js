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
  fileActionMenu: null,
  clearCacheModal: null,
  activeJobId: '',
  activeJob: null,
  recoveryAction: '',
  recoveryDeadline: 0,
  editorZoom: loadEditorZoom(),
  lastSaveFeedback: null,
  toast: null,
  statusRefreshInFlight: false,
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
      if (hasRecoverableRestartJob()) {
        throw new Error('Recoverable unauthorized during restart');
      }
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

function hasRecoverableRestartJob() {
  return isRecoveryWindowActive() || !!(state.activeJob && state.activeJob.status === 'running' && ['restart', 'deploy'].includes(state.activeJob.action));
}

function startRecoveryWindow(action) {
  if (!['restart', 'deploy'].includes(action)) {
    return;
  }
  state.recoveryAction = action;
  state.recoveryDeadline = Date.now() + 90_000;
}

function clearRecoveryWindow() {
  state.recoveryAction = '';
  state.recoveryDeadline = 0;
}

function isRecoveryWindowActive() {
  return ['restart', 'deploy'].includes(state.recoveryAction) && Date.now() < state.recoveryDeadline;
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
    if (data.path) {
      state.currentFile.path = data.path;
    }
    state.currentFile.dirty = false;
    state.validation = data.validation || state.validation;
    state.status = state.status || {};
    state.lastSaveFeedback = data.validation && data.validation.restart_ok === false
      ? { tone: 'danger', text: '校验失败' }
      : { tone: 'ok', text: '校验通过' };
    setToast(data.validation && data.validation.restart_ok === false ? 'error' : 'success', data.validation && data.validation.restart_ok === false ? '文件已保存，但重启校验未通过' : '文件已保存');
    await loadTree(state.currentDir);
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
  return refreshStatusWithRetry({ retryOnDisconnect: false, attempts: 1, silent: false });
}

async function refreshStatusWithRetry({ retryOnDisconnect = false, attempts = 1, silent = false } = {}) {
  if (state.statusRefreshInFlight) {
    return false;
  }
  state.statusRefreshInFlight = true;
  let remaining = Math.max(1, attempts);
  try {
    while (remaining > 0) {
      try {
        const data = await api('/_admin/api/status');
        state.validation = data.validation || state.validation;
        state.status = data.status || state.status;
        if (data.job) {
          state.activeJob = data.job;
          state.activeJobId = data.job.id || state.activeJobId;
        } else if (!hasRecoverableRestartJob()) {
          state.activeJob = null;
          state.activeJobId = '';
        }
        if (!state.activeJob || state.activeJob.status !== 'running') {
          clearRecoveryWindow();
        }
        render();
        return true;
      } catch (error) {
        remaining -= 1;
        if (retryOnDisconnect && shouldRetryStatusRefresh(error) && remaining > 0) {
          if (!silent && state.toast?.text !== '服务正在重启，等待重新连接') {
            setToast('info', '服务正在重启，等待重新连接');
          }
          await delay(1500);
          continue;
        }
        if (!silent) {
          const message = error.message === 'Failed to fetch'
            ? '状态刷新失败，服务可能正在重启或已断开'
            : (error.message || '状态刷新失败');
          setToast('error', message);
        }
        return false;
      }
    }
    return false;
  } finally {
    state.statusRefreshInFlight = false;
  }
}

async function runAction(action) {
  if (!confirmAction(action)) {
    return;
  }
  const isRecoverableAction = ['restart', 'deploy'].includes(action);
  if (isRecoverableAction) {
    startRecoveryWindow(action);
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
    if (isRecoverableAction) {
      void refreshStatusWithRetry({ retryOnDisconnect: true, attempts: 20, silent: true });
    }
    pollActiveJob();
  } catch (error) {
    if (isRecoverableAction && isRecoverableStatusDisconnect(error)) {
      setToast('info', `${labelForAction(action)}已发出，等待服务恢复连接`);
      const recovered = await refreshStatusWithRetry({ retryOnDisconnect: true, attempts: 20, silent: false });
      if (recovered && state.activeJobId) {
        pollActiveJob();
        return;
      }
    }
    if (isRecoverableAction) {
      clearRecoveryWindow();
    }
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
  let reconnectAttempts = 0;
  const maxReconnectAttempts = 40;
  while (keepPolling && state.activeJobId) {
    try {
      reconnectAttempts = 0;
      const data = await api(`/_admin/api/jobs?id=${encodeURIComponent(state.activeJobId)}`);
      state.activeJob = data.job || null;
      render();
      if (!state.activeJob || state.activeJob.status !== 'running') {
        keepPolling = false;
        await refreshStatusWithRetry({ retryOnDisconnect: true, attempts: 20, silent: false });
        if (state.activeJob) {
          setToast(state.activeJob.status === 'succeeded' ? 'success' : 'error', `${state.activeJob.label || labelForAction(state.activeJob.action)}已${state.activeJob.status === 'succeeded' ? '完成' : '失败'}`);
        }
        break;
      }
    } catch (error) {
      if (shouldKeepPollingAfterDisconnect(error)) {
        reconnectAttempts += 1;
        if (reconnectAttempts >= maxReconnectAttempts) {
          keepPolling = false;
          setToast('error', '服务长时间未恢复连接，请手动刷新状态确认结果');
          break;
        }
        if (state.toast?.text !== '服务正在重启，等待重新连接') {
          setToast('info', '服务正在重启，等待重新连接');
        }
        await delay(1500);
        continue;
      }
      keepPolling = false;
      setToast('error', error.message || '任务状态查询失败');
      break;
    }
    await delay(1000);
  }
}

function shouldKeepPollingAfterDisconnect(error) {
  return isRecoverableStatusDisconnect(error)
    && state.activeJob
    && state.activeJob.status === 'running'
    && ['restart', 'deploy'].includes(state.activeJob.action);
}

function shouldRetryStatusRefresh(error) {
  return isRecoverableStatusDisconnect(error)
    && (
      (state.activeJob && state.activeJob.status === 'running' && ['restart', 'deploy'].includes(state.activeJob.action))
      || ['status', 'editor', 'browser'].includes(state.view)
    );
}

function isRecoverableStatusDisconnect(error) {
  const message = String(error?.message || '');
  return message === 'Failed to fetch'
    || /Recoverable unauthorized during restart/i.test(message)
    || /Unexpected end of JSON input/i.test(message)
    || /Unexpected token/i.test(message)
    || /not valid JSON/i.test(message)
    || /JSON/i.test(message) && /unexpected end|unterminated|end of data/i.test(message)
    || /NetworkError/i.test(message)
    || /Load failed/i.test(message);
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
    if (button.dataset.type !== 'dir') {
      let pressTimer = 0;
      button.addEventListener('pointerdown', () => {
        button.classList.add('danger-press');
        pressTimer = window.setTimeout(() => {
          button.dataset.longPressTriggered = 'true';
          openTreeItemActionMenu(button.dataset.treeOpen);
        }, 650);
      });
      ['pointerup', 'pointerleave', 'pointercancel'].forEach((eventName) => {
        button.addEventListener(eventName, () => {
          if (pressTimer) {
            window.clearTimeout(pressTimer);
            pressTimer = 0;
          }
          button.classList.remove('danger-press');
        });
      });
    }
    button.addEventListener('click', (event) => {
      if (button.dataset.longPressTriggered === 'true') {
        button.dataset.longPressTriggered = '';
        event.preventDefault();
        return;
      }
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
    refreshButton.addEventListener('click', () => refreshStatusWithRetry({ retryOnDisconnect: true, attempts: 20, silent: false }));
  }

  const createEnvButton = document.getElementById('create-env-button');
  if (createEnvButton) {
    createEnvButton.addEventListener('click', createEnvFromCurrentDir);
  }

  const createMdButton = document.getElementById('create-md-button');
  if (createMdButton) {
    createMdButton.addEventListener('click', createMdFromCurrentDir);
  }

  const fileActionBackdrop = document.getElementById('file-action-backdrop');
  if (fileActionBackdrop) {
    fileActionBackdrop.addEventListener('click', closeTreeItemActionMenu);
  }
  const fileActionCancel = document.getElementById('file-action-cancel');
  if (fileActionCancel) {
    fileActionCancel.addEventListener('click', closeTreeItemActionMenu);
  }
  const fileActionRename = document.getElementById('file-action-rename');
  if (fileActionRename) {
    fileActionRename.addEventListener('click', renameTreeItemFromMenu);
  }
  const fileActionCopy = document.getElementById('file-action-copy');
  if (fileActionCopy) {
    fileActionCopy.addEventListener('click', copyTreeItemFromMenu);
  }
  const fileActionDelete = document.getElementById('file-action-delete');
  if (fileActionDelete) {
    fileActionDelete.addEventListener('click', deleteTreeItemFromMenu);
  }

  const clearCacheButton = document.getElementById('clear-cache-button');
  if (clearCacheButton) {
    clearCacheButton.addEventListener('click', openClearCacheModal);
  }
  const clearCacheBackdrop = document.getElementById('clear-cache-backdrop');
  if (clearCacheBackdrop) {
    clearCacheBackdrop.addEventListener('click', closeClearCacheModal);
  }
  const clearCacheCancel = document.getElementById('clear-cache-cancel');
  if (clearCacheCancel) {
    clearCacheCancel.addEventListener('click', closeClearCacheModal);
  }
  const clearCacheConfirm = document.getElementById('clear-cache-confirm');
  if (clearCacheConfirm) {
    clearCacheConfirm.addEventListener('click', confirmClearCache);
  }
  const clearCacheFilter = document.getElementById('clear-cache-filter');
  if (clearCacheFilter) {
    clearCacheFilter.addEventListener('input', handleClearCacheFilter);
  }
  document.querySelectorAll('.clear-cache-checkbox').forEach((cb) => {
    cb.addEventListener('change', handleClearCacheCheckbox);
  });

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
    bindCodeEditor(textEditor);
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
      bindCodeEditor(envSourceEditor);
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
    dirty.className = `badge ${state.currentFile?.dirty ? 'warn' : 'ok'} material-state-chip editor-status-chip`;
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
          <button id="logout-button" class="ghost-btn material-outlined-button logout-btn" type="button">退出登录</button>
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
            <div class="topbar-title ${state.view === 'editor' ? 'compact-title' : ''}">
              ${renderTopbarTitle()}
            </div>
            <div class="topbar-meta">
              ${renderTopbarTools()}
            </div>
          </div>
        </header>
        ${state.view === 'browser' ? renderBrowserPage() : state.view === 'status' ? renderStatusPage() : renderEditorPage()}
      </div>
      ${renderFileActionMenu()}
      ${renderClearCacheModal()}
    </div>
  `;
}

function renderBrowserPage() {
  const items = state.treeItems || [];
  const isCacheInfoDir = pathBaseName(state.currentDir) === 'Cache_Info';
  return `
    <div class="browser-page-grid page-scene">
      <section class="panel">
        <div class="panel-body">
          <div class="status-row compact-meta-row">
            <span class="badge info material-state-chip">当前目录 ${escapeHtml(state.currentDir || '/')}</span>
            <span class="badge info material-state-chip">项目文件 ${items.length}</span>
          </div>
          <div class="tree-nav">
            <button class="secondary-btn material-tonal-button" type="button" data-tree-open="" data-type="dir">回到根目录/</button>
            ${state.currentDir ? `<button class="secondary-btn material-outlined-button" type="button" data-tree-open="${escapeAttr(parentPath(state.currentDir))}" data-type="dir">返回上级</button>` : ''}
            ${canCreateEnvInCurrentDir() ? `<button id="create-env-button" class="secondary-btn material-outlined-button" type="button">新建 env</button>` : ''}
            ${canCreateMdInCurrentDir() ? `<button id="create-md-button" class="secondary-btn material-outlined-button" type="button">新增 md</button>` : ''}
            ${isCacheInfoDir ? `<button id="clear-cache-button" class="secondary-btn material-outlined-button danger-btn" type="button">清除缓存</button>` : ''}
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
          <div class="status-toolbar">
            <div class="status-row material-chip-row status-toolbar-inline">
              ${validationBadge()}
              ${healthBadge()}
              <button id="refresh-status-button" class="secondary-btn material-outlined-button status-refresh-btn" type="button">刷新状态</button>
            </div>
          </div>
          <div class="button-row action-cluster">
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
    { label: '配置校验', value: validation.restart_ok ? '通过' : '失败', tone: validation.restart_ok ? 'ok' : 'danger' },
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
          ${renderCodeEditorShell('text-editor', 'text-editor', '', 'text-area auto-resize source-mode no-wrap-editor code-editor-textarea')}
        </div>
      </section>
    </div>
  `;
}

function renderEnvEditor() {
  const entries = getVisibleEnvEntries();
  const allExpanded = entries.length > 0 && entries.every(({ entry, sourceIndex }) => state.envExpanded[envEntryId(entry, sourceIndex)]);
  const hasTailLines = (state.currentFile?.tail_lines || []).some((line) => String(line || '').trim() !== '');
  return `
    <div id="env-editor" class="env-list pane-${state.editorPane}">
      ${state.editorPane === 'edit' ? `<div class="env-toolbar">
        <button id="env-toggle-all" class="secondary-btn material-tonal-button" type="button">${allExpanded ? '全部收起' : '全部展开'}</button>
      </div>` : ''}
      ${state.editorPane === 'edit' ? entries.map(({ entry, sourceIndex }, index) => renderEnvEntry(entry, index, sourceIndex)).join('') : ''}
      ${state.editorPane === 'edit' && hasTailLines ? `<section class="env-card mode-scene">
        <div class="env-body">
          <textarea id="env-tail-lines" name="env-tail-lines" rows="1" class="comment-input auto-resize no-wrap-editor" spellcheck="false" wrap="off">${escapeHtml((state.currentFile.tail_lines || []).join('\n'))}</textarea>
        </div>
      </section>` : ''}
      ${state.editorPane === 'preview' ? `<section class="editor-card mode-scene">
        <div class="editor-body">
          ${renderCodeEditorShell('env-source-editor', 'env-source-editor', state.currentFile.source_content || renderEnvRawPreview(), 'text-area auto-resize env-source-editor source-mode no-wrap-editor code-editor-textarea env-highlight-textarea', 'env')}
        </div>
      </section>` : ''}
    </div>
  `;
}

function renderEnvEntry(entry, index, sourceIndex = index) {
  const entryId = envEntryId(entry, sourceIndex);
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
          <textarea class="env-value-input auto-resize no-wrap-editor" rows="1" name="env-value-${sourceIndex}" data-index="${sourceIndex}" data-field="value" spellcheck="false" wrap="off">${escapeHtml(entry.value || '')}</textarea>
        </div>
      </div>
    </section>
  `;
}

function renderCodeEditorShell(id, name, value, className, highlightLanguage = '') {
  return `
    <div class="code-editor-shell ${highlightLanguage ? `code-editor-shell-${escapeAttr(highlightLanguage)}` : ''}" data-editor-shell="${escapeAttr(id)}">
      <div id="${escapeAttr(id)}-gutter" class="code-editor-gutter">${renderLineNumbers(value)}</div>
      ${highlightLanguage ? `<pre id="${escapeAttr(id)}-highlight" class="code-editor-highlight" aria-hidden="true">${highlightText(value || '', highlightLanguage)}</pre>` : ''}
      <textarea id="${escapeAttr(id)}" name="${escapeAttr(name)}" class="${escapeAttr(className)}" data-highlight-language="${escapeAttr(highlightLanguage)}" spellcheck="false" wrap="off" style="${escapeAttr(editorZoomStyle())}">${escapeHtml(value || '')}</textarea>
    </div>
  `;
}

function renderLineNumbers(content) {
  const total = Math.max(1, String(content || '').split('\n').length);
  return Array.from({ length: total }, (_, index) => `<span aria-hidden="true">${index + 1}</span>`).join('');
}

function getVisibleEnvEntries() {
  return (state.currentFile?.env_entries || [])
    .map((entry, sourceIndex) => ({ entry, sourceIndex }))
    .filter(({ entry }) => isVisibleEnvEntry(entry));
}

function isVisibleEnvEntry(entry) {
  return String(entry?.key || '').trim() !== '';
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

function renderFileActionMenu() {
  if (!state.fileActionMenu?.path) {
    return '';
  }
  return `
    <div id="file-action-backdrop" class="file-action-backdrop is-open"></div>
    <div class="file-action-modal" role="dialog" aria-modal="true" aria-label="文件操作菜单">
      <div class="file-action-title">${escapeHtml(displayFileName(state.fileActionMenu.path))}</div>
      <div class="file-action-buttons">
        <button id="file-action-copy" class="secondary-btn material-tonal-button" type="button">复制</button>
        <button id="file-action-rename" class="secondary-btn material-tonal-button" type="button">重命名</button>
        <button id="file-action-delete" class="secondary-btn material-outlined-button danger-btn" type="button">删除</button>
      </div>
      <button id="file-action-cancel" class="ghost-btn material-outlined-button" type="button">取消</button>
    </div>
  `;
}

function renderClearCacheModal() {
  if (!state.clearCacheModal?.open) {
    return '';
  }
  const providers = state.clearCacheModal.providers || [];
  const selected = state.clearCacheModal.selected || [];
  const filter = state.clearCacheModal.filter || '';
  const filtered = providers.filter((p) => p.toLowerCase().includes(filter.toLowerCase()));
  return `
    <div id="clear-cache-backdrop" class="file-action-backdrop is-open"></div>
    <div class="clear-cache-modal" role="dialog" aria-modal="true" aria-label="清除缓存">
      <div class="clear-cache-title">清除缓存</div>
      <div class="clear-cache-desc">选择要清除缓存的 provider，清除后无法恢复。</div>
      <div class="clear-cache-filter-row">
        <input id="clear-cache-filter" type="text" class="text-input" placeholder="搜索 provider..." value="${escapeAttr(filter)}">
      </div>
      <div id="clear-cache-list" class="clear-cache-list">
        ${filtered.length === 0 ? '<div class="clear-cache-empty">没有匹配的 provider</div>' : filtered.map((p) => `
          <label class="clear-cache-item">
            <input type="checkbox" class="clear-cache-checkbox" value="${escapeAttr(p)}" ${selected.includes(p) ? 'checked' : ''}>
            <span>${escapeHtml(p)}</span>
          </label>
        `).join('')}
      </div>
      <div class="clear-cache-actions">
        <button id="clear-cache-confirm" class="secondary-btn material-outlined-button danger-btn" type="button" ${selected.length === 0 ? 'disabled' : ''}>确认清除</button>
        <button id="clear-cache-cancel" class="ghost-btn material-outlined-button" type="button">取消</button>
      </div>
    </div>
  `;
}

function canCreateEnvInCurrentDir() {
	if (state.currentDir === '') {
		return !(state.treeItems || []).some((item) => !item.is_dir && item.name === '.env');
	}
	return state.currentDir === 'providers';
}

function canCreateMdInCurrentDir() {
	return state.currentDir === 'providers';
}

function renderTopbarTools() {
  if (state.view === 'editor') {
    return `
      <span id="file-dirty-badge" class="badge ${state.currentFile?.dirty ? 'warn' : 'ok'} material-state-chip editor-status-chip">${state.currentFile?.dirty ? '未保存' : '已同步'}</span>
      ${state.lastSaveFeedback ? `<span class="badge ${escapeAttr(state.lastSaveFeedback.tone)} material-state-chip editor-status-chip">${escapeHtml(state.lastSaveFeedback.text)}</span>` : ''}
      <button id="cancel-file-button" class="secondary-btn material-outlined-button topbar-action-btn" type="button" ${state.currentFile ? '' : 'disabled'}>返回</button>
      <button id="save-file-button" class="save-btn topbar-action-btn" type="button" ${state.currentFile ? '' : 'disabled'}>保存</button>
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
      <div class="topbar-title-group editor-title-group">
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

function pathBaseName(path) {
  const value = String(path || '');
  const parts = value.split('/').filter(Boolean);
  return parts[parts.length - 1] || '';
}

async function createEnvFromCurrentDir() {
	let name = '';
	if (state.currentDir !== '') {
		name = window.prompt('输入文件名（不用 .env 后缀）', '');
		if (!name) {
			return;
		}
	}
	try {
		const data = await api('/_admin/api/file', {
			method: 'POST',
			body: {
				dir: state.currentDir,
				name,
			},
		});
		await loadTree(state.currentDir);
		await openFile(data.path);
		setToast('success', state.currentDir === '' ? '.env 已创建' : '新 env 已创建');
	} catch (error) {
		setToast('error', error.message || '新建 env 失败');
	}
}

async function createMdFromCurrentDir() {
	if (!canCreateMdInCurrentDir()) {
		return;
	}
	const name = window.prompt('输入文件名（不用 .md 后缀）', '');
	if (!name) {
		return;
	}
	try {
		const data = await api('/_admin/api/file', {
			method: 'POST',
			body: {
				dir: state.currentDir,
				name,
				kind: 'md',
			},
		});
		await loadTree(state.currentDir);
		await openFile(data.path);
		setToast('success', '新 md 文件已创建');
	} catch (error) {
		setToast('error', error.message || '新建 md 文件失败');
	}
}

function openTreeItemActionMenu(path) {
  state.fileActionMenu = { path };
  render();
}

function closeTreeItemActionMenu() {
  state.fileActionMenu = null;
  render();
}

async function openClearCacheModal() {
  const items = state.treeItems || [];
  const providers = [...new Set(items
    .filter((item) => !item.is_dir && (item.name.endsWith('.json') || item.name.endsWith('.txt')))
    .map((item) => item.name.replace(/\.(json|txt)$/, ''))
    .filter((name) => name && name !== '全提供商总计' && name !== '已启用提供商总计'))];
  state.clearCacheModal = { open: true, providers, selected: [], filter: '' };
  render();
}

function closeClearCacheModal() {
  state.clearCacheModal = null;
  render();
}

function handleClearCacheFilter(event) {
  if (!state.clearCacheModal) {
    return;
  }
  state.clearCacheModal.filter = event.target.value;
  render();
}

function handleClearCacheCheckbox(event) {
  if (!state.clearCacheModal) {
    return;
  }
  const value = event.target.value;
  const checked = event.target.checked;
  if (checked) {
    if (!state.clearCacheModal.selected.includes(value)) {
      state.clearCacheModal.selected = [...state.clearCacheModal.selected, value];
    }
  } else {
    state.clearCacheModal.selected = state.clearCacheModal.selected.filter((v) => v !== value);
  }
  const confirmBtn = document.getElementById('clear-cache-confirm');
  if (confirmBtn) {
    confirmBtn.disabled = state.clearCacheModal.selected.length === 0;
  }
}

async function confirmClearCache() {
  const selected = state.clearCacheModal?.selected || [];
  if (selected.length === 0) {
    return;
  }
  if (!window.confirm(`确认清除 ${selected.length} 个 provider 的缓存记录？`)) {
    return;
  }
  closeClearCacheModal();
  const results = [];
  for (const provider of selected) {
    try {
      await api('/_admin/api/cacheinfo/providers/clear', {
        method: 'POST',
        body: { provider_id: provider },
      });
      results.push({ provider, ok: true });
    } catch (error) {
      results.push({ provider, ok: false, error: error.message });
    }
  }
  const failed = results.filter((r) => !r.ok);
  if (failed.length === 0) {
    setToast('success', `已清除 ${results.length} 个 provider 的缓存`);
  } else {
    setToast('error', `清除完成，但 ${failed.length} 个 provider 失败：${failed.map((f) => f.provider).join(', ')}`);
  }
  await loadTree(state.currentDir);
  render();
}

async function renameTreeItemFromMenu() {
  const path = state.fileActionMenu?.path;
  if (!path) {
    return;
  }
  const currentName = displayFileName(path);
  const currentStem = currentName.endsWith('.env') ? currentName.slice(0, -4) : currentName;
  const input = window.prompt('输入新文件名', currentStem);
  state.fileActionMenu = null;
  if (!input) {
    render();
    return;
  }
  const newName = normalizeRenameInput(currentName, input.trim());
  try {
    const data = await api('/_admin/api/file', {
      method: 'PATCH',
      body: {
        path,
        new_name: newName,
      },
    });
    await loadTree(state.currentDir);
    if (state.currentFile?.path === path) {
      await openFile(data.path);
    } else {
      render();
    }
    setToast('success', '文件已重命名');
  } catch (error) {
    setToast('error', error.message || '重命名失败');
  }
}

async function copyTreeItemFromMenu() {
  const path = state.fileActionMenu?.path;
  if (!path) {
    return;
  }
  const currentName = displayFileName(path);
  const input = window.prompt('输入复制后的文件名', defaultCopyInput(currentName));
  state.fileActionMenu = null;
  if (!input) {
    render();
    return;
  }
  const newName = normalizeCopyInput(currentName, input.trim());
  try {
    const data = await api('/_admin/api/file', {
      method: 'POST',
      body: {
        path,
        name: newName,
        kind: 'copy',
      },
    });
    await loadTree(state.currentDir);
    await openFile(data.path);
    setToast('success', '文件已复制');
  } catch (error) {
    setToast('error', error.message || '复制失败');
  }
}

async function deleteTreeItemFromMenu() {
  const path = state.fileActionMenu?.path;
  state.fileActionMenu = null;
  if (!path) {
    return;
  }
  if (!window.confirm('确定删除这个文件？')) {
    render();
    return;
  }
  try {
    await api('/_admin/api/file', {
      method: 'DELETE',
      body: { path },
    });
    await loadTree(state.currentDir);
    if (state.currentFile?.path === path) {
      closeCurrentFile();
      return;
    }
    render();
    setToast('success', '文件已删除');
  } catch (error) {
    setToast('error', error.message || '删除失败');
  }
}

function defaultCopyInput(currentName) {
  if (currentName === '.env') {
    return '.env-副本';
  }
  if (currentName.endsWith('.env')) {
    return `${currentName.slice(0, -4)}-副本`;
  }
  const lastDot = currentName.lastIndexOf('.');
  if (lastDot > 0) {
    return `${currentName.slice(0, lastDot)}-副本`;
  }
  return `${currentName}-副本`;
}

function normalizeCopyInput(currentName, input) {
  if (input.includes('.')) {
    return input;
  }
  if (currentName === '.env') {
    return input;
  }
  if (currentName.endsWith('.env')) {
    return `${input}.env`;
  }
  const lastDot = currentName.lastIndexOf('.');
  if (lastDot > 0) {
    return `${input}${currentName.slice(lastDot)}`;
  }
  return input;
}

function normalizeRenameInput(currentName, input) {
  if (input.includes('.')) {
    return input;
  }
  if (currentName === '.env') {
    return `${input}.env`;
  }
  if (currentName.endsWith('.env.example')) {
    return `${input}.env.example`;
  }
  if (currentName.endsWith('.env')) {
    return `${input}.env`;
  }
  return input;
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
  return entries
    .filter((entry) => isVisibleEnvEntry(entry))
    .map((entry) => ({
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
  if (!textarea.dataset.baseMinHeight) {
    textarea.dataset.baseMinHeight = String(parseFloat(window.getComputedStyle(textarea).minHeight) || 0);
  }
  const minHeight = Number(textarea.dataset.baseMinHeight || 0);
  textarea.style.height = '0px';
  textarea.style.height = `${Math.max(textarea.scrollHeight, minHeight)}px`;
  syncCodeEditor(textarea);
}

function loadEditorZoom() {
  try {
    const saved = Number(window.localStorage.getItem('admin-editor-zoom') || '14');
    return Number.isFinite(saved) ? clampEditorZoom(saved) : 14;
  } catch {
    return 14;
  }
}

function persistEditorZoom() {
  try {
    window.localStorage.setItem('admin-editor-zoom', String(state.editorZoom));
  } catch {
  }
}

function clampEditorZoom(value) {
  return Math.min(26, Math.max(8, Math.round(value * 2) / 2));
}

function editorZoomStyle() {
  return `font-size:${state.editorZoom}px; line-height:1.5;`;
}

function setEditorZoom(value) {
  const next = clampEditorZoom(value);
  if (next === state.editorZoom) {
    return;
  }
  state.editorZoom = next;
  persistEditorZoom();
  document.querySelectorAll('.code-editor-textarea').forEach((textarea) => {
    textarea.style.fontSize = `${state.editorZoom}px`;
    textarea.style.lineHeight = '1.5';
    autoSizeTextarea(textarea);
    syncCodeEditor(textarea);
  });
}

function bindCodeEditor(textarea) {
  if (!(textarea instanceof HTMLTextAreaElement) || textarea.dataset.editorBound === 'true') {
    return;
  }
  textarea.dataset.editorBound = 'true';
  textarea.style.fontSize = `${state.editorZoom}px`;
  textarea.style.lineHeight = '1.5';
  textarea.addEventListener('input', () => syncCodeEditor(textarea));
  textarea.addEventListener('scroll', () => syncCodeEditor(textarea));
  textarea.addEventListener('wheel', (event) => {
    if (!event.ctrlKey) {
      return;
    }
    event.preventDefault();
    setEditorZoom(state.editorZoom + (event.deltaY < 0 ? 1 : -1));
  }, { passive: false });

  let pinchDistance = 0;
  textarea.addEventListener('touchstart', (event) => {
    if (event.touches.length === 2) {
      pinchDistance = touchDistance(event.touches[0], event.touches[1]);
    }
  }, { passive: true });
  textarea.addEventListener('touchmove', (event) => {
    if (event.touches.length !== 2 || pinchDistance <= 0) {
      return;
    }
    event.preventDefault();
    const nextDistance = touchDistance(event.touches[0], event.touches[1]);
    const delta = nextDistance - pinchDistance;
    if (Math.abs(delta) >= 16) {
      setEditorZoom(state.editorZoom + (delta > 0 ? 0.5 : -0.5));
      pinchDistance = nextDistance;
    }
  }, { passive: false });
  textarea.addEventListener('touchend', () => {
    pinchDistance = 0;
  });
  syncCodeEditor(textarea);
}

function syncCodeEditor(textarea) {
  const shell = textarea.closest('[data-editor-shell]');
  const gutter = document.getElementById(`${textarea.id}-gutter`);
  if (!gutter) {
    return;
  }
  const highlight = document.getElementById(`${textarea.id}-highlight`);
  const totalLines = Math.max(1, String(textarea.value || '').split('\n').length);
  const lineDigits = Math.max(String(totalLines).length, 2);
  gutter.style.fontSize = `${state.editorZoom}px`;
  gutter.style.lineHeight = '1.5';
  const gutterWidth = `${Math.max(lineDigits + 1.5, 3.5)}ch`;
  if (shell) {
    shell.style.gridTemplateColumns = `${gutterWidth} minmax(0, 1fr)`;
  }
  gutter.style.width = gutterWidth;
  gutter.innerHTML = renderLineNumbers(textarea.value || '');
  gutter.scrollTop = textarea.scrollTop;
  if (highlight) {
    highlight.style.fontSize = `${state.editorZoom}px`;
    highlight.style.lineHeight = '1.5';
    highlight.style.left = gutterWidth;
    highlight.innerHTML = highlightText(textarea.value || '', textarea.dataset.highlightLanguage || '');
    highlight.scrollTop = textarea.scrollTop;
    highlight.scrollLeft = textarea.scrollLeft;
  }
}

function touchDistance(a, b) {
  return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY);
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
