'use strict';
(() => {
  const $ = (id) => document.getElementById(id);
  // Carried in an attribute rather than an inline script so no script source
  // needs to be allowed beyond the served files.
  const settings = (() => {
    try { return JSON.parse(document.body.dataset.config || '{}'); } catch { return {}; }
  })();
  const state = {
    prefix: document.body.dataset.prefix || '',
    zipEnabled: settings.zipEnabled !== false,
    zipMaxFiles: Number.isInteger(settings.zipMaxFiles) && settings.zipMaxFiles > 0 ? settings.zipMaxFiles : 200,
    zipMaxBytes: Number.isFinite(settings.zipMaxBytes) && settings.zipMaxBytes > 0 ? settings.zipMaxBytes : 20 * 1024 ** 3,
    sort: 'name', direction: 1,
    selected: new Set(), notice: '',
    loading: false, zipPreparing: false, zipController: null,
  };
  const compareNames = new Intl.Collator('ko', { numeric: true, sensitivity: 'base' }).compare;
  const mobileLayout = matchMedia('(max-width: 740px)');
  // The date and size columns are hidden on narrow screens, so the empty-folder
  // cell has to stop spanning them or the fixed table stops filling its width.
  const spanMessage = () => {
    const cell = document.querySelector('#files .message');
    if (cell) cell.colSpan = (mobileLayout.matches ? 3 : 5) - (state.zipEnabled ? 0 : 1);
  };
  mobileLayout.addEventListener('change', spanMessage);

  // Rows are rendered by the server. Everything here reads them back rather
  // than building its own, so the list a person sees and the list an agent
  // fetches can never drift apart.
  const rows = () => [...document.querySelectorAll('#files tr[data-key]')];
  const entryOf = (row) => ({
    key: row.dataset.key,
    name: row.dataset.name,
    folder: row.dataset.folder === '1',
    size: Number(row.dataset.size),
    modified: row.dataset.modified,
  });
  const entries = () => rows().map(entryOf);

  function formatSize(size) {
    if (!Number.isFinite(size) || size < 0) return '—';
    if (size < 1024) return size + ' B';
    const power = Math.min(4, Math.floor(Math.log(size) / Math.log(1024)));
    return (size / 1024 ** power).toFixed(1) + ' ' + ['B', 'KiB', 'MiB', 'GiB', 'TiB'][power];
  }
  // The server prints UTC because it cannot know the reader's zone; rewrite
  // each cell once the browser, which does know, has the page.
  function localiseDates(scope) {
    for (const row of scope.querySelectorAll('tr[data-key]')) {
      const value = row.dataset.modified;
      const cell = row.querySelector('.date-cell');
      if (!cell || !value || !Number.isFinite(Date.parse(value))) continue;
      const date = new Date(value);
      const pad = (v) => String(v).padStart(2, '0');
      cell.textContent = `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
      cell.title = date.toString();
      const meta = row.querySelector('.entry-mobile-meta');
      if (meta && !row.dataset.folder) meta.textContent = [formatSize(Number(row.dataset.size)), cell.textContent.slice(0, 10)].join(' · ');
    }
  }

  function status() {
    const items = entries();
    const folders = items.filter(e => e.folder).length;
    let text = `${folders}개 폴더 · ${items.length - folders}개 파일`;
    if ($('load-more')) text += ' · 다음 페이지 있음';
    $('status').textContent = state.loading ? '불러오는 중…' : text;
  }

  function renderSelection() {
    $('notice').hidden = !state.notice;
    $('notice').textContent = state.notice;
    if (!state.zipEnabled) return;
    const items = entries();
    const selected = state.selected.size;
    for (const checkbox of document.querySelectorAll('[data-select-key]')) {
      checkbox.checked = state.selected.has(checkbox.dataset.selectKey);
      checkbox.disabled = state.zipPreparing || (!checkbox.checked && selected >= state.zipMaxFiles);
      checkbox.closest('tr').classList.toggle('selected', checkbox.checked);
    }
    const all = $('select-all');
    if (all) {
      all.checked = items.length > 0 && items.every(e => state.selected.has(e.key));
      all.indeterminate = selected > 0 && !all.checked;
      all.disabled = !items.length || state.loading || state.zipPreparing;
    }
    $('selection-tools').hidden = !selected;
    document.body.classList.toggle('has-selection', selected > 0);
    $('clear-selection').disabled = state.zipPreparing;
    $('download-zip').hidden = !selected;
    $('download-zip').disabled = state.zipPreparing || state.loading;
    $('download-zip').textContent = state.zipPreparing ? '확인 중…' : `ZIP 다운로드 (${selected})`;
  }

  // Reordering the rows that are already here needs no request and no second
  // renderer; the column links still work when scripting is off.
  function applySort() {
    const body = $('files');
    const ordered = rows().sort((a, b) => {
      const x = entryOf(a), y = entryOf(b);
      if (x.folder !== y.folder) return x.folder ? -1 : 1;
      let cmp = 0;
      if (state.sort === 'size') cmp = x.size - y.size;
      else if (state.sort === 'modified') cmp = (Date.parse(x.modified) || 0) - (Date.parse(y.modified) || 0);
      return (cmp || compareNames(x.name, y.name)) * state.direction;
    });
    for (const row of ordered) body.append(row);
    for (const link of document.querySelectorAll('.sort-link')) {
      const active = state.sort === link.dataset.sort;
      link.title = $('load-more') ? '불러온 항목만 정렬합니다.' : '현재 폴더 정렬';
      link.closest('th').setAttribute('aria-sort', active ? (state.direction === 1 ? 'ascending' : 'descending') : 'none');
      link.querySelector('.sort-mark').textContent = active ? (state.direction === 1 ? '↑' : '↓') : '';
    }
    const value = state.sort + ':' + state.direction;
    if ([...$('mobile-sort').options].some(option => option.value === value)) $('mobile-sort').value = value;
  }

  function attach(scope) {
    localiseDates(scope);
    for (const checkbox of scope.querySelectorAll('[data-select-key]')) {
      checkbox.addEventListener('change', () => {
        if (checkbox.checked && state.selected.size >= state.zipMaxFiles) {
          state.notice = `한 번에 최대 ${state.zipMaxFiles}개 항목까지 선택할 수 있습니다. 하위 파일 수도 서버에서 제한을 확인합니다.`;
        } else if (checkbox.checked) state.selected.add(checkbox.dataset.selectKey);
        else state.selected.delete(checkbox.dataset.selectKey);
        renderSelection();
      });
    }
    for (const row of scope.querySelectorAll('tr[data-preview="1"]')) {
      const link = row.querySelector('.entry-link');
      if (!link || !window.MoriPreview) continue;
      link.addEventListener('click', event => {
        if (event.button !== 0 || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
        event.preventDefault();
        window.MoriPreview.open(entryOf(row), entries(), link);
      });
    }
  }

  // "더 불러오기" is a plain link to the next page. With scripting on, that
  // page is fetched and its rows appended, so the accumulated list is kept
  // without a second way of drawing a row.
  async function loadMore(link) {
    if (state.loading) return;
    state.loading = true;
    link.textContent = '불러오는 중…';
    status();
    try {
      const response = await fetch(link.href, { credentials: 'same-origin', cache: 'no-store', headers: { 'X-Mori-Request': '1' } });
      if (!response.ok) throw new Error(`목록을 불러오지 못했습니다. (${response.status})`);
      const next = new DOMParser().parseFromString(await response.text(), 'text/html');
      const seen = new Set(rows().map(row => row.dataset.key));
      const added = document.createDocumentFragment();
      for (const row of next.querySelectorAll('#files tr[data-key]')) {
        if (!seen.has(row.dataset.key)) added.append(document.importNode(row, true));
      }
      $('files').querySelector('.message-row')?.remove();
      $('files').append(added);
      const following = next.getElementById('load-more');
      if (following) link.href = following.getAttribute('href');
      else link.remove();
      attach($('files'));
      applySort();
    } catch (error) {
      $('error').hidden = false;
      $('error').textContent = error.message;
    } finally {
      state.loading = false;
      if (link.isConnected) link.textContent = '더 불러오기';
      status();
      renderSelection();
    }
  }

  async function downloadZIP() {
    if (!state.zipEnabled || !state.selected.size || state.zipPreparing) return;
    const keys = [...state.selected];
    const items = entries();
    const estimatedSize = items.filter(e => !e.folder && state.selected.has(e.key)).reduce((n, e) => n + (e.size || 0), 0);
    if (estimatedSize > state.zipMaxBytes) {
      state.notice = `선택 파일 합계가 ZIP 제한 ${formatSize(state.zipMaxBytes)}를 넘었습니다.`;
      renderSelection(); return;
    }
    const controller = new AbortController();
    state.zipController = controller; state.zipPreparing = true;
    state.notice = items.some(e => e.folder && state.selected.has(e.key)) ? '선택한 폴더의 하위 파일과 용량을 확인하고 있습니다.' : '';
    renderSelection();
    try {
      const response = await fetch('/_mori/api/archive', {
        method: 'POST', credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        headers: { 'Content-Type': 'application/json', 'X-Mori-Request': '1' },
        body: JSON.stringify({ prefix: state.prefix, keys })
      });
      const data = await response.json().catch(() => ({}));
      if (controller !== state.zipController) return;
      if (!response.ok) throw new Error(data.message || 'ZIP 다운로드를 준비하지 못했습니다.');
      if (typeof data.url !== 'string' || !/^\/_mori\/api\/archive\?token=[A-Za-z0-9_-]{43}$/.test(data.url)) throw new Error('ZIP 다운로드 응답이 올바르지 않습니다.');
      const link = document.createElement('a');
      // The server supplies Content-Disposition; a download attribute can prevent
      // scripted ZIP downloads from starting in WebKit.
      link.href = data.url;
      link.hidden = true; document.body.append(link); link.click(); link.remove();
      state.notice = '';
    } catch (error) {
      if (controller !== state.zipController || error.name === 'AbortError') return;
      state.notice = error.message || 'ZIP 다운로드를 준비하지 못했습니다.';
    } finally {
      if (controller === state.zipController) {
        state.zipPreparing = false; state.zipController = null; renderSelection();
      }
    }
  }

  // Folders used to live behind "#/path". Send those links to the real path so
  // an old bookmark still lands on the folder it named.
  if (location.hash.startsWith('#/')) {
    const target = location.hash.slice(1);
    if (/^\/[^\\]*$/.test(target) && !target.includes('//') && !target.split('/').includes('..')) {
      location.replace(target.endsWith('/') ? target : target + '/');
      return;
    }
    location.replace('/');
    return;
  }

  $('select-all')?.addEventListener('change', () => {
    const items = entries();
    if ($('select-all').checked && items.length > state.zipMaxFiles) {
      state.notice = `한 번에 최대 ${state.zipMaxFiles}개까지 가능합니다. 파일 또는 폴더를 개별 선택해 주세요.`;
    } else {
      state.selected = $('select-all').checked ? new Set(items.map(e => e.key)) : new Set();
      state.notice = '';
    }
    renderSelection();
  });
  $('clear-selection').addEventListener('click', () => { state.selected.clear(); state.notice = ''; renderSelection(); });
  $('download-zip').addEventListener('click', downloadZIP);
  $('mobile-sort').addEventListener('change', event => {
    const [sort, dir] = event.target.value.split(':');
    state.sort = sort; state.direction = Number(dir); applySort();
  });
  for (const link of document.querySelectorAll('.sort-link')) {
    link.addEventListener('click', event => {
      if (event.button !== 0 || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
      event.preventDefault();
      state.direction = state.sort === link.dataset.sort ? -state.direction : 1;
      state.sort = link.dataset.sort;
      applySort();
    });
  }
  document.addEventListener('click', event => {
    const link = event.target.closest?.('#load-more');
    if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
    event.preventDefault();
    loadMore(link);
  });
  if (!state.zipEnabled) $('selection-tools').hidden = true;
  spanMessage();
  attach(document);
  applySort();
  status();
  renderSelection();
})();
