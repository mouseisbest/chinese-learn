// 导入页逻辑。
//
// 两步走：先预览（不写库），确认后才提交。
// 数据源的格式无法预期，所以预览这一步是必需的——
// 用户得亲眼看到「抽出了哪些字」才能确定解析正确。

(function () {
  'use strict';

  var stepInput = document.getElementById('step-input');
  if (!stepInput) return;

  var stepPreview = document.getElementById('step-preview');
  var stepDone = document.getElementById('step-done');
  var errBox = document.getElementById('err-box');

  var elText = document.getElementById('import-text');
  var elSem = document.getElementById('sem-pick');
  var elSrcName = document.getElementById('src-name');

  var btnPreview = document.getElementById('preview-btn');
  var btnCommit = document.getElementById('commit-btn');
  var btnBack = document.getElementById('back-btn');
  var btnAgain = document.getElementById('again-import');

  var token = null;      // 预览凭据
  var previewToInsert = 0;

  btnPreview.addEventListener('click', doPreview);
  btnCommit.addEventListener('click', doCommit);
  btnBack.addEventListener('click', function () {
    stepPreview.hidden = true;
    stepInput.hidden = false;
  });

  if (btnAgain) {
    btnAgain.addEventListener('click', function () {
      stepDone.hidden = true;
      stepPreview.hidden = true;
      stepInput.hidden = false;
      elText.value = '';
      elSrcName.value = '';
      token = null;
      elText.focus();
    });
  }

  // Ctrl/Cmd + Enter 直接预览，省一次点击。
  elText.addEventListener('keydown', function (e) {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
      e.preventDefault();
      doPreview();
    }
  });

  function doPreview() {
    hideError();

    var text = elText.value;
    if (!text.trim()) {
      showError('请先粘贴内容');
      return;
    }

    btnPreview.disabled = true;
    btnPreview.textContent = '正在识别…';

    fetch('/api/import/preview', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        semester_id: parseInt(elSem.value, 10) || 0,
        source_name: elSrcName.value,
        text: text
      })
    })
      .then(parseResponse)
      .then(function (data) {
        token = data.token;
        previewToInsert = data.to_insert;
        renderPreview(data);
        stepInput.hidden = true;
        stepPreview.hidden = false;
      })
      .catch(function (err) {
        showError(err.message || '预览失败，请重试');
      })
      .finally(function () {
        btnPreview.disabled = false;
        btnPreview.textContent = '预览一下';
      });
  }

  function renderPreview(data) {
    document.getElementById('stat-total-chars').textContent = data.total_runes + ' 个字符';
    document.getElementById('stat-unique').textContent = data.unique_hanzi;
    document.getElementById('stat-new').textContent = data.to_insert;
    document.getElementById('stat-exists').textContent = data.already_exists;

    // 被跳过的内容分类展示，让用户确认没有误判。
    var note = document.getElementById('skip-note');
    var parts = [];
    var labels = {
      punct: '标点符号', latin: '英文字母', digit: '数字',
      symbol: '部首/特殊符号', other: '其他'
    };
    var skipped = data.skipped || {};
    Object.keys(labels).forEach(function (k) {
      if (skipped[k] > 0) parts.push(labels[k] + ' ' + skipped[k] + ' 个');
    });
    if (parts.length > 0) {
      note.textContent = '已跳过：' + parts.join(' · ');
      note.hidden = false;
    } else {
      note.hidden = true;
    }

    if (data.truncated) {
      note.textContent = (note.textContent ? note.textContent + '。' : '') +
        '内容过长，只取前 5000 个不同的汉字。';
      note.hidden = false;
    }

    // 字表用 textContent 填充，绝不用 innerHTML。
    var box = document.getElementById('preview-chars');
    box.textContent = '';
    if (data.preview) {
      box.textContent = data.preview.split('').join(' ');
    } else {
      box.textContent = '（没有抽出汉字）';
    }

    var commitBtn = document.getElementById('commit-btn');
    if (data.to_insert === 0) {
      commitBtn.textContent = '全部已存在，无需导入';
      commitBtn.disabled = true;
    } else {
      commitBtn.textContent = '确认导入 ' + data.to_insert + ' 个新字';
      commitBtn.disabled = false;
    }
  }

  function doCommit() {
    hideError();
    if (!token) {
      showError('预览已失效，请重新预览');
      return;
    }

    btnCommit.disabled = true;
    btnCommit.textContent = '正在导入…';

    fetch('/api/import/commit', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        token: token,
        semester_id: parseInt(elSem.value, 10) || 0,
        source_name: elSrcName.value
      })
    })
      .then(parseResponse)
      .then(function (data) {
        stepPreview.hidden = true;
        stepDone.hidden = false;
        document.getElementById('done-msg').textContent =
          '已导入。这个学期现在一共有 ' + data.semester_total + ' 个字。';
      })
      .catch(function (err) {
        showError(err.message || '导入失败，请重试');
        btnCommit.disabled = false;
        btnCommit.textContent = '确认导入 ' + previewToInsert + ' 个新字';
      });
  }

  // parseResponse 统一处理 JSON 响应与错误信息。
  function parseResponse(r) {
    return r.json().catch(function () {
      throw new Error('服务器返回了无法解析的内容');
    }).then(function (data) {
      if (!r.ok) {
        throw new Error(data && data.error ? data.error : '请求失败（' + r.status + '）');
      }
      return data;
    });
  }

  function showError(msg) {
    errBox.textContent = msg;
    errBox.hidden = false;
    window.scrollTo({ top: 0, behavior: 'smooth' });
  }

  function hideError() {
    errBox.hidden = true;
  }
})();
