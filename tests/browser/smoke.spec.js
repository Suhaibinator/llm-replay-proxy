const { test, expect } = require('@playwright/test');

test.describe.configure({ mode: 'serial' });

test('control panel completes the record, inspect, edit, compare, restore, export and import workflow', async ({ page, request }, testInfo) => {
  const consoleErrors = [];
  page.on('console', message => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('pageerror', error => consoleErrors.push(error.message));

  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Replay Lab' })).toBeVisible();
  await expect(page.getByText('Proxy ready')).toBeVisible();

  await page.getByRole('button', { name: 'New collection' }).click();
  await page.getByLabel('Name').fill('Browser smoke');
  await page.getByLabel(/JSON Pointer exclusions/).fill('/metadata/request_id');
  await page.getByRole('button', { name: 'Create and activate' }).click();
  await expect(page.getByLabel('Active collection')).toHaveValue(/\d+/);
  await expect(page.getByText('/metadata/request_id')).toBeVisible();

  await page.getByRole('button', { name: /Record Always call upstream/ }).click();
  await expect(page.getByRole('status')).toContainText('Settings saved');
  const inference = await request.post('/v1/chat/completions', {
    data: { model: 'fixture-model', messages: [{ role: 'user', content: 'hello browser' }], metadata: { request_id: 'one' } },
  });
  expect(inference.status()).toBe(200);
  expect((await inference.json()).choices[0].message.content).toBe('fixture answer');

  await page.getByRole('button', { name: 'Refresh' }).click();
  await expect(page.getByRole('heading', { name: 'Traffic analytics' })).toBeVisible();
  await expect(page.getByText('Cache hit rate')).toBeVisible();
  await expect(page.getByRole('img', { name: 'Request volume over time' })).toBeVisible();
  await expect(page.getByText('chat/completions')).toBeVisible();
  await page.getByRole('tab', { name: 'History' }).click();
  await expect(page.getByText('recorded', { exact: true })).toBeVisible();
  await page.getByLabel('Search history').fill('does-not-exist');
  await expect(page.getByText('No matching requests')).toBeVisible();
  await page.getByLabel('Search history').fill('fixture-model');
  await expect(page.getByText('recorded', { exact: true })).toBeVisible();
  await page.getByRole('tab', { name: 'Recordings' }).click();
  await page.getByLabel('Search recordings').fill('does-not-exist');
  await expect(page.getByText('No matching recordings')).toBeVisible();
  await page.getByLabel('Search recordings').fill('');
  await page.getByRole('button', { name: 'Inspect' }).click();
  await expect(page.getByRole('tab', { name: 'Response' })).toHaveAttribute('data-state', 'active');
  await expect(page.getByRole('region', { name: 'Response browser' })).toContainText('fixture answer');
  await expect(page.getByRole('dialog')).toContainText('fixture answer');

  await page.getByRole('tab', { name: 'Request' }).click();
  await expect(page.getByRole('tabpanel', { name: 'Request' })).toContainText('hello browser');

  await page.getByRole('tab', { name: 'Plain text' }).click();
  await page.getByRole('dialog').getByRole('textbox').fill('edited browser answer');
  await page.getByRole('button', { name: 'Save new revision' }).click();
  await expect(page.getByRole('dialog')).toContainText('edited browser answer');

  await page.getByRole('tab', { name: 'Revisions' }).click();
  await expect(page.getByRole('dialog').getByText(/Revision \d+ · edit/)).toBeVisible();
  await page.getByRole('button', { name: 'Restore' }).click();
  await expect(page.getByRole('dialog')).toContainText('fixture answer');

  await page.getByRole('tab', { name: 'Compare' }).click();
  await page.getByPlaceholder('{"model":"…","messages":[]}').fill(
    JSON.stringify({ model: 'different-model', messages: [{ role: 'user', content: 'hello browser' }] }),
  );
  await page.getByRole('button', { name: 'Compare' }).click();
  await expect(page.getByRole('dialog').getByText('/model')).toBeVisible();

  const collectionID = await page.getByLabel('Active collection').inputValue();
  const snapshot = await request.get(`/api/collections/${collectionID}/export`);
  expect(snapshot.status()).toBe(200);
  const bytes = await snapshot.body();
  expect(bytes.subarray(0, 15).toString()).toBe('SQLite format 3');
  const snapshotPath = testInfo.outputPath('collection.sqlite');
  require('fs').writeFileSync(snapshotPath, bytes);

  await page.getByRole('button', { name: 'Close' }).click();
  await page.locator('input[type="file"]').setInputFiles(snapshotPath);
  await expect(page.getByRole('status')).toContainText('Snapshot imported');

  await page.getByLabel('First event (ms)').fill('25');
  await page.getByLabel('First event (ms)').blur();
  await page.getByRole('button', { name: 'Instant playback' }).click();
  await expect(page.getByLabel('First event (ms)')).toHaveValue('0');
  await expect(page.getByLabel('Delay multiplier')).toHaveValue('0');

  await page.screenshot({ path: testInfo.outputPath('control-panel.png'), fullPage: true });
  expect(consoleErrors).toEqual([]);
});

test('mobile, keyboard, replay-miss comparison, collection switching and edit errors remain usable', async ({ browser, request }, testInfo) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  const consoleErrors = [];
  page.on('console', message => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('pageerror', error => consoleErrors.push(error.message));

  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Replay Lab' })).toBeVisible();
  await expect(page.getByLabel('Active collection')).toHaveValue(/\d+/);

  await page.getByRole('button', { name: /Replay Recordings only/ }).click();
  await expect(page.getByRole('status')).toContainText('Settings saved');
  const miss = await request.post('/v1/chat/completions', {
    data: { model: 'missing-model', messages: [{ role: 'user', content: 'not recorded' }] },
  });
  expect(miss.status()).toBe(404);
  expect((await miss.json()).error.code).toBe('recording_not_found');

  await page.getByRole('button', { name: 'Refresh' }).click();
  await page.getByRole('tab', { name: 'History' }).click();
  await expect(page.getByText('miss', { exact: true })).toBeVisible();
  await page.getByRole('tab', { name: 'Recordings' }).click();
  await page.getByRole('button', { name: 'Inspect' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();

  await page.getByRole('tab', { name: 'Compare' }).click();
  await page.getByLabel('Missed request').selectOption({ index: 1 });
  await page.getByRole('button', { name: 'Compare', exact: true }).click();
  await expect(dialog.getByText('/model')).toBeVisible();

  await page.getByRole('tab', { name: 'Advanced' }).click();
  const revisionEditor = dialog.getByRole('textbox');
  await revisionEditor.fill('{ malformed');
  await page.getByRole('button', { name: 'Validate & activate' }).click();
  await expect(dialog.getByRole('alert')).toContainText(/valid JSON|Unexpected token|JSON/i);

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(page.getByRole('button', { name: 'Inspect' })).toBeFocused();

  await page.getByRole('button', { name: 'New collection' }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByLabel('Name')).toBeFocused();
  await page.getByLabel('Name').fill('Mobile alternate');
  await page.getByRole('button', { name: 'Create and activate' }).click();
  await expect(page.getByLabel('Active collection')).toHaveValue(/\d+/);
  await expect(page.getByText('No recordings yet')).toBeVisible();
  await page.getByLabel('Active collection').selectOption({ label: 'Browser smoke' });
  await expect(page.getByText('chat/completions')).toBeVisible();

  await page.getByLabel('First event (ms)').fill('17');

  // Background traffic refresh must not overwrite a settings draft.
  await page.waitForResponse(response => response.url().includes('/api/history?') && response.ok(), { timeout: 7000 });
  await expect(page.getByLabel('First event (ms)')).toHaveValue('17');
  await page.getByLabel('First event (ms)').blur();
  await page.getByLabel('Delay multiplier').fill('1.5');
  await page.getByLabel('Delay multiplier').blur();
  await page.reload();
  await expect(page.getByLabel('First event (ms)')).toHaveValue('17');
  await expect(page.getByLabel('Delay multiplier')).toHaveValue('1.5');

  await page.screenshot({ path: testInfo.outputPath('control-panel-mobile.png'), fullPage: true });
  expect(consoleErrors).toEqual([]);
  await context.close();
});
