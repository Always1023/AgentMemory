//
// 四件事：
//   1. 把当前判到的明暗套回给后端写日志——界面上「没跟着系统变」时，有这条日志
//      就能分清是前端没判对、还是窗口没收到系统主题；
//   2. 没有配好时走首次运行引导；配好了走主界面；
//   3. 每一步都把答案先存下来，所以中途关掉窗口，下次打开能接着走；
//      收集期间不去轮询后端——问得越勤，收集自己的人越挤。

const bridge = window.go && window.go.main ? window.go.main.App : null;

// PROGRESS_EVENT 要与后端 ui.go 里的 progressEvent 一致。
const PROGRESS_EVENT = "collect:progress";
// 读取统计的过场事件（I-051）；名称与后端 ui.go 保持一致。
const READING_STARTED_EVENT = "state:reading-started";
const READING_PROGRESS_EVENT = "state:reading-progress";
const READING_FINISHED_EVENT = "state:reading-finished";

const themeQuery = window.matchMedia("(prefers-color-scheme: dark)");

// state 是后端给的界面状态；step 是引导现在停在第几步。
let state = null;
let step = 0;

// failureMessage 是「这一下没做成」的原因，渲染完再盖回进度那一行——
// 直接写进去会被 render() 覆盖掉。
let failureMessage = "";

// ── 小工具 ────────────────────────────────────────────────────────────────

function $(id) {
  return document.getElementById(id);
}

function fill(id, value) {
  const node = $(id);
  if (!node) {
    return;
  }
  const text = value === undefined || value === null ? "" : String(value);
  node.textContent = text.trim() === "" ? "—" : text;
  // 写上内容就把这一行显出来：进度、报错都靠它，别让 hidden 一直留着（I-052）。
  node.hidden = false;
}

// fillOptional 与 fill 的差别：空内容就藏起来，不写「—」。
// 顶部状态句与进度行属于「没话说就别占位置」，用这一条（I-052）。
function fillOptional(id, value) {
  const node = $(id);
  if (!node) {
    return;
  }
  const text = value === undefined || value === null ? "" : String(value);
  node.textContent = text;
  node.hidden = text.trim() === "";
}

// renderHeadline 同时管文字与显隐：只 fill 不设 hidden 会让该出现的句子不出现、
// 该消失的时候留一个「—」（I-052）。
function renderHeadline(text) {
  fillOptional("headline", text);
}

function show(id) {
  $("view-wizard").hidden = id !== "view-wizard";
  $("view-main").hidden = id !== "view-main";
  $("view-log").hidden = id !== "view-log";
  $("view-search").hidden = id !== "view-search";
  $("view-settings").hidden = id !== "view-settings";

  // 导航栏跟着视图走：首页、日志、搜索、设置四页；引导期间藏起来。
  const inApp = id === "view-main" || id === "view-log" || id === "view-search" || id === "view-settings";
  $("nav").hidden = !inApp;
  $("nav-main").classList.toggle("nav__item--on", id === "view-main");
  $("nav-log").classList.toggle("nav__item--on", id === "view-log");
  $("nav-search").classList.toggle("nav__item--on", id === "view-search");
  $("nav-settings").classList.toggle("nav__item--on", id === "view-settings");
}

// setReading 控制读取过场与下面具体数字的显隐：读取时只见过场，读完再见数字。
let readingActive = false;

function setReading(active, text, percent) {
  const box = $("reading");
  if (!box) {
    return;
  }
  readingActive = !!active;
  box.hidden = !active;
  // 引导页也用同一套渠道行；这里只藏主界面那一份，别把引导里的渠道列表一起藏掉。
  $("channel-stats").hidden = active;
  $("main-channels").hidden = active;
  // 读取时只留过场；下面的主操作（增量收集）等读完再露（I-076）。
  $("main-actions").hidden = active;
  if (!active) {
    return;
  }
  fill("reading-text", text || "正在读取数据…");
  const value = Math.max(0, Math.min(100, Number(percent) || 0));
  $("reading-fill").style.width = value + "%";
  $("reading-bar").setAttribute("aria-valuenow", String(value));
}

// reportView 把「现在界面上是哪个视图」回给后端写日志。
//
// events 一并报过去：进度那个事件出口拿不到的话，收集照收、只是进度永远不动，
// 界面安安静静——这类「安静地不工作」只有日志能兜住。
function reportView(view) {
  if (bridge && bridge.ReportView) {
    bridge.ReportView(view, !!(window.runtime && window.runtime.EventsOn)).catch(() => {});
  }
}

// reportFrontendError 把未捕获的异常写进后端日志（I-052）。
//
// 前端出错时窗口可能只是「不动了」，控制台又没人看；留一行日志，
// 至少能分清是前端没跑对还是后端没回来。截断到 500 字，避免把整篇堆栈塞进日志。
function reportFrontendError(text) {
  const message = String(text === undefined || text === null ? "" : text).slice(0, 500);
  if (message === "") {
    return;
  }
  if (window.console && window.console.error) {
    window.console.error(message);
  }
  if (bridge && bridge.ReportFrontendError) {
    bridge.ReportFrontendError(message).catch(() => {});
  }
}

// el 造一个元素：标签、类名、文字。
function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (text !== undefined && text !== null) {
    node.textContent = text;
  }
  return node;
}

function button(text, className, onClick) {
  const node = el("button", className, text);
  node.type = "button";
  node.addEventListener("click", onClick);
  return node;
}

function showWizardError(message) {
  const node = $("wizard-error");
  node.textContent = message || "";
  node.hidden = !message;
}

// 一档状态 → 一个状态点样式。界面只认 ok / none / bad 三档，
// 不去解释后端那三种枚举值。
const DOT_BY_STATE = { ok: "dot dot--ok", none: "dot", bad: "dot dot--bad" };

// ── 明暗（人提的：做成小图标、用户可控）───────────────────────────────────
//
// 三档循环：跟随系统 → 亮 → 暗。选择记在**浏览器本地**（localStorage）——
// 它是「这台机器上这个窗口怎么看」，不是收集与备份的配置，所以不进 config.json。
const THEME_KEY = "agentmemory.theme";
const THEME_ORDER = ["auto", "light", "dark"];
const THEME_ICON = { auto: "◐", light: "☀", dark: "☾" };
const THEME_NAME = { auto: "跟随系统", light: "亮色", dark: "暗色" };

let themeChoice = "auto";

// applyTheme 把选择落到 <html data-theme>：auto 时不设属性，交给系统。
function applyTheme() {
  const root = document.documentElement;
  if (themeChoice === "auto") {
    root.removeAttribute("data-theme");
  } else {
    root.setAttribute("data-theme", themeChoice);
  }
  const button = $("theme-toggle");
  if (!button) {
    return;
  }
  button.textContent = THEME_ICON[themeChoice];
  button.title = "外观：" + THEME_NAME[themeChoice] + "（点一下换）";
  button.setAttribute("aria-label", button.title);
}

// cycleTheme 换下一档，并记住。
function cycleTheme() {
  const at = THEME_ORDER.indexOf(themeChoice);
  themeChoice = THEME_ORDER[(at + 1) % THEME_ORDER.length];
  try {
    localStorage.setItem(THEME_KEY, themeChoice);
  } catch (ignored) {
    // 存不下就算了：这次会话里照样能切，只是下次打开回到默认。
  }
  applyTheme();
  reportTheme();
}

// loadTheme 读回上次的选择。
function loadTheme() {
  try {
    const saved = localStorage.getItem(THEME_KEY);
    if (THEME_ORDER.indexOf(saved) >= 0) {
      themeChoice = saved;
    }
  } catch (ignored) {
    // 读不到就用默认（跟随系统）。
  }
  applyTheme();
}


// 首页渠道的三段（I-066）。后端给每行一个 group，前端只按组名分段；
// 检测失败只在出现时单列一段，免得错误被归进「未安装」。
const CHANNEL_GROUPS = [
  ["failed", "检测失败"],
  ["has_sessions", "已安装 · 有会话"],
  ["installed_no_sessions", "已安装 · 无会话"],
  ["not_installed", "未安装"],
];

// renderChannelRows 画渠道行。withAction 为 false 时不画「重试」——
// 引导页只是让人看一眼有哪些渠道，那儿不是干活的地方（人提的）。
//
// 首页按后端给的 group 分三段并加小标题；引导页不分组，仍按后端给的顺序平铺——
// 分组只发生在干活的那一页。
function renderChannelRows(container, channels, withAction = true) {
  container.replaceChildren();
  if (!withAction) {
    for (const row of channels) {
      container.append(channelRow(row, false));
    }
    return;
  }
  for (const [group, label] of CHANNEL_GROUPS) {
    const rows = channels.filter((row) => row.group === group);
    if (!rows.length) {
      continue;
    }
    container.append(el("li", "rows__group", label));
    for (const row of rows) {
      container.append(channelRow(row, true));
    }
  }
}

// channelRow 画一行：状态点、名字、说明，首页再加行末标记。
function channelRow(row, withAction) {
  const item = document.createElement("li");
  item.className = "row";
  item.dataset.id = row.id;

  const dot = document.createElement("span");
  dot.className = DOT_BY_STATE[row.state] || "dot";

  const name = document.createElement("span");
  name.className = "row__name";
  name.textContent = row.name;

  const detail = document.createElement("span");
  detail.className = "row__detail";
  detail.textContent = row.detail;

  item.append(dot, name, detail);

  // 末尾那个标记：还能收的就是一个能点的箭头，没有新内容的就是一个绿勾（人提的）。
  if (withAction) {
    const mark = channelMark(row);
    if (mark) {
      item.append(mark);
    }
  }
  return item;
}

// retryChannel 只重跑一个渠道；别的行不动（后端也只更新那一行）。
async function retryChannel(id) {
  // 单点某一行：主按钮不跟着灰（人提的）。
  beginCollectCall(true);
  fill("collect-progress", "正在重试…");
  try {
    try {
      state = await bridge.RetryChannel(id);
    } catch (err) {
      failureMessage = "没重试成：" + err;
      state = await readState(true);
    }
    render();
    if (failureMessage) {
      fill("collect-progress", failureMessage);
      failureMessage = "";
    }
  } catch (err) {
    fill("collect-progress", failureMessage || "没重试成：" + err);
  } finally {
    endCollectCall();
  }
}

// ── 主题 ──────────────────────────────────────────────────────────────────

// reportTheme 把「人选的那档 + 系统判到的那套」回给后端写日志，并刷新小图标。
//
// 为什么要有这一行：窗口「没跟着系统变」有两种可能——前端没判对，或窗口本身没收到
// 系统主题。有了这一行，一眼能分清是哪种；人手动选了亮/暗时，这里也会写出来。
function reportTheme() {
  const system = themeQuery.matches ? "dark" : "light";
  const shown = themeChoice === "auto" ? system : themeChoice;
  applyTheme();
  if (bridge && bridge.ReportTheme) {
    bridge.ReportTheme(themeChoice + " / 系统判到：" + system + " / 实际显示：" + shown).catch(() => {});
  }
}

// ── 渲染 ──────────────────────────────────────────────────────────────────

function render() {
  // 顶部那句话：没话说就整行不显示（人提的：别拿「配置就绪」这种小字占地方）。
  renderHeadline(state.headline);
  // wizardReplay：从设置页重走进来的那一遍。配置里早写着「走完了」，但那一步只说明
  // 首次运行结束，不代表人不想再看一遍（人问过「引导找不到了」）。
  const inWizard = !state.firstRunDone || wizardReplay;
  if (inWizard) {
    show("view-wizard");
    renderWizard();
  } else {
    show("view-main");
    renderMain();
  }
  reportView(inWizard ? "wizard" : "main");
}

function renderMain() {
  // 数字到手才收起过场：读取期间不先画旧值，避免「0 → 真数」的跳变。
  setReading(false);
  // 主界面不管配置（那些在设置页里）：这里只填「看」的那几处。
  // 渠道那一段顶上的数字（I-023）：由后端给，前端只摆。
  fill("channel-stats", state.channelStats);
  renderChannelRows($("main-channels"), state.channels);
  renderRun(state.lastRun);
  // 这次调用还没回来时，按钮就一直按着：旧快照说「没在收」也不能把它放开（I-052）。
  setCollecting(!!state.collecting || (collectingCalls > 0 && !collectingScoped));
}

// readState 读取主界面状态：先让过场占住面板，再等后端把统计读完。
//
// StateWithProgress 会在读取期间推事件；即使事件晚到，Promise 没回来之前
// 过场也一直显示，不会先露出旧数字。
//
// silent 为真时不显示过场：收集过程中每跑完一个渠道都会重读一次，
// 每次都盖一层「正在读取数据」会让人以为整轮重来了（I-052）。
async function readState(silent) {
  if (!silent) {
    setReading(true, "正在读取数据…", 0);
  }
  try {
    return await bridge.StateWithProgress();
  } catch (err) {
    // 读失败要把过场收起来，否则界面永远停在「正在读取数据」（I-052）。
    setReading(false);
    throw err;
  }
}

// ── 收集（I-014）────────────────────────────────────────────────────────────

// 收集期间按钮点不动；后端也有一道兜底（正在收的时候再点会被拒）。
function setCollecting(on) {
  // 单点某一行的时候，主按钮**照旧能点**（人提的：一个一个点、或一次全收，两件事随时都能做）。
  $("collect").disabled = !!on && !collectingScoped;
  // 行内那些箭头照旧一起开关：收集进行中不让再点，免得人以为点了没反应。
  for (const node of document.querySelectorAll(".mark--collect")) {
    node.disabled = !!on;
  }
}

// 按钮的开关**只由「这次调用还没回来」决定**，不看进度事件。
//
// 以前进度事件也去开关按钮，于是「事件晚一步到」就会把按钮一直按在禁用上——
// 人报的「点完某个渠道，增量收集就点不动了」就是这个。
let collectingCalls = 0;
// 这一轮是「单点某一行」还是「全部收一遍」。单点时不把主按钮按灰。
let collectingScoped = false;

function beginCollectCall(scoped) {
  collectingCalls++;
  collectingScoped = !!scoped;
  setCollecting(true);
}

function endCollectCall() {
  stopBusyTicker();
  collectingCalls = Math.max(0, collectingCalls - 1);
  if (collectingCalls === 0) {
    collectingScoped = false;
    // 拿后端刚给的状态为准：它说还在收，就还按着。
    setCollecting(!!(state && state.collecting));
  }
}

// renderRun 收完之后把那几个数字落在面板里。
//
// 顶部那句由后端给（headline），这里说的是「哪一轮、多久、备份怎么样」——
// 两处不重复：顶上一句话，面板里细节。
function renderRun(run) {
  if (!run) {
    // 没有本轮结果时，先看归档里有没有东西：有就不能说成「从来没收集过」（I-061）。
    const archived = (state && state.archivedTotal) || 0;
    // 归档里混了网页版捕获取来的份数时，把来源拆开说清楚（I-078）。
    const desktop = (state && state.archivedDesktop) || 0;
    const web = (state && state.archivedWeb) || 0;
    const split = web > 0 ? `（桌面端 ${desktop} · 网页版 ${web}）` : "";
    fill("collect-progress", archived > 0
      ? `本次启动还没收过，归档里已有 ${archived} 段${split}。`
      : "还没收过。");
    $("collect-hint").hidden = true;
    return;
  }

  if (run.err) {
    // 历史行只说「上次没跑完」，不冒充「这一轮」。
    const when = run.fromHistory ? "上次收集没跑完" : "这一轮没跑完";
    fill("collect-progress", when + "（" + run.at + "）");
  } else {
    // 重试也走这条路：它是「再跑一遍某个渠道」，不是收全部，别让它冒充「上次收集」。
    // 单点那一行：用时已经跟在那一行的勾后面了，这里就不再重复一遍（人提的）。
    if (run.scope) {
      fill("collect-progress", `上次单点收「${run.scope}」· ${run.at}`);
    } else {
      // 历史行没有存用时，只给时间，不显示「用时 」这种空话（I-061）。
      const took = run.duration ? ` · 用时 ${run.duration}` : "";
      fill("collect-progress", `上次收集 ${run.at}${took}`);
    }
  }

  // 这一行是「增量收集」这个动作的结论（这一轮收了多少段）。
  // 备份成没成不在首页讲（I-053）：它跟具体渠道无关，结果去日志页看。
  const hint = $("collect-hint");
  if (run.scope) {
    hint.textContent = "";
    hint.hidden = true;
    return;
  }

  const parts = [];
  if (run.found) {
    parts.push(`检测到 ${run.found} 段`);
  }
  if (run.fromHistory && run.archived) {
    parts.push(`收下 ${run.archived} 段`);
  }
  if (run.fromHistory && run.failures) {
    parts.push(`失败 ${run.failures} 处`);
  }
  hint.textContent = parts.join(" · ");
  hint.hidden = parts.length === 0;
}

// onProgress 是后端推过来的进度：点下去立刻就地换字。
function onProgress(progress) {
  if (!progress) {
    return;
  }
  // 备份是整轮的收尾，不在首页出现（I-053）：不换字、不挂秒数、也不写渠道行。
  // 结果在运行记录里，日志页看。
  if (progress.stage === "backup") {
    return;
  }
  // 只换那一行文字；按钮的开关在 beginCollectCall / endCollectCall 那儿（见上）。
  busyText = progress.text;
  fill("collect-progress", progress.text);

  switch (progress.stage) {
    case "channel":
      // 按行显示进度：那一行末尾从 0% 走到 100%，看着就不是卡住了（人提的）。
      if (progress.channel && progress.percent >= 0) {
        const mark = rowMark(progress.channel);
        if (mark) {
          delete mark.dataset.phase;
          mark.textContent = progress.percent + "%";
          mark.classList.add("mark--busy");
          mark.disabled = true;
        }
      }
      startBusyTicker();
      break;
    case "done":
      stopBusyTicker();
      break;
    default:
      startBusyTicker();
  }

  // 这个渠道跑完了：从后端重读一遍状态，把那一行画成准的（收下了就是勾）。
  // 用静默读法：收集还没结束，别让过场在每个渠道之间闪一次（I-052）。
  if (progress.finished) {
    refreshMainView(true);
  }
}

// onReadingProgress 只动读取过场，不碰收集按钮与渠道行（读取不是收集）。
function onReadingStarted(progress) {
  if (!readingActive) {
    return;
  }
  setReading(true, (progress && progress.text) || "正在读取数据…", progress ? progress.percent : 0);
}

function onReadingProgress(progress) {
  if (!progress || !readingActive) {
    return;
  }
  // done 只说明后端读完了，数字还要等 Promise 回来再由 renderMain 收起过场——
  // 在这里提前隐藏会先露出旧数字（I-052）。
  setReading(true, progress.text, progress.percent);
}

function onReadingFinished() {
  // 只把进度条画满；数字等 readState 的 Promise 返回后再露（见 renderMain）。
  if (!readingActive) {
    return;
  }
  fill("reading-text", "读取完成");
  $("reading-fill").style.width = "100%";
  $("reading-bar").setAttribute("aria-valuenow", "100");
}

// 一轮在跑的时候，手上要有一个**在动**的数字：不然耗时的步骤看着就像卡住了。
//
// 收某个渠道时按文件走百分比（那一行的 0%→100%）；检测这类数不出「第几个」的
// 步骤，就在面板那行后面跟一个走了几秒。备份不在首页显示（I-053）。
let busyText = "";
let busyTick = null;
let busyStart = 0;

function rowMark(channelID) {
  const row = document.querySelector(`.row[data-id="${channelID}"]`);
  return row ? row.querySelector(".mark") : null;
}

function startBusyTicker() {
  if (busyTick !== null) {
    return;
  }
  busyStart = Date.now();
  busyTick = setInterval(() => {
    const secs = Math.max(1, Math.round((Date.now() - busyStart) / 1000));
    fill("collect-progress", `${busyText}（已 ${secs} 秒）`);
  }, 500);
}

function stopBusyTicker() {
  if (busyTick !== null) {
    clearInterval(busyTick);
    busyTick = null;
  }
  busyText = "";
}

async function collect() {
  beginCollectCall(false);
  fill("collect-progress", "正在开始…");

  let failure = "";
  try {
    try {
      state = await bridge.CollectNow();
    } catch (err) {
      failure = String(err);
      state = await readState(true);
    }
    render();
    if (failure) {
      // 顶部已经有结论了（「收集没能完成：…」）；这一行留给「为什么点不动」这类原因。
      fill("collect-progress", "没收集成：" + failure);
    }
  } catch (err) {
    // 连状态都读不出来：把原因留在原地，别把界面清空。
    fill("collect-progress", "没收集成：" + (failure || err));
  } finally {
    endCollectCall();
  }
}

// ── 首次运行引导 ──────────────────────────────────────────────────────────

function renderWizard() {
  step = state.firstRunStep || 0;
  const last = state.firstRunSteps - 1;

  for (const section of document.querySelectorAll("#view-wizard [data-step]")) {
    section.hidden = Number(section.dataset.step) !== step;
  }
  // 首次运行与「重走」是两回事（人特意提过，这两条路不能长得一样）：
  //   首次：还没有能用的配置，走完才算配好——所以能「跳过」（先不配，之后在设置里改）；
  //   重走：配置早就在，只是再看一遍、顺手改一改——随时能「返回设置」，没有可跳的东西。
  fill("wizard-progress",
    (wizardReplay ? "重走引导 · " : "") + `第 ${step + 1} 步 · 共 ${state.firstRunSteps} 步`);

  // 第 1 步没有「上一步」；重走时右侧那个「返回设置」已经够用，
  // 左侧不再重复放一个一模一样的（I-075）。
  const back = $("wizard-back");
  back.hidden = step === 0;
  back.textContent = "上一步";

  $("wizard-next").textContent = step === last ? (wizardReplay ? "看完了" : "完成") : "下一步";

  const skip = $("wizard-skip");
  if (wizardReplay) {
    // 重走时那一位换成「返回设置」：任何一步都能回设置页。
    skip.hidden = false;
    skip.textContent = "返回设置";
  } else {
    // 首次引导不再给「跳过」：第三步保持默认「仅本地」，直接点「完成」（I-075）。
    skip.hidden = true;
  }

  // 第 1 步：归档目录（首次进入留空；已经走过 / 重走时才回填）
  //
  // 新用户要自己选目录，不拿配置里的默认值替人做决定（I-075）。
  const archive = $("wizard-archive");
  if (wizardReplay || state.firstRunStep > 0) {
    archive.value = state.archiveDir || "";
  }
  archive.placeholder = "";

  // 第 2 步：检测到的渠道
  renderChannelRows($("wizard-channels"), state.channels, false);

  // 第 3 步：备份方式与仓库地址
  renderBackupChoices();

  showWizardError("");
}

function renderBackupChoices() {
  const box = $("wizard-modes");
  box.replaceChildren();
  box.append(buildModeChoices(state.backupModes, state.backupMode, "backup-mode", syncWizardGit));

  syncWizardGit();
}

// buildModeChoices 造一组备份方式的单选项。
//
// 引导与高级设置共用这一份：备份方式就那么几种，两处各写一遍迟早会改漏一处。
// 返回的容器留着，刷新时只改选中状态、不重建——人正在做的事不该被打断。
function buildModeChoices(modes, current, name, onPick) {
  const box = el("div", "choices");
  for (const mode of modes || []) {
    const label = el("label", "choice");

    const input = document.createElement("input");
    input.type = "radio";
    input.name = name;
    input.value = mode.value;
    input.checked = mode.value === current;
    input.addEventListener("change", () => onPick(mode.value));

    label.append(input, el("span", "", mode.label));
    box.append(label);
  }
  return box;
}

// needsRemote 判断某种备份方式要不要仓库那一块（地址不用人填，后端从本地仓库里认）。
//
// 判断依据由后端给（modes[].needsRemote），前端不硬编码备份方式的标识。
function needsRemote(mode, modes) {
  return (modes || []).some((m) => m.value === mode && m.needsRemote);
}

// modeHintOf 取某种备份方式那句提示；没有就返回空串。
function modeHintOf(mode, modes) {
  const found = (modes || []).find((m) => m.value === mode);
  return found ? found.hint || "" : "";
}

// syncWizardGit 引导页第 3 步里的 Git 那一段：与设置页是同一块
//（人第二次澄清：文案与功能两端都要同步，别各写一遍）。
//
// 地址不再手填（人提的：有「试连」就够了）：远程从本地仓库里认出来。
let wizardGitTarget = null;

function syncWizardGit() {
  const picked = document.querySelector('input[name="backup-mode"]:checked');
  const needs = needsRemote(picked ? picked.value : "", state.backupModes);
  const box = $("wizard-git");
  box.hidden = !needs;
  if (!needs) {
    unmountGitPanel(wizardGitTarget);
    wizardGitTarget = null;
    return;
  }
  if (!wizardGitTarget) {
    wizardGitTarget = mountGitPanel(box);
  }
}

// 把当前这一步填的东西收集起来。仓库地址不在里面：远程由后端从本地仓库里认出来（I-029）。
function wizardInput(nextStep) {
  const picked = document.querySelector('input[name="backup-mode"]:checked');
  return {
    step: nextStep,
    archiveDir: $("wizard-archive").value.trim(),
    // 仓库地址不再有这一项：远程由后端从本地仓库里认出来（I-029）。
    backupMode: picked ? picked.value : "",
  };
}

// wizardArchiveMissing 报告首次引导第 1 步是否还没选目录。
//
// 首次运行不再拿默认目录替人做决定；重走引导允许沿用当前目录（I-075）。
function wizardArchiveMissing() {
  return !wizardReplay && step === 0 && $("wizard-archive").value.trim() === "";
}

// wizardSaving 挡住连点：两次 SaveWizard 的响应谁后到谁覆盖 state，步号会乱（I-052）。
let wizardSaving = false;

function setWizardBusy(busy) {
  wizardSaving = !!busy;
  for (const id of ["wizard-back", "wizard-next", "wizard-skip"]) {
    const node = $(id);
    if (node) {
      node.disabled = !!busy;
    }
  }
}

async function saveStep(nextStep) {
  if (wizardSaving) {
    return;
  }
  setWizardBusy(true);
  try {
    state = await bridge.SaveWizard(wizardInput(nextStep));
    render();
  } catch (err) {
    showWizardError("没能保存：" + err);
  } finally {
    setWizardBusy(false);
  }
}

async function finishWizard() {
  if (wizardSaving) {
    return;
  }
  setWizardBusy(true);
  const last = state.firstRunSteps - 1;
  const replay = wizardReplay;
  try {
    state = await bridge.SaveWizard(wizardInput(last));
    state = await bridge.FinishWizard();
    if (replay) {
      // 重走：从设置页进来的，看完/改完就回设置页去（人通常还想接着调别的）。
      // 打不开设置页时保留 wizardReplay，别把「返回设置」变成死按钮（I-052）。
      if (await openSettings()) {
        wizardReplay = false;
      }
      return;
    }
    wizardReplay = false;
    render();
  } catch (err) {
    showWizardError("没能完成：" + err);
  } finally {
    setWizardBusy(false);
  }
}

// ── 设置页（I-017）────────────────────────────────────────────────────────
//
// 整窗切页，不是侧边浮层：窗口本来就不大（860×640，最小 560×420），抽屉再切
// 一刀，剩下的宽度装不下这些内容。
//
// 四条规矩：
//   1. 没进来就不渲染——设置页的内容是进来时现建的，返回就清空（验收标准 5）；
//   2. 有没保存的改动时，点「返回」先问一句，不默默丢掉（验收标准 6）；
//   3. 切一下就能生效的项（备份方式、定时的开关与时间）当场就存（验收标准 2）；
//      手打的路径要按「保存」才落盘——不然敲到一半就被存下去了；
//   4. 「下次运行时间」取后端从系统里读回来的值，不是我们配置里的（验收标准 3）。

let settings = null;
let form = null;
let settingsDirty = false;
let settingsAsking = false;
let settingsMessage = "";
// settingsMessageKind 决定那句话怎么显示：error（红）/ ok（绿）（I-054）。
let settingsMessageKind = "error";
// pendingView：被「有改动还没保存」拦住时，人原本要去哪一页（现在只有日志）。
let pendingView = "";
// wizardReplay：这一遍引导是从设置页重走进来的，不是首次运行。
let wizardReplay = false;

// SETTINGS_FAQ 是设置页「答疑」的数据源：加一条只加一项，不动渲染（I-074）。
const SETTINGS_FAQ = [
  {
    question: "Cherry Studio 迁移失败怎么办？",
    answer:
      "先备份整个 %APPDATA%\\CherryStudio 目录，别反复启动 v2 重试。" +
      "确认升级路径是先到 v1.9.13 再装 v2；如果改过数据目录，迁移读的是 ~/.cherrystudio/config/config.json 指向的那一份，v2 必须指向同一个目录。" +
      "v1 的原始数据还在 IndexedDB（%APPDATA%\\CherryStudio\\IndexedDB\\file__0.indexeddb.leveldb），迁移失败不会删它；恢复 v1 备份后按正确版本路径重试，v2 的迁移失败页也可以导出诊断包。" +
      "AgentMemory 读的是迁移完成后的 Data\\cherrystudio.sqlite，迁移没完成前读不到。",
  },
];

async function openSettings() {
  // 已经在这一页：不重建表单。人可能是点了两次导航，正在敲的路径不能被清掉（I-052）。
  if (!$("view-settings").hidden) {
    return true;
  }
  pendingView = "";
  try {
    settings = await bridge.Settings();
  } catch (err) {
    // 从引导页重走时进来的话，主界面是藏着的——报错要写在看得见的那一页（I-052）。
    if (!$("view-wizard").hidden) {
      showWizardError("打不开设置：" + err);
    } else {
      fill("collect-progress", "打不开设置：" + err);
    }
    return false;
  }

  settingsDirty = false;
  settingsAsking = false;
  settingsMessage = "";
  settingsMessageKind = "error";
  renderSettings();
  show("view-settings");
  reportView("settings");
  return true;
}

// group 造设置页里的一段面板（与主界面同一套面板样式）。
function group(title, ...children) {
  const box = el("section", "panel");
  box.append(el("h3", "panel__title", title), ...children);
  return box;
}

function hint(text) {
  return el("p", "hint", text);
}

function inputRow(...children) {
  const row = el("div", "input-row");
  row.append(...children);
  return row;
}

// renderFAQ 把 SETTINGS_FAQ 铺成一组可展开的问答；第一条默认展开（I-074）。
function renderFAQ() {
  const list = el("div", "faq");
  SETTINGS_FAQ.forEach((item, index) => {
    const details = el("details", "faq__item");
    details.open = index === 0;
    details.append(el("summary", "faq__q", item.question), el("p", "faq__a", item.answer));
    list.append(details);
  });
  return list;
}

// renderSettings 现建抽屉里的内容。
function renderSettings() {
  form = {};

  const archive = el("input", "input");
  archive.type = "text";
  archive.spellcheck = false;
  archive.id = "set-archive";
  archive.value = settings.archiveDir || "";
  archive.placeholder = settings.archiveDefault || "";
  archive.addEventListener("input", markSettingsDirty);
  form.archive = archive;

  const browse = button("选择文件夹", "btn btn--quiet", () => browseDirectory(archive));
  form.browse = browse;

  // 换目录时数据怎么办，先让人选（人提的：直接给选项，选了才让点「选择文件夹」）。
  // 选了就记成「有改动」：直接返回时也会被拦一下（I-052）。
  form.archiveSwap = buildModeChoices(
    [
      { value: "copy", label: "换目录，并把旧目录里的归档复制过去" },
      { value: "keep", label: "换目录，旧目录里的归档留在原地" },
    ],
    "",
    "archive-swap",
    () => {
      syncArchiveSwap();
      markSettingsDirty();
    }
  );
  form.archiveSwapHint = el("p", "hint");
  form.archiveSwapChoice = "";
  syncArchiveSwap();

  // 切一下当场就存（验收标准 2：切换后立即生效）。
  form.modeBox = buildModeChoices(settings.backupModes, settings.backupMode, "set-backup-mode", (mode) =>
    saveSettings({ backupMode: mode })
  );

  const scheduleOn = document.createElement("input");
  scheduleOn.type = "checkbox";
  scheduleOn.id = "set-schedule-on";
  scheduleOn.checked = !!settings.scheduleEnabled;
  scheduleOn.addEventListener("change", () => saveSettings({ scheduleEnabled: scheduleOn.checked }));
  form.scheduleOn = scheduleOn;

  const scheduleAt = el("input", "input");
  scheduleAt.type = "time";
  scheduleAt.id = "set-schedule-at";
  scheduleAt.value = settings.scheduleDailyAt || "09:00";
  scheduleAt.addEventListener("change", () => saveSettings({ scheduleDailyAt: scheduleAt.value }));
  form.scheduleAt = scheduleAt;

  form.scheduleDetail = el("p", "hint");

  // 对外接口（F15 / I-091）：默认关；打开后外部 Agent 才拉得起 MCP 服务。
  const mcpOn = document.createElement("input");
  mcpOn.type = "checkbox";
  mcpOn.id = "set-mcp-on";
  mcpOn.checked = !!settings.mcpEnabled;
  mcpOn.addEventListener("change", () => saveSettings({ mcpEnabled: mcpOn.checked }));
  form.mcpOn = mcpOn;
  const mcpLabel = el("label", "choice");
  mcpLabel.append(mcpOn, el("span", "", "允许外部 Agent 通过 MCP 读取这个库（只读）"));
  form.mcpHint = el("p", "hint");

  // 选中哪一种备份方式，就跟哪一句话（话由后端给，见 ui.go 的 backupModeHints）。
  form.modeHint = el("p", "hint");

  // Git 那一段：选 Git 仓库时才去问后端（别的模式不必打扰 git 与网络）。
  form.gitPanel = el("div", "field field--stack");

  const onLabel = el("label", "choice");
  onLabel.append(scheduleOn, el("span", "", "每天自动收一次"));

  // 归档目录换了之后的那句提示，以及「把旧目录里的 N 段复制过来」那个按钮（I-024）。
  form.archiveNote = el("p", "hint");
  form.migrateButton = button("复制旧目录里的归档", "btn btn--quiet", migrateArchive);
  form.migrateRow = inputRow(form.migrateButton);
  form.migrateResult = el("p", "hint");

  $("settings-body").replaceChildren(
    group(
      "归档目录",
      hint("收上来的聊天记录变成 Markdown 放在这里。换目录的话，先选旧归档怎么处理，再去挑文件夹。"),
      form.archiveSwap,
      form.archiveSwapHint,
      inputRow(archive, browse),
      form.archiveNote,
      form.migrateRow,
      form.migrateResult
    ),
    group("备份方式", form.modeBox, form.modeHint, form.gitPanel),
    group(
      "定时",
      hint("开着的时候由系统的计划任务在每天这个点跑一次：先增量收集写到归档目录，再按上面的备份方式备份（选 Git 就自动 add / commit / push）。程序本身不常驻。"),
      inputRow(onLabel, scheduleAt),
      form.scheduleDetail
    ),
    group(
      "对外接口",
      hint("打开后，dsh / Claude Code 这类外部 Agent 可以用 MCP 只读地查这个库：检索、读会话、读维度、导出切片、统计。它不能写库，也不能触发收集。配置时命令填 exe 路径、参数填 -mcp，两个框分开（ccs 里最容易把 -mcp 一起填进命令框）。"),
      inputRow(mcpLabel),
      form.mcpHint
    ),
    group("配置文件", hint(settings.configPath || "")),
    group(
      "首次运行引导",
      hint("哪一项该怎么填忘了？可以重走一遍那三步：选归档目录 → 看渠道 → 定备份方式。走的还是同一套步骤，只改上面这些项，不动你的文件。"),
      inputRow(button("重走一遍引导", "btn btn--quiet", replayWizard))
    ),
    group("答疑", hint("这里放一些容易混淆的说明；以后有新问题会继续加。"), renderFAQ())
  );

  refreshSettings();
}

// refreshSettings 把抽屉里「跟着后端状态走」的地方刷新一遍。
//
// 不重建输入框：人敲到一半的路径被覆盖掉是最恼人的事。
function refreshSettings() {
  // 「旧目录里还留着 N 份」这类提示：后端不说就没有，不占位置。
  form.archiveNote.textContent = settings.archiveNote || "";
  form.archiveNote.hidden = !settings.archiveNote;

  // 路径以配置为准回填：后端可能忽略了空串（「归档目录不能为空」），
  // 不回填的话输入框会停在一个人以为存上了的值上（I-052）。
  //
  // 但有未保存的改动时（人刚敲了路径，又去切了一下备份方式触发了自动保存），
  // 绝不能拿旧配置把人正在敲的值盖掉。
  if (!settingsDirty && document.activeElement !== form.archive) {
    form.archive.value = settings.archiveDir || "";
  }

  // 换过归档目录才有这段：按钮上写清有多少段要搬。
  const oldDir = settings.archiveOld || "";
  form.migrateRow.hidden = oldDir === "";
  if (oldDir !== "") {
    form.migrateButton.textContent = "复制旧目录里的 " + settings.archiveOldCount + " 段归档过来";
    form.migrateButton.disabled = !!state.collecting;
  }

  for (const radio of form.modeBox.querySelectorAll("input[type=radio]")) {
    radio.checked = radio.value === settings.backupMode;
  }
  // 选中的那种方式跟的那句话：没有就不占位置。
  form.modeHint.textContent = modeHintOf(settings.backupMode, settings.backupModes);
  form.modeHint.hidden = form.modeHint.textContent === "";
  form.scheduleOn.checked = !!settings.scheduleEnabled;
  if (document.activeElement !== form.scheduleAt) {
    form.scheduleAt.value = settings.scheduleDailyAt || "09:00";
  }
  form.scheduleDetail.textContent = scheduleLine(settings);
  form.mcpOn.checked = !!settings.mcpEnabled;
  form.mcpHint.textContent = settings.mcpEnabled
    ? "命令：" + (settings.mcpCommand || "") + " ｜ 参数：" + (settings.mcpArgs || "-mcp")
    : "";
  form.mcpHint.hidden = !settings.mcpEnabled;
  renderSettingsFoot();

  // 选了 Git 仓库才挂上那一块；别的模式不打扰 git 与网络。
  syncGitPanel();
}

// syncArchiveSwap 看「换目录时旧归档怎么办」选没选：没选就不让点「选择文件夹」（人提的）。
function syncArchiveSwap() {
  const picked = document.querySelector('input[name="archive-swap"]:checked');
  form.archiveSwapChoice = picked ? picked.value : "";
  form.browse.disabled = form.archiveSwapChoice === "";
  form.archiveSwapHint.textContent = form.archiveSwapChoice === "" ? "先在上面选一项，再挑文件夹。" : "";
  form.archiveSwapHint.hidden = form.archiveSwapChoice !== "";
}

// resetArchiveSwap 把「换目录怎么办」的选择清回未选。
//
// 只在两处调：进设置页现建表单时；路径真的保存成功之后。
// 不能放在 refreshSettings 里——自动保存（切备份方式 / 定时）会把
// 人正在做的选择清掉，下一次保存还会静默不复制（I-052）。
function resetArchiveSwap() {
  if (!form || !form.archiveSwap) {
    return;
  }
  for (const radio of form.archiveSwap.querySelectorAll("input[type=radio]")) {
    radio.checked = false;
  }
  form.archiveSwapChoice = "";
  syncArchiveSwap();
}

// syncGitPanel 按「当前选的是不是 Git 仓库」挂上 / 摘掉设置页里那一块。
//
// 已经挂上了就不重挂：重挂会清掉签名，把本来不用画的又整块画一遍
//（人报的「点备份方式时窗口有重影」）。
function syncGitPanel() {
  const needsGit = needsRemote(settings.backupMode, settings.backupModes);
  form.gitPanel.hidden = !needsGit;
  if (!needsGit) {
    unmountGitPanel(form.gitTarget);
    form.gitTarget = null;
    return;
  }
  if (!form.gitTarget) {
    form.gitTarget = mountGitPanel(form.gitPanel);
  }
  ensureGitGuide();
}

// ── Git 那一段（I-029：1-2-3 三步）──────────────────────────────────────
//
// 三步：① 建仓库（本机建 + 网页建一个空的）② 把一段话交给自己的 AI，让它带着你
// 建远程、配密钥 ③ 在这里验一次连接（认出远程就自己试）。
// 地址不再手填：远程从本地仓库里认出来，认到了保存时就记进配置。
//
// 这一块**两端共用**（设置页与引导页挂的是同一份渲染）。样式刻意压得比别的模块轻——
// 它是一段说明，不该比「定时」那一块还抢眼（人提的）。
// 状态与话都从后端来（bridge.GitGuide），前端只负责摆出来、把按钮接上。

let git = null;
let gitFetching = false;
let gitTesting = false;
let gitTargets = [];
let gitAutoTested = false;

// mountGitPanel 把这一块挂到一个位置上（设置页 / 引导页），返回的位置用来摘掉。
function mountGitPanel(box) {
  const target = { box: box, sig: "" };
  gitTargets.push(target);
  if (git) {
    renderGitPanel(target);
  } else {
    // 先把位置占住一句话，别让这一块从 0 长出来——那会让整页重排一次，
    // 而这次重排正落在人点按钮的那一刻上。
    box.replaceChildren(el("p", "hint", "正在读 Git 状态…"));
  }
  ensureGitGuide();
  return target;
}

// unmountGitPanel 摘掉一个位置（抽屉收起来、不选 Git 仓库了）。
//
// 摘的时候把位置连同签名一起丢掉：下次挂上来该画就画。
function unmountGitPanel(target) {
  if (!target) {
    return;
  }
  gitTargets = gitTargets.filter((one) => one !== target);
  if (target.box) {
    target.box.replaceChildren();
  }
}

// ensureGitGuide 读一次 Git 状态（同一次里几处只读一遍），再画到每个位置上。
async function ensureGitGuide() {
  if (gitFetching) {
    return;
  }
  gitFetching = true;
  let guide;
  try {
    guide = await bridge.GitGuide();
  } catch (err) {
    gitFetching = false;
    for (const target of gitTargets) {
      target.sig = "";
      target.box.replaceChildren(el("p", "error", "读 Git 状态失败：" + err));
    }
    return;
  }
  gitFetching = false;
  git = guide;
  renderGitPanels();

  // 第 3 步是「自动试一次」：认出远程就自己试，不用人点（一次只做一次）。
  // 只读的一条 ls-remote，带超时，不推任何东西。
  if (!gitAutoTested && guide.repoReady && guide.detectedRemote) {
    gitAutoTested = true;
    gitTestRemote(guide.detectedRemote);
  }
}

function renderGitPanels() {
  for (const target of gitTargets) {
    renderGitPanel(target);
  }
}

// renderGitPanel 画这一块。状态跟上次一样就不重画——
//
// 重画一次 = 这一块重排一次，而这块内容是在 git 命令回来之后才画的（约两百
// 毫秒后），正赶上人的视线落在按钮上。这一层是「重影」的另一半。
function renderGitPanel(target) {
  const box = target.box;
  const sig = JSON.stringify(git);
  if (sig === target.sig && box.childElementCount > 0) {
    return;
  }
  target.sig = sig;

  box.replaceChildren();

  // 现状压成一行小字：这一块要轻，不能比别的模块占地方。
  const now = [];
  now.push(git.gitAvailable ? "git 已就绪" + (git.gitVersion ? " " + git.gitVersion : "") : "没找到 git");
  now.push(git.repoReady ? "本机已是仓库" : "本机还不是仓库");
  if (git.detectedRemote) {
    now.push("远程 " + git.detectedRemote);
  }
  box.append(el("p", "git-now", now.join(" · ")));

  // git 都没装：先把这一步做完（它不是三步里的一步，是前提）。
  if (!git.gitAvailable) {
    box.append(el("p", "hint", git.note));
    box.append(row(button("重新检查", "btn btn--quiet btn--small", ensureGitGuide)));
    return;
  }

  // 编号只写在标题里（列表本身不再自动编号——人看到的 1.1 / 2.2 / 3.3 就是这么来的）。
  const steps = el("ol", "git-steps");

  const one = el("li", "git-step");
  one.append(el("p", "git-step__title", "1. 建仓库"));
  one.append(el("p", "hint", "本机建一个；网页上那个要「空」仓库（别勾「初始化 README」）。"));
  const oneRow = el("div", "input-row");
  if (!git.repoReady) {
    oneRow.append(button("就地建仓库", "btn btn--quiet btn--small", gitInit));
  }
  oneRow.append(button("打开新建仓库页", "btn btn--quiet btn--small", openRepoPage));
  one.append(oneRow);
  steps.append(one);

  const two = el("li", "git-step");
  two.append(el("p", "git-step__title", "2. 交给你的 AI"));
  two.append(el("p", "hint", "把这段复制给你的 AI：它会一步一步带你建仓库、配密钥、配远程。"));
  two.append(el("pre", "prompt-box", git.prompt || ""));
  two.append(row(button("复制这段 Prompt", "btn btn--quiet btn--small", copyGitPrompt)));
  steps.append(two);

  const three = el("li", "git-step");
  three.append(el("p", "git-step__title", "3. 验一次"));
  if (git.note) {
    three.append(el("p", "hint", git.note));
  }
  const testButton = button(gitTesting ? "正在测试…" : (git.tested ? "再试一次" : "试连"), "btn btn--quiet btn--small", () => gitTestRemote(""));
  testButton.disabled = gitTesting;
  three.append(row(testButton));
  if (gitTesting) {
    three.append(el("p", "hint", "正在测（只读检查，不推任何东西）…"));
  } else if (git.tested) {
    three.append(gitResult(git));
  }
  steps.append(three);

  box.append(steps);
}

// gitResult 造试连的结果那一行。
//
// 连上了就是一个**绿色的勾**（人提的：打勾最直观，一眼就知道成没成）；
// 没连上给一个叉加说明——两边的形状一样，只用颜色和符号区分。
function gitResult(current) {
  const line = el("p", current.ok ? "git-result git-result--ok" : "git-result git-result--bad");
  line.append(
    el("span", "git-result__mark", current.ok ? "✓" : "✗"),
    el("span", "", current.detail)
  );
  return line;
}

// row 造一行按钮（几处的摆法一致）。
function row() {
  const node = el("div", "input-row");
  for (const one of arguments) {
    node.append(one);
  }
  return node;
}

async function copyGitPrompt(event) {
  const node = event && event.currentTarget ? event.currentTarget : null;
  try {
    await bridge.CopyText(git && git.prompt ? git.prompt : "");
  } catch (err) {
    gitSay("复制失败：" + err);
    return;
  }
  if (node) {
    node.textContent = "已复制 ✓";
    // 给一眼反馈，但别让它永远停在那儿（I-052）。
    setTimeout(() => {
      if (node.isConnected) {
        node.textContent = "复制这段 Prompt";
      }
    }, 1500);
  }
}

// gitSay 把一句系统错放到看得见的地方：设置页有底部那行，引导页就写在面板里。
function gitSay(text) {
  if (form && form.footMessage && $("view-settings").hidden === false) {
    // 这是错话，不能沿用上一句「已保存」的绿（I-054）。
    settingsMessage = text;
    settingsMessageKind = "error";
    renderSettingsFoot();
    return;
  }
  for (const target of gitTargets) {
    target.box.append(el("p", "error", text));
  }
}

async function gitInit() {
  try {
    git = await bridge.GitInit();
  } catch (err) {
    gitSay("建仓库失败：" + err);
    return;
  }
  renderGitPanels();
}

async function openRepoPage() {
  try {
    await bridge.OpenRepoPage();
  } catch (err) {
    gitSay("打不开浏览器：" + err);
  }
}

// gitTestRemote 试一次连接。地址不再从输入框来：后端认得出来。
//
// 点下去立刻给反馈（I-058）：按钮置灰、文字换「正在测试…」，测试中重复点
// 不会再发第二次请求；结束（成或败）后恢复按钮并画出结果。
async function gitTestRemote(remoteOverride) {
  if (gitTesting) {
    return;
  }
  gitTesting = true;
  // 测试中的样子不来自后端返回（git 没变，签名叫它不重画），
  // 所以先把签名清掉强制画一次「正在测试…」。
  for (const target of gitTargets) {
    target.sig = "";
  }
  renderGitPanels();
  let startError = "";
  try {
    git = await bridge.GitTestRemote(remoteOverride || "");
  } catch (err) {
    startError = "试连没能开始：" + err;
  } finally {
    gitTesting = false;
  }
  renderGitPanels();
  if (startError) {
    gitSay(startError);
  }
}

// scheduleLine 写定时那一段底下那句话。
//
// 「上次运行」取后端从运行记录里读回的最近一次定时运行，放在「下次」前面
// （I-073）：先看上一轮跑没跑，再看下一轮什么时候来。
function scheduleLine(current) {
  const parts = [];
  if (current.scheduleLastRun) {
    parts.push("上次运行 " + current.scheduleLastRun);
  }
  if (current.scheduleInstalled && current.scheduleNextRun) {
    parts.push("系统里的任务：下次 " + current.scheduleNextRun);
  } else if (current.scheduleInstalled) {
    parts.push("系统里有这个任务，但读不出下次运行时间");
  } else {
    parts.push("系统里没有这个任务");
  }
  if (current.scheduleNote) {
    parts.push(current.scheduleNote);
  }
  return parts.join(" · ");
}

function markSettingsDirty() {
  settingsDirty = true;
  // 又动了一处：上一句「已保存」不再成立，得收起来（I-054）。
  settingsMessage = "";
  settingsMessageKind = "error";
  renderSettingsFoot();
}

// renderSettingsFoot 画底部那一行。
//
// 节点**只建一次**（buildSettingsFoot），之后只改字与显隐。以前是每来一次都整个
// 重建：敲一个字、存一下就把按钮删了重造，按钮会闪一下，鼠标正停在上面也会掉
// ——人报的「点按钮时窗口闪」就有这一份。
//
// 平常是「保存」；有没保存的改动被拦下时，换成一句询问加两个出路（验收标准 6）。
function renderSettingsFoot() {
  if (!form || !form.footSave) {
    buildSettingsFoot();
  }

  form.footMessage.textContent = settingsMessage || "";
  form.footMessage.className = settingsMessageKind === "ok" ? "hint foot__ok" : "error";
  form.footMessage.hidden = !settingsMessage;

  form.footAsk.hidden = !settingsAsking;
  form.footGiveUp.hidden = !settingsAsking;
  form.footKeep.hidden = !settingsAsking;
  form.footSave.hidden = settingsAsking;
  form.footDirty.hidden = settingsAsking;
  form.footDirty.textContent = settingsDirty ? "有改动还没保存" : "";
}

// buildSettingsFoot 只在设置页现建的时候建这一份节点，之后交给上面那个函数改字。
function buildSettingsFoot() {
  const foot = $("settings-foot");
  foot.replaceChildren();

  form.footMessage = el("p", "error");
  form.footAsk = hint("有改动还没保存，返回就丢了。");
  form.footGiveUp = button("放弃改动", "btn", () => {
    const next = pendingView;
    pendingView = "";
    settingsDirty = false;
    settingsAsking = false;
    hideSettings();
    // 人是点「日志」被拦下的：放弃改动之后把这一程走完（I-052）。
    if (next === "log") {
      openLog();
    }
  });
  form.footKeep = button("继续编辑", "btn btn--primary", () => {
    settingsAsking = false;
    renderSettingsFoot();
  });
  form.footSave = button("保存", "btn btn--primary", saveSettingsEdits);
  form.footDirty = el("span", "hint");

  // 「保存」那一行靠右（人提的）：它是最常按的那一下，放在右下角顺手。
  const saveRow = inputRow(form.footSave, form.footDirty);
  saveRow.classList.add("input-row--end");

  foot.append(form.footMessage, form.footAsk, inputRow(form.footGiveUp, form.footKeep), saveRow);
}

// saveSettingsEdits 按「保存」时把那两个手打的输入框写下去。
//
// 单选项与定时的开关、时间不在这里：它们切一下就已经存过了。
async function saveSettingsEdits() {
  const archiveDir = form.archive.value.trim();
  if (archiveDir === "") {
    // 后端遇到空串会「当作没改」，不能让它悄悄成功（I-052）。
    settingsMessage = "归档目录不能为空";
    settingsMessageKind = "error";
    renderSettingsFoot();
    return;
  }
  const ok = await saveSettings({
    archiveDir: archiveDir,
    copyOldArchives: form.archiveSwapChoice === "copy",
    scheduleDailyAt: form.scheduleAt.value,
  });
  if (!ok) {
    // 保存失败：保留「有改动」，人还能再试；提示已经写在底部。
    renderSettingsFoot();
    return;
  }
  // 路径真的存下去了才清选择：自动保存不会碰它（I-052）。
  resetArchiveSwap();
  // 以后端归一化后的值为准（末尾斜杠之类），别让人以为没存成。
  form.archive.value = settings.archiveDir || archiveDir;
  settingsDirty = false;
  // 存上了就明说一声，别只把「有改动」收起来（I-054）。
  settingsMessage = "已保存";
  settingsMessageKind = "ok";
  renderSettingsFoot();
}

// saveSettings 存一项或几项，然后刷新抽屉与主界面。
// 返回是否成功——调用方据此决定要不要清「有改动」（I-052）。
async function saveSettings(partial) {
  settingsMessage = "";
  settingsMessageKind = "error";
  let ok = true;
  try {
    settings = await bridge.SaveSettings(partial);
  } catch (err) {
    ok = false;
    // 报错要报在抽屉里：进度那一行在遮罩后面，人看不见。
    settingsMessage = "没存上：" + err;
    try {
      settings = await bridge.Settings();
    } catch (ignored) {
      settingsMessage = "没存上，而且读不回当前设置：" + err;
    }
  }
  // 成功与失败都要按真实状态重画：失败时单选项不能停在没存上的那一档（I-052）。
  refreshSettings();
  // 这里**不**刷主界面：State() 要重新检测一遍所有渠道（几百次文件系统调用），
  // 而人这会儿正停在设置页上，刷了也看不见，只会让每次改动都卡一下。
  // 等离开设置页回主界面时统一刷一次。
  return ok;
}

// refreshMainView 设置页里改完之后，主界面上显示的那几项也要跟着变。
// silent 由调用方决定：回到首页要显示读取过场；收集过程中每完成一个渠道则静默重读。
async function refreshMainView(silent) {
  try {
    state = await readState(silent);
  } catch (ignored) {
    return;
  }
  renderHeadline(state.headline);
  renderMain();
}

// browseDirectory 打开系统的文件夹选择框，选中的路径填进输入框。
//
// 取消（对话框返回空）时什么都不改——「没选」不是「清空」（验收标准 8）。
async function browseDirectory(input) {
  try {
    const picked = await bridge.BrowseDirectory(input.value.trim());
    if (picked) {
      input.value = picked;
      markSettingsDirty();
    }
  } catch (err) {
    settingsMessage = "打不开文件夹选择框：" + err;
    settingsMessageKind = "error";
    renderSettingsFoot();
  }
}

// pickDirectory 引导页第 1 步的「选择文件夹」：选中的路径只填进输入框，
// 等「下一步」时一起存；这里不标「有改动」（引导没有保存按钮）。
async function pickDirectory(input) {
  try {
    const picked = await bridge.BrowseDirectory(input.value.trim());
    if (picked) {
      input.value = picked;
    }
    return {};
  } catch (err) {
    return { err: err };
  }
}

// migrateArchive 把旧归档目录里的归档复制过来（I-024）。只复制、不搬。
//
// 搬完要**当场重算**：已收集、待收集都跟着这次搬动变——三个数都是现数的
// （已收集数的是当前归档目录），所以刷一次主界面就对了（人专门提过这点）。
async function migrateArchive() {
  form.migrateButton.disabled = true;
  try {
    const result = await bridge.MigrateArchive();
    form.migrateResult.textContent = result.message;
    settingsMessage = "";
  } catch (err) {
    form.migrateResult.textContent = "没搬成：" + err;
  }
  form.migrateResult.hidden = false;

  await refreshMainView(true);
  try {
    settings = await bridge.Settings();
  } catch (ignored) {
    // 读不回设置也别把按钮留在灰上：这一下没搬完，人还要能再点（I-052）。
    form.migrateButton.disabled = false;
    return;
  }
  refreshSettings();
}

// leaveSettings 是「要离开设置页」：有没保存的改动先问一句。
//
// 返回 false 表示留住了（人还没决定），调用方就别往下走。
function leaveSettings() {
  if (settingsDirty) {
    settingsAsking = true;
    renderSettingsFoot();
    return false;
  }
  return true;
}

function closeSettings() {
  if ($("view-settings").hidden) {
    // 不在设置页：导航栏的「首页」还要能把日志页切回主界面。
    // （以前这里直接 return，人在日志页点「首页」会像按钮坏了。）
    if (!$("view-log").hidden) {
      show("view-main");
      reportView("main");
      // 回到首页先显示读取过场，读完再露数字（I-051 标准 4）。
      refreshMainView();
    }
    return;
  }
  pendingView = "";
  if (!leaveSettings()) {
    return;
  }
  hideSettings();
}

// channelMark 造渠道行末尾那个标记（人提的：两种状态，别再用「重试」两个字）。
//
//   还有没转移的内容（或上一轮失败）：一个转圈似的箭头 —— **能点**，点一下就收这个渠道；
//   没有新内容：一个绿勾，不用点。
// 后端给哪一档（row.action），前端只负责摆出来。
function channelMark(row) {
  if (row.action === "done") {
    // 刚单点收过这一行：勾后面跟上用时（人提的：那段话该在这儿，不在顶部）。
    const done = el("span", "mark mark--done", row.lastDuration ? "✓ " + row.lastDuration : "✓");
    done.title = row.lastDuration ? `刚收完，用时 ${row.lastDuration}` : "没有新内容";
    return done;
  }
  if (row.action !== "collect") {
    return null;
  }
  const go = el("button", "mark mark--collect", "↻");
  go.type = "button";
  // 两个待收数分开传（新会话 / 已收集有更新）；提示里给个总数，明细在行说明里。
  const pending = (row.pendingNew || 0) + (row.pendingUpdated || 0);
  go.title = pending > 0 ? `还有 ${pending} 个会话没收完，点一下收它` : "点一下收它";
  go.disabled = !!state.collecting;
  go.addEventListener("click", () => retryChannel(row.id));
  return go;
}

// ── 搜索页（I-084 / F13）──────────────────────────────────────────────
//
// 关键词全文检索：后端查 FTS5 索引，这里只摆输入框、结果行与三态提示
//（没输入 / 没命中 / 失败）。进来才聚焦，搜完就地出结果。

async function openSearch() {
  if (!$("view-settings").hidden && !leaveSettings()) {
    pendingView = "search";
    return;
  }
  pendingView = "";
  show("view-search");
  reportView("search");
  $("search-input").focus();
  await fillSearchOptions();
}

// fillSearchOptions 用维度表里的渠道与项目填过滤下拉；填不上不挡搜索。
async function fillSearchOptions() {
  try {
    const opts = await window.go.main.App.SearchOptions();
    const channelSelect = $("search-channel");
    // 「全部渠道」必须带空 value：不写的话浏览器拿显示文字当值，
    // 后端按渠道精确过滤就查不到任何东西（I-084 验收反馈）。
    const allChannels = el("option", "", "全部渠道");
    allChannels.value = "";
    channelSelect.replaceChildren(allChannels);
    for (const one of opts.channels || []) {
      const node = document.createElement("option");
      node.value = one.value;
      node.textContent = one.label;
      channelSelect.append(node);
    }
    const projectList = $("search-project-options");
    projectList.replaceChildren();
    for (const project of opts.projects || []) {
      const node = document.createElement("option");
      node.value = project;
      projectList.append(node);
    }
  } catch (err) {
    $("search-note").textContent = "过滤下拉没加载出来，仍可按关键词搜索。";
    $("search-note").hidden = false;
  }
}

let lastSearchQuery = "";
// searchTimer 是「边打边搜」的计时器：回车 / 点按钮 / 改过滤条件都立刻查，
// 打字停手 500 毫秒才查一次。
let searchTimer = 0;

async function runSearch() {
  if (searchTimer) {
    clearTimeout(searchTimer);
    searchTimer = 0;
  }
  const query = $("search-input").value.trim();
  lastSearchQuery = query;
  const list = $("search-rows");
  const note = $("search-note");
  list.replaceChildren();
  if (!query) {
    note.textContent = "输入一个关键词再搜。";
    note.hidden = false;
    return;
  }
  const go = $("search-go");
  go.disabled = true;
  note.textContent = "正在搜…";
  note.hidden = false;
  try {
    const channelID = $("search-channel").value;
    const days = Number($("search-days").value) || 0;
    const project = $("search-project").value.trim();
    const hits = await window.go.main.App.SearchMemory(query, channelID, project, days);
    note.textContent = hits.length
      ? `命中 ${hits.length} 条`
      : "没有命中的内容，换个词试试。";
    note.hidden = false;
    list.replaceChildren(...(hits || []).map(searchRow));
  } catch (err) {
    note.textContent = "搜索没做成：" + err + " 可以再试一次。";
    note.hidden = false;
  } finally {
    go.disabled = false;
  }
}

// exportSlice 把当前检索词的命中导出成 NDJSON（F14）：落点由后端定，
// 界面只把条数与路径原样说给人听。
async function exportSlice() {
  const note = $("search-note");
  if (!lastSearchQuery) {
    note.textContent = "先搜一次再导出。";
    note.hidden = false;
    return;
  }
  const btn = $("search-export");
  btn.disabled = true;
  try {
    const out = await window.go.main.App.ExportSearchNDJSON(lastSearchQuery);
    note.textContent = `已导出 ${out.count} 条切片 → ${out.path}`;
    note.hidden = false;
  } catch (err) {
    note.textContent = "导出没做成：" + err + " 可以再试一次。";
    note.hidden = false;
  } finally {
    btn.disabled = false;
  }
}

// searchRow 造一条命中：第一行渠道 · 会话标题 · 种类/时间，下面是两行片段。
// 标题和片段分行、长标题省略号收口，行高才不会一大一小（I-084 验收反馈）。
function searchRow(h) {
  const item = el("li", "search-hit");
  const head = el("div", "search-hit__head");
  head.append(
    el("span", "search-hit__channel", h.channel),
    el("span", "search-hit__title", h.session),
    el("span", "search-hit__when", [h.kind, h.when].filter(Boolean).join(" · "))
  );
  item.append(head, el("p", "search-hit__snippet", h.snippet));
  return item;
}

// ── 日志页（I-030）──────────────────────────────────────────────────────
//
// 进来的时候才去读：这一页是定时任务写的账，平时没人看（按需加载）。
async function openLog() {
  if (!$("view-settings").hidden && !leaveSettings()) {
    // 先记下去向：人在底部选「放弃改动」之后，这一程要继续走完（I-052）。
    pendingView = "log";
    return;
  }
  pendingView = "";
  show("view-log");
  reportView("log");
  await renderLog();
}

async function renderLog() {
  const list = $("log-rows");
  const note = $("log-note");
  list.replaceChildren(el("li", "row", "正在读运行记录…"));
  note.hidden = true;

  let rows;
  try {
    rows = await bridge.RunLog();
  } catch (err) {
    list.replaceChildren();
    note.textContent = "读运行记录失败：" + err;
    note.hidden = false;
    return;
  }

  list.replaceChildren();
  if (!rows || rows.length === 0) {
    note.textContent = "还没有记录：收一次之后，这里就会一天一行。";
    note.hidden = false;
    return;
  }
  for (const day of rows) {
    list.append(logRow(day));
  }
}

// logRow 造一行：哪天 · 那天什么情况（一天一行）。
function logRow(day) {
  const item = el("li", "row");
  item.append(el("span", "row__name", day.label),
    el("span", "row__detail", logDetail(day)));
  return item;
}

// logDetail 说清那天的情况；没跑的那天照实说「没跑」。
function logDetail(day) {
  if (!day.runs) {
    return "没跑";
  }
  const parts = [`收下 ${day.archived} 段`, `跑了 ${day.runs} 轮`];
  if (day.source) {
    parts.push(day.source);
  }
  // 那天的备份：首页不显示，这里一次说清（I-053）。
  if (day.backups) {
    parts.push(`备份成功 ${day.backups} 次`);
  }
  if (day.backupFailures) {
    parts.push(`备份失败 ${day.backupFailures} 次`);
  }
  if (day.backupNote) {
    parts.push((day.backupNoteFailed ? "最近一次失败：" : "备份：") + day.backupNote);
  }
  if (day.failures) {
    parts.push(`失败 ${day.failures} 处`);
  }
  return parts.join(" · ");
}

// clearSettingsPage 把设置页现建的东西清干净（内容与底部那一行都不留）。
function clearSettingsPage() {
  // 先把 Git 面板从注册表里摘掉：设置页的内容一清，那个盒子就成了游离节点，
  // 留在 gitTargets 里只会越积越多（I-052）。
  if (form && form.gitTarget) {
    unmountGitPanel(form.gitTarget);
    form.gitTarget = null;
  }
  $("settings-body").replaceChildren();
  $("settings-foot").replaceChildren();
  form = null;
  settingsDirty = false;
  settingsAsking = false;
  settingsMessage = "";
  pendingView = "";
}

// replayWizard 从设置页重走一遍首次运行引导（步骤与 I-013 是同一套）。
//
// 为什么要有这个入口：引导原先只在配置里没写过「走完了」时才出现，界面里没有
// 别的地方能碰到它——人问「引导功能为什么找不到了」，答案就是「没有入口」，
// 想再看一遍得去删配置文件，那不是给人留的门。
//
// 不动配置里「走完了」那个标记：它记的是「首次运行结束」，跟人现在想不想再看
// 一遍是两回事（render() 靠 wizardReplay 把视图留在引导上）。
async function replayWizard() {
  // 先把状态读回最新的：设置页里刚改的备份方式、仓库地址、归档目录要能反映到引导里
  // （人提的：引导页的备份设置要和设置页同步）。不读的话，引导显示的是打开设置页
  // 之前的旧状态。
  //
  // 这里走不带过场的 State()：引导页没有那块读取过场，而 setReading 管的是主界面
  // 那一份渠道列表——在引导里开过场会把主界面留在「正在读取」上（I-051）。
  //
  // 读失败时**不要**先清设置页：清了人就只剩一块空白（I-052）。
  try {
    state = await bridge.State();
  } catch (err) {
    settingsMessage = "读不回当前状态：" + err;
    settingsMessageKind = "error";
    renderSettingsFoot();
    return;
  }

  clearSettingsPage();
  state.firstRunStep = 0;
  wizardReplay = true;
  render();
}

// hideSettings 真正关上，并把内容清掉（折叠时不渲染内容，验收标准 5）。
function hideSettings() {
  clearSettingsPage();
  const back = state && state.firstRunDone ? "view-main" : "view-wizard";
  show(back);
  reportView(back === "view-main" ? "main" : "wizard");

  // 回到主界面就刷一次：先显示读取过场，读完再露数字（I-051 标准 4）。
  if (back === "view-main") {
    refreshMainView();
  }
}

// ── 启动 ──────────────────────────────────────────────────────────────────

async function boot() {
  loadTheme();
  reportTheme();

  $("theme-toggle").addEventListener("click", cycleTheme);
  if (themeQuery.addEventListener) {
    themeQuery.addEventListener("change", reportTheme);
  }

  // 未捕获的异常与未处理的 Promise 拒绝都写进同一份日志（I-052）。
  window.addEventListener("error", (event) => {
    reportFrontendError(
      (event.message || "脚本错误") + " @ " + (event.filename || "?") + ":" + (event.lineno || 0)
    );
  });
  window.addEventListener("unhandledrejection", (event) => {
    reportFrontendError("未处理的 Promise 拒绝：" + String(event.reason));
  });

  if (!bridge) {
    fill("headline", "窗口已打开，但没有拿到后端桥接（这是在浏览器里打开的？）");
    return;
  }

  $("wizard-back").addEventListener("click", () => {
    if (step > 0) {
      saveStep(step - 1);
    }
  });
  $("wizard-next").addEventListener("click", () => {
    if (wizardArchiveMissing()) {
      showWizardError("先选一个归档目录，再点「下一步」。");
      return;
    }
    if (step === state.firstRunSteps - 1) {
      finishWizard();
    } else {
      saveStep(step + 1);
    }
  });
  $("wizard-skip").addEventListener("click", async () => {
    if (!wizardReplay) {
      return;
    }
    // 重走时这一位是「返回设置」，不是「跳过」。
    if (await openSettings()) {
      wizardReplay = false;
    }
  });
  $("collect").addEventListener("click", collect);
  // 导航栏：首页 ⇄ 设置。点「首页」走 closeSettings——设置页里有没保存的改动时它会先问一句。
  $("nav-main").addEventListener("click", closeSettings);
  $("nav-log").addEventListener("click", openLog);
  $("nav-settings").addEventListener("click", openSettings);
  $("nav-search").addEventListener("click", openSearch);
  $("search-go").addEventListener("click", runSearch);
  $("search-export").addEventListener("click", exportSlice);
  $("search-input").addEventListener("keydown", (event) => {
    // 中文输入法里回车是「上屏」，不是「搜索」：不挡的话第一次回车会把拼音
    // 当关键词搜掉，人看到的是「没有命中的内容」（I-084 验收反馈）。
    if (event.key === "Enter" && !event.isComposing) {
      runSearch();
    }
  });
  // 边打边搜：停手半秒就查一次。回车与按钮照旧立刻查；输入法拼字期间不查。
  $("search-input").addEventListener("compositionstart", () => {
    searchTimer = clearTimeout(searchTimer);
  });
  $("search-input").addEventListener("input", () => {
    if (searchTimer) {
      clearTimeout(searchTimer);
    }
    const query = $("search-input").value.trim();
    if (!query) {
      return;
    }
    searchTimer = setTimeout(() => {
      searchTimer = 0;
      runSearch();
    }, 500);
  });
  // 引导第 1 步也有「选择文件夹」（验收标准 10）：填进输入框，等「下一步」时一起存。
  $("wizard-archive-browse").addEventListener("click", async () => {
    const picked = await pickDirectory($("wizard-archive"));
    if (picked.err) {
      showWizardError("打不开文件夹选择框：" + picked.err);
    }
  });
  // Esc 也能从设置页返回（验收标准 4）。
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !$("view-settings").hidden) {
      closeSettings();
    }
  });

  // 进度由后端推事件过来（window.runtime 是 Wails 注入的，不经过生成的 JS 桩）。
  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn(PROGRESS_EVENT, onProgress);
    window.runtime.EventsOn(READING_STARTED_EVENT, onReadingStarted);
    window.runtime.EventsOn(READING_PROGRESS_EVENT, onReadingProgress);
    window.runtime.EventsOn(READING_FINISHED_EVENT, onReadingFinished);
  }

  // 打开首页先显示读取过场，读完再露数字（I-051 标准 4）。
  // 首次打开时所有视图都是 hidden：读取过场在主界面里，不先露出来，
  // 人只会看到一个空窗口（I-052）。这里直接露主界面（不经过 show，
  // 导航栏先不出现；首次运行的人等会儿会看到引导）。
  $("view-main").hidden = false;

  try {
    state = await readState();
  } catch (err) {
    fill("headline", "读配置失败：" + err);
    return;
  }
  render();
}

boot();

