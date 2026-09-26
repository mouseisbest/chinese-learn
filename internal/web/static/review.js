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
  var cardStage = document.getElementById('card-stage');
  var elPinyin = document.getElementById('big-pinyin');
  var elOtherPinyin = document.getElementById('other-pinyin');
  var elRevealedPinyin = document.getElementById('revealed-pinyin');
  var pinyinPanel = document.getElementById('pinyin-panel');
  var hanziPanel = document.getElementById('hanzi-panel');
  var btnReveal = document.getElementById('reveal-btn');

  // ---- 状态 ----
  var queue = [];        // 待出卡的字
  var current = null;    // 当前显示的字
  var lastShown = null;  // 上一个字，供撤销
  var answered = 0;
  var knownCount = 0;
  var unknownCount = 0;
  // pinyinFirst：是否走「先拼音 → 翻牌 → 判断」的流程，由设置决定。
  // revealed：当前这张牌是否已翻开。
  var pinyinFirst = true;
  var revealed = false;
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
  btnReveal.addEventListener('click', reveal);
  btnSpeak.addEventListener('click', speakCurrent);
  btnUndo.addEventListener('click', undoLast);
  exitLink.addEventListener('click', confirmExit);

  document.addEventListener('keydown', function (e) {
    if (showingSummary) {
      if (e.key === 'Enter') { e.preventDefault(); document.getElementById('again-btn').click(); }
      return;
    }

    // 拼音阶段：空格或回车翻牌。翻牌前不接受判定。
    if (pinyinFirst && !revealed) {
      if (e.key === ' ' || e.key === 'Spacebar' || e.key === 'Enter') {
        e.preventDefault();
        reveal();
      }
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
        // 拼音阶段由设置控制，服务端在计划里告诉我们。
        pinyinFirst = data.pinyin_first !== false;
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
    charShownAt = Date.now();
    elReason.textContent = item.reason_label && item.reason !== 'new' ? item.reason_label : '';

    // 汉字和翻牌后的拼音都先填好，翻牌时直接显示，不用等渲染。
    elChar.textContent = item.ch || '　';
    elRevealedPinyin.textContent = item.pinyin || '';
    // 有些生僻字没有拼音，这时让汉字占满空间（CSS 靠这个类判断）。
    hanziPanel.classList.toggle('no-pinyin', !item.pinyin);

    if (pinyinFirst) {
      // 先出拼音，孩子看拼音想字形。想不出来就翻牌看答案。
      revealed = false;
      elPinyin.textContent = item.pinyin || '（没有拼音）';
      renderOtherReadings(item.other_readings);
      setPhase('pinyin');
    } else {
      revealed = true;
      setPhase('hanzi');
    }

    // 刻意不自动朗读：读出来等于给了答案，自评就失去意义了。
    // 孩子认不出时自己点「读一遍」即可。

    // 预取下一张：解码字体、预热渲染，让点击后换字无延迟。
    prefetchNext();
  }

  // setPhase 切换「拼音阶段」和「汉字阶段」的显示。
  function setPhase(phase) {
    var isPinyin = (phase === 'pinyin');
    cardStage.dataset.phase = phase;
    pinyinPanel.hidden = !isPinyin;
    hanziPanel.hidden = isPinyin;
    btnReveal.hidden = !isPinyin;
    // 朗读按钮只在汉字阶段可用：拼音阶段听到读音就没得想了。
    btnSpeak.hidden = isPinyin;

    // 判定按钮要等翻牌后才能点，否则等于没看字就作答。
    btnKnow.disabled = isPinyin;
    btnUnknown.disabled = isPinyin;
  }

  // reveal 翻开当前这张牌，显示汉字并启用判定按钮。
  function reveal() {
    if (revealed || !current) return;
    revealed = true;
    setPhase('hanzi');
  }

  function renderOtherReadings(readings) {
    elOtherPinyin.textContent = '';
    if (!readings || readings.length === 0) return;
    // 多音字：其他读音用小字标在常用音下面，让孩子知道还有别的读法。
    elOtherPinyin.textContent = readings.join(' · ');
  }

  function prefetchNext() {
    if (queue.length === 0) return;
    var probe = document.createElement('span');
    probe.style.position = 'absolute';
    probe.style.visibility = 'hidden';
    probe.style.fontSize = '10px';
    probe.style.fontFamily = getComputedStyle(elChar).fontFamily;
    probe.textContent = queue[0].ch || '';
    document.body.appendChild(probe);
    setTimeout(function () { document.body.removeChild(probe); }, 50);
  }

  // ---- 作答 ----

  function answer(result) {
    if (showingSummary || !current) return;
    // 没翻牌就不能判定——否则等于没看字就作答。
    // 按钮在拼音阶段是 disabled 的，这里再挡一层防脚本误调。
    if (pinyinFirst && !revealed) return;

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
      u.rate = 0.6;    // 放慢，给孩子跟读的时间
      u.pitch = 1.15;  // 略高，接近女声的音色

      var voice = pickChineseVoice();
      if (voice) u.voice = voice;

      window.speechSynthesis.speak(u);
    } catch (e) {
      // 朗读是增强功能，失败不影响认字。
    }
  }

  // pickChineseVoice 挑一个中文女声。
  //
  // 浏览器的语音列表里没有性别字段，只能靠名字里的关键词判断。
  // 各平台的命名习惯：Chrome/Edge 用 "Google 普通话（中国大陆）"、
  // "Microsoft Xiaoxiao"；苹果用 "Ting-Ting"、"Mei-Jia"；
  // 安卓常见 "zh-cn-x-ccc-local" 这类内部代号。
  // 挑不到女声就退回任意中文语音，再没有就返回 null 用系统默认。
  var FEMALE_HINTS = [
    'xiaoxiao', 'xiaoyi', 'xiaomo', 'xiaoxuan', 'xiaohan',
    'ting-ting', 'tingting', 'meijia', 'mei-jia', 'sinji',
    'huihui', 'yaoyao', 'kangkang', 'female', 'woman', 'girl',
    '婷婷', '女'
  ];
  var MALE_HINTS = ['yunxi', 'yunjian', 'kangkang', 'male', 'man', '云希'];

  function pickChineseVoice() {
    var voices = window.speechSynthesis.getVoices();
    var chinese = [];
    for (var i = 0; i < voices.length; i++) {
      var lang = (voices[i].lang || '').toLowerCase();
      if (lang.indexOf('zh') === 0 || lang.indexOf('cmn') === 0) {
        chinese.push(voices[i]);
      }
    }
    if (chinese.length === 0) return null;

    // 先找明确是女声的
    for (var j = 0; j < chinese.length; j++) {
      if (matchesAny(chinese[j].name, FEMALE_HINTS)) return chinese[j];
    }
    // 再排除明确是男声的
    for (var k = 0; k < chinese.length; k++) {
      if (!matchesAny(chinese[k].name, MALE_HINTS)) return chinese[k];
    }
    return chinese[0];
  }

  function matchesAny(name, hints) {
    if (!name) return false;
    var n = name.toLowerCase();
    for (var i = 0; i < hints.length; i++) {
      if (n.indexOf(hints[i]) !== -1) return true;
    }
    return false;
  }

  // 语音列表在部分浏览器里是异步加载的，第一次取可能为空。
  if ('speechSynthesis' in window) {
    window.speechSynthesis.getVoices(); // 触发加载
    window.speechSynthesis.onvoiceschanged = function () {
      window.speechSynthesis.getVoices(); // 列表就绪后缓存起来
    };
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
