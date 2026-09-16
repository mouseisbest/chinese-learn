// 各页面共用的脚本。
//
// 目前管两件事：学期切换下拉框、操作成功后的横幅提示。

(function () {
  'use strict';

  // ---- 学期切换：选中后自动提交，不用再点按钮 ----
  var select = document.getElementById('sem-select');
  var form = document.getElementById('sem-form');
  if (select && form) {
    select.addEventListener('change', function () {
      form.submit();
    });
  }

  // ---- 成功提示横幅 ----
  var banner = document.getElementById('flash-banner');
  if (!banner) return;

  // 错误提示不自动消失：用户需要时间看清哪里填错了，
  // 得自己点 × 关掉。成功提示 4 秒后自动淡出。
  var isError = banner.classList.contains('is-error');
  var HIDE_DELAY = isError ? 0 : 4000;
  var timer = null;

  function hide() {
    if (timer) clearTimeout(timer);
    banner.classList.add('is-hiding');
    setTimeout(function () {
      if (banner.parentNode) banner.parentNode.removeChild(banner);
    }, 400);
    // 把 msg 参数从地址栏去掉：刷新页面时提示不该再次出现。
    removeMsgParam();
  }

  function removeMsgParam() {
    if (!window.history || !window.history.replaceState) return;
    try {
      var u = new URL(window.location.href);
      if (!u.searchParams.has('msg')) return;
      u.searchParams.delete('msg');
      var qs = u.searchParams.toString();
      window.history.replaceState(null, '', u.pathname + (qs ? '?' + qs : '') + u.hash);
    } catch (e) {
      // 老浏览器不支持 URL API：忽略，不影响功能。
    }
  }

  var closeBtn = banner.querySelector('.flash-close');
  if (closeBtn) {
    closeBtn.addEventListener('click', hide);
  }

  // 错误提示不自动消失，交由用户手动关闭。
  if (HIDE_DELAY > 0) {
    // 鼠标悬停时先不消失，避免正在看的时候跑掉。
    banner.addEventListener('mouseenter', function () {
      if (timer) clearTimeout(timer);
    });
    banner.addEventListener('mouseleave', function () {
      timer = setTimeout(hide, 1200);
    });

    timer = setTimeout(hide, HIDE_DELAY);
  }
})();
