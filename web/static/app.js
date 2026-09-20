/**
 * TalkIntent Embedded Web Dashboard
 * Vanilla ES6 JavaScript application.
 */

// Application state
const state = {
  currentUser: null,
  members: [],
  cancelAskPoll: false,
  activeQueryId: null,
};

const TERMINAL_STATUSES = ['completed', 'refused', 'error', 'timeout', 'expired'];

// ---------------------------------------------------------------------------
// Authentication & Token Management (F11)
// ---------------------------------------------------------------------------

function getToken() {
  // 1. Check URL hash fragment (#token=...), which browsers never send to server
  const hash = window.location.hash;
  if (hash && hash.includes('token=')) {
    const match = hash.match(/token=([^&]+)/);
    if (match && match[1]) {
      const t = decodeURIComponent(match[1]);
      localStorage.setItem('talkintent_token', t);
      history.replaceState(null, "", window.location.pathname + window.location.search);
      return t;
    }
  }
  // 2. Retrieve from localStorage
  return localStorage.getItem('talkintent_token') || "";
}

function authHeaders(extra = {}) {
  const token = getToken();
  const headers = { 'Content-Type': 'application/json', ...extra };
  if (token) {
    headers['Authorization'] = 'Bearer ' + token;
  }
  return headers;
}

async function checkAuth() {
  const token = getToken();
  const badge = document.getElementById('user-badge');
  const infoText = document.getElementById('user-info-text');
  const btnLogin = document.getElementById('btn-open-login');
  const btnLogout = document.getElementById('btn-logout');

  if (!token) {
    if (badge) {
      badge.className = 'badge badge-offline';
      badge.innerText = '未登录';
    }
    if (infoText) infoText.style.display = 'none';
    if (btnLogin) btnLogin.style.display = 'inline-flex';
    if (btnLogout) btnLogout.style.display = 'none';
    state.currentUser = null;
    return;
  }

  try {
    const res = await fetch('/api/v1/members/me', { headers: authHeaders() });
    if (res.ok) {
      const me = await res.json();
      state.currentUser = me;
      if (badge) {
        badge.className = 'badge badge-online';
        badge.innerText = '已认证';
      }
      if (infoText) {
        infoText.style.display = 'inline';
        infoText.innerText = me.name + (me.id ? ' (' + me.id + ')' : '');
      }
      if (btnLogin) btnLogin.style.display = 'none';
      if (btnLogout) btnLogout.style.display = 'inline-flex';
      closeLoginModal();
    } else if (res.status === 401) {
      if (badge) {
        badge.className = 'badge badge-offline';
        badge.innerText = 'Token 无效';
      }
      if (infoText) infoText.style.display = 'none';
      if (btnLogin) btnLogin.style.display = 'inline-flex';
      if (btnLogout) btnLogout.style.display = 'none';
      state.currentUser = null;
    }
  } catch (err) {
    console.error('Auth verification failed', err);
  }
}

function openLoginModal() {
  const modal = document.getElementById('login-modal');
  const input = document.getElementById('token-input');
  const msg = document.getElementById('login-msg');
  if (msg) msg.innerText = '';
  if (input) {
    input.value = getToken();
    setTimeout(() => input.focus(), 50);
  }
  if (modal) modal.style.display = 'flex';
}

function closeLoginModal() {
  const modal = document.getElementById('login-modal');
  if (modal) modal.style.display = 'none';
}

async function login() {
  const input = document.getElementById('token-input');
  const msg = document.getElementById('login-msg');
  const token = input ? input.value.trim() : '';

  if (!token) {
    if (msg) msg.innerText = '请输入有效的 Member Token';
    return;
  }

  localStorage.setItem('talkintent_token', token);
  closeLoginModal();
  showToast('正在验证 Token...', 'info');

  await checkAuth();

  if (state.currentUser) {
    showToast('登录成功，欢迎 ' + state.currentUser.name, 'success');
  } else {
    showToast('Token 认证失败，请检查 Token 是否有效', 'danger');
  }

  // Refresh active tab
  const activeTab = document.querySelector('.nav button.active');
  if (activeTab && activeTab.id) {
    const tabName = activeTab.id.replace('nav-btn-', '');
    showTab(tabName);
  }
}

function logout() {
  localStorage.removeItem('talkintent_token');
  state.currentUser = null;
  checkAuth();
  showToast('已安全退出登录', 'info');

  // Refresh current view
  loadMembers();
}

// ---------------------------------------------------------------------------
// Navigation Tabs
// ---------------------------------------------------------------------------

function showTab(name) {
  document.querySelectorAll('.tab-content').forEach(el => el.style.display = 'none');
  document.querySelectorAll('.nav button').forEach(el => el.classList.remove('active'));

  const target = document.getElementById('tab-' + name);
  if (target) target.style.display = 'block';

  const btn = document.getElementById('nav-btn-' + name);
  if (btn) btn.classList.add('active');

  if (name === 'members') loadMembers();
  if (name === 'inbound') loadInboundAudit();
  if (name === 'outbound') loadOutboundAudit();
  if (name === 'ask') initAskTab();
  if (name === 'detail') initDetailTab();
  if (name === 'feishu') loadFeishuBinding();
  if (name === 'admin') initAdminTab();
}

// ---------------------------------------------------------------------------
// View 1: Members & Online Status
// ---------------------------------------------------------------------------

async function loadMembers() {
  const tbody = document.getElementById('members-table-body');
  const countEl = document.getElementById('members-count');
  const datalist = document.getElementById('members-datalist');

  try {
    const res = await fetch('/api/v1/members', { headers: authHeaders() });
    if (!res.ok) {
      if (res.status === 401) {
        tbody.innerHTML = '<tr><td colspan="8" style="text-align:center; color: var(--danger);">需要先登录以查看团队成员列表</td></tr>';
      } else {
        tbody.innerHTML = `<tr><td colspan="8" style="text-align:center; color: var(--danger);">加载失败: HTTP ${res.status}</td></tr>`;
      }
      return;
    }

    const data = await res.json();
    const members = data.members || [];
    state.members = members;

    if (countEl) countEl.innerText = `${members.length} 成员`;

    // Populate autocomplete datalist
    if (datalist) {
      datalist.innerHTML = members.map(m => {
        const aliases = (m.aliases || []).join(', ');
        return `<option value="${escapeHtml(m.name)}">${aliases ? '别名: ' + escapeHtml(aliases) : ''}</option>`;
      }).join('');
    }

    if (members.length === 0) {
      tbody.innerHTML = '<tr><td colspan="8" style="text-align:center; color: var(--text-muted);">暂无注册成员</td></tr>';
      return;
    }

    tbody.innerHTML = members.map(m => {
      const isMe = state.currentUser && state.currentUser.id === m.id;
      const lastSeen = m.last_seen_at ? formatTimestamp(m.last_seen_at) : '-';
      const workspaces = (m.workspaces || []).map(w => `<span class="tool-tag">${escapeHtml(w)}</span>`).join('') || '-';
      const aliases = (m.aliases || []).join(', ') || '-';

      return `
        <tr>
          <td>
            <strong>${escapeHtml(m.name)}</strong>
            ${isMe ? ' <span class="badge badge-online" style="font-size:10px;">我</span>' : ''}
          </td>
          <td><span style="color: var(--text-muted); font-size:13px;">${escapeHtml(aliases)}</span></td>
          <td>
            <span class="badge ${m.online ? 'badge-online' : 'badge-offline'}">
              ${m.online ? '在线' : '离线'}
            </span>
          </td>
          <td style="font-size: 13px; color: var(--text-muted);">${lastSeen}</td>
          <td><code>${escapeHtml(m.machine_name || '-')}</code></td>
          <td>${workspaces}</td>
          <td>
            <span class="badge ${m.has_feishu_bot ? 'badge-success' : 'badge-offline'}">
              ${m.has_feishu_bot ? '已绑定' : '未绑定'}
            </span>
          </td>
          <td>
            <button class="btn btn-sm btn-primary" onclick="askMember('${escapeHtml(m.name)}')">向TA提问</button>
          </td>
        </tr>
      `;
    }).join('');
  } catch (err) {
    console.error(err);
    if (tbody) tbody.innerHTML = `<tr><td colspan="8" style="text-align:center; color: var(--danger);">请求异常: ${escapeHtml(err.message)}</td></tr>`;
  }
}

function askMember(name) {
  showTab('ask');
  const targetInput = document.getElementById('ask-target');
  const queryInput = document.getElementById('ask-query');
  if (targetInput) targetInput.value = name;
  if (queryInput) setTimeout(() => queryInput.focus(), 50);
}

// ---------------------------------------------------------------------------
// View 2: Inbound Audit (谁查了我)
// ---------------------------------------------------------------------------

async function loadInboundAudit() {
  const tbody = document.getElementById('inbound-table-body');
  const countEl = document.getElementById('inbound-count');

  try {
    const res = await fetch('/api/v1/audit/inbound?limit=50&offset=0', { headers: authHeaders() });
    if (!res.ok) {
      if (res.status === 401) {
        tbody.innerHTML = '<tr><td colspan="8" style="text-align:center; color: var(--danger);">需要登录后查看 Inbound 审计记录</td></tr>';
      } else {
        tbody.innerHTML = `<tr><td colspan="8" style="text-align:center; color: var(--danger);">加载失败: HTTP ${res.status}</td></tr>`;
      }
      return;
    }

    const data = await res.json();
    const entries = data.entries || [];
    if (countEl) countEl.innerText = `${entries.length} 记录`;

    if (entries.length === 0) {
      tbody.innerHTML = '<tr><td colspan="8" style="text-align:center; color: var(--text-muted);">暂无 Inbound 访问审计记录</td></tr>';
      return;
    }

    tbody.innerHTML = entries.map((e, idx) => {
      const timeStr = formatTimestamp(e.timestamp);
      const tools = (e.tools_used || []).map(t => `<span class="tool-tag">${escapeHtml(t)}</span>`).join('') || '-';
      const durationStr = e.duration_ms ? `${e.duration_ms} ms` : '-';
      const answerText = e.answer || '(暂无探针回答或执行中)';

      return `
        <tr>
          <td style="font-size: 13px; color: var(--text-muted);">${timeStr}</td>
          <td>
            <strong>${escapeHtml(e.asker_name || e.asker_id || '-')}</strong>
            <span style="font-size: 11px; color: var(--text-subtle);">(${escapeHtml(e.asker_type || 'member')})</span>
          </td>
          <td style="max-width: 240px; word-break: break-word;">${escapeHtml(e.query)}</td>
          <td>
            <button class="btn-sm" onclick="toggleAnswer('inbound-ans-${idx}')">查看回答</button>
            <div id="inbound-ans-${idx}" class="answer-box" style="display:none;">
              <div class="answer-header">
                <span style="font-size: 11px; color: var(--text-muted);">合成回答</span>
                <button class="btn-sm" onclick="copyText(this.parentElement.nextElementSibling.innerText, '')">复制</button>
              </div>
              <div>${escapeHtml(answerText)}</div>
            </div>
          </td>
          <td>${statusBadge(e.status)}</td>
          <td>${tools}</td>
          <td>${durationStr}</td>
          <td>
            <button class="btn-sm" onclick="viewQueryDetail('${escapeHtml(e.query_id)}')">时间线详情</button>
          </td>
        </tr>
      `;
    }).join('');
  } catch (err) {
    console.error(err);
    if (tbody) tbody.innerHTML = `<tr><td colspan="8" style="text-align:center; color: var(--danger);">请求异常: ${escapeHtml(err.message)}</td></tr>`;
  }
}

// ---------------------------------------------------------------------------
// View 3: Outbound Audit (我的提问)
// ---------------------------------------------------------------------------

async function loadOutboundAudit() {
  const tbody = document.getElementById('outbound-table-body');
  const countEl = document.getElementById('outbound-count');

  try {
    const res = await fetch('/api/v1/audit/outbound?limit=50&offset=0', { headers: authHeaders() });
    if (!res.ok) {
      if (res.status === 401) {
        tbody.innerHTML = '<tr><td colspan="6" style="text-align:center; color: var(--danger);">需要登录后查看 Outbound 审计记录</td></tr>';
      } else {
        tbody.innerHTML = `<tr><td colspan="6" style="text-align:center; color: var(--danger);">加载失败: HTTP ${res.status}</td></tr>`;
      }
      return;
    }

    const data = await res.json();
    const entries = data.entries || [];
    if (countEl) countEl.innerText = `${entries.length} 记录`;

    if (entries.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" style="text-align:center; color: var(--text-muted);">暂无 Outbound 提问记录</td></tr>';
      return;
    }

    tbody.innerHTML = entries.map((e, idx) => {
      const timeStr = formatTimestamp(e.timestamp);
      const answerText = e.answer || '(暂无探针回答或执行中)';

      return `
        <tr>
          <td style="font-size: 13px; color: var(--text-muted);">${timeStr}</td>
          <td><strong>${escapeHtml(e.target_member_name || e.target_member_id || '-')}</strong></td>
          <td style="max-width: 260px; word-break: break-word;">${escapeHtml(e.query)}</td>
          <td>
            <button class="btn-sm" onclick="toggleAnswer('outbound-ans-${idx}')">查看回答</button>
            <div id="outbound-ans-${idx}" class="answer-box" style="display:none;">
              <div class="answer-header">
                <span style="font-size: 11px; color: var(--text-muted);">合成回答</span>
                <button class="btn-sm" onclick="copyText(this.parentElement.nextElementSibling.innerText, '')">复制</button>
              </div>
              <div>${escapeHtml(answerText)}</div>
            </div>
          </td>
          <td>${statusBadge(e.status)}</td>
          <td>
            <button class="btn-sm" onclick="viewQueryDetail('${escapeHtml(e.query_id)}')">时间线详情</button>
          </td>
        </tr>
      `;
    }).join('');
  } catch (err) {
    console.error(err);
    if (tbody) tbody.innerHTML = `<tr><td colspan="6" style="text-align:center; color: var(--danger);">请求异常: ${escapeHtml(err.message)}</td></tr>`;
  }
}

// ---------------------------------------------------------------------------
// View 4: Ask Form View (Submit + Long-Poll)
// ---------------------------------------------------------------------------

function initAskTab() {
  if (state.members.length === 0) {
    loadMembers();
  }
}

async function submitAskQuery() {
  const targetInput = document.getElementById('ask-target');
  const queryInput = document.getElementById('ask-query');
  const wsInput = document.getElementById('ask-workspace');
  const timeoutInput = document.getElementById('ask-timeout');

  const target = targetInput ? targetInput.value.trim() : '';
  const query = queryInput ? queryInput.value.trim() : '';
  const workspace = wsInput ? wsInput.value.trim() : '';
  const timeout = timeoutInput ? parseInt(timeoutInput.value, 10) || 60 : 60;

  if (!target) {
    showToast('请输入目标成员姓名或别名', 'danger');
    if (targetInput) targetInput.focus();
    return;
  }
  if (!query) {
    showToast('请输入提问内容', 'danger');
    if (queryInput) queryInput.focus();
    return;
  }

  const submitBtn = document.getElementById('ask-submit-btn');
  const cancelBtn = document.getElementById('ask-cancel-btn');
  const progressBox = document.getElementById('ask-progress');
  const progressText = document.getElementById('ask-progress-text');
  const resultCard = document.getElementById('ask-result');
  const candidatesBox = document.getElementById('ask-candidates');

  if (submitBtn) submitBtn.disabled = true;
  if (cancelBtn) cancelBtn.style.display = 'inline-flex';
  if (progressBox) progressBox.style.display = 'flex';
  if (progressText) progressText.innerText = '正在向 Hub 提交提问并路由目标...';
  if (resultCard) resultCard.style.display = 'none';
  if (candidatesBox) candidatesBox.style.display = 'none';

  state.cancelAskPoll = false;

  try {
    const payload = {
      target: target,
      query: query,
      target_workspace: workspace || undefined,
      timeout_seconds: timeout,
      wait: true,
    };

    const res = await fetch('/api/v1/queries', {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify(payload),
    });

    if (res.status === 400) {
      const errData = await res.json();
      if (errData.error && errData.error.code === 'AMBIGUOUS_TARGET') {
        if (progressBox) progressBox.style.display = 'none';
        if (submitBtn) submitBtn.disabled = false;
        if (cancelBtn) cancelBtn.style.display = 'none';

        renderCandidates(errData.error.details ? errData.error.details.candidates : []);
        return;
      }
      throw new Error((errData.error && errData.error.message) || `HTTP ${res.status}`);
    }

    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }

    const data = await res.json();
    state.activeQueryId = data.query_id;

    // Check if query already finished in the immediate response (e.g. 200 OK with completed/refused status)
    if (TERMINAL_STATUSES.includes(data.status)) {
      if (progressBox) progressBox.style.display = 'none';
      if (submitBtn) submitBtn.disabled = false;
      if (cancelBtn) cancelBtn.style.display = 'none';

      renderAskResult(data);
      renderQueryDetail(data);
      showToast(`提问完成: ${statusLabel(data.status)}`, data.status === 'completed' ? 'success' : 'danger');
      return;
    }

    if (progressText) {
      const statusText = data.status === 'queued' ? '目标离线，已进入缓冲队列' : '已派发至现场探针';
      progressText.innerText = `${statusText} (Query ID: ${data.query_id})，正在等待现场探针执行...`;
    }

    // Start long-polling until terminal state
    await pollQueryUntilTerminal(data.query_id);
  } catch (err) {
    console.error('Submit query failed', err);
    showToast('提交提问失败: ' + err.message, 'danger');
    if (progressBox) progressBox.style.display = 'none';
    if (submitBtn) submitBtn.disabled = false;
    if (cancelBtn) cancelBtn.style.display = 'none';
  }
}

function renderCandidates(candidates = []) {
  const candidatesBox = document.getElementById('ask-candidates');
  const listEl = document.getElementById('ask-candidates-list');
  if (!candidatesBox || !listEl) return;

  listEl.innerHTML = candidates.map(c => `
    <button type="button" class="candidate-btn" onclick="selectCandidate('${escapeHtml(c.name)}')">
      ${escapeHtml(c.name)} <span style="font-size: 11px; opacity:0.8;">(匹配别名: ${escapeHtml(c.matched_alias || '-')})</span>
    </button>
  `).join('');

  candidatesBox.style.display = 'block';
}

function selectCandidate(name) {
  const targetInput = document.getElementById('ask-target');
  const candidatesBox = document.getElementById('ask-candidates');
  if (targetInput) targetInput.value = name;
  if (candidatesBox) candidatesBox.style.display = 'none';
  submitAskQuery();
}

function cancelAskPoll() {
  state.cancelAskPoll = true;
  const progressBox = document.getElementById('ask-progress');
  const submitBtn = document.getElementById('ask-submit-btn');
  const cancelBtn = document.getElementById('ask-cancel-btn');

  if (progressBox) progressBox.style.display = 'none';
  if (submitBtn) submitBtn.disabled = false;
  if (cancelBtn) cancelBtn.style.display = 'none';
  showToast('已取消等待感知结果', 'info');
}

async function pollQueryUntilTerminal(queryId) {
  const submitBtn = document.getElementById('ask-submit-btn');
  const cancelBtn = document.getElementById('ask-cancel-btn');
  const progressBox = document.getElementById('ask-progress');
  const progressText = document.getElementById('ask-progress-text');
  const resultCard = document.getElementById('ask-result');

  const terminalStatuses = ['completed', 'refused', 'error', 'timeout', 'expired'];

  while (!state.cancelAskPoll) {
    try {
      const res = await fetch(`/api/v1/queries/${encodeURIComponent(queryId)}?wait=10s`, {
        headers: authHeaders(),
      });

      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`);
      }

      const q = await res.json();

      if (progressText) {
        progressText.innerText = `当前状态: ${statusLabel(q.status)} (耗时: ${q.duration_ms || 0}ms)...`;
      }

      if (terminalStatuses.includes(q.status)) {
        // Query finished!
        if (progressBox) progressBox.style.display = 'none';
        if (submitBtn) submitBtn.disabled = false;
        if (cancelBtn) cancelBtn.style.display = 'none';

        renderAskResult(q);
        showToast(`提问完成: ${statusLabel(q.status)}`, q.status === 'completed' ? 'success' : 'danger');
        return;
      }

      // If not terminal, pause briefly before next poll cycle
      await new Promise(r => setTimeout(r, 1000));
    } catch (err) {
      console.error('Polling query failed', err);
      if (state.cancelAskPoll) return;
      await new Promise(r => setTimeout(r, 2000));
    }
  }
}

function renderAskResult(q) {
  const resultCard = document.getElementById('ask-result');
  const badge = document.getElementById('ask-status-badge');
  const duration = document.getElementById('ask-duration');
  const tokens = document.getElementById('ask-tokens');
  const tools = document.getElementById('ask-tools');
  const answer = document.getElementById('ask-answer');
  const copyBtn = document.getElementById('ask-copy-btn');
  const detailBtn = document.getElementById('ask-detail-btn');

  if (!resultCard) return;

  if (badge) {
    badge.className = `badge badge-${q.status}`;
    badge.innerText = statusLabel(q.status);
  }

  if (duration) {
    duration.innerText = q.duration_ms ? `耗时: ${q.duration_ms} ms` : '';
  }

  if (tokens) {
    if (q.token_usage && q.token_usage.total_tokens) {
      tokens.innerText = `Token: ${q.token_usage.total_tokens} (Prompt: ${q.token_usage.prompt_tokens}, Completion: ${q.token_usage.completion_tokens})`;
    } else {
      tokens.innerText = '';
    }
  }

  if (tools) {
    const toolTags = (q.tools_used || []).map(t => `<span class="tool-tag">${escapeHtml(t)}</span>`).join('');
    tools.innerHTML = toolTags || '<span style="font-size:12px; color: var(--text-subtle);">无</span>';
  }

  const text = q.answer || q.error_message || '(现场探针未返回回答)';
  if (answer) {
    answer.innerText = text;
  }

  if (copyBtn) {
    copyBtn.onclick = () => copyText(text, 'ask-copy-btn');
  }

  if (detailBtn) {
    detailBtn.onclick = () => viewQueryDetail(q.query_id);
  }

  resultCard.style.display = 'block';
}

// ---------------------------------------------------------------------------
// View 5: Query Detail Timeline View
// ---------------------------------------------------------------------------

function initDetailTab() {
  const idInput = document.getElementById('detail-query-id');
  if (idInput && idInput.value.trim()) {
    fetchQueryDetail();
  }
}

function viewQueryDetail(id) {
  showTab('detail');
  const idInput = document.getElementById('detail-query-id');
  if (idInput) idInput.value = id;
  fetchQueryDetail(id);
}

async function fetchQueryDetail(targetId) {
  const idInput = document.getElementById('detail-query-id');
  const qid = (targetId || (idInput ? idInput.value.trim() : '')).trim();

  if (!qid) {
    showToast('请输入 Query ID', 'danger');
    if (idInput) idInput.focus();
    return;
  }

  try {
    const res = await fetch(`/api/v1/queries/${encodeURIComponent(qid)}`, {
      headers: authHeaders(),
    });

    if (!res.ok) {
      if (res.status === 403) {
        showToast('您没有权限查看该 Query 的详情 (只有提问人、被问人和管理员有权查看)', 'danger');
      } else if (res.status === 404) {
        showToast('未找到该 Query ID 对应的记录', 'danger');
      } else {
        showToast(`查询失败: HTTP ${res.status}`, 'danger');
      }
      return;
    }

    const q = await res.json();
    renderQueryDetail(q);
  } catch (err) {
    console.error('Fetch query detail failed', err);
    showToast('查询请求异常: ' + err.message, 'danger');
  }
}

function renderQueryDetail(q) {
  const container = document.getElementById('detail-container');
  if (!container) return;

  const statusEl = document.getElementById('detail-status');
  if (statusEl) {
    statusEl.className = `badge badge-${q.status}`;
    statusEl.innerText = statusLabel(q.status);
  }

  const durationEl = document.getElementById('detail-duration');
  if (durationEl) durationEl.innerText = `${q.duration_ms || 0} ms`;

  const originEl = document.getElementById('detail-origin');
  if (originEl) originEl.innerText = q.origin || 'rest';

  const wsEl = document.getElementById('detail-workspace');
  if (wsEl) wsEl.innerText = q.target_workspace || '自动/全部';

  const askerEl = document.getElementById('detail-asker');
  if (askerEl) askerEl.innerText = `${q.asker_name || '-'} (${q.asker_id || '-'})`;

  const targetEl = document.getElementById('detail-target');
  if (targetEl) targetEl.innerText = `${q.target_member_name || '-'} (${q.target_member_id || '-'})`;

  const queryTextEl = document.getElementById('detail-query-text');
  if (queryTextEl) queryTextEl.innerText = `提问内容: ${q.query || '-'}`;

  const tokensEl = document.getElementById('detail-tokens');
  if (tokensEl) {
    if (q.token_usage && q.token_usage.total_tokens) {
      tokensEl.innerText = `Token 消耗: ${q.token_usage.total_tokens} (Prompt: ${q.token_usage.prompt_tokens}, Completion: ${q.token_usage.completion_tokens})`;
    } else {
      tokensEl.innerText = '';
    }
  }

  const toolsEl = document.getElementById('detail-tools');
  if (toolsEl) {
    const toolTags = (q.tools_used || []).map(t => `<span class="tool-tag">${escapeHtml(t)}</span>`).join('');
    toolsEl.innerHTML = toolTags || '<span style="font-size:12px; color: var(--text-subtle);">无探针工具调用</span>';
  }

  const errorEl = document.getElementById('detail-error');
  if (errorEl) {
    if (q.error_message) {
      errorEl.style.display = 'block';
      errorEl.innerText = `错误信息: ${q.error_message}`;
    } else {
      errorEl.style.display = 'none';
    }
  }

  const answerEl = document.getElementById('detail-answer');
  const copyBtn = document.getElementById('detail-copy-btn');
  const answerText = q.answer || (q.error_message ? `(错误: ${q.error_message})` : '(暂无探针回答)');
  if (answerEl) answerEl.innerText = answerText;
  if (copyBtn) copyBtn.onclick = () => copyText(answerText, 'detail-copy-btn');

  // Render Visual Timeline
  renderDetailTimeline(q);

  container.style.display = 'block';
}

function renderDetailTimeline(q) {
  const timelineEl = document.getElementById('detail-timeline');
  if (!timelineEl) return;

  const isTerminal = ['completed', 'refused', 'error', 'timeout', 'expired'].includes(q.status);
  const isFailed = ['refused', 'error', 'timeout', 'expired'].includes(q.status);

  const items = [
    {
      title: '1. Query 创建与鉴权',
      time: q.created_at ? formatTimestamp(q.created_at) : '-',
      desc: `提问人 [${q.asker_name || q.asker_id}] 发起提问，目标成员 [${q.target_member_name || q.target_member_id}]`,
      markerClass: 'completed',
    },
    {
      title: q.status === 'queued' ? '2. 目标离线，排队等待 (Queued)' : '2. WebSocket 派发至目标现场 (Dispatched)',
      time: q.created_at ? formatTimestamp(q.created_at) : '-',
      desc: q.status === 'queued'
        ? `目标探针当前未连线，Query 已安全进入离线缓冲队列 (当前位次: ${q.queue_position || 1}，TTL 过期时刻: ${q.ttl_expires_at ? formatTimestamp(q.ttl_expires_at) : '未设置'})`
        : 'Hub 通过长连接已将 Query 请求分发至开发者本地运行的 Client Daemon',
      markerClass: 'completed',
    },
    {
      title: '3. 现场 Agent 探针执行',
      time: q.duration_ms ? `耗时: ${q.duration_ms} ms` : '-',
      desc: (q.tools_used && q.tools_used.length > 0)
        ? `只读沙箱调用的探针工具: ${q.tools_used.join(', ')}。本地隐私护栏过滤生效。`
        : (isTerminal ? '现场探针直接回答，未调用额外工具' : '正在执行现场工具探查与 LLM 推理...'),
      markerClass: isTerminal ? 'completed' : 'active',
    },
    {
      title: `4. 终态结算 (${statusLabel(q.status)})`,
      time: q.completed_at ? formatTimestamp(q.completed_at) : (isTerminal ? formatTimestamp(q.created_at + (q.duration_ms || 0)) : '等待完成...'),
      desc: isFailed
        ? `执行失败/终止: ${q.error_message || statusLabel(q.status)}`
        : (q.status === 'completed'
            ? `成功合成回答并写入 Hub Inbound/Outbound 审计日志。总计消耗 ${q.token_usage?.total_tokens || 0} tokens。`
            : '等待现场 Agent 探针返回结果响应'),
      markerClass: isTerminal ? (isFailed ? 'failed' : 'completed') : '',
    },
  ];

  timelineEl.innerHTML = items.map(item => `
    <div class="timeline-item">
      <div class="timeline-marker ${item.markerClass}"></div>
      <div class="timeline-content">
        <div class="timeline-title">
          <span>${escapeHtml(item.title)}</span>
          <span class="timeline-time">${escapeHtml(item.time)}</span>
        </div>
        <div class="timeline-desc">${escapeHtml(item.desc)}</div>
      </div>
    </div>
  `).join('');
}

// ---------------------------------------------------------------------------
// View 6: Feishu Binding Manager View
// ---------------------------------------------------------------------------

async function loadFeishuBinding() {
  const boundCard = document.getElementById('fs-bound-card');
  const statusBadgeEl = document.getElementById('fs-status-badge');
  const connectedAtEl = document.getElementById('fs-connected-at');
  const reconnectsEl = document.getElementById('fs-reconnects');
  const boundAppIdEl = document.getElementById('fs-bound-app-id');
  const errorMsgEl = document.getElementById('fs-error-msg');
  const appIdInput = document.getElementById('fs-app-id');
  const resultEl = document.getElementById('fs-result');

  if (resultEl) resultEl.innerText = '';

  try {
    const res = await fetch('/api/v1/feishu/binding', { headers: authHeaders() });
    if (!res.ok) {
      if (res.status === 401) {
        if (resultEl) {
          resultEl.style.color = 'var(--danger)';
          resultEl.innerText = '请先登录以查看或配置飞书 Bot 绑定凭据。';
        }
      }
      return;
    }

    const b = await res.json();
    if (b.bound) {
      if (boundCard) boundCard.style.display = 'block';
      if (boundAppIdEl && b.app_id) boundAppIdEl.innerText = `App ID: ${b.app_id}`;
      if (connectedAtEl) {
        if (b.connected_at) {
          const d = new Date(b.connected_at);
          connectedAtEl.innerText = `连接于: ${d.toLocaleTimeString()}`;
          connectedAtEl.style.display = 'inline';
        } else {
          connectedAtEl.innerText = '';
          connectedAtEl.style.display = 'none';
        }
      }
      if (reconnectsEl) {
        if (typeof b.reconnects === 'number' && b.reconnects > 0) {
          reconnectsEl.innerText = `(重连: ${b.reconnects} 次)`;
          reconnectsEl.style.display = 'inline';
        } else {
          reconnectsEl.innerText = '';
          reconnectsEl.style.display = 'none';
        }
      }
      if (statusBadgeEl) {
        if (b.status === 'connected') {
          statusBadgeEl.className = 'badge badge-success';
          statusBadgeEl.innerText = '已连接 (长连接)';
        } else if (b.status === 'connecting') {
          statusBadgeEl.className = 'badge badge-warning';
          statusBadgeEl.innerText = '连接中...';
        } else if (b.status === 'error') {
          statusBadgeEl.className = 'badge badge-danger';
          statusBadgeEl.innerText = '连接错误';
        } else {
          statusBadgeEl.className = 'badge badge-offline';
          statusBadgeEl.innerText = '未连接';
        }
      }
      if (errorMsgEl) {
        if (b.error) {
          errorMsgEl.innerText = `错误信息: ${b.error}`;
          errorMsgEl.style.display = 'block';
        } else {
          errorMsgEl.style.display = 'none';
        }
      }
      if (appIdInput && b.app_id) appIdInput.value = b.app_id;
    } else {
      if (boundCard) boundCard.style.display = 'none';
      if (statusBadgeEl) {
        statusBadgeEl.className = 'badge badge-offline';
        statusBadgeEl.innerText = '未绑定';
      }
    }
  } catch (err) {
    console.error('Load Feishu binding failed', err);
  }
}

async function saveFeishuBinding() {
  const appId = (document.getElementById('fs-app-id')?.value || '').trim();
  const appSecret = (document.getElementById('fs-app-secret')?.value || '').trim();
  const resultEl = document.getElementById('fs-result');

  if (!appId || !appSecret) {
    if (resultEl) {
      resultEl.style.color = 'var(--danger)';
      resultEl.innerText = '请填写完整的 App ID 和 App Secret';
    }
    return;
  }

  const payload = {
    app_id: appId,
    app_secret: appSecret,
  };

  try {
    const res = await fetch('/api/v1/feishu/binding', {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify(payload),
    });

    if (res.ok) {
      if (resultEl) {
        resultEl.style.color = 'var(--success)';
        resultEl.innerText = '飞书应用配置已安全保存并启动长连接 (Hub 服务端采用 AES-GCM-256 加密存储)。';
      }
      showToast('飞书 Bot 配置保存成功', 'success');
      loadFeishuBinding();
    } else {
      if (resultEl) {
        resultEl.style.color = 'var(--danger)';
        resultEl.innerText = `保存失败: HTTP ${res.status}`;
      }
      showToast(`保存失败: HTTP ${res.status}`, 'danger');
    }
  } catch (err) {
    console.error('Save Feishu binding failed', err);
    if (resultEl) {
      resultEl.style.color = 'var(--danger)';
      resultEl.innerText = `请求异常: ${err.message}`;
    }
  }
}

async function deleteFeishuBinding() {
  if (!confirm('确定要解绑当前专属飞书 Bot 凭据吗？解绑后其他人将无法在飞书通过该 Bot 向您提问。')) {
    return;
  }

  try {
    const res = await fetch('/api/v1/feishu/binding', {
      method: 'DELETE',
      headers: authHeaders(),
    });

    if (res.ok || res.status === 204) {
      showToast('飞书 Bot 凭据已成功解绑', 'success');
      const boundCard = document.getElementById('fs-bound-card');
      if (boundCard) boundCard.style.display = 'none';

      const secretInput = document.getElementById('fs-app-secret');
      if (secretInput) secretInput.value = '';

      loadFeishuBinding();
    } else {
      showToast(`解绑失败: HTTP ${res.status}`, 'danger');
    }
  } catch (err) {
    console.error('Delete Feishu binding failed', err);
    showToast('解绑异常: ' + err.message, 'danger');
  }
}

// ---------------------------------------------------------------------------
// View 7: Admin Invite Generator
// ---------------------------------------------------------------------------

function initAdminTab() {
  const adminInput = document.getElementById('admin-token');
  if (adminInput && !adminInput.value) {
    adminInput.value = localStorage.getItem('talkintent_admin_token') || '';
  }
}

async function generateInvite() {
  const adminTokenInput = document.getElementById('admin-token');
  const nameInput = document.getElementById('inv-name');
  const aliasesInput = document.getElementById('inv-aliases');
  const expiresInput = document.getElementById('inv-expires');

  const adminToken = adminTokenInput ? adminTokenInput.value.trim() : '';
  const name = nameInput ? nameInput.value.trim() : '';
  const aliasesRaw = aliasesInput ? aliasesInput.value.trim() : '';
  const expires = expiresInput ? parseInt(expiresInput.value, 10) || 48 : 48;

  const aliases = aliasesRaw ? aliasesRaw.split(',').map(s => s.trim()).filter(Boolean) : [];

  if (!adminToken) {
    showToast('请输入管理员 Token (Admin Token)', 'danger');
    if (adminTokenInput) adminTokenInput.focus();
    return;
  }

  if (!name) {
    showToast('请填写成员姓名', 'danger');
    if (nameInput) nameInput.focus();
    return;
  }

  // Persist admin token for convenience
  localStorage.setItem('talkintent_admin_token', adminToken);

  try {
    const res = await fetch('/api/v1/admin/invites', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + adminToken,
      },
      body: JSON.stringify({
        target_name: name,
        aliases: aliases,
        expires_in_hours: expires,
      }),
    });

    if (res.ok) {
      const data = await res.json();
      const resultCard = document.getElementById('invite-result');
      const codeDisplay = document.getElementById('invite-code-display');
      const cmdDisplay = document.getElementById('invite-command-display');
      const copyBtn = document.getElementById('invite-copy-btn');
      const copyCmdBtn = document.getElementById('invite-copy-cmd-btn');

      const pairCmd = `talkintent pair --hub ${window.location.origin} --code ${data.code}`;
      if (codeDisplay) codeDisplay.innerText = data.code;
      if (cmdDisplay) cmdDisplay.value = pairCmd;
      if (copyBtn) copyBtn.onclick = () => copyText(data.code, 'invite-copy-btn');
      if (copyCmdBtn) copyCmdBtn.onclick = () => copyText(pairCmd, 'invite-copy-cmd-btn');
      if (resultCard) resultCard.style.display = 'block';

      showToast(`邀请码生成成功: ${data.code}`, 'success');
    } else if (res.status === 401) {
      showToast('生成失败: 管理员 Token 认证失败 (401 Unauthorized)', 'danger');
    } else {
      showToast(`生成失败: HTTP ${res.status}`, 'danger');
    }
  } catch (err) {
    console.error('Generate invite failed', err);
    showToast('生成请求异常: ' + err.message, 'danger');
  }
}

// ---------------------------------------------------------------------------
// Helpers & Utilities
// ---------------------------------------------------------------------------

function toggleAnswer(id) {
  const el = document.getElementById(id);
  if (el) {
    el.style.display = el.style.display === 'none' ? 'block' : 'none';
  }
}

function statusLabel(status) {
  switch (status) {
    case 'completed': return '已完成';
    case 'dispatched': return '已派发';
    case 'queued': return '排队中';
    case 'refused': return '已拒绝';
    case 'error': return '执行错误';
    case 'timeout': return '已超时';
    case 'expired': return '已过期';
    default: return status || '未知';
  }
}

function statusBadge(status) {
  return `<span class="badge badge-${escapeHtml(status || 'offline')}">${escapeHtml(statusLabel(status))}</span>`;
}

function formatTimestamp(ts) {
  if (!ts) return '-';
  const d = new Date(ts);
  if (isNaN(d.getTime())) return '-';
  const pad = n => String(n).padStart(2, '0');
  const year = d.getFullYear();
  const month = pad(d.getMonth() + 1);
  const day = pad(d.getDate());
  const hours = pad(d.getHours());
  const minutes = pad(d.getMinutes());
  const seconds = pad(d.getSeconds());
  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`;
}

function escapeHtml(text) {
  if (!text) return '';
  return String(text)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

async function copyText(text, btnId) {
  let valToCopy = text;
  if (!valToCopy && btnId) {
    const btn = document.getElementById(btnId);
    if (btn && btn.previousElementSibling && btn.previousElementSibling.value) {
      valToCopy = btn.previousElementSibling.value;
    }
  }

  if (!valToCopy) return;

  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(valToCopy);
    } else {
      const textarea = document.createElement('textarea');
      textarea.value = valToCopy;
      textarea.style.position = 'fixed';
      textarea.style.opacity = '0';
      document.body.appendChild(textarea);
      textarea.select();
      document.execCommand('copy');
      document.body.removeChild(textarea);
    }

    showToast('已成功复制到剪贴板', 'info');

    if (btnId) {
      const btn = document.getElementById(btnId);
      if (btn) {
        const origText = btn.innerText;
        btn.innerText = '已复制 ✓';
        setTimeout(() => { btn.innerText = origText; }, 2000);
      }
    }
  } catch (err) {
    console.error('Copy failed', err);
    showToast('复制失败，请手动选择复制', 'danger');
  }
}

function showToast(message, type = 'info') {
  const container = document.getElementById('toast-container');
  if (!container) return;

  const toast = document.createElement('div');
  toast.className = `toast toast-${type}`;
  toast.innerText = message;

  container.appendChild(toast);

  setTimeout(() => {
    toast.style.opacity = '0';
    toast.style.transition = 'opacity 0.3s ease';
    setTimeout(() => {
      if (toast.parentElement) toast.parentElement.removeChild(toast);
    }, 300);
  }, 3500);
}

// ---------------------------------------------------------------------------
// Initialization
// ---------------------------------------------------------------------------

window.addEventListener('DOMContentLoaded', () => {
  checkAuth();
  loadMembers();
});

// Explicitly bind handlers to window to ensure HTML inline onclick works in all environments
window.showTab = showTab;
window.openLoginModal = openLoginModal;
window.closeLoginModal = closeLoginModal;
window.login = login;
window.logout = logout;
window.loadMembers = loadMembers;
window.askMember = askMember;
window.loadInboundAudit = loadInboundAudit;
window.loadOutboundAudit = loadOutboundAudit;
window.submitAskQuery = submitAskQuery;
window.selectCandidate = selectCandidate;
window.cancelAskPoll = cancelAskPoll;
window.fetchQueryDetail = fetchQueryDetail;
window.viewQueryDetail = viewQueryDetail;
window.loadFeishuBinding = loadFeishuBinding;
window.saveFeishuBinding = saveFeishuBinding;
window.deleteFeishuBinding = deleteFeishuBinding;
window.generateInvite = generateInvite;
window.toggleAnswer = toggleAnswer;
window.copyText = copyText;
