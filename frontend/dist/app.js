// CF Auto Desktop · Wails 前端逻辑
// 通过 window.go.main.App.* 与 Go 后端 IPC。

const App = () => window?.go?.main?.App;
const EventsOn = (name, cb) => {
  if (window?.runtime?.EventsOn) return window.runtime.EventsOn(name, cb);
  // wails v2 老式 API
  if (window?.wails?.Events?.On) return window.wails.Events.On(name, cb);
};

const logPanel = document.getElementById('log-panel');
const statusPill = document.getElementById('status-pill');

function now() {
  return new Date().toTimeString().slice(0, 8);
}

function log(msg) {
  const line = `${now()}  ${msg}\n`;
  logPanel.textContent += line;
  logPanel.scrollTop = logPanel.scrollHeight;
}

function setStatus(text, kind = 'ok') {
  statusPill.textContent = `● ${text}`;
  statusPill.className = `status-pill status-${kind}`;
}

function setBusy(busy) {
  const disable = (id, val) => {
    const el = document.getElementById(id);
    if (el) el.disabled = val;
  };
  disable('btn-scan', busy);
  disable('btn-preview', busy);
  disable('btn-stop', !busy);
  disable('btn-download-cfst', busy);
}

function bind(id, handler) {
  const el = document.getElementById(id);
  if (el) el.addEventListener('click', handler);
}

// ── 加载初始状态（重启后恢复 UI） ──────────────────────────────────────────
async function loadStatus() {
  const A = App();
  if (!A) {
    log('Wails 绑定未就绪（浏览器预览模式）');
    return;
  }
  try {
    const st = await A.GetStatus();
    if (st.error) {
      log('读取状态失败：' + st.error);
      return;
    }

    // ── cfst 标签 ──
    if (st.cfst) {
      document.getElementById('cfst-label').textContent = `已安装 · ${st.cfst}`;
    }

    // ── 设置项恢复 ──
    const autoEl = document.getElementById('auto-scan');
    if (autoEl) autoEl.checked = !!st.auto_scan;
    try {
      const loginOn = await A.LoginEnabled();
      const loginEl = document.getElementById('login-start');
      if (loginEl) loginEl.checked = !!loginOn;
    } catch (_) {}
    const intervalEl = document.getElementById('interval');
    if (intervalEl && st.every_hours) {
      intervalEl.value = `每 ${st.every_hours} 小时`;
    }
    const modeEl = document.getElementById('scan-mode');
    if (modeEl && st.scan_mode) {
      modeEl.value = st.scan_mode;
    }

    // ── 配置恢复：YAML 路径 + 节点下拉 ──
    if (st.yaml_path) {
      window.__yamlPath = st.yaml_path;
      log(`恢复原始 YAML：${st.yaml_path.split('/').pop()}`);
    }
    const sel = document.getElementById('node-select');
    if (sel && Array.isArray(st.node_names) && st.node_names.length > 0) {
      sel.innerHTML = '';
      for (const name of st.node_names) {
        const opt = document.createElement('option');
        opt.value = name;
        opt.textContent = name;
        sel.appendChild(opt);
      }
      if (st.node_name) sel.value = st.node_name;
    }

    // ── 状态胶囊 ──
    if (st.configured) {
      setStatus('运行中 · 等待扫描', 'ok');
      log(`已加载配置：节点 ${st.node_name || '(未知)'}`);
      if (st.subscribe_url) {
        log(`订阅 URL：${st.subscribe_url}`);
        log('→ 在 Clash Party 选「订阅 / 导入 URL」粘贴即可');
      }
    } else {
      setStatus('待配置', 'idle');
      log('未配置 — 请先「导入 YAML」→「生成 Clash 配置」');
    }

    if (st.last_status) log(`上次状态：${st.last_status}`);
  } catch (e) {
    log('GetStatus 异常：' + e);
  }
}

// ── 事件订阅（Go → 前端） ────────────────────────────────────────────────
EventsOn('log', (msg) => log(msg));
EventsOn('scan-completed', (data) => {
  const n = data?.chosen?.length ?? 0;
  log(`扫描结束：${data?.qualified ?? 0} 个合格，暂选 ${n} 个 IP。`);
  setStatus(`完成 · 候选 ${n} 个`, 'ok');
  setBusy(false);
});
EventsOn('scan-failed', (data) => {
  log('扫描失败：' + (data?.error || '未知'));
  setStatus('扫描失败 · 保留旧池', 'err');
  setBusy(false);
});

// ── 事件绑定 ────────────────────────────────────────────────────────────
bind('btn-import-yaml', async () => {
  const A = App();
  log('→ 点击「导入 YAML」');
  if (!A) return log('Wails 后端未就绪');
  try {
    const path = await A.OpenYAMLDialog();
    if (!path) {
      log('已取消选择');
      setStatus('待配置', 'idle');
      return;
    }
    log('选择文件：' + path.split('/').pop());
    const r = await A.ImportYAML(path);
    if (r.error) {
      log('YAML 校验失败：' + r.error);
      setStatus('YAML 校验失败', 'err');
      return;
    }
    // 更新下拉框
    const sel = document.getElementById('node-select');
    sel.innerHTML = '';
    for (const name of r.nodes) {
      const opt = document.createElement('option');
      opt.value = name;
      opt.textContent = name;
      sel.appendChild(opt);
    }
    if (r.preferred) sel.value = r.preferred;
    window.__yamlPath = r.path;
    log(`找到 ${r.nodes.length} 个 VLESS+WS+TLS 节点` +
        (r.preferred ? `，默认选中 ${r.preferred}` : ''));
    setStatus('YAML 已导入', 'ok');
  } catch (e) {
    log('导入失败：' + e);
    setStatus('导入失败', 'err');
  }
});

bind('btn-generate', async () => {
  const A = App();
  log('→ 点击「生成 Clash 配置」');
  if (!A) return log('Wails 后端未就绪');
  const yamlPath = window.__yamlPath;
  const node = document.getElementById('node-select').value;
  if (!yamlPath) {
    log('请先点击「导入 YAML」选择文件');
    return;
  }
  if (!node || node === '—') {
    log('请先在「原节点」下拉中选一个节点');
    return;
  }
  try {
    setStatus('生成中…', 'busy');
    const r = await A.GenerateConfig(yamlPath, node);
    log('已生成：' + r.generated);
    log('推荐：在 Clash Party 选「订阅 / 导入 URL」粘贴下方链接：');
    log('  ' + r.subscribe_url);
    log('→ 或点右侧「复制订阅 URL」。候选池走 HTTP，Clash 每 60s 自动拉取。');
    setStatus('已生成', 'ok');
  } catch (e) {
    log('生成失败：' + e);
    setStatus('生成失败', 'err');
  }
});

bind('btn-download-cfst', async () => {
  const A = App();
  log('→ 点击「一键下载官方 cfst」');
  if (!A) return log('Wails 后端未就绪');
  try {
    const ok = await A.Confirm(
      '下载官方 cfst',
      '将从 GitHub 官方仓库 XIU2/CloudflareSpeedTest 的 Releases 下载 cfst。\n\n' +
      '· 仅使用 HTTPS，并校验 GitHub 提供的 SHA256\n' +
      '· 只解压 cfst 与 ip.txt，不执行任何脚本\n\n继续吗？'
    );
    if (!ok) {
      log('已取消下载');
      return;
    }
    setBusy(true);
    setStatus('下载 cfst 中…', 'busy');
    log('开始下载官方 cfst…');
    const r = await A.DownloadCfst();
    document.getElementById('cfst-label').textContent = `已安装 · ${r.path.split('/').slice(-2)[0]}`;
    setStatus('cfst 已安装', 'ok');
    log('官方 cfst 安装完成');
  } catch (e) {
    setStatus('cfst 下载失败', 'err');
    log('下载失败：' + e);
    try { await A.Alert('下载失败', String(e) + '\n\n可改用「手动导入」。'); } catch {}
  } finally {
    setBusy(false);
  }
});

bind('btn-import-cfst', async () => {
  const A = App();
  log('→ 点击「手动导入」cfst');
  if (!A) return log('Wails 后端未就绪');
  try {
    const path = await A.OpenCfstDialog();
    if (!path) {
      log('已取消选择');
      return;
    }
    const ok = await A.Confirm(
      '导入测速程序',
      '仅导入你信任的 CloudflareSpeedTest 文件。\n应用会复制 cfst + ip.txt 到私有目录。继续吗？'
    );
    if (!ok) return;
    const r = await A.ImportCfst(path);
    document.getElementById('cfst-label').textContent = `已安装 · ${r.path.split('/').slice(-2)[0]}`;
    log('手动导入完成');
    setStatus('cfst 已安装', 'ok');
  } catch (e) {
    log('导入失败：' + e);
    setStatus('导入失败', 'err');
  }
});

bind('btn-scan', async () => {
  const A = App();
  if (!A) return log('（PoC）扫描需 Wails 后端');
  setBusy(true);
  setStatus('扫描中', 'busy');
  log('立即扫描并应用：点击按钮');
  try {
    await A.StartScan(false);
  } catch (e) {
    setStatus('扫描失败', 'err');
    log('启动扫描失败：' + e);
    setBusy(false);
  }
});

bind('btn-preview', async () => {
  const A = App();
  if (!A) return log('（PoC）预览需 Wails 后端');
  setBusy(true);
  setStatus('预览中', 'busy');
  log('仅预览结果：点击按钮');
  try {
    await A.StartScan(true);
  } catch (e) {
    setStatus('预览失败', 'err');
    log('预览失败：' + e);
    setBusy(false);
  }
});

bind('btn-stop', async () => {
  const A = App();
  if (!A) return log('（PoC）停止需 Wails 后端');
  try {
    await A.StopScan();
    setStatus('已停止', 'idle');
    log('已请求停止扫描');
  } catch (e) {
    log('停止失败：' + e);
  }
  setBusy(false);
});

bind('btn-copy-url', async () => {
  const A = App();
  if (!A) return log('（PoC）复制订阅 URL 需 Wails 后端');
  try {
    const url = await A.CopySubscribeURL();
    await navigator.clipboard.writeText(url);
    log('订阅 URL 已复制到剪贴板：');
    log('  ' + url);
    log('→ 在 Clash Party 选「订阅 / 导入 URL」粘贴即可');
  } catch (e) {
    log('复制失败：' + e);
  }
});

bind('login-start', async (e) => {
  const A = App();
  if (!A) return log('Wails 后端未就绪');
  const want = e.target.checked;
  try {
    await A.SetLogin(want);
    log('开机登录启动：' + (want ? '已开启' : '已关闭'));
  } catch (err) {
    log('设置登录自启失败：' + err);
    e.target.checked = !want;
  }
});

bind('btn-open-config', async () => {
  const A = App();
  if (!A) return log('（PoC）打开配置需 Wails 后端');
  try {
    const r = await A.OpenGeneratedConfig();
    if (r?.path) {
      await navigator.clipboard.writeText(r.path);
      log('配置路径已复制到剪贴板：' + r.path);
    }
  } catch (e) {
    log('打开失败：' + e);
  }
});

bind('btn-rollback', async () => {
  const A = App();
  log('→ 点击「回滚节点池」');
  if (!A) return log('Wails 后端未就绪');
  try {
    const ok = await A.Confirm('确认回滚', '恢复上一次候选池吗？现有配置将备份，可再次回滚切换。');
    if (!ok) return;
    const r = await A.Rollback();
    log(r.rolled ? '回滚完成。' : '尚无可回滚历史。');
  } catch (e) {
    log('回滚失败：' + e);
  }
});

bind('btn-open-folder', async () => {
  const A = App();
  if (!A) return log('（PoC）打开工作目录需 Wails 后端');
  try {
    await A.OpenConfigFolder();
    log('已在 Finder 中打开工作目录');
  } catch (e) {
    log('打开失败：' + e);
  }
});

// ── 启动 ───────────────────────────────────────────────────────────────
window.addEventListener('DOMContentLoaded', async () => {
  const A = App();
  if (A?.SystemInfo) {
    try {
      const info = await A.SystemInfo();
      log(`Go 后端就绪：${info.go} · ${info.os}/${info.arch}`);
    } catch (e) {
      log(`SystemInfo 异常：${e}`);
    }
  }
  await loadStatus();
});
