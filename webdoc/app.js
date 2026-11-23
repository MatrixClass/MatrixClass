const pages = {
  home: {
    title: '把课堂控制，交给一个清晰的工作台',
    subtitle: '矩联电子教室 MatrixClass 是面向局域网教室的教师端与学生端系统。少一点复杂入口，多一点可预期的课堂协作。',
    hero: true,
    sections: [
      { title: '从开机到下课', body: '<p>教师端选择数字频道后即可开始课堂，学生端首次启动配置密码和频道 ID，之后会自动连接并在网络变化时持续重连。</p><div class="callout"><strong>设计原则</strong>所有由教师发起的动作，都在学生端以独立、置顶、可恢复的窗口呈现。</div>' },
      { title: '一套工具覆盖整节课', body: '<div class="feature-grid"><div class="feature-card"><div class="feature-icon">▦</div><h3>看得见</h3><p>屏幕广播、屏幕监控和设备视图让教师掌握课堂状态。</p></div><div class="feature-card"><div class="feature-icon">↗</div><h3>发得快</h3><p>文件分发、断点续传、进度反馈和失败重试减少等待。</p></div><div class="feature-card"><div class="feature-icon">⌘</div><h3>管得住</h3><p>远程命令、程序启动、锁屏和管控策略集中编排。</p></div></div>' },
    ],
  },
  quickstart: { title: '快速开始', subtitle: '从构建程序到加入第一间教室。', sections: [
    { title: '1. 获取程序', body: '<p>从项目的 <a href="https://github.com/MatrixClass/MatrixClass/releases" target="_blank" rel="noreferrer"><strong>Releases</strong></a> 下载对应平台的教师端和学生端，或从源码构建。</p><pre><code>go test -tags mygo_nowebview -count=1 ./...\n\ngo build -tags mygo_nowebview -ldflags "-H windowsgui" -o dist\\matrixclass-teacher.exe ./cmd/teacher\ngo build -tags mygo_nowebview -ldflags "-H windowsgui" -o dist\\matrixclass-student.exe ./cmd/student</code></pre>' },
    { title: '2. 启动教师端', body: '<p>第一次启动教师端时选择数字频道 ID，并完成教师账号设置。频道 ID 建议使用教室或年级约定的数字，例如 <code>1</code>、<code>12</code>。</p>' },
    { title: '3. 启动学生端', body: '<p>学生端安装成功后会自动启动。首次启动设置退出密码和连接频道 ID；配置完成后，学生端会自动发现对应频道的教师端。</p><div class="callout"><strong>注意</strong>教师端和学生端必须处于可互通的局域网，并允许 UDP 广播/组播和 KCP 监听端口。</div>' },
  ]},
  teacher: { title: '教师端', subtitle: '把课堂动作集中在一个可操作、可恢复的控制台里。', sections: [
    { title: '设备视图与监控', body: '<p>设备视图展示在线设备、分组和连接状态。屏幕监控用于查看学生屏幕，支持多选、批量操作和离线设备清理。</p><ul><li><strong>Ctrl+A</strong>：全选当前视图设备。</li><li><strong>Ctrl+鼠标左键</strong>：切换单个设备选择状态。</li><li><strong>长按拖动</strong>：框选多个设备。</li><li><strong>点击空白处</strong>：清除选择。</li></ul>' },
    { title: '课堂互动', body: '<p>教师端发布消息、随堂测验或点名签到后，学生端会自动弹出相应的独立窗口。教师结束操作后，窗口才会关闭。</p>' },
    { title: '远程操作', body: '<p>远程操作页面采用双栏布局：左侧选择启动程序或执行命令，右侧显示当前操作内容。程序可从学生端控制面板程序列表选择，也可以输入绝对路径。</p><pre><code>远程命令示例：\n  cmd /c ipconfig\n  powershell -NoProfile Get-ComputerInfo</code></pre>' },
  ]},
  student: { title: '学生端', subtitle: '默认安静运行，只有教师发起动作时才打扰学生。', sections: [
    { title: '首次启动', body: '<ol><li>设置学生端退出密码。</li><li>输入纯数字频道 ID。</li><li>保存后自动搜索教师端。</li></ol><p>频道和密码保存在学生端数据目录，频道文件只保存数字频道值，密码以哈希形式保存。</p>' },
    { title: '托盘参数设置', body: '<p>右键学生端托盘图标，选择“参数设置”。进入设置前必须输入退出密码，可以修改频道 ID；保存后学生端会关闭旧连接并自动连接新频道。</p>' },
    { title: '文件与作业', body: '<p>教师分发文件时，学生端会弹出独立下载队列窗口；教师收取作业时，学生端会弹出独立提交页面。文件默认保存到桌面或教师指定路径，不会自动打开。</p>' },
  ]},
  network: { title: '网络与部署', subtitle: '理解频道发现、KCP 会话和跨平台桌面权限。', sections: [
    { title: '频道 ID 与发现', body: '<p>频道 ID 是纯数字逻辑频道，不是教师姓名或机房名称。学生端通过 UDP 广播/组播发现教师端，再使用 KCP 建立会话；如果组播不可用，系统会使用 KCP 回退路径。</p>' },
    { title: 'KCP 与网卡', body: '<p>系统不会固定绑定单一网卡，会刷新本机网络接口和地址快照。网络切换后学生端会持续尝试重连。多网卡环境下仍需确保教师端和学生端存在一条可达的局域网路径。</p>' },
    { title: '平台能力', body: '<div class="table-wrap"><table><thead><tr><th>平台</th><th>发布架构</th><th>注意事项</th></tr></thead><tbody><tr><td>Windows</td><td>amd64 / arm64</td><td>允许专用网络，安装包需要 NSIS</td></tr><tr><td>Linux</td><td>amd64 / arm64 / loong64</td><td>X11、PipeWire/PulseAudio 和桌面权限按发行版配置</td></tr><tr><td>macOS</td><td>amd64 / arm64 / Universal 2</td><td>未签名应用需要手动授权屏幕录制、麦克风和辅助功能</td></tr></tbody></table></div>' },
  ]},
  faq: { title: '常见问题', subtitle: '先从连接、画面、文件和权限四类问题排查。', sections: [
    { title: '学生端显示正在重连', body: '<p>检查教师端是否选择了相同的数字频道，检查两台设备是否处于同一局域网，并允许防火墙中的专用网络访问。学生端会持续重试，不需要手动重启。</p>' },
    { title: '屏幕监控一段时间后无画面', body: '<p>优先确认学生端仍在线，再检查桌面采集权限和网络丢包。教师端会在解码错误或连接恢复后请求关键帧并重启监控流。</p>' },
    { title: '文件分发卡住', body: '<p>查看学生端下载队列中的进度和重试按钮。文件传输支持断点续传和覆盖重传；如果学生端已经离线，恢复连接后再重试即可。</p>' },
    { title: 'Linux CI 为什么不测试真实屏幕和音频', body: '<p>GitHub Actions 的 Linux Runner 通常没有桌面会话、音频设备或 PipeWire/PulseAudio。CI 会跳过依赖硬件的部分，但继续测试编码、协议、传输和业务逻辑。</p>' },
  ]},
  development: { title: '开发指南', subtitle: '保持模块边界清晰，让每次改动都能被测试和发布。', sections: [
    { title: '本地验证', body: '<pre><code>go test -tags mygo_nowebview -count=1 ./...\n\n# 构建 Linux 多架构\n./scripts/build-linux-package.sh dist/linux amd64 arm64 loong64\n\n# 构建全平台发行包\n./scripts/build-all.ps1 -Version 0.1.0</code></pre>' },
    { title: 'CI 检查', body: '<p>Pull Request 必须通过 <code>CI / test-and-build</code>。该汇总检查依赖所有平台矩阵任务：Linux amd64/arm64/loong64、Windows amd64/arm64、macOS amd64/arm64。</p>' },
    { title: '目录规范', body: '<p>源码只放在 <code>repo</code>，发行文件放在 <code>release</code>，端口和测试文档放在 <code>doc</code>，临时验证产物放在 <code>other</code>。不要将本地二进制、日志和运行数据提交到 Git。</p>' },
  ]},
};

const navGroups = [
  ['开始使用', [['快速开始', 'quickstart']]],
  ['使用指南', [['教师端', 'teacher'], ['学生端', 'student'], ['网络与部署', 'network'], ['常见问题', 'faq']]],
  ['开发者', [['开发指南', 'development']]],
];

const content = document.querySelector('#content');
const sidebarNav = document.querySelector('#sidebar-nav');
const searchModal = document.querySelector('#search-modal');
const searchInput = document.querySelector('#search-input');
const searchResults = document.querySelector('#search-results');
let searchIndex = [];
let selectedResult = 0;

function routeKey() { return location.hash.replace(/^#\/?/, '') || 'home'; }
function pageFor(key) { return pages[key] || pages.home; }
function linkFor(key) { return `#/${key === 'home' ? '' : key}`; }

function renderNav() {
  sidebarNav.innerHTML = `<a class="nav-link ${routeKey() === 'home' ? 'active' : ''}" href="#/">概览</a>` + navGroups.map(([title, items]) => `<div class="nav-group"><p class="nav-title">${title}</p>${items.map(([label, key]) => `<a class="nav-link ${routeKey() === key ? 'active' : ''}" href="${linkFor(key)}">${label}</a>`).join('')}</div>`).join('');
}

function renderHome(page) {
  return `<section class="hero"><p class="eyebrow">局域网电子教室 · MATRIXCLASS</p><h1>${page.title}<br><span>让每个设备都在课堂里。</span></h1><p>${page.subtitle}</p><div class="hero-actions"><a class="button primary" href="#/quickstart">快速开始 →</a><a class="button secondary" href="https://github.com/MatrixClass/MatrixClass" target="_blank" rel="noreferrer">查看源码</a></div></section>${page.sections.map(section => `<section class="prose"><h2>${section.title}</h2>${section.body}</section>`).join('')}`;
}

function renderDoc(page) {
  return `<header class="doc-header"><p class="eyebrow">MATRIXCLASS DOCS</p><h1>${page.title}</h1><p>${page.subtitle}</p></header><article class="prose">${page.sections.map(section => `<h2>${section.title}</h2>${section.body}`).join('')}</article><footer class="page-footer"><a href="#/">← 返回概览</a><a href="#/development">开发指南 →</a></footer>`;
}

function render() {
  const key = routeKey();
  const page = pageFor(key);
  document.title = `${page.title} · MatrixClass 文档`;
  content.innerHTML = `<div class="page">${page.hero ? renderHome(page) : renderDoc(page)}</div>`;
  renderNav();
  document.querySelector('#sidebar').classList.remove('open');
  window.scrollTo({ top: 0, behavior: 'smooth' });
}

function buildSearchIndex() {
  searchIndex = Object.entries(pages).flatMap(([key, page]) => {
    const text = [page.title, page.subtitle, ...page.sections.map(section => `${section.title} ${section.body.replace(/<[^>]*>/g, ' ')}`)].join(' ');
    return [{ key, title: page.title, description: page.subtitle, text: text.toLowerCase() }];
  });
}

function updateSearch() {
  const query = searchInput.value.trim().toLowerCase();
  const matches = query ? searchIndex.filter(item => item.text.includes(query)) : searchIndex;
  selectedResult = 0;
  searchResults.innerHTML = matches.length ? matches.map((item, index) => `<a class="search-result ${index === 0 ? 'selected' : ''}" href="${linkFor(item.key)}"><strong>${item.title}</strong><span>${item.description}</span></a>`).join('') : '<p class="search-empty">没有找到相关文档。</p>';
}

function openSearch() { searchModal.hidden = false; searchInput.value = ''; updateSearch(); setTimeout(() => searchInput.focus(), 0); }
function closeSearch() { searchModal.hidden = true; }

document.querySelector('#search-trigger').addEventListener('click', openSearch);
document.querySelectorAll('[data-close-search]').forEach(element => element.addEventListener('click', closeSearch));
document.querySelector('#theme-toggle').addEventListener('click', () => { document.body.classList.toggle('dark'); localStorage.setItem('matrixclass-theme', document.body.classList.contains('dark') ? 'dark' : 'light'); });
document.querySelector('#mobile-menu').addEventListener('click', () => document.querySelector('#sidebar').classList.toggle('open'));
searchInput.addEventListener('input', updateSearch);
document.addEventListener('keydown', event => {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); openSearch(); }
  if (event.key === 'Escape') closeSearch();
  if (!searchModal.hidden && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
    const results = [...searchResults.querySelectorAll('.search-result')];
    if (!results.length) return;
    selectedResult = (selectedResult + (event.key === 'ArrowDown' ? 1 : results.length - 1)) % results.length;
    results.forEach((result, index) => result.classList.toggle('selected', index === selectedResult));
    event.preventDefault();
  }
});

if (localStorage.getItem('matrixclass-theme') === 'dark') document.body.classList.add('dark');
buildSearchIndex();
window.addEventListener('hashchange', render);
render();
