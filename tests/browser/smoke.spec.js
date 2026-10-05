const { test, expect } = require('@playwright/test');

test.describe.configure({ mode: 'serial' });

// The console is hash-routed: #/overview, #/traffic, #/conversations,
// #/recordings, #/settings. The shell owns the recording inspector and the
// history dialog, which can also be deep-linked (#/recordings?recording=ID,
// #/traffic?history=ID); the smoke test opens them that way so it exercises
// the shell without depending on how a view lays out its rows.

async function activeCollection(request) {
  const settings = await (await request.get('/api/settings')).json();
  return settings.active_collection_id;
}
async function firstRecording(request, collectionID) {
  const rows = await (await request.get(`/api/recordings?collection_id=${collectionID}`)).json();
  expect(rows.length).toBeGreaterThan(0);
  return rows[0].id;
}
const modeSwitch = page => page.getByRole('radiogroup', { name: 'Traffic mode' });
function watchErrors(page) {
  const consoleErrors = [];
  page.on('console', message => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('pageerror', error => consoleErrors.push(error.message));
  return consoleErrors;
}

test('control panel completes the record, inspect, edit, compare, restore, export and import workflow', async ({ page, request }, testInfo) => {
  const consoleErrors = watchErrors(page);

  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Replay Lab' })).toBeVisible();
  await expect(page.getByText('Proxy ready')).toBeVisible();

  // Navigation is a set of links named after the views; the view titles itself.
  await page.getByRole('link', { name: 'Settings' }).click();
  await expect(page).toHaveURL(/#\/settings$/);
  await expect(page.getByRole('heading', { name: 'Settings', level: 2 })).toBeVisible();

  await page.getByRole('button', { name: 'New collection' }).click();
  await page.getByLabel('Name').fill('Browser smoke');
  await page.getByLabel(/JSON Pointer exclusions/).fill('/metadata/request_id');
  await page.getByRole('button', { name: 'Create and activate' }).click();
  await expect(page.getByRole('status')).toContainText('Collection activated');
  await expect(page.getByLabel('Active collection')).toContainText('Browser smoke');
  await expect(page.getByText('/metadata/request_id')).toBeVisible();

  // The mode switch lives in the header and warns while a mode spends money.
  await modeSwitch(page).getByRole('radio', { name: 'Record' }).click();
  await expect(page.getByRole('status')).toContainText('Record mode');
  await expect(modeSwitch(page).getByRole('radio', { name: 'Record' })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByText('Record mode.')).toBeVisible();
  const inference = await request.post('/v1/chat/completions', {
    data: { model: 'fixture-model', messages: [{ role: 'user', content: 'hello browser' }], metadata: { request_id: 'one' } },
  });
  expect(inference.status()).toBe(200);
  expect((await inference.json()).choices[0].message.content).toBe('fixture answer');

  await page.getByRole('button', { name: 'Refresh' }).click();
  const collectionID = await activeCollection(request);
  const recordingID = await firstRecording(request, collectionID);

  await page.goto(`/#/recordings?recording=${recordingID}`);
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Response' })).toHaveAttribute('data-state', 'active');
  await expect(page.getByRole('region', { name: 'Response browser' })).toContainText('fixture answer');
  await expect(dialog).toContainText('fixture answer');

  await page.getByRole('tab', { name: 'Request' }).click();
  await expect(page.getByRole('tabpanel', { name: 'Request' })).toContainText('hello browser');

  await page.getByRole('tab', { name: 'Plain text' }).click();
  await dialog.getByRole('textbox').fill('edited browser answer');
  await page.getByRole('button', { name: 'Save new revision' }).click();
  await expect(dialog).toContainText('edited browser answer');

  await page.getByRole('tab', { name: 'Revisions' }).click();
  await expect(dialog.getByText(/Revision \d+ · edit/)).toBeVisible();
  await page.getByRole('button', { name: 'Restore' }).click();
  await expect(dialog).toContainText('fixture answer');

  await page.getByRole('tab', { name: 'Compare' }).click();
  await page.getByPlaceholder('{"model":"…","messages":[]}').fill(
    JSON.stringify({ model: 'different-model', messages: [{ role: 'user', content: 'hello browser' }] }),
  );
  await page.getByRole('button', { name: 'Compare' }).click();
  await expect(dialog.getByText('/model')).toBeVisible();

  const snapshot = await request.get(`/api/collections/${collectionID}/export`);
  expect(snapshot.status()).toBe(200);
  const bytes = await snapshot.body();
  expect(bytes.subarray(0, 15).toString()).toBe('SQLite format 3');
  const snapshotPath = testInfo.outputPath('collection.sqlite');
  require('fs').writeFileSync(snapshotPath, bytes);

  // Closing the inspector drops its parameter from the hash.
  await page.getByRole('button', { name: 'Close' }).click();
  await expect(dialog).toBeHidden();
  await expect(page).toHaveURL(/#\/recordings$/);

  await page.getByRole('link', { name: 'Settings' }).click();
  await page.locator('input[type="file"]').setInputFiles(snapshotPath);
  await expect(page.getByRole('status')).toContainText('Snapshot imported');

  await page.getByLabel('First event delay (ms)').fill('25');
  await page.getByLabel('First event delay (ms)').blur();
  await expect(page.getByRole('status')).toContainText('Settings saved');
  await page.getByRole('button', { name: 'Instant playback' }).click();
  await expect(page.getByLabel('First event delay (ms)')).toHaveValue('0');
  await expect(page.getByLabel('Delay multiplier')).toHaveValue('0');

  // A non-active collection can be deleted after an inline confirmation.
  await page.getByRole('button', { name: 'New collection' }).click();
  await page.getByLabel('Name').fill('Disposable');
  await page.getByRole('button', { name: 'Create and activate' }).click();
  await expect(page.getByLabel('Active collection')).toContainText('Disposable');
  // The imported snapshot added a second "Browser smoke"; either one will do.
  const smokeRow = page.getByRole('listitem').filter({ hasText: 'Browser smoke' }).first();
  await smokeRow.getByRole('button', { name: 'Make active' }).click();
  await expect(page.getByLabel('Active collection')).toContainText('Browser smoke');
  const disposableRow = page.getByRole('listitem').filter({ hasText: 'Disposable' });
  await disposableRow.getByRole('button', { name: 'Delete' }).click();
  await page.getByRole('group', { name: 'Delete' }).getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByRole('status')).toContainText('Collection deleted');
  await expect(page.getByText('Disposable', { exact: true })).toHaveCount(0);

  await page.screenshot({ path: testInfo.outputPath('control-panel.png'), fullPage: true });
  expect(consoleErrors).toEqual([]);
});

test('mobile, keyboard, replay-miss comparison, collection switching and edit errors remain usable', async ({ browser, request }, testInfo) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  const consoleErrors = watchErrors(page);

  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Replay Lab' })).toBeAttached();
  await expect(page.getByLabel('Active collection')).toContainText('Browser smoke');
  // No horizontal page overflow at phone width.
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);

  await modeSwitch(page).getByRole('radio', { name: 'Replay' }).click();
  await expect(page.getByRole('status')).toContainText('Replay mode');
  await expect(page.getByText('Replay mode.')).toHaveCount(0);
  const miss = await request.post('/v1/chat/completions', {
    data: { model: 'missing-model', messages: [{ role: 'user', content: 'not recorded' }] },
  });
  expect(miss.status()).toBe(404);
  expect((await miss.json()).error.code).toBe('recording_not_found');

  await page.getByRole('button', { name: 'Refresh' }).click();
  await page.getByRole('link', { name: 'Traffic' }).click();
  await expect(page).toHaveURL(/#\/traffic$/);

  const collectionID = await activeCollection(request);
  const recordingID = await firstRecording(request, collectionID);
  await page.goto(`/#/recordings?recording=${recordingID}`);
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();

  await page.getByRole('tab', { name: 'Compare' }).click();
  await page.getByLabel('Missed request').click();
  await page.getByRole('option').nth(1).click();
  await page.getByRole('button', { name: 'Compare', exact: true }).click();
  await expect(dialog.getByText('/model')).toBeVisible();

  await page.getByRole('tab', { name: 'Advanced' }).click();
  const revisionEditor = dialog.getByRole('textbox');
  await revisionEditor.fill('{ malformed');
  await page.getByRole('button', { name: 'Validate & activate' }).click();
  await expect(dialog.getByRole('alert')).toContainText(/valid JSON|Unexpected token|JSON/i);

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(page).toHaveURL(/#\/recordings$/);

  await page.getByRole('link', { name: 'Settings' }).click();
  await page.getByRole('button', { name: 'New collection' }).click();
  await expect(page.getByLabel('Name')).toBeFocused();
  await page.getByLabel('Name').fill('Mobile alternate');
  await page.getByRole('button', { name: 'Create and activate' }).click();
  await expect(page.getByLabel('Active collection')).toContainText('Mobile alternate');
  await page.getByLabel('Active collection').click();
  await page.getByRole('option', { name: 'Browser smoke', exact: true }).click();
  await expect(page.getByLabel('Active collection')).toContainText('Browser smoke');

  await page.getByLabel('First event delay (ms)').fill('17');

  // Background refresh must not overwrite a settings draft.
  await page.waitForResponse(response => response.url().endsWith('/api/settings') && response.request().method() === 'GET' && response.ok(), { timeout: 7000 });
  await expect(page.getByLabel('First event delay (ms)')).toHaveValue('17');
  await page.getByLabel('First event delay (ms)').blur();
  await page.getByLabel('Delay multiplier').fill('1.5');
  await page.getByLabel('Delay multiplier').blur();
  await expect(page.getByRole('status')).toContainText('Settings saved');
  await page.reload();
  await expect(page.getByLabel('First event delay (ms)')).toHaveValue('17');
  await expect(page.getByLabel('Delay multiplier')).toHaveValue('1.5');

  await page.screenshot({ path: testInfo.outputPath('control-panel-mobile.png'), fullPage: true });
  expect(consoleErrors).toEqual([]);
  await context.close();
});
