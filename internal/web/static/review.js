// 复习页逻辑。
//
// 设计要点：
//   - 点击后立刻换下一个字，不等网络往返（本地预取队列）
//   - 提交失败时把答案存在本地，恢复后补交，不打断孩子
//   - 渲染一律用 textContent，绝不用 innerHTML
//   - 朗读用浏览器自带的语音合成，不可用时静默降级

(function () {
  'use strict';

  var app = document.getElementById('review-app');
  if (!app) return;

  // 今天已经没有要复习的字了，页面是空状态。
  if (app.dataset.empty === '1') {
    bindEmptyState();
    return;
  }

  var childId = app.dataset.childId;

  var elChar = document.getElementById('big-char');
  var elReason = document.getElementById('reason-hint');
  var elFill = document.getElementById('progress-fill');
  var elProgress = document.getElementById('progress-text');
  var elOverlay = document.getElementById('summary-overlay');
  var btnKnow = document.getElementById('btn-know');
  var btnUnknown = document.getElementById('btn-unknown');
  var btnSpeak = document.getElementById('speak-btn');
  var btnUndo = document.getElementById('btn-undo');
  var exitLink = document.getElementById('exit-link');

  // ---- 状态 ----
  var queue = [];        // 待出卡的字
  var current = null;    // 当前显示的字
  var lastShown = null;  // 上一个字，供撤销
  var answered = 0;
  var knownCount = 0;
  var unknownCount = 0;
  var mistakes = [];     // 本批答错的字，小结时展示
  var plannedTotal = 0;
  var sessionId = makeId();
  var submitQueue = [];  // 提交失败的待补交记录
  var showingSummary = false;
  var charShownAt = 0;

  // ---- 初始化 ----
  loadPlan();

  // ---- 事件绑定 ----
  btnKnow.addEventListener('click', function () { answer('known'); });
  btnUnknown.addEventListener('click', function () { answer('unknown'); });
  btnSpeak.addEventListener('click', speakCurrent);
  btnUndo.addEventListener('click', undoLast);
  exitLink.addEventListener('click', confirmExit);

  document.addEventListener('keydown', function (e) {
    if (showingSummary) {
      if (e.key === 'Enter') { e.preventDefault(); document.getElementById('again-btn').click(); }
      return;
    }
    switch (e.key) {
      case '1': case 'ArrowLeft': case 'j': case 'J':
        e.preventDefault(); answer('known'); break;
      case '2': case 'ArrowRight': case 'k': case 'K':
        e.preventDefault(); answer('unknown'); break;
      case 'Backspace':
        e.preventDefault(); undoLast(); break;
      case ' ': case 'Spacebar':
        e.preventDefault(); speakCurrent(); break;
    }
  });

  // 离开页面时若还有未提交的答案，提醒一下。
  window.addEventListener('beforeunload', function (e) {
    if (submitQueue.length > 0) {
      e.preventDefault();
      e.returnValue = '';
    }
  });

  // 网络恢复后补交。
  window.addEventListener('online', flushPending);

  // ---- 队列 ----

  function loadPlan() {
    fetch('/api/session/plan')
      .then(function (r) { return r.json(); })
      .then(function (data) {
        if (data.error) { showError(data.error); return; }
        queue = data.items || [];
        plannedTotal = queue.length;
        if (queue.length === 0) {
          window.location.reload();
          return;
        }
        updateProgress();
        nextCard();
      })
      .catch(function () {
        showError('加载失败，请刷新页面重试');
      });
  }

  function nextCard() {
    if (queue.length === 0) {
      finish();
      return;
    }
    current = queue.shift();
    showChar(current);
  }

  function showChar(item) {
    lastShown = item;
    elChar.textContent = item.ch || '　';
    elReason.textContent = item.reason_label && item.reason !== 'new' ? item.reason_label : '';
    charShownAt = Date.now();

    // 刻意不自动朗读：读出来等于给了答案，自评就失去意义了。
    // 孩子认不出时自己点「读一遍」即可。

    // 预取下一张：解码字体、预热渲染，让点击后换字无延迟。
    if (queue.length > 0) {
      var probe = document.createElement('span');
      probe.style.position = 'absolute';
      probe.style.visibility = 'hidden';
      probe.style.fontSize = '10px';
      probe.style.fontFamily = getComputedStyle(elChar).fontFamily;
      probe.textContent = queue[0].ch || '';
      document.body.appendChild(probe);
      setTimeout(function () { document.body.removeChild(probe); }, 50);
    }
  }

  // ---- 作答 ----

  function answer(result) {
    if (showingSummary || !current) return;

    var item = current;
    var latency = Date.now() - charShownAt;
    current = null; // 防止一次点击触发两次提交

    // 计数与进度在本地立刻更新，不等网络。
    answered++;
    if (result === 'known') {
      knownCount++;
    } else {
      unknownCount++;
      if (mistakes.indexOf(item.ch) === -1) mistakes.push(item.ch);
    }
    updateProgress();

    submit(item, result, latency);

    // 立刻出下一张——这是「无延迟感」的关键。
    nextCard();
  }

  function submit(item, result, latency) {
    var payload = {
      hanzi_id: item.hanzi_id,
      result: result,
      session_id: sessionId,
      latency_ms: latency
    };

    fetch('/api/review', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
      .then(function (r) {
        // 409 表示这个字已经答过了（连点或另一个标签页），不算失败。
        if (r.status === 409) return null;
        if (!r.ok) throw new Error('submit failed');
        return r.json();
      })
      .then(function (data) {
        if (data && data.error && data.code !== 'duplicate') {
          queuePending(payload);
        }
      })
      .catch(function () {
        // 网络不稳，先存起来，恢复后补交。
        queuePending(payload);
      });
  }

  function queuePending(payload) {
    submitQueue.push(payload);
    localStorage.setItem('pendingReviews', JSON.stringify(submitQueue));
  }

  function flushPending() {
    var stored = localStorage.getItem('pendingReviews');
    if (stored) {
      try {
        var arr = JSON.parse(stored);
        if (Array.isArray(arr)) submitQueue = arr.concat(submitQueue);
      } catch (e) { /* 忽略损坏的数据 */ }
      localStorage.removeItem('pendingReviews');
    }
    if (submitQueue.length === 0) return;

    var pending = submitQueue.splice(0, submitQueue.length);
    pending.forEach(function (payload) {
      fetch('/api/review', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      }).catch(function () {
        // 仍然失败：放回去等下次
        queuePending(payload);
      });
    });
  }

  // 页面加载时也尝试补交一次。
  flushPending();

  function undoLast() {
    if (showingSummary || answered === 0) return;

    fetch('/api/review/undo', { method: 'POST' })
      .then(function (r) {
        if (!r.ok) throw new Error('undo failed');
        return r.json();
      })
      .then(function (data) {
        if (!data.hanzi_id) return;
        // 把撤销掉的字放回队首，并回退本地计数。
        if (lastShown) {
          queue.unshift(lastShown);
        }
        answered = Math.max(0, answered - 1);
        if (data.result === 'known') {
          knownCount = Math.max(0, knownCount - 1);
        } else {
          unknownCount = Math.max(0, unknownCount - 1);
        }
        updateProgress();
        current = null;
        nextCard();
      })
      .catch(function () { /* 撤销失败就忽略，不影响继续 */ });
  }

  // ---- 朗读 ----

  function speakCurrent() {
    if (!current || !current.ch) return;
    if (!('speechSynthesis' in window)) return; // 静默降级

    try {
      window.speechSynthesis.cancel();
      var u = new SpeechSynthesisUtterance(current.ch);
      u.lang = 'zh-CN';
      u.rate = 0.85;   // 慢一点，方便孩子听清
      u.pitch = 1.0;

      var voice = pickChineseVoice();
      if (voice) u.voice = voice;

      window.speechSynthesis.speak(u);
    } catch (e) {
      // 朗读是增强功能，失败不影响认字。
    }
  }

  function pickChineseVoice() {
    var voices = window.speechSynthesis.getVoices();
    for (var i = 0; i < voices.length; i++) {
      var lang = voices[i].lang || '';
      if (lang.indexOf('zh') === 0 || lang.indexOf('cmn') === 0) return voices[i];
    }
    return null;
  }

  // 语音列表在部分浏览器里是异步加载的。
  if ('speechSynthesis' in window) {
    window.speechSynthesis.onvoiceschanged = function () { /* 触发一次加载 */ };
  }

  // ---- 进度与小结 ----

  function updateProgress() {
    var total = plannedTotal || 1;
    var done = answered;
    elFill.style.width = Math.min(100, (done / total) * 100) + '%';
    elProgress.textContent = done + ' / ' + total;
  }

  function finish() {
    showingSummary = true;

    document.getElementById('sum-total').textContent = answered;
    document.getElementById('sum-known').textContent = knownCount;
    document.getElementById('sum-unknown').textContent = unknownCount;

    var mark = document.getElementById('summary-mark');
    var title = document.getElementById('summary-title');
    if (unknownCount === 0 && answered > 0) {
      mark.textContent = '🎉';
      mark.style.color = 'var(--know)';
      title.textContent = '全部认识，太棒了！';
    } else {
      mark.textContent = '✓';
      mark.style.color = 'var(--brand)';
      title.textContent = '这一批做完了';
    }

    // 列出不认识的字，让孩子再看一眼。
    var box = document.getElementById('summary-mistakes');
    box.textContent = '';
    if (mistakes.length > 0) {
      var label = document.createElement('div');
      label.textContent = '这几个字明天还会见到，先看一眼：';
      var chars = document.createElement('div');
      chars.className = 'mistake-chars';
      chars.textContent = mistakes.join(' ');
      box.appendChild(label);
      box.appendChild(chars);
    }

    elOverlay.hidden = false;

    // 「再练一批」重新拉一次计划。
    //
    // 不能简单地 location.reload()——如果刚做完的这批耗尽了今日配额，
    // 重载后还是同一个空页面，用户看到的就是「点了没反应」。
    // 所以这里显式请求新队列，并区分「真没字了」和「加载失败」。
    var againBtn = document.getElementById('again-btn');
    againBtn.addEventListener('click', function () {
      againBtn.disabled = true;
      againBtn.textContent = '正在准备…';

      fetch('/api/session/plan')
        .then(function (r) { return r.json(); })
        .then(function (data) {
          if (data.error) throw new Error(data.error);
          if (!data.items || data.items.length === 0) {
            // 确实没有更多了，直接说明原因，不要静默重载。
            showNoMore(data);
            return;
          }
          // 有新的一批：重新载入页面，复习页会用新队列初始化。
          window.location.href = '/review';
        })
        .catch(function () {
          againBtn.disabled = false;
          againBtn.textContent = '再练一批';
          setFooterNote('加载失败，请稍后再试');
        });
    });
  }

  // showNoMore 把小结浮层切换成「今天没有更多了」的说明。
  function showNoMore(data) {
    var title = document.getElementById('summary-title');
    var mark = document.getElementById('summary-mark');
    var againBtn = document.getElementById('again-btn');

    var reason = '今天要复习的字都做完了。';
    if (data.new_total > 0 && data.new_left === 0) {
      reason = '今天的新字学完了，剩下的字明天再来。';
    } else if (data.new_total === 0 && data.due_total === 0) {
      reason = '这个学期的字都学完了，去「导入」加新字吧。';
    }

    mark.textContent = '🌙';
    mark.style.color = 'var(--brand)';
    title.textContent = '今天到此为止';
    setFooterNote(reason);

    againBtn.textContent = '回首页';
    againBtn.disabled = false;
    // 换掉点击行为，避免再点一次又走一遍同样流程。
    againBtn.onclick = function () { window.location.href = '/'; };
  }

  // setFooterNote 在小结卡片里显示一行说明文字。
  function setFooterNote(text) {
    var box = document.getElementById('summary-mistakes');
    if (!box) return;
    box.textContent = text;
  }

  function confirmExit() {
    if (answered === 0 || confirm('还有字没练完，确定要先回去吗？')) {
      window.location.href = '/';
    }
  }

  function showError(msg) {
    elChar.textContent = '';
    elReason.textContent = msg;
    elReason.style.color = 'var(--unknown-dark)';
  }

  // ---- 工具 ----

  function makeId() {
    // 不含日期：一个 session 可能跨逻辑日（23:50 开始 00:10 结束），
    // 把日期嵌进 ID 会让去重索引误伤。
    var chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
    var out = '';
    for (var i = 0; i < 20; i++) {
      out += chars.charAt(Math.floor(Math.random() * chars.length));
    }
    return out;
  }

  // ---- 空状态 ----

  function bindEmptyState() {
    var again = document.getElementById('load-more');
    if (again) {
      again.addEventListener('click', function () {
        window.location.href = '/review';
      });
    }
  }
})();
