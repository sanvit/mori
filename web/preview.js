'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const dialog = $('preview');
  const body = $('preview-body');
  const MAX_TEXT = 1024 * 1024;
  const LIB = { media: '/_mori/vendor/media-chrome-4.19.2/index.js', pdf: '/_mori/vendor/pdfjs-6.3.289/pdf.mjs', worker: '/_mori/vendor/pdfjs-6.3.289/pdf.worker.mjs', base: '/_mori/vendor/pdfjs-6.3.289/' };
  const kinds = {
    image: 'png jpg jpeg gif webp avif bmp ico',
    video: 'mp4 m4v webm ogv mov',
    audio: 'mp3 m4a aac wav ogg oga opus flac',
    pdf: 'pdf',
    text: 'txt md markdown rst log csv tsv json jsonl ndjson yaml yml toml ini conf cfg env sh bash zsh bat ps1 go py js mjs cjs jsx ts tsx java c h cc cpp cs rs rb php html htm css scss xml svg sql vue svelte gitignore vtt srt'
  };
  const extKinds = new Map(Object.entries(kinds).flatMap(([kind, list]) => list.split(' ').map(ext => [ext, kind])));
  const labels = { image: '이미지', video: '영상', audio: '오디오', pdf: 'PDF', text: '텍스트', unsupported: '파일' };
  let active = null, playlist = [], trigger = null, historyID = null, closingHistory = false;
  let mediaPromise = null, pdfPromise = null;
  const el = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  };
  const svg = (name, cls = '') => {
    const paths = {
      play: 'm9 5 11 7-11 7Z', music: 'M9 18V5l11-2v12M9 7l11-2M9 18c0 2-6 3-6 0s6-3 6 0Zm11-3c0 2-6 3-6 0s6-3 6 0Z',
      file: 'M6 2h8l5 5v15H6ZM14 2v6h5M9 13h7M9 17h7', left: 'm14 5-7 7 7 7', right: 'm10 5 7 7-7 7',
      plus: 'M12 5v14M5 12h14', minus: 'M5 12h14', fit: 'M9 3H3v6m12-6h6v6M3 15v6h6m6 0h6v-6'
    };
    const n = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    for (const [k, v] of Object.entries({ viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.6', 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true', class: cls })) n.setAttribute(k, v);
    const p = document.createElementNS(n.namespaceURI, 'path'); p.setAttribute('d', paths[name] || paths.file); n.append(p); return n;
  };
  function button(label, handler, glyph, className = 'preview-tool') {
    const n = el('button', className, glyph ? undefined : label); n.type = 'button'; n.title = label; n.setAttribute('aria-label', label);
    if (glyph) n.append(svg(glyph));
    n.addEventListener('click', handler); return n;
  }
  function kindOf(name) {
    const base = String(name).split('/').pop().toLowerCase();
    if (['readme', 'license', 'dockerfile', 'makefile', '.env', '.gitignore'].includes(base)) return 'text';
    return extKinds.get(base.includes('.') ? base.split('.').pop() : '') || 'unsupported';
  }
  function size(n) { if (!Number.isFinite(n) || n < 0) return ''; if (n < 1024) return n + ' B'; const p = Math.min(4, Math.floor(Math.log2(n) / 10)); return (n / 1024 ** p).toFixed(1) + ' ' + ['B', 'KiB', 'MiB', 'GiB', 'TiB'][p]; }
  function objectURL(entry, download = false) { return '/_mori/api/object?' + new URLSearchParams({ key: entry.key, ...(download ? { download: '1' } : {}) }); }
  function current(s) { return active === s && !s.abort.signal.aborted && dialog.open; }
  function cleanup() {
    const s = active; active = null;
    if (s) { s.abort.abort(); for (const fn of s.cleanups.reverse()) { try { fn(); } catch { /* Best effort release of independent resources. */ } } }
    body.replaceChildren();
  }
  function hide() {
    cleanup();
    if (dialog.open) dialog.close();
    document.body.classList.remove('preview-open');
    if (trigger?.isConnected) trigger.focus({ preventScroll: true });
  }
  function close() {
    const back = historyID && history.state?.moriPreview === historyID;
    historyID = null; hide();
    if (back) { closingHistory = true; history.back(); }
  }
  function navigation() {
    const index = playlist.findIndex(e => e.key === active?.entry.key);
    $('preview-previous').disabled = index <= 0;
    $('preview-next').disabled = index < 0 || index >= playlist.length - 1;
    $('preview-counter').textContent = index >= 0 ? `${index + 1} / ${playlist.length}` : '';
  }
  function move(delta) {
    const index = playlist.findIndex(e => e.key === active?.entry.key), next = playlist[index + delta];
    if (next) show(next);
  }
  function message(s, heading, detail, retry = true) {
    if (!current(s)) return;
    body.setAttribute('aria-busy', 'false');
    const box = el('div', 'preview-message'); box.setAttribute('role', 'status');
    box.append(svg(s.kind === 'audio' ? 'music' : 'file', 'message-icon'), el('h3', '', heading), el('p', '', detail));
    const actions = el('div', 'message-actions');
    if (retry) actions.append(button('다시 불러오기', () => show(s.entry), null, 'preview-button'));
    const link = el('a', 'preview-button', '다운로드'); link.href = objectURL(s.entry, true); link.download = s.entry.name;
    actions.append(link); box.append(actions); body.replaceChildren(box);
  }
  function error(s, heading = '미리보기를 불러오지 못했습니다.') {
    const extra = s.source?.mode === 'presigned' ? ' 서명 주소가 만료되었거나 S3 CORS 설정이 필요할 수도 있습니다.' : '';
    message(s, heading, '파일 형식과 연결 상태를 확인해 주세요.' + extra + ' 원본은 다운로드해서 열 수 있습니다.');
  }
  async function descriptor(s) {
    const response = await fetch('/_mori/api/preview?' + new URLSearchParams({ key: s.entry.key }), { credentials: 'same-origin', cache: 'no-store', signal: s.abort.signal });
    const value = await response.json();
    if (!response.ok) throw new Error('preview source unavailable');
    if (!['image', 'video', 'audio', 'pdf', 'text', 'unsupported'].includes(value.kind)) throw new Error('invalid preview type');
    if (value.kind !== 'unsupported') {
      const source = new URL(value.url, location.href);
      if (!['http:', 'https:'].includes(source.protocol) || source.username || source.password) throw new Error('invalid source');
      // Even a malformed helper response cannot forward the site's credentials.
      if (value.mode !== 'presigned' && (source.origin !== location.origin || source.pathname !== '/_mori/api/object')) throw new Error('invalid proxy source');
    }
    return value;
  }
  async function show(entry) {
    cleanup();
    const s = { entry, kind: kindOf(entry.name), abort: new AbortController(), cleanups: [], source: null };
    active = s;
    dialog.dataset.kind = s.kind;
    $('preview-title').textContent = entry.name;
    $('preview-meta').textContent = [labels[s.kind], size(entry.size)].filter(Boolean).join(' · ');
    $('preview-download').href = objectURL(entry, true); $('preview-download').download = entry.name;
    $('preview-original').href = objectURL(entry);
    const loading = el('div', 'preview-message'); loading.append(el('span', 'spinner'), el('p', '', '불러오는 중…'));
    body.replaceChildren(loading); body.setAttribute('aria-busy', 'true'); navigation();
    try {
      s.source = await descriptor(s);
      if (!current(s)) return;
      s.kind = s.source.kind; dialog.dataset.kind = s.kind;
      $('preview-meta').textContent = [s.source.renderHTML ? 'HTML' : labels[s.kind], size(entry.size)].filter(Boolean).join(' · ');
      if (s.kind === 'image') await image(s);
      else if (s.kind === 'audio' || s.kind === 'video') await media(s);
      else if (s.kind === 'pdf') await pdf(s);
      else if (s.kind === 'text' && s.source.renderHTML) html(s);
      else if (s.kind === 'text') await text(s);
      else message(s, '다운로드해서 열 수 있는 파일입니다.', '이 형식은 브라우저 안에서 미리보기를 제공하지 않습니다.', false);
    } catch (e) {
      if (current(s) && e.name !== 'AbortError') error(s);
    }
  }
  function open(entry, entries, opener) {
    if (closingHistory) return;
    playlist = entries.filter(e => !e.folder && kindOf(e.name) !== 'unsupported');
    trigger = opener || document.activeElement;
    if (!dialog.open) {
      historyID = 'mori-' + Date.now() + '-' + Math.random().toString(36).slice(2);
      history.pushState({ ...history.state, moriPreview: historyID }, '', location.href);
      dialog.showModal(); document.body.classList.add('preview-open'); $('preview-close').focus();
    }
    show(entry);
  }
  async function image(s) {
    const wrap = el('div', 'image-preview'); const stage = el('div', 'image-stage');
    const img = el('img', 'preview-image'); img.alt = s.entry.name; img.decoding = 'async'; img.draggable = false;
    stage.append(img);
    let factor = 1;
    const update = () => {
      stage.classList.toggle('zoomed', factor !== 1);
      img.style.width = factor === 1 ? '' : Math.round(img.naturalWidth * factor) + 'px';
      img.style.height = factor === 1 ? '' : Math.round(img.naturalHeight * factor) + 'px';
      caption.textContent = factor === 1 ? '화면에 맞춤' : Math.round(factor * 100) + '%';
    };
    const controls = el('div', 'image-tools');
    const caption = el('span', 'zoom-label', '화면에 맞춤');
    controls.append(button('축소', () => { factor = Math.max(.25, factor - .25); update(); }, 'minus'), caption, button('확대', () => { factor = Math.min(4, factor + .25); update(); }, 'plus'), button('화면에 맞춤', () => { factor = 1; update(); }, 'fit'));
    img.addEventListener('dblclick', () => { factor = factor === 1 ? 2 : 1; update(); });
    img.addEventListener('load', () => { if (current(s)) { body.setAttribute('aria-busy', 'false'); } }, { once: true });
    img.addEventListener('error', () => error(s, '이 이미지를 표시할 수 없습니다.'), { once: true });
    s.cleanups.push(() => { img.removeAttribute('src'); });
    wrap.append(stage, controls); body.replaceChildren(wrap); img.src = s.source.url;
  }
  async function media(s) {
    const audio = s.kind === 'audio';
    const wrap = el('div', audio ? 'audio-preview' : 'video-preview');
    if (audio) {
      const art = el('div', 'audio-art'); art.append(svg('music'));
      wrap.append(art, el('div', 'audio-name', s.entry.name));
    }
    const controller = el('media-controller', audio ? 'mori-player audio-player' : 'mori-player');
    controller.setAttribute('defaultstreamtype', 'on-demand'); controller.setAttribute('autohide', '3');
    controller.setAttribute('novolumepref', ''); controller.setAttribute('nomutedpref', '');
    if (audio) controller.setAttribute('audio', '');
    const content = el(audio ? 'audio' : 'video'); content.slot = 'media'; content.preload = 'metadata'; content.controls = true;
    content.setAttribute('aria-label', s.entry.name); content.setAttribute('playsinline', '');
    // No crossorigin attribute is needed for ordinary media playback. No object Blob.
    s.cleanups.push(() => { content.pause(); content.removeAttribute('src'); content.load(); });
    content.addEventListener('loadedmetadata', () => { if (current(s)) body.setAttribute('aria-busy', 'false'); });
    content.addEventListener('error', () => error(s, '이 브라우저에서 재생할 수 없습니다.'));
    controller.append(content);
    const chrome = el('div', 'player-chrome');
    const timeline = el('media-time-range'); timeline.setAttribute('aria-label', '재생 위치'); timeline.setAttribute('notooltip', ''); chrome.append(timeline);
    const bar = el('media-control-bar');
    const control = (tag, label, attrs = {}) => { const n = el(tag); n.title = label; n.setAttribute('aria-label', label); for (const [k,v] of Object.entries(attrs)) n.setAttribute(k,v); return n; };
    bar.append(control('media-play-button', '재생 / 일시정지'), control('media-seek-backward-button', '10초 뒤로', { seekoffset: '10' }), control('media-seek-forward-button', '10초 앞으로', { seekoffset: '10' }), control('media-time-display', '재생 시간', { showduration: '' }));
    bar.append(el('span', 'player-spacer'), control('media-playback-rate-button', '재생 속도', { rates: '0.5 0.75 1 1.25 1.5 2' }), control('media-mute-button', '음소거'), control('media-volume-range', '음량'));
    if (!audio) bar.append(control('media-pip-button', '작은 화면으로 보기'), control('media-fullscreen-button', '전체 화면'));
    chrome.append(bar); chrome.hidden = true; controller.append(chrome);
    wrap.append(controller); body.replaceChildren(wrap);
    content.src = s.source.url;
    try {
      mediaPromise ||= import(LIB.media).catch(e => { mediaPromise = null; throw e; });
      await mediaPromise;
      if (!current(s)) return;
      if (!customElements.get('media-controller')) throw new Error('media chrome unavailable');
      content.controls = false; chrome.hidden = false; controller.classList.add('enhanced');
      if (!audio) {
        const play = control('media-play-button', '재생 / 일시정지'); play.className = 'center-play'; play.slot = 'centered-chrome'; controller.append(play);
      }
    } catch {
      if (current(s)) {
        // A packaging/network failure must not leave the user without playback controls.
        content.controls = true; controller.classList.add('native-player');
        body.setAttribute('aria-busy', 'false');
      }
    }
  }
  function html(s) {
    const frame = el('iframe', 'html-preview');
    frame.title = s.entry.name;
    frame.setAttribute('sandbox', s.source.htmlScripts ? 'allow-scripts' : '');
    frame.referrerPolicy = 'no-referrer';
    frame.addEventListener('load', () => { if (current(s)) body.setAttribute('aria-busy', 'false'); });
    s.cleanups.push(() => { frame.src = 'about:blank'; });
    // A separate response supplies the configured CSP; never grant same-origin access.
    frame.src = s.source.url; body.replaceChildren(frame);
  }
  async function text(s) {
    const response = await fetch(s.source.url, { signal: s.abort.signal, credentials: s.source.mode === 'presigned' ? 'omit' : 'same-origin', headers: Number(s.entry.size) === 0 ? {} : { Range: `bytes=0-${MAX_TEXT - 1}` } });
    if (!response.ok) throw new Error('text unavailable');
    const reader = response.body.getReader(), decoder = new TextDecoder('utf-8');
    let length = 0, parts = [], limited = Number(s.entry.size) > MAX_TEXT || Number(response.headers.get('Content-Length')) > MAX_TEXT || Number((response.headers.get('Content-Range') || '').split('/')[1]) > MAX_TEXT;
    try {
      while (length < MAX_TEXT) {
        const { value, done } = await reader.read();
        if (done) break;
        const left = MAX_TEXT - length, chunk = value.subarray(0, left);
        parts.push(decoder.decode(chunk, { stream: true })); length += chunk.byteLength;
        if (value.byteLength > left) limited = true;
      }
      // Stop even when a server ignores Range. Never buffer an arbitrary whole file.
      if (length >= MAX_TEXT) await reader.cancel();
      parts.push(decoder.decode());
    } finally { reader.releaseLock(); }
    if (!current(s)) return;
    const wrap = el('div', 'text-preview'); const pre = el('pre', 'preview-code'); pre.tabIndex = 0;
    pre.setAttribute('aria-label', s.entry.name + ' 텍스트 내용'); pre.textContent = parts.join('');
    const tools = el('div', 'text-tools'); const count = el('span', '', limited ? '처음 1 MiB만 표시합니다.' : 'UTF-8 · 읽기 전용');
    const toggle = button('줄바꿈 켜기', () => { const on = pre.classList.toggle('wrap'); toggle.textContent = on ? '줄바꿈 끄기' : '줄바꿈 켜기'; toggle.setAttribute('aria-pressed', String(on)); }, null, 'text-toggle'); toggle.setAttribute('aria-pressed', 'false');
    tools.append(count, toggle); wrap.append(tools, pre); body.replaceChildren(wrap); body.setAttribute('aria-busy', 'false');
  }
  async function pdfText(s, page) {
    // PDF.js getTextContent() uses ReadableStream's async iterator, which
    // Safari/iOS 18 lacks. getReader() keeps accessibility text working there.
    const reader = page.streamTextContent().getReader();
    const cancel = () => { void reader.cancel().catch(() => {}); };
    s.abort.signal.addEventListener('abort', cancel, { once: true });
    const words = []; let length = 0, done = false;
    try {
      while (current(s) && length < MAX_TEXT) {
        const chunk = await reader.read();
        if (chunk.done) { done = true; break; }
        for (const item of chunk.value.items) {
          const word = String(item.str || '').slice(0, MAX_TEXT - length);
          words.push(word); length += word.length + 1;
          if (length >= MAX_TEXT) break;
        }
      }
      return words.join(' ');
    } finally {
      s.abort.signal.removeEventListener('abort', cancel);
      if (!done) await reader.cancel().catch(() => {});
      reader.releaseLock();
    }
  }
  async function pdf(s) {
    pdfPromise ||= import(LIB.pdf).catch(e => { pdfPromise = null; throw e; });
    let pdfjs;
    try { pdfjs = await pdfPromise; }
    catch { if (current(s)) message(s, 'PDF 뷰어를 불러오지 못했습니다.', '새 탭에서 보기 또는 다운로드로 확인할 수 있습니다.'); return; }
    if (!current(s)) return;
    pdfjs.GlobalWorkerOptions.workerSrc = LIB.worker;
    const task = pdfjs.getDocument({
      url: s.source.url, withCredentials: s.source.mode !== 'presigned',
      cMapUrl: LIB.base + 'cmaps/', cMapPacked: true, wasmUrl: LIB.base + 'wasm/',
      standardFontDataUrl: LIB.base + 'standard_fonts/',
      isEvalSupported: false, enableXfa: false, useSystemFonts: true,
      disableAutoFetch: true, disableStream: true, rangeChunkSize: 256 * 1024,
      isOffscreenCanvasSupported: false, isImageDecoderSupported: false,
      maxImageSize: 16 * 1024 * 1024, canvasMaxAreaInBytes: 16 * 1024 * 1024
    });
    let renderTask = null, timer = null, requestID = 0, destroyed = false;
    s.cleanups.push(() => { destroyed = true; requestID++; clearTimeout(timer); renderTask?.cancel(); task.destroy().catch(() => {}); });
    task.onPassword = (submit, reason) => {
      if (!current(s)) return;
      const form = el('form', 'pdf-password'); form.autocomplete = 'off';
      form.append(el('h3', '', '암호가 설정된 PDF입니다.'), el('p', '', reason === pdfjs.PasswordResponses.INCORRECT_PASSWORD ? '암호가 올바르지 않습니다. 다시 입력해 주세요.' : '암호는 브라우저에서만 사용되며 서버로 보내지 않습니다.'));
      const label = el('label', '', 'PDF 암호'); const input = el('input'); input.type = 'password'; input.required = true; input.autocomplete = 'off'; label.append(input);
      const go = el('button', 'preview-button', '열기'); go.type = 'submit'; form.append(label, go);
      form.addEventListener('submit', e => { e.preventDefault(); const value = input.value; input.value = ''; go.disabled = true; submit(value); });
      body.replaceChildren(form); body.setAttribute('aria-busy', 'false'); input.focus();
    };
    let doc;
    try { doc = await task.promise; }
    catch (e) { if (current(s)) error(s, 'PDF를 열 수 없습니다.'); return; }
    if (!current(s)) return;
    const wrap = el('div', 'pdf-preview'), toolbar = el('div', 'pdf-tools'), stage = el('div', 'pdf-stage');
    let zoom = 1, running = false, pending = false;
    const pages = [];
    const total = el('span', 'page-total', doc.numPages + ' 페이지');
    const out = button('축소', () => { zoom = Math.max(.5, zoom - .25); layout(); }, 'minus');
    const into = button('확대', () => { zoom = Math.min(3, zoom + .25); layout(); }, 'plus');
    const fit = button('폭에 맞춤', () => { zoom = 1; layout(); }, 'fit');
    toolbar.append(total, el('span', 'pdf-tool-space'), out, into, fit);
    stage.tabIndex = 0; stage.setAttribute('aria-label', 'PDF 페이지 연속 스크롤');
    wrap.append(toolbar, stage); body.replaceChildren(wrap);
    const first = await doc.getPage(1);
    if (!current(s)) return;
    const initial = first.getViewport({ scale: 1 });
    for (let i = 1; i <= doc.numPages; i++) {
      const slot = el('div', 'pdf-page');
      slot.setAttribute('aria-label', `${i} / ${doc.numPages} 페이지`);
      stage.append(slot);
      pages.push({ number: i, slot, width: initial.width, height: initial.height, canvas: null, page: null });
    }
    function release(p) {
      if (p.canvas) { p.canvas.width = 0; p.canvas.height = 0; p.canvas.remove(); p.canvas = null; }
      p.slot.replaceChildren(); p.page?.cleanup(); p.page = null;
    }
    function dimensions(p) {
      const scale = Math.max(.1, (stage.clientWidth - 32) / p.width) * zoom;
      p.slot.style.width = p.width * scale + 'px'; p.slot.style.height = p.height * scale + 'px';
      return scale;
    }
    function layout() {
      body.setAttribute('aria-busy', 'true');
      requestID++; renderTask?.cancel();
      for (const p of pages) { if (!running) release(p); dimensions(p); }
      schedule();
    }
    function schedule() { pending = true; if (!running) void renderVisible(); }
    async function renderVisible() {
      running = true; pending = false;
      const id = requestID;
      try {
        const bounds = stage.getBoundingClientRect();
        const nearby = pages.filter(p => {
          const box = p.slot.getBoundingClientRect();
          return box.bottom > bounds.top - 100 && box.top < bounds.bottom + 100;
        });
        for (const p of pages) if (!nearby.includes(p) || p.version !== id) release(p);
        for (const p of nearby) {
          if (destroyed || !current(s) || id !== requestID) break;
          if (p.canvas) continue;
          const page = await doc.getPage(p.number);
          if (destroyed || !current(s) || id !== requestID) break;
          p.page = page;
          const original = page.getViewport({ scale: 1 }); p.width = original.width; p.height = original.height;
          const viewport = page.getViewport({ scale: dimensions(p) });
          // Stay below iOS canvas limits, including very tall/wide PDF pages.
          const dpr = Math.min(window.devicePixelRatio || 1, 2, 4096 / viewport.width, 4096 / viewport.height, Math.sqrt(4 * 1024 * 1024 / (viewport.width * viewport.height)));
          const canvas = el('canvas', 'pdf-canvas'); p.canvas = canvas; p.version = id;
          canvas.width = Math.max(1, Math.floor(viewport.width * dpr)); canvas.height = Math.max(1, Math.floor(viewport.height * dpr));
          canvas.style.width = viewport.width + 'px'; canvas.style.height = viewport.height + 'px';
          canvas.setAttribute('role', 'img'); canvas.setAttribute('aria-label', `${s.entry.name}, ${p.number} / ${doc.numPages} 페이지`);
          p.slot.append(canvas);
          renderTask = page.render({ canvasContext: canvas.getContext('2d'), viewport, transform: [dpr, 0, 0, dpr, 0, 0] });
          await renderTask.promise; renderTask = null;
          if (!current(s) || id !== requestID) break;
          body.setAttribute('aria-busy', 'false');
          try {
            const content = await pdfText(s, page);
            if (!current(s) || id !== requestID) break;
            const accessible = el('div', 'sr-only'); accessible.setAttribute('role', 'document');
            accessible.textContent = content; p.slot.append(accessible);
          } catch {
            // Text extraction is supplementary. Never replace a successfully
            // rendered PDF with a page-rendering error when only text fails.
          }
        }
      } catch (e) {
        if (current(s) && id === requestID && e.name !== 'RenderingCancelledException') error(s, '이 PDF 페이지를 표시할 수 없습니다.');
      } finally {
        renderTask = null; running = false;
        if (!destroyed && current(s) && (pending || id !== requestID)) schedule();
      }
    }
    stage.addEventListener('scroll', schedule, { passive: true });
    let width = stage.clientWidth;
    const observer = new ResizeObserver(() => {
      if (stage.clientWidth === width) return;
      width = stage.clientWidth; clearTimeout(timer); timer = setTimeout(layout, 120);
    }); observer.observe(stage);
    s.cleanups.push(() => { observer.disconnect(); for (const p of pages) release(p); });
    layout();
  }
  $('preview-close').addEventListener('click', close);
  $('preview-previous').addEventListener('click', () => move(-1));
  $('preview-next').addEventListener('click', () => move(1));
  dialog.addEventListener('cancel', e => { e.preventDefault(); close(); });
  dialog.addEventListener('click', e => { if (e.target === dialog) close(); });
  dialog.addEventListener('keydown', e => {
    if (active?.kind !== 'image' || ['INPUT', 'BUTTON', 'A'].includes(e.target.tagName)) return;
    if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') { e.preventDefault(); move(e.key === 'ArrowLeft' ? -1 : 1); }
  });
  addEventListener('popstate', () => { closingHistory = false; if (dialog.open && history.state?.moriPreview !== historyID) { historyID = null; hide(); } });
  addEventListener('hashchange', () => { historyID = null; if (dialog.open) hide(); });
  addEventListener('pagehide', () => { if (dialog.open) hide(); });
  window.MoriPreview = Object.freeze({ open, close, kindOf });
})();
