import { expect, test } from '@playwright/test';

const session = {
  ok: true,
  session: { username: 'admin', csrf_token: 'csrf', expires_at: '2030-01-01T00:00:00Z' },
  base_path: '',
  locale: 'en',
  capabilities: { browse: true, upload: true, mutate: true, editor_save: true }
};
const files = ['sample.txt', 'second.txt'].map((name, index) => ({
  name, path: name, kind: 'file', size_bytes: 12,
  modified_at: '2026-09-05T10:54:28Z', mode: '-rw-r--r--',
  is_archive: false, previewable: true, editable: true, version: `v${index + 1}`
}));
const records = files.map((file, index) => ({
  id: String(index + 1).padStart(32, '0'),
  name: file.name, original_path: file.path, kind: file.kind, size_bytes: file.size_bytes,
  deleted_at: '2026-09-11T10:54:28Z'
}));

for (const width of [390, 1280]) {
  for (const mode of ['single', 'batch'] as const) {
    for (const outcome of ['success', 'partial failure'] as const) {
      test(`open recycle bin refreshes after delayed ${mode} ${outcome} at ${width}px`, async ({ page }) => {
        let releaseMove!: () => void;
        const moveGate = new Promise<void>((resolve) => { releaseMove = resolve; });
        let recycled: typeof records = [];
        let trashReads = 0;
        const endpoint = mode === 'single' ? '/do/rm' : '/do/batch/delete';
        const selectedFiles = mode === 'single' ? files.slice(0, 1) : files;
        await page.route('**/api/session', (route) => route.fulfill({ json: session }));
        await page.route('**/api/jobs', (route) => route.fulfill({ json: { ok: true, jobs: [] } }));
        await page.route('**/api/favorites', (route) => route.fulfill({ json: { ok: true, entries: [] } }));
        await page.route(/\/api\/listing/, (route) => route.fulfill({
          json: {
            ok: true,
            directory: {
              path: '', parent_path: null, listing_token: 'listing-token', truncated: false,
              entries: files.filter((file) => !recycled.some((entry) => entry.name === file.name))
            }
          }
        }));
        await page.route(/\/api\/trash(?:\?.*)?$/, (route) => {
          trashReads++;
          return route.fulfill({ json: { ok: true, entries: recycled } });
        });
        await page.route(`**${endpoint}`, async (route) => {
          expect(route.request().method()).toBe('POST');
          if (mode === 'single') {
            expect(new URLSearchParams(route.request().postData() ?? '').get('path')).toBe(files[0].path);
          } else {
            expect(route.request().postDataJSON()).toEqual({
              listing_token: 'listing-token',
              entries: selectedFiles.map(({ name, version }) => ({ name, version }))
            });
          }
          await moveGate;
          recycled = records.slice(0, outcome === 'partial failure' ? 1 : selectedFiles.length);
          await route.fulfill(outcome === 'success'
            ? { json: { ok: true } }
            : { status: 500, json: { ok: false, code: 'execution_partial' } });
        });

        try {
          await page.setViewportSize({ width, height: 844 });
          await page.goto('/');
          await expect(page.getByRole('button', { name: 'sample.txt', exact: true })).toBeVisible();
          if (mode === 'single') {
            await page.getByRole('button', { name: 'Actions sample.txt', exact: true }).click();
            await page.getByRole('menuitem', { name: 'Move to recycle bin', exact: true }).click();
          } else {
            await page.locator('[aria-label="Select all"]').getByRole('checkbox').check();
            await page.getByRole('button', { name: 'Move to recycle bin', exact: true }).click();
          }
          const confirmation = page.getByRole('dialog', {
            name: mode === 'single' ? 'Move sample.txt to the recycle bin?' : 'Move 2 items to the recycle bin?'
          });
          const moveStarted = page.waitForRequest((request) => new URL(request.url()).pathname === endpoint);
          const moveFinished = page.waitForResponse((response) => new URL(response.url()).pathname === endpoint);
          await confirmation.getByRole('button', { name: 'Move to recycle bin', exact: true }).click();
          await moveStarted;
          await expect(confirmation).toHaveCount(0);

          const recycleBin = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Recycle bin', exact: true }) });
          const emptyTrash = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/trash');
          await page.getByRole('button', { name: 'Recycle bin', exact: true }).click();
          expect(await (await emptyTrash).json()).toEqual({ ok: true, entries: [] });
          await expect(recycleBin.getByText('Recycle bin is empty', { exact: true })).toBeVisible();
          expect(recycled).toHaveLength(0);
          // The workspace is aria-hidden behind the drawer, but the file has not moved.
          await expect(page.getByRole('button', { name: 'sample.txt', exact: true, includeHidden: true })).toHaveCount(1);

          // Release only after the stale empty result is rendered. No sleep, reopen or manual refresh.
          releaseMove();
          expect((await moveFinished).status()).toBe(outcome === 'success' ? 200 : 500);
          await expect(recycleBin.getByText('sample.txt', { exact: true })).toBeVisible();
          if (mode === 'batch' && outcome === 'success') {
            await expect(recycleBin.getByText('second.txt', { exact: true })).toBeVisible();
          }
          await expect(recycleBin.getByText('Recycle bin is empty', { exact: true })).toHaveCount(0);
          expect(trashReads).toBeGreaterThanOrEqual(2);
          if (outcome === 'partial failure') {
            await expect(page.getByRole('alert', { includeHidden: true })).toContainText('The operation did not finish cleanly.');
          }
        } finally {
          releaseMove();
        }
      });
    }
  }
}
