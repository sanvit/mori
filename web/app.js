'use strict';
(() => {
  const $ = (id) => document.getElementById(id);
  const state = { title: 'Files', prefix: '', entries: [], cursor: '', sort: 'name', direction: 1, loading: false, controller: null, error: '', selected: new Set(), zipPreparing: false, zipController: null, zipEnabled: true, zipMaxFiles: 200, zipMaxBytes: 20 * 1024 ** 3, downloadMode: 'proxy', previewMode: 'proxy', notice: '' };
  const compareNames = new Intl.Collator('ko', { numeric: true, sensitivity: 'base' }).compare;
  const NS = 'http://www.w3.org/2000/svg';

  // All names and server messages are rendered as text, never as HTML.
  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }
  function icon(kind, className = '') {
    const svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '1.35');
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    svg.setAttribute('aria-hidden', 'true');
    svg.setAttribute('class', className);
    const paths = {
      folder: ['M2.5 7.5V5.8A1.8 1.8 0 0 1 4.3 4h5l2 2h8.4a1.8 1.8 0 0 1 1.8 1.8V19H2.5Z', 'M2.5 8h19'],
      root: ['M3 6.5A1.5 1.5 0 0 1 4.5 5h5l2 2h8A1.5 1.5 0 0 1 21 8.5v10a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 18.5Z'],
      file: ['M6 2.5h8l5 5V21H6Z', 'M14 2.5V8h5', 'M9 12h7M9 15h7'],
      code: ['M6 2.5h8l5 5V21H6Z', 'M14 2.5V8h5', 'm10.5 12-2 2 2 2m4-4 2 2-2 2'],
      image: ['M6 2.5h8l5 5V21H6Z', 'M14 2.5V8h5', 'm8 18 3-4 2 2 2-3 2 5M9 10h.01'],
      video: ['M5 5h10v14H5Z', 'm15 10 5-3v10l-5-3'],
      audio: ['M9 18V5l11-2v12M9 7l11-2', 'M9 18c0 2-6 3-6 0s6-3 6 0Zm11-3c0 2-6 3-6 0s6-3 6 0Z'],
      pdf: ['M6 2.5h8l5 5V21H6Z', 'M14 2.5V8h5M9 12h7M9 16h4'],
      archive: ['M6 2.5h8l5 5V21H6Z', 'M14 2.5V8h5', 'M10 4v2h2v2h-2v2h2v2h-2v2h2v3h-2v-3'],
      parent: ['M17.5 18v-5a4 4 0 0 0-4-4H5', 'm9 5-4 4 4 4'],
      download: ['M12 3v12m-4-4 4 4 4-4', 'M5 16v4h14v-4'],
      chevron: ['m9 5 7 7-7 7']
    };
    for (const d of paths[kind] || paths.file) {
      const p = document.createElementNS(NS, 'path');
      p.setAttribute('d', d);
      if (kind === 'folder' || kind === 'root') { p.setAttribute('fill', 'currentColor'); p.setAttribute('fill-opacity', '.12'); }
      svg.append(p);
    }
    return svg;
  }
  function kindOf(entry) {
    if (entry.folder) return 'folder';
    const previewKind = window.MoriPreview?.kindOf(entry.name);
    if (['video', 'audio', 'pdf'].includes(previewKind)) return previewKind;
    const ext = entry.name.split('.').pop().toLowerCase();
    if (/^(zip|gz|7z|rar|tar|xz)$/.test(ext)) return 'archive';
    if (/^(json|js|ts|css|html|go|py|yaml|yml|xml|sh)$/.test(ext)) return 'code';
    if (/^(png|jpg|jpeg|gif|svg|webp|avif)$/.test(ext)) return 'image';
    return 'file';
  }
  function folderURL(prefix) { return '#/' + prefix.split('/').map(encodeURIComponent).join('/'); }
  function prefixFromHash() {
    const raw = location.hash.replace(/^#\/?/, '');
    if (!raw) return '';
    const prefix = decodeURIComponent(raw);
    if (!prefix.endsWith('/') || prefix.startsWith('/') || prefix.includes('\\') || /[\x00-\x1f\x7f]/.test(prefix) || prefix.slice(0, -1).split('/').some(s => !s || s === '.' || s === '..')) {
      throw new Error('올바르지 않은 폴더 경로입니다.');
    }
    return prefix;
  }
  function formatSize(size) {
    if (!Number.isFinite(size) || size < 0) return '—';
    if (size < 1024) return size + ' B';
    const power = Math.min(4, Math.floor(Math.log(size) / Math.log(1024)));
    return (size / 1024 ** power).toFixed(1) + ' ' + ['B', 'KiB', 'MiB', 'GiB', 'TiB'][power];
  }
  function formatDate(value) {
    if (!value || !Number.isFinite(Date.parse(value))) return '—';
    const date = new Date(value);
    const pad = (v) => String(v).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
  }
  function fileURL(entry, download) {
    return '/api/object?' + new URLSearchParams({ key: entry.key, ...(download ? { download: '1' } : {}) });
  }
  async function api(route, params, signal) {
    const response = await fetch('/api/' + route + '?' + new URLSearchParams(params || {}), { signal, cache: 'no-store', credentials: 'same-origin' });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.message || `요청을 처리할 수 없습니다. (${response.status})`);
    return data;
  }
  function renderBreadcrumbs() {
    const nav = $('breadcrumbs');
    nav.replaceChildren();
    const parts = state.prefix.split('/').filter(Boolean);
    const root = element('a', 'root');
    root.href = folderURL('');
    root.append(icon('root', 'root-icon'), element('span', '', state.title));
    if (!parts.length) root.setAttribute('aria-current', 'page');
    nav.append(root);
    let path = '';
    parts.forEach((name, i) => {
      path += name + '/';
      nav.append(icon('chevron', 'separator'));
      const link = element('a');
      link.href = folderURL(path);
      link.append(element('span', '', name));
      if (i === parts.length - 1) link.setAttribute('aria-current', 'page');
      nav.append(link);
    });
    document.title = (state.prefix ? '/' + state.prefix + ' · ' : '') + state.title;
    $('heading').textContent = '/' + state.prefix + ' 파일 목록';
  }
  function entryRow(entry, parent = false) {
    const kind = parent ? 'parent' : kindOf(entry);
    const row = element('tr', parent ? 'parent-row' : entry.folder ? 'folder-row' : 'file-row');
    row.dataset.key = entry.key;
    const nameCell = element('td');
    const link = element('a', 'entry-link');
    link.href = entry.folder ? folderURL(entry.key) : fileURL(entry, false);
    if (!entry.folder) { link.target = '_blank'; link.rel = 'noopener noreferrer'; }
    link.title = parent ? '상위 폴더' : entry.name;
    if (parent) link.setAttribute('aria-label', '상위 폴더');
    const title = element('span', 'entry-title');
    title.append(element('span', 'entry-name', parent ? '..' : entry.name));
    if (!parent) title.append(element('span', 'entry-mobile-meta', entry.folder ? '폴더' : [formatSize(entry.size), entry.modified ? formatDate(entry.modified).slice(0,10) : ''].filter(Boolean).join(' · ')));
    link.append(icon(kind, 'entry-icon ' + kind), title);
    if (!entry.folder && window.MoriPreview?.kindOf(entry.name) !== 'unsupported' && window.MoriPreview) {
      link.title = entry.name + ' 미리보기';
      link.addEventListener('click', event => {
        if (event.button !== 0 || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
        event.preventDefault();
        const byKey = new Map(state.entries.map(item => [item.key, item]));
        const entries = [...document.querySelectorAll('tr.file-row')].map(row => byKey.get(row.dataset.key)).filter(Boolean);
        window.MoriPreview.open(entry, entries, link);
      });
    }
    nameCell.append(link);
    const date = element('td', 'date-cell', parent ? '' : formatDate(entry.modified));
    if (entry.modified) date.title = new Date(entry.modified).toString();
    const size = element('td', 'size-cell', parent ? '' : entry.folder ? '—' : formatSize(entry.size));
    if (!entry.folder) size.title = entry.size + ' bytes';
    const action = element('td', 'action-cell');
    if (!entry.folder) {
      const download = element('a', 'icon-button download');
      download.href = fileURL(entry, true);
      download.download = entry.name;
      download.title = state.downloadMode === 'presigned' ? 'S3 직접 다운로드' : '서버 경유 다운로드';
      download.setAttribute('aria-label', entry.name + ' 다운로드');
      download.append(icon('download'));
      action.append(download);
    }
    if (!state.zipEnabled) {
      row.append(nameCell, date, size, action);
      return row;
    }
    const selectCell = element('td', 'select-cell');
    if (!parent) {
      const label = element('label', 'check-hit');
      const checkbox = element('input');
      checkbox.type = 'checkbox'; checkbox.dataset.selectKey = entry.key;
      checkbox.setAttribute('aria-label', entry.name + ' 선택');
      if (entry.folder) checkbox.title = '하위 폴더와 파일을 모두 ZIP에 포함합니다.';
      checkbox.addEventListener('change', () => {
        if (checkbox.checked && state.selected.size >= state.zipMaxFiles) {
          state.notice = `한 번에 최대 ${state.zipMaxFiles}개 항목까지 선택할 수 있습니다. 하위 파일 수도 서버에서 제한을 확인합니다.`;
        } else if (checkbox.checked) state.selected.add(entry.key);
        else state.selected.delete(entry.key);
        renderSelection();
      });
      label.append(checkbox); selectCell.append(label);
    }
    row.append(selectCell, nameCell, date, size, action);
    return row;
  }
  function render() {
    const entries = [...state.entries].sort((a, b) => {
      if (!!a.folder !== !!b.folder) return a.folder ? -1 : 1;
      let cmp = 0;
      if (state.sort === 'size') cmp = a.size - b.size;
      else if (state.sort === 'modified') cmp = (Date.parse(a.modified) || 0) - (Date.parse(b.modified) || 0);
      return ((cmp || compareNames(a.name, b.name)) * state.direction);
    });
    const fragment = document.createDocumentFragment();
    if (state.prefix) {
      const parts = state.prefix.split('/').filter(Boolean); parts.pop();
      fragment.append(entryRow({ key: parts.length ? parts.join('/') + '/' : '', name: '..', folder: true }, true));
    }
    for (const entry of entries) fragment.append(entryRow(entry));
    if (!entries.length) {
      const row = element('tr', 'message-row');
      const message = state.loading ? '파일 목록을 불러오는 중…' : state.error ? '목록을 불러오지 못했습니다.' : state.cursor ? '다음 페이지에 항목이 더 있습니다.' : '이 폴더는 비어 있습니다.';
      const cell = element('td', 'message', message); cell.colSpan = state.zipEnabled ? 5 : 4; row.append(cell); fragment.append(row);
    }
    $('files').replaceChildren(fragment);
    const folders = entries.filter(e => e.folder).length;
    let status = `${folders}개 폴더 · ${entries.length - folders}개 파일`;
    if (state.cursor) status += ' · 다음 페이지 있음';
    $('status').textContent = state.loading ? '불러오는 중…' : state.error && !state.entries.length ? '' : status;
    $('refresh').disabled = state.loading;
    $('load-more').hidden = !state.cursor;
    $('load-more').disabled = state.loading;
    $('load-more').textContent = state.loading ? '불러오는 중…' : state.error ? '다시 불러오기' : '더 불러오기';
    $('error').hidden = !state.error;
    $('error').textContent = state.error;
    $('files').setAttribute('aria-busy', String(state.loading));
    document.querySelectorAll('[data-sort]').forEach(button => {
      const active = state.sort === button.dataset.sort;
      button.title = state.cursor ? '불러온 항목만 정렬합니다.' : '현재 폴더 정렬';
      button.closest('th').setAttribute('aria-sort', active ? state.direction === 1 ? 'ascending' : 'descending' : 'none');
      button.querySelector('.sort-mark').textContent = active ? state.direction === 1 ? '↑' : '↓' : '';
    });
    const value = state.sort + ':' + state.direction;
    if ([...$('mobile-sort').options].some(option => option.value === value)) $('mobile-sort').value = value;
    renderSelection();
  }
  function renderSelection() {
    $('notice').hidden = !state.notice;
    $('notice').textContent = state.notice;
    if (!state.zipEnabled) {
      $('selection-tools').hidden = true;
      document.body.classList.remove('has-selection');
      return;
    }
    const items = state.entries;
    const selected = state.selected.size;
    document.querySelectorAll('[data-select-key]').forEach(checkbox => {
      checkbox.checked = state.selected.has(checkbox.dataset.selectKey);
      checkbox.disabled = state.zipPreparing || (!checkbox.checked && selected >= state.zipMaxFiles);
      checkbox.closest('tr').classList.toggle('selected', checkbox.checked);
    });
    $('select-all').checked = items.length > 0 && items.every(e => state.selected.has(e.key));
    $('select-all').indeterminate = selected > 0 && !$('select-all').checked;
    $('select-all').disabled = !items.length || state.loading || state.zipPreparing;
    $('selection-tools').hidden = !selected;
    document.body.classList.toggle('has-selection', selected > 0);
    $('clear-selection').disabled = state.zipPreparing;
    $('download-zip').hidden = !selected;
    $('download-zip').disabled = state.zipPreparing || state.loading;
    $('download-zip').textContent = state.zipPreparing ? '확인 중…' : `ZIP 다운로드 (${selected})`;
  }
  // BROWSER_ZIP_ENABLED=false: drop the selection column entirely. A missing
  // flag (older servers) keeps ZIP enabled.
  function disableZIP() {
    state.zipEnabled = false;
    state.selected.clear();
    state.zipController?.abort(); state.zipController = null; state.zipPreparing = false;
    document.querySelector('col.select-col')?.remove();
    $('select-all')?.closest('th')?.remove();
    $('selection-tools').hidden = true;
  }
  async function downloadZIP() {
    if (!state.zipEnabled || !state.selected.size || state.zipPreparing) return;
    const keys = [...state.selected];
    const estimatedSize = state.entries.filter(e => !e.folder && state.selected.has(e.key)).reduce((n, e) => n + (e.size || 0), 0);
    if (estimatedSize > state.zipMaxBytes) {
      state.notice = `선택 파일 합계가 ZIP 제한 ${formatSize(state.zipMaxBytes)}를 넘었습니다.`;
      renderSelection(); return;
    }
    const controller = new AbortController();
    state.zipController = controller; state.zipPreparing = true;
    state.notice = state.entries.some(e => e.folder && state.selected.has(e.key)) ? '선택한 폴더의 하위 파일과 용량을 확인하고 있습니다.' : '';
    renderSelection();
    try {
      const response = await fetch('/api/archive', {
        method: 'POST', credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        headers: { 'Content-Type': 'application/json', 'X-Mori-Request': '1' },
        body: JSON.stringify({ prefix: state.prefix, keys })
      });
      const data = await response.json().catch(() => ({}));
      if (controller !== state.zipController) return;
      if (!response.ok) throw new Error(data.message || 'ZIP 다운로드를 준비하지 못했습니다.');
      if (typeof data.url !== 'string' || !/^\/api\/archive\?token=[A-Za-z0-9_-]{43}$/.test(data.url)) throw new Error('ZIP 다운로드 응답이 올바르지 않습니다.');
      const link = element('a');
      link.href = data.url; link.download = typeof data.filename === 'string' ? data.filename : 'files.zip';
      link.hidden = true; document.body.append(link); link.click(); link.remove();
      state.notice = `${Number.isInteger(data.files) ? data.files + '개 파일 · ' : ''}ZIP 다운로드를 요청했습니다. 진행 상태는 브라우저에서 확인해 주세요.`;
    } catch (error) {
      if (controller !== state.zipController || error.name === 'AbortError') return;
      state.notice = error.message || 'ZIP 다운로드를 준비하지 못했습니다.';
    } finally {
      if (controller === state.zipController) {
        state.zipPreparing = false; state.zipController = null; renderSelection();
      }
    }
  }
  async function load({ append = false, refresh = false } = {}) {
    state.controller?.abort();
    const controller = new AbortController();
    state.controller = controller;
    let prefix;
    try { prefix = prefixFromHash(); }
    catch (error) {
      state.prefix = ''; state.entries = []; state.cursor = ''; state.loading = false; state.selected.clear();
      state.zipController?.abort(); state.zipController = null; state.zipPreparing = false; state.notice = '';
      state.error = error instanceof URIError ? '올바르지 않은 폴더 경로입니다.' : error.message;
      renderBreadcrumbs(); render(); return;
    }
    if (!append) {
      state.entries = []; state.cursor = ''; state.selected.clear(); state.notice = '';
      state.zipController?.abort(); state.zipController = null; state.zipPreparing = false;
    }
    state.prefix = prefix; state.loading = true; state.error = '';
    renderBreadcrumbs(); render();
    const requestedCursor = append ? state.cursor : '';
    try {
      const data = await api('list', { prefix, ...(requestedCursor ? { cursor: requestedCursor } : {}), ...(refresh ? { refresh: '1' } : {}) }, controller.signal);
      if (controller !== state.controller) return;
      if (!Array.isArray(data.entries) || data.entries.some(e => typeof e.key !== 'string' || typeof e.name !== 'string')) throw new Error('파일 목록 응답이 올바르지 않습니다.');
      if (data.cursor && data.cursor === requestedCursor) throw new Error('다음 목록을 불러올 수 없습니다. 새로고침해 주세요.');
      state.entries = [...new Map([...state.entries, ...data.entries].map(e => [e.key, e])).values()];
      state.cursor = data.cursor || '';
    } catch (error) {
      if (controller !== state.controller || error.name === 'AbortError') return;
      state.error = error.message || '저장소에 연결할 수 없습니다. 새로고침해 주세요.';
    } finally {
      if (controller === state.controller) { state.loading = false; render(); }
    }
  }
  $('select-all').addEventListener('change', () => {
    const items = state.entries;
    if ($('select-all').checked && items.length > state.zipMaxFiles) {
      state.notice = `한 번에 최대 ${state.zipMaxFiles}개까지 가능합니다. 파일 또는 폴더를 개별 선택해 주세요.`;
    } else {
      state.selected = $('select-all').checked ? new Set(items.map(e => e.key)) : new Set();
      state.notice = '';
    }
    renderSelection();
  });
  $('clear-selection').addEventListener('click', () => { state.selected.clear(); state.notice = ''; renderSelection(); });
  $('mobile-sort').addEventListener('change', e => { const [sort, dir] = e.target.value.split(':'); state.sort = sort; state.direction = Number(dir); render(); });
  $('download-zip').addEventListener('click', downloadZIP);
  $('refresh').addEventListener('click', () => load({ refresh: true }));
  $('load-more').addEventListener('click', () => load({ append: true }));
  document.querySelectorAll('[data-sort]').forEach(button => button.addEventListener('click', () => {
    state.direction = state.sort === button.dataset.sort ? -state.direction : 1;
    state.sort = button.dataset.sort; render();
  }));
  addEventListener('hashchange', () => load());
  renderBreadcrumbs();
  api('config').then(config => {
    state.title = typeof config.title === 'string' && config.title ? config.title : 'Files';
    if (Number.isInteger(config.zipMaxFiles) && config.zipMaxFiles > 0 && config.zipMaxFiles <= 10000) state.zipMaxFiles = config.zipMaxFiles;
    if (Number.isFinite(config.zipMaxBytes) && config.zipMaxBytes > 0) state.zipMaxBytes = config.zipMaxBytes;
    state.downloadMode = config.downloadMode === 'presigned' ? 'presigned' : 'proxy';
    state.previewMode = config.previewMode === 'presigned' ? 'presigned' : 'proxy';
    if (config.zipEnabled === false) disableZIP();
    renderBreadcrumbs(); render();
  }).catch(() => { /* The listing request reports any connection or authentication error. */ });
  load();
})();
