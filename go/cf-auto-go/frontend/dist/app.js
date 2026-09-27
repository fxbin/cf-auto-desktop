// PoC 前端脚本：阶段 1 只做日志与状态演示
// 阶段 2 会用 window.go.main.App.<method> 与 Go 后端 IPC

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

function bind(id, handler) {
  const el = document.getElementById(id);
  if (el) el.addEventListener('click', handler);
}

bind('btn-import-yaml', () => {
  log('导入 YAML：点击「导入 YAML」按钮（阶段 2 接 Go 后端文件对话框）');
  setStatus('已请求导入 YAML', 'busy');
});

bind('btn-generate', () => {
  log('生成 Clash 配置：点击「生成 Clash 配置」');
  setStatus('生成中…', 'busy');
});

bind('btn-download-cfst', () => {
  log('一键下载官方 cfst：点击按钮');
  setStatus('下载中…', 'busy');
});

bind('btn-scan', () => {
  log('立即扫描并应用：点击按钮');
  setStatus('扫描中', 'busy');
  setTimeout(() => {
    log('（PoC）扫描完成');
    setStatus('完成 · 候选 0 个', 'ok');
  }, 800);
});

bind('btn-preview', () => {
  log('仅预览结果：点击按钮');
  setStatus('预览中', 'busy');
});

bind('btn-stop', () => {
  log('停止扫描：点击按钮');
  setStatus('已停止', 'idle');
});

bind('btn-copy-url', () => {
  log('复制订阅 URL：点击按钮（阶段 2 从 Go 后端取 URL）');
});

bind('btn-open-config',   () => log('打开生成的配置'));
bind('btn-rollback',      () => log('回滚节点池'));
bind('btn-open-folder',   () => log('打开工作目录'));

// 启动问候：如果 Wails 绑定已就绪，打印 Go 侧信息
window.addEventListener('DOMContentLoaded', async () => {
  if (window?.go?.main?.App?.SystemInfo) {
    try {
      const info = await window.go.main.App.SystemInfo();
      log(`Go 后端就绪：${info.go} · ${info.os}/${info.arch}`);
      setStatus('运行中 · 等待配置', 'ok');
    } catch (e) {
      log(`Go 后端绑定异常：${e}`);
      setStatus('后端异常', 'err');
    }
  } else {
    log('PoC · Wails 绑定未就绪（浏览器预览模式）');
  }
});
