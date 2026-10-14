'use strict';

// No client persistence of passwords or TMDB credentials. Paths and metadata
// are inserted as text; files are changed only by the explicitly checked plan.
const $ = id => document.getElementById(id);
const appBase = new URL('.', location.href).pathname;
const state = {
  csrf: '', setup: false, authMode: 'local', dsmToken: '', settings: {}, inventory: null, inventoryId: '',
  selections: Object.create(null), focusId: '', draft: {}, plan: null,
  confirmed: new Set(), excluded: new Set(), busy: 0, folder: null, folderTarget: '', groups: [], matches: {}, automatic: new Set(), preparing: false, operationActive: false, pollTimer: null, journalKey: ''
};
const kindLabel = kind => kind === 'tv' ? 'Сериал' : 'Фильм';
const filename = path => String(path || '').split('/').pop();
const list = value => Array.isArray(value) ? value : [];
const copy = value => JSON.parse(JSON.stringify(value));

function node(tag, text, className) {
  const el = document.createElement(tag);
  if (text !== undefined) el.textContent = String(text);
  if (className) el.className = className;
  return el;
}

function message(text, type = '', target = 'global-status') {
  const el = $(target);
  el.textContent = text;
  el.classList.remove('error', 'success');
  if (type) el.classList.add(type);
}

function setBusy(delta) {
  state.busy += delta;
  if (delta > 0 && state.busy === 1) {
    document.querySelectorAll('button,input,select').forEach(el => {
      el.dataset.disabledBeforeBusy = el.disabled ? '1' : '0';
      el.disabled = true;
    });
    document.body.setAttribute('aria-busy', 'true');
  } else if (state.busy === 0) {
    document.querySelectorAll('button,input,select').forEach(el => {
      el.disabled = el.dataset.disabledBeforeBusy === '1';
      delete el.dataset.disabledBeforeBusy;
    });
    document.body.removeAttribute('aria-busy');
    syncButtons();
  }
}

function syncButtons() {
  $('stop-operation').disabled = !state.operationActive;
  if (state.busy || state.preparing) return;
  $('create-plan').disabled = !state.inventory || !Object.keys(state.selections).length;
  $('apply-plan').disabled = !state.plan || !state.confirmed.size;
  $('confirm-all').disabled = !state.plan || !videoEntries().length;
  $('folder-select').disabled = !state.folder;
  $('folder-parent').disabled = !state.folder || !state.folder.parent || state.folder.parent === state.folder.path;
  if (state.operationActive) {
    $('prepare-button').disabled = true;
    $('create-plan').disabled = true;
    $('apply-plan').disabled = true;
    $('settings-form').querySelector('button[type=submit]').disabled = true;
  } else {
    $('prepare-button').disabled = false;
    $('settings-form').querySelector('button[type=submit]').disabled = false;
  }
}

async function api(path, body, quiet = false) {
  if (!quiet) setBusy(1);
  try {
    const headers = { Accept: 'application/json' };
    if (state.dsmToken) headers['X-Syno-Token'] = state.dsmToken;
    const options = { credentials: 'same-origin', headers, cache: 'no-store' };
    if (body === undefined) options.signal = AbortSignal.timeout(15000);
    if (body !== undefined) {
      options.method = 'POST';
      headers['Content-Type'] = 'application/json';
      headers['X-CSRF-Token'] = state.csrf;
      options.body = JSON.stringify(body);
    }
    let response;
    try { response = await fetch(appBase + path.replace(/^\//, ''), options); }
    catch (_) { throw new Error('Нет связи с NAS. Проверьте подключение и состояние пакета.'); }
    let result;
    try { result = await response.json(); }
    catch (_) { throw new Error('Сервер вернул неожиданный ответ. Проверьте журнал пакета.'); }
    if (!response.ok) {
      if (response.status === 401 && path !== '/api/login' && path !== '/api/setup') {
        showAuth(false);
        message(state.authMode === 'dsm' ? 'Сессия DSM завершилась или доступ запрещён. Войдите в DSM учётной записью администратора и откройте приложение заново.' : 'Сессия завершилась. Войдите снова.', '', 'auth-status');
      }
      const error = new Error(typeof result.error === 'string' ? result.error : `Ошибка сервера (${response.status}).`);
      error.result = result;
      throw error;
    }
    return result;
  } finally { if (!quiet) setBusy(-1); }
}

function action(handler, target = 'global-status') {
  return async event => {
    event.preventDefault();
    if (state.busy || state.preparing) return;
    try { await handler(event); }
    catch (error) { message(error.message || 'Не удалось выполнить операцию.', 'error', target); }
  };
}

function showAuth(setup) {
  clearTimeout(state.pollTimer);
  state.csrf = '';
  state.setup = setup;
  if ($('folder-dialog').open) $('folder-dialog').close();
  state.folder = null;
  $('app-view').hidden = true;
  $('auth-view').hidden = false;
  $('auth-form').hidden = state.authMode === 'dsm';
  $('dsm-login').hidden = state.authMode !== 'dsm';
  $('setup-token-label').hidden = !setup;
  $('setup-token').required = setup;
  $('auth-password').autocomplete = setup ? 'new-password' : 'current-password';
  $('auth-password').minLength = setup ? 12 : 1;
  $('auth-submit').textContent = setup ? 'Настроить приложение' : 'Войти';
  $('auth-description').textContent = state.authMode === 'dsm'
    ? 'Откройте приложение из DSM после входа учётной записью администратора NAS. Отдельный пароль Plex Helper не нужен.'
    : setup
    ? 'Введите код из файла setup-token в папке состояния пакета и создайте пароль администратора (от 12 символов).'
    : 'Войдите, чтобы работать с медиатекой на NAS.';
  clearInventory();
}

async function loadState() {
  if (appBase === '/webman/3rdparty/plex-helper/' && !state.dsmToken) {
    state.authMode = 'dsm';
    // Read the current DSM session's CSRF token from DSM's own endpoint.
    // Keep it only in page memory; never put it in URLs or browser storage.
    const response = await fetch('/webman/login.cgi', {credentials:'same-origin', cache:'no-store'});
    if (!response.ok) throw new Error('Не удалось проверить текущий вход DSM. Откройте приложение из DSM заново.');
    const login = await response.json();
    if (login.success && typeof login.SynoToken === 'string') state.dsmToken = login.SynoToken;
  }
  const result = await api('/api/state');
  state.authMode = result.authMode || 'local';
  document.body.dataset.authError = result.authError || '';
  if (!result.authenticated) {
    showAuth(Boolean(result.setupRequired));
    if (result.authError === 'helper_failed') {
      message('Не удалось проверить вход DSM. Проверьте журнал пакета.', 'error', 'auth-status');
    }
    return;
  }
  state.csrf = result.csrf || state.csrf;
  state.settings = result.settings || {};
  $('auth-view').hidden = true;
  $('app-view').hidden = false;
  $('logout').hidden = state.authMode === 'dsm';
  $('dsm-user').hidden = state.authMode !== 'dsm';
  $('dsm-user').textContent = result.user ? `DSM: ${result.user}` : '';
  $('version').textContent = result.version ? `v${result.version}` : 'Synology NAS';
  $('settings-source').value = state.settings.sourceRoot || '';
  $('settings-library').value = state.settings.libraryRoot || '';
  $('scan-source').value = state.settings.sourceRoot || '';
  $('settings-language').value = state.settings.language || 'ru-RU';
  $('settings-naming-language').value = state.settings.namingLanguage || 'en-US';
  $('settings-token').value = '';
  $('clear-token').checked = false;
  $('token-status').textContent = state.settings.tmdbConfigured
    ? 'Токен TMDB сохранён. Оставьте поле пустым, чтобы сохранить его.'
    : 'Токен TMDB не настроен. Ручное распознавание доступно без него.';
  if (!state.settings.sourceRoot || !state.settings.libraryRoot) $('settings-panel').open = true;
  try { await refreshJournals(); }
  catch (error) { if ($('auth-view').hidden) message(`Не удалось загрузить журнал: ${error.message}`, 'error'); }
  if ($('auth-view').hidden) pollJournals();
}

function clearPlan() {
  state.plan = null;
  state.confirmed.clear();
  $('preview-panel').hidden = true;
  $('preview-list').replaceChildren();
  syncButtons();
}

function clearInventory() {
  state.inventory = null;
  state.inventoryId = '';
  state.selections = Object.create(null);
  state.focusId = '';
  state.draft = {};
  state.groups = [];
  state.matches = {};
  state.automatic.clear();
  state.excluded.clear();
  clearPlan();
  $('workspace').hidden = true;
  $('file-list').replaceChildren();
  $('candidates').replaceChildren();
  $('editor-content').hidden = true;
  $('editor-empty').hidden = false;
  $('scan-summary').textContent = 'Начните с папки для подготовки';
}

function renderIssues(id, issues) {
  const container = $(id);
  container.replaceChildren();
  container.hidden = !list(issues).length;
  list(issues).forEach(issue => container.append(node('p', [issue.source, issue.message].filter(Boolean).join(': '))));
}

function hintText(parsed) {
  const parts = [kindLabel(parsed.kind)];
  if (parsed.year) parts.push(String(parsed.year));
  if (parsed.kind === 'tv') {
    if (parsed.season !== undefined && parsed.season !== null) parts.push(`Сезон ${parsed.season}`);
    if (list(parsed.episodes).length) parts.push(`${parsed.episodes.length > 1 ? 'Серии' : 'Серия'} ${parsed.episodes.join(', ')}`);
    if (parsed.air_date) parts.push(parsed.air_date);
  }
  return parts.join(' · ');
}

function renderFiles() {
  const items = list(state.inventory && state.inventory.items);
  $('workspace').hidden = false;
  $('file-count').textContent = `${items.length} файлов`;
  $('empty-files').hidden = Boolean(items.length);
  const ready = Object.keys(state.selections).length;
  $('matched-count').textContent = `Готово: ${ready} из ${items.length}`;
  $('match-summary').textContent = ready === items.length && items.length
    ? 'Все совпадения выбраны. Проверьте предпросмотр ниже. Если нужно, нажмите название сериала или файла, чтобы заменить совпадение.'
    : `Требуют проверки: ${items.length - ready}. Выберите название сериала один раз для всех его серий. Файлы с неясным номером серии исправьте вручную.`;
  const body = $('file-list');
  body.replaceChildren();
  const groupedItems = state.groups.flatMap(g => g.itemIds.map(id => items.find(item => item.id === id)).filter(Boolean));
  const groupStarts = new Map(state.groups.filter(g => g.kind === 'tv' && g.itemIds.length > 1).map(g => [g.itemIds[0], g]));
  for (const item of groupedItems.length ? groupedItems : items) {
    const group = groupStarts.get(item.id);
    if (group) {
      const row = node('tr', undefined, 'series-group');
      const cell = node('td'); cell.colSpan = 3;
      const resolved = group.itemIds.map(id => state.selections[id]).find(Boolean);
      const button = node('button', `${(resolved || group).title} · ${group.itemIds.length} файлов`, 'series-button');
      button.type = 'button';
      button.addEventListener('click', () => { if (!state.busy && !state.preparing) { selectFile(group.itemIds.find(id => !state.selections[id]) || group.itemIds[0]); $('editor-heading').scrollIntoView({behavior:'smooth',block:'center'}); } });
      cell.append(button, node('span', 'Выбрать или заменить сериал сразу для всей группы', 'help'));
      const match = state.matches[group.id];
      if (match && match.reason) cell.append(node('p', match.reason, 'warning'));
      row.append(cell); body.append(row);
    }
    const parsed = item.parsed || {};
    const selected = state.selections[item.id];
    const tr = node('tr');
    if (item.id === state.focusId) tr.classList.add('active');
    const file = node('td');
    const button = node('button', filename(parsed.path), 'filename-button');
    button.type = 'button';
    button.title = parsed.path || '';
    button.addEventListener('click', () => { if (!state.busy && !state.preparing) selectFile(item.id); });
    file.append(button, node('div', parsed.path, 'file-detail'));
    const recognized = node('td');
    recognized.append(node('div', (selected || parsed).title || 'Название не определено', 'recognized-title'));
    recognized.append(node('div', hintText({ ...parsed, ...(selected || {}) }), 'file-detail'));
    if (list(parsed.warnings).length) recognized.append(node('div', parsed.warnings.join('; '), 'warning'));
    const status = node('td');
    status.append(node('span', selected ? (state.automatic.has(item.id) ? 'Подобрано' : 'Выбрано') : 'Проверить', `match-pill${selected ? ' done' : ''}`));
    const match = state.matches[(groupFor(item.id) || {}).id];
    const issue = match && list(match.issues).find(issue => issue.source === parsed.path);
    if (issue) status.append(node('p', issue.message, 'warning'));
    tr.append(file, recognized, status);
    body.append(tr);
  }
  syncButtons();
}

function currentItem() { return list(state.inventory && state.inventory.items).find(item => item.id === state.focusId); }
function groupFor(id) { return state.groups.find(group => group.itemIds.includes(id)); }
function matchScope() { return $('match-scope').value; }
function matchingSeasons() {
  const group = groupFor(state.focusId);
  const seasons = matchScope() === 'group' && group ? [...group.seasons] : [];
  if ($('edit-season').value !== '') seasons.push(Number($('edit-season').value));
  if (group && matchScope() === 'group') for (const id of group.itemIds) {
    const prior = state.selections[id];
    if (prior && prior.season !== undefined && prior.season !== null) seasons.push(prior.season);
  }
  return [...new Set(seasons)].sort((a,b) => a-b);
}
function updateScope() {
  const group = groupFor(state.focusId);
  const count = matchScope() === 'group' && group ? group.itemIds.length : 1;
  $('save-match').textContent = count > 1 ? `Применить ко всем ${count} файлам` : 'Применить к файлу';
  $('group-hint').textContent = count > 1
    ? `Название, год и ID будут общими для ${count} файлов. Сезон и номер каждой серии сохранятся. Поля номеров ниже изменяют только выбранный файл.`
    : 'Изменения относятся только к выбранному файлу.';
}

function selectFile(id) {
  state.focusId = id;
  const item = currentItem();
  if (!item) return;
  const parsed = item.parsed || {};
  state.draft = copy(state.selections[id] || {
    kind: parsed.kind === 'tv' ? 'tv' : 'movie', title: parsed.title || '', year: parsed.year || 0,
    provider: parsed.provider || '', id: parsed.provider_id || '',
    ...(parsed.season !== undefined && parsed.season !== null ? { season: parsed.season } : {}),
    episodes: list(parsed.episodes), air_date: parsed.air_date || ''
  });
  $('editor-empty').hidden = true;
  $('editor-content').hidden = false;
  $('editor-path').textContent = parsed.path;
  $('search-query').value = state.draft.title;
  const group = groupFor(id);
  const isSeries = parsed.kind === 'tv';
  $('scope-label').hidden = !isSeries || !group || group.itemIds.length < 2;
  $('match-scope').value = isSeries && group && group.itemIds.length > 1 ? 'group' : 'file';
  $('manual-details').open = false;
  $('candidates').replaceChildren();
  fillEditor(state.draft);
  updateScope();
  renderFiles();
  const match = group && state.matches[group.id];
  if (match && !state.selections[id] && match.candidates.length) renderCandidates(match.candidates);
}

function fillEditor(metadata) {
  $('edit-title').value = metadata.title || '';
  $('edit-naming-title').value = metadata.naming_title || metadata.title || '';
  $('edit-kind').value = metadata.kind === 'tv' ? 'tv' : 'movie';
  $('edit-year').value = metadata.year || '';
  $('edit-season').value = metadata.season !== undefined && metadata.season !== null ? metadata.season : '';
  $('edit-episodes').value = list(metadata.episodes).join(',');
  $('edit-airdate').value = metadata.air_date || '';
  $('edit-provider').value = metadata.provider || '';
  $('edit-id').value = metadata.id || '';
  updateEpisodeFields();
}

function updateEpisodeFields() { $('episode-fields').hidden = $('edit-kind').value !== 'tv'; }

function readEditor() {
  const result = {
    ...copy(state.draft), kind: $('edit-kind').value, title: $('edit-title').value.trim(),
    naming_title: $('edit-naming-title').value.trim(),
    year: Number($('edit-year').value) || 0,
    provider: $('edit-provider').value, id: $('edit-id').value.trim()
  };
  if (!result.title || !result.naming_title) throw new Error('Укажите название фильма или сериала и название для файлов.');
  if (Boolean(result.provider) !== Boolean(result.id)) throw new Error('Укажите и провайдер, и ID, либо оставьте оба поля пустыми.');
  if (result.kind !== state.draft.kind || result.provider !== state.draft.provider || result.id !== state.draft.id) {
    delete result.seasons;
    delete result.episode_titles;
  }
  delete result.season;
  delete result.episodes;
  delete result.air_date;
  if (result.kind === 'tv') {
    const season = $('edit-season').value.trim();
    const rawEpisodes = $('edit-episodes').value.trim();
    const airDate = $('edit-airdate').value;
    if (season === '') throw new Error('Укажите сезон. Сезон 0 используется для специальных выпусков.');
    result.season = Number(season);
    if (!Number.isInteger(result.season) || result.season < 0 || result.season > 999) throw new Error('Номер сезона должен быть от 0 до 999.');
    if (rawEpisodes) {
      if (!/^\d+(?:\s*,\s*\d+)*$/.test(rawEpisodes)) throw new Error('Номера серий: целые числа через запятую, например 1,2.');
      result.episodes = rawEpisodes.split(',').map(value => Number(value.trim()));
      if (result.episodes.some(value => value < 1 || value > 9999)) throw new Error('Номер серии должен быть от 1 до 9999.');
      if (result.episodes.some((value, index) => index > 0 && value !== result.episodes[index - 1] + 1)) throw new Error('Номера серий должны идти подряд по возрастанию, например 1,2,3.');
    } else if (airDate) {
      result.episodes = [];
      result.air_date = airDate;
    } else { throw new Error('Укажите сезон и номер серии либо дату эфира.'); }
  }
  return result;
}

function invalidateDraft() {
  if (!state.focusId) return;
  delete state.selections[state.focusId];
  clearPlan();
  renderFiles();
}

function renderCandidates(candidates) {
  const container = $('candidates');
  container.replaceChildren();
  if (!list(candidates).length) { container.append(node('p', 'Совпадений нет. Уточните запрос или заполните карточку вручную.', 'help')); return; }
  for (const candidate of candidates) {
    const button = node('button', undefined, 'candidate');
    button.type = 'button';
    button.append(node('strong', `${candidate.title}${candidate.year ? ` (${candidate.year})` : ''}`));
    button.append(node('small', `${kindLabel(candidate.kind)} · ${candidate.provider || 'tmdb'}:${candidate.id}`));
    if (candidate.overview) button.append(node('small', candidate.overview));
    button.addEventListener('click', action(async () => {
      const itemId = state.focusId;
      const seasons = candidate.kind === 'tv' ? matchingSeasons() : [];
      const metadata = await api('/api/metadata', { kind: candidate.kind, id: String(candidate.id), seasons });
      if (itemId !== state.focusId) return;
      // Keep user-editable episode coordinates when replacing the series identity.
      const season = $('edit-season').value;
      const episodes = $('edit-episodes').value;
      const airDate = $('edit-airdate').value;
      state.draft = copy(metadata);
      fillEditor(metadata);
      if (candidate.kind === 'tv') {
        $('edit-season').value = season;
        $('edit-episodes').value = episodes;
        $('edit-airdate').value = airDate;
      }
      if (candidate.kind !== 'tv') $('match-scope').value = 'file';
      updateScope();
      const selected = await saveMatch(readEditor());
      container.querySelectorAll('button').forEach(el => el.classList.toggle('selected', el === button));
      await createPlan();
      message(`Выбрано «${metadata.title}» для ${selected} файлов. Проверьте предпросмотр; файлы ещё не изменены.`, 'success');
    }));
    container.append(button);
  }
}

function videoEntries() { return list(state.plan && state.plan.entries).filter(entry => entry.role === 'video'); }
async function saveMatch(metadata) {
  const group = groupFor(state.focusId);
  const result = await api('/api/match', {inventoryId:state.inventoryId, itemId:state.focusId, scope:matchScope(), metadata, selections:state.selections});
  const affected = matchScope() === 'group' && group ? group.itemIds : [state.focusId];
  for (const id of affected) { delete state.selections[id]; state.automatic.delete(id); }
  Object.assign(state.selections, result.selections);
  if (group) state.matches[group.id] = result;
  state.draft = copy(metadata);
  clearPlan(); renderFiles();
  return Object.keys(result.selections).length;
}

async function createPlan() {
  clearPlan();
  if (!Object.keys(state.selections).length) return;
  if (!state.settings.libraryRoot) { $('settings-panel').open = true; throw new Error('Сначала сохраните папку медиатеки в настройках.'); }
  state.plan = await api('/api/plan', {inventoryId:state.inventoryId, library:state.settings.libraryRoot, selections:state.selections});
  videoEntries().filter(entry => !state.excluded.has(entry.mediaId)).forEach(entry => state.confirmed.add(entry.mediaId));
  renderPlan();
}

function updateConfirmation() {
  const videos = videoEntries();
  $('confirmation-count').textContent = state.confirmed.size
    ? `Выбрано видеофайлов: ${state.confirmed.size} из ${videos.length}` : 'Ни одна операция не выбрана';
  $('confirm-all').checked = videos.length > 0 && state.confirmed.size === videos.length;
  $('confirm-all').indeterminate = state.confirmed.size > 0 && state.confirmed.size < videos.length;
  syncButtons();
}

function renderPlan() {
  const entries = list(state.plan && state.plan.entries);
  $('preview-panel').hidden = false;
  $('preview-count').textContent = `${videoEntries().length} видео · ${entries.length} файлов`;
  renderIssues('plan-issues', state.plan.issues);
  const body = $('preview-list');
  body.replaceChildren();
  for (const entry of entries) {
    const tr = node('tr');
    const checkCell = node('td');
    if (entry.role === 'video') {
      const checkbox = node('input');
      checkbox.type = 'checkbox';
      checkbox.dataset.mediaId = entry.mediaId;
      checkbox.setAttribute('aria-label', `Подтвердить переименование ${filename(entry.source)}`);
      checkbox.checked = state.confirmed.has(entry.mediaId);
      checkbox.addEventListener('change', () => {
        if (checkbox.checked) { state.confirmed.add(entry.mediaId); state.excluded.delete(entry.mediaId); }
        else { state.confirmed.delete(entry.mediaId); state.excluded.add(entry.mediaId); }
        updateConfirmation();
      });
      checkCell.append(checkbox);
    } else checkCell.append(node('span', 'Вместе с видео', 'role-label'));
    tr.append(checkCell, node('td', entry.source), node('td', entry.destination));
    body.append(tr);
  }
  updateConfirmation();
  $('preview-panel').scrollIntoView({ behavior: 'smooth', block: 'start' });
}

const journalStatus = {
  complete: 'Завершено', completed: 'Завершено', applied: 'Применено', partial: 'Частично выполнено', failed: 'Ошибка',
  undone: 'Отменено', undo_partial: 'Отменено частично', applying: 'Применяется', undoing: 'Отменяется',
  pending: 'Требует проверки', planned: 'Не начато', moved: 'Переименовано', restored: 'Возвращено', undo_pending: 'Требует проверки', stopped: 'Остановлено', error: 'Ошибка', interrupted: 'Прервано', 'needs-attention': 'Требует внимания'
};

function renderProgress(progress) {
  state.operationActive = Boolean(progress && progress.active);
  $('operation-panel').hidden = !state.operationActive;
  if (state.operationActive) {
    $('operation-heading').textContent = progress.kind === 'undo' ? 'Идёт отмена' : 'Идёт переименование';
    const percent = progress.total ? ` · ${Math.min(100, Math.round(100 * progress.bytes / progress.total))}% текущей проверки` : '';
    $('operation-summary').textContent = `${progress.phase || 'Обработка'} · файл ${progress.fileIndex || 1} из ${progress.fileCount} · выполнено ${progress.completed || 0}${percent}${progress.stopRequested ? ' · Остановка запрошена' : ''}`;
    $('operation-path').textContent = progress.path || '';
    if (progress.total) $('operation-progress').value = Math.min(100, 100 * progress.bytes / progress.total);
    else $('operation-progress').removeAttribute('value');
  }
  syncButtons();
  if (progress && progress.stopRequested) $('stop-operation').disabled = true;
}

function pollJournals() {
  clearTimeout(state.pollTimer);
  state.pollTimer = setTimeout(async () => {
    if (!$('auth-view').hidden) return;
    try { await refreshJournals(); }
    catch (error) { message(`Не удалось обновить журнал: ${error.message}`, 'error'); }
    if ($('auth-view').hidden) pollJournals();
  }, state.operationActive ? 1500 : state.busy ? 500 : 5000);
}

$('stop-operation').addEventListener('click', async () => {
  if (!state.operationActive) return;
  $('stop-operation').disabled = true;
  try {
    await api('/api/stop', {}, true);
    message('Остановка запрошена. Уже выполненные переименования останутся в журнале.');
    await refreshJournals();
  } catch (error) { message(error.message, 'error'); }
});

async function refreshJournals() {
  const result = await api('/api/journals', undefined, true);
  renderProgress(result.progress);
  const journals = list(result.journals);
  const container = $('history-list');
  container.querySelectorAll('button').forEach(button => { button.disabled = state.operationActive || Boolean(state.busy); });
  const key = JSON.stringify(journals);
  if (state.journalKey === key) return;
  state.journalKey = key;
  container.replaceChildren();
  $('history-empty').hidden = Boolean(journals.length);
  for (const [index, journal] of journals.entries()) {
    const row = node('div', undefined, 'journal');
    const description = node('div');
    const date = journal.createdAt && new Date(journal.createdAt);
    const dateText = date && !Number.isNaN(date.valueOf()) ? date.toLocaleString('ru-RU') : journal.id;
    description.append(node('div', dateText, 'journal-title'));
    description.append(node('div', `${journalStatus[journal.status] || journal.status} · ${list(journal.entries).length} файлов · ${journal.id}`, 'journal-details'));
    const moved = list(journal.entries).filter(entry => entry.status === 'moved').length;
    const restored = list(journal.entries).filter(entry => entry.status === 'restored').length;
    const pending = list(journal.entries).filter(entry => entry.status === 'pending' || entry.status === 'undo_pending').length;
    description.append(node('div', `Переименовано: ${moved} · возвращено: ${restored}${pending ? ` · проверить результат: ${pending}` : ''}`, 'journal-details'));
    description.append(node('p', `Папка назначения: ${journal.library}`, 'journal-details'));
    if (journal.error) description.append(node('p', journal.error, 'journal-error'));
    const files = node('details', undefined, 'journal-files');
    files.open = index === 0;
    files.append(node('summary', 'Пути и результат операций'));
    list(journal.entries).forEach(entry => {
      files.append(node('p', `${journalStatus[entry.status] || entry.status || ''}: ${entry.source} → ${entry.destination}${entry.error ? ` · ${entry.error}` : ''}`));
    });
    description.append(files);
    row.append(description);
    if (journal.status !== 'undone' && list(journal.entries).some(entry => entry.status !== 'restored' && entry.status !== 'planned')) {
      const undo = node('button', 'Отменить');
      undo.disabled = state.operationActive || Boolean(state.busy);
      undo.type = 'button';
      undo.addEventListener('click', action(async () => {
        try {
          message('Выполняется отмена. Старый журнал требует проверки содержимого. Обычный возврат на том же томе выполняется после одного прохода; новые операции отменяются без чтения содержимого.');
          const pending = api('/api/undo', { journalId: journal.id });
          pollJournals();
          await pending;
          clearInventory();
          await refreshJournals();
          message('Операция отменена. Для нового предпросмотра выполните сканирование.', 'success');
        } catch (error) {
          if (error.result && error.result.journal) { clearInventory(); await refreshJournals(); }
          throw error;
        }
      }));
      row.append(undo);
    }
    container.append(row);
  }
}

async function browseFolder(path) {
  state.folder = null;
  $('folder-list').replaceChildren();
  $('folder-path').textContent = path || '/';
  message('Чтение папок…', '', 'folder-status');
  try {
    const result = await api(`/api/browse?path=${encodeURIComponent(path || '/')}`);
    state.folder = result;
    $('folder-path').textContent = result.path;
    $('folder-list').replaceChildren();
    for (const dir of list(result.dirs)) {
      const button = node('button', dir.name);
      button.type = 'button';
      button.addEventListener('click', action(() => browseFolder(dir.path), 'folder-status'));
      $('folder-list').append(button);
    }
    message(list(result.dirs).length ? '' : 'Вложенных доступных папок нет.', '', 'folder-status');
  } catch (error) { message(error.message, 'error', 'folder-status'); }
  syncButtons();
}

$('auth-form').addEventListener('submit', action(async () => {
  message('', '', 'auth-status');
  const password = $('auth-password').value;
  const result = await api(state.setup ? '/api/setup' : '/api/login', state.setup ? { token: $('setup-token').value.trim(), password } : { password });
  state.csrf = result.csrf || '';
  $('auth-password').value = '';
  $('setup-token').value = '';
  await loadState();
}, 'auth-status'));

$('logout').addEventListener('click', action(async () => {
  await api('/api/logout', {});
  showAuth(false);
}));

$('open-settings').addEventListener('click', () => {
  $('settings-panel').open = !$('settings-panel').open;
  if ($('settings-panel').open) $('settings-panel').scrollIntoView({ behavior: 'smooth' });
});

$('settings-form').addEventListener('submit', action(async () => {
  const settings = {
    sourceRoot: $('settings-source').value.trim(), libraryRoot: $('settings-library').value.trim(),
    language: $('settings-language').value, namingLanguage: $('settings-naming-language').value, clearTmdbToken: $('clear-token').checked
  };
  const token = $('settings-token').value.trim();
  if (token) settings.tmdbToken = token;
  if (token && settings.clearTmdbToken) throw new Error('Выберите одно действие: сохранить новый токен или удалить текущий.');
  await api('/api/settings', settings);
  clearInventory();
  await loadState();
  $('settings-panel').open = false;
  message('Настройки сохранены.', 'success');
}));

$('scan-form').addEventListener('submit', action(async () => {
  message('Сканирование файлов…');
  clearInventory();
  renderIssues('scan-issues', []);
  const result = await api('/api/scan', { source: $('scan-source').value.trim(), kind: $('scan-kind').value });
  state.inventory = result.inventory || result;
  state.inventoryId = result.inventoryId || '';
  state.groups = list(result.groups);
  renderIssues('scan-issues', state.inventory.issues);
  renderFiles();
  $('scan-summary').textContent = `${list(state.inventory.items).length} видеофайлов найдено`;
  if (!list(state.inventory.items).length) { message('В этой папке нет видеофайлов.'); return; }
  if (!state.settings.tmdbConfigured) {
    selectFile(state.inventory.items[0].id);
    message('Файлы найдены. Добавьте токен TMDB в настройках для автоматического поиска или выберите название вручную для всей группы.'); return;
  }
  state.preparing = true;
  setBusy(1);
  try {
    for (let index = 0; index < state.groups.length; index++) {
      const group = state.groups[index];
      message(`Подбираю названия: ${index + 1} из ${state.groups.length} — ${group.title} (${group.itemIds.length} файлов)…`);
      const match = await api('/api/prepare', {inventoryId:state.inventoryId, itemId:group.itemIds[0]});
      state.matches[group.id] = match;
      Object.assign(state.selections, match.selections);
      Object.keys(match.selections).forEach(id => state.automatic.add(id));
    }
  } finally { state.preparing = false; setBusy(-1); renderFiles(); }
  const unresolved = state.inventory.items.find(item => !state.selections[item.id]);
  if (unresolved) selectFile(unresolved.id);
  await createPlan();
  const ready = Object.keys(state.selections).length;
  message(`Подготовлено ${ready} из ${state.inventory.items.length} файлов. ${unresolved ? 'Проверьте оставшиеся совпадения. ' : ''}Файлы ещё не изменены.`, ready ? 'success' : '');
}));

$('scan-source').addEventListener('input', clearInventory);
$('scan-kind').addEventListener('change', clearInventory);

$('search-form').addEventListener('submit', action(async () => {
  const result = await api('/api/search', {
    query: $('search-query').value.trim(), kind: $('edit-kind').value, year: 0
  });
  renderCandidates(result.candidates);
  message(matchScope() === 'group' ? 'Выберите сериал — он применится ко всем сериям этой группы.' : 'Выберите результат — он попадёт в предпросмотр.');
}));

$('manual-form').addEventListener('submit', action(async () => {
  const item = currentItem();
  if (!item) throw new Error('Сначала выберите файл.');
  const metadata = readEditor();
  const seasons = matchingSeasons();
  if (metadata.kind === 'tv' && metadata.provider === 'tmdb' && seasons.some(season => !metadata.seasons || !metadata.seasons[String(season)])) {
    const details = await api('/api/metadata', { kind: 'tv', id: String(metadata.id), seasons });
    metadata.seasons = details.seasons;
  }
  const count = await saveMatch(metadata);
  await createPlan();
  message(`Выбрано «${metadata.title}» для ${count} файлов. Проверьте предпросмотр.`, 'success');
}));

$('match-scope').addEventListener('change', updateScope);

$('manual-form').querySelectorAll('input,select').forEach(el => {
  el.addEventListener(el.tagName === 'SELECT' ? 'change' : 'input', () => {
    updateEpisodeFields();
    invalidateDraft();
  });
});

$('create-plan').addEventListener('click', action(async () => {
  await createPlan();
  message('Предпросмотр готов. Проверьте пути и нажмите «Переименовать». Файлы ещё не изменены.');
}));

$('confirm-all').addEventListener('change', event => {
  state.confirmed.clear();
  videoEntries().forEach(entry => {
    if (event.target.checked) { state.confirmed.add(entry.mediaId); state.excluded.delete(entry.mediaId); }
    else state.excluded.add(entry.mediaId);
  });
  $('preview-list').querySelectorAll('input[type=checkbox]').forEach(el => { el.checked = state.confirmed.has(el.dataset.mediaId); });
  updateConfirmation();
});

$('apply-plan').addEventListener('click', action(async () => {
  if (!state.plan || !state.confirmed.size) return;
  message(`Переименовываю ${state.confirmed.size} видеофайлов. Результат будет сохранён в журнале.`);
  try {
    const pending = api('/api/apply', { planId: state.plan.id, confirmed: Array.from(state.confirmed) });
    pollJournals();
    await pending;
    clearInventory();
    await refreshJournals();
    message('Выбранные операции применены. Журнал сохранён. Для следующих изменений выполните новое сканирование.', 'success');
  } catch (error) {
    if (error.result && error.result.journal) { clearInventory(); await refreshJournals(); }
    throw error;
  }
  finally { await refreshJournals(); }
}));

$('refresh-history').addEventListener('click', action(async () => {
  await refreshJournals();
  message('Журнал обновлён.');
}));

document.querySelectorAll('[data-browse]').forEach(button => {
  button.addEventListener('click', action(async () => {
    state.folderTarget = button.dataset.browse;
    $('folder-dialog').showModal();
    await browseFolder($(state.folderTarget).value.trim() || '/');
  }, 'folder-status'));
});
$('folder-close').addEventListener('click', () => $('folder-dialog').close());
$('folder-parent').addEventListener('click', action(() => browseFolder(state.folder.parent), 'folder-status'));
$('folder-select').addEventListener('click', () => {
  if (state.busy || !state.folder) return;
  const input = $(state.folderTarget);
  input.value = state.folder.path;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  $('folder-dialog').close();
});

syncButtons();
loadState().catch(error => {
  showAuth(false);
  message(error.message, 'error', 'auth-status');
});
