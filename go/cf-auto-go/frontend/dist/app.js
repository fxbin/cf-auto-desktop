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

// ── 加载初始状态 ──────────────────────────────────────────────────────────
async function loadStatus() {
  const A = App();
  if (!A) {
    log('PoC · Wails 绑定未就绪（浏览器预览模式）');
    return;
  }
  try {
    const st = await A.GetStatus();
    if (st.error) {
      log('读取状态失败：' + st.error);
      return;
    }
    if (st.configured) {
      setStatus('运行中 · 等待扫描', 'ok');
      log(`已加载配置：节点 ${st.node_name}`);
    } else {
      setStatus('待配置', 'idle');
    }
    if (st.cfst) {
      document.getElementById('cfst-label').textContent = `已安装 · ${st.cfst}`;
    }
    if (st.subscribe_url) {
      log(`订阅 URL（推荐）：${st.subscribe_url}`);
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
  if (!A) return log('（PoC）导入 YAML 需 Wails 后端');
  // TODO: 用 runtime.OpenFileDialog 让用户选；这里先提示
  log('请通过系统文件对话框选择原始 Clash YAML');
  setStatus('等待选择文件', 'busy');
});

bind('btn-generate', async () => {
  const A = App();
  if (!A) return log('（PoC）生成配置需 Wails 后端');
  log('生成 Clash 配置：点击按钮');
  setStatus('生成中…', 'busy');
  // 需要用户先选 YAML；此处是 PoC
  log('请先通过「导入 YAML」选择文件');
  setStatus('待配置', 'idle');
});

bind('btn-download-cfst', async () => {
  const A = App();
  if (!A) return log('（PoC）下载 cfst 需 Wails 后端');
  if (!confirm('将从 GitHub 官方仓库 XIU2/CloudflareSpeedTest 的 Releases 下载 cfst。\n' +
    '· 仅使用 HTTPS，并校验 GitHub 提供的 SHA256\n' +
    '· 只解压 cfst 与 ip.txt，不执行任何脚本\n\n继续吗？')) return;
  setBusy(true);
  setStatus('下载 cfst 中…', 'busy');
  log('开始下载官方 cfst…');
  try {
    const r = await A.DownloadCfst();
    document.getElementById('cfst-label').textContent = `已安装 · ${r.path.split('/').slice(-2)[0]}`;
    setStatus('cfst 已安装', 'ok');
    log('官方 cfst 安装完成');
  } catch (e) {
    setStatus('cfst 下载失败', 'err');
    log('下载失败：' + e);
    alert('下载失败：' + e + '\n\n可改用「手动导入」。');
  } finally {
    setBusy(false);
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
  if (!A) return log('（PoC）回滚需 Wails 后端');
  if (!confirm('恢复上一次候选池吗？现有配置将备份。')) return;
  try {
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
