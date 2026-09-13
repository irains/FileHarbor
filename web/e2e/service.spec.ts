import { expect, test, type Locator, type Page } from '@playwright/test';

const username = process.env.FILEHARBOR_E2E_USERNAME;
const password = process.env.FILEHARBOR_E2E_PASSWORD;
const hasServiceConfiguration = Boolean(process.env.PLAYWRIGHT_BASE_URL && username && password);

// Service login submits a CI-only password. Never retain Playwright media because it
// could reproduce the interaction or serialize request data.
test.use({ trace: 'off', video: 'off', screenshot: 'off' });

// Keep diagnostics deliberately narrow: never log headers, cookies, login bodies,
// page HTML, or arbitrary browser errors from this authenticated service test.
async function expectRecycledFile(page: Page, panel: Locator, name: string, id: string) {
  let responseStatus: number | undefined;
  let entryCount: number | undefined;
  let matchingEntry = false;
  try {
    const responsePromise = page.waitForResponse((response) => response.request().method() === 'GET'
      && new URL(response.url()).pathname === '/api/trash');
    await page.getByRole('button', { name: 'Recycle bin', exact: true }).click();
    await expect(panel).toBeVisible();
    const response = await responsePromise;
    responseStatus = response.status();
    expect(responseStatus, 'GET /api/trash must succeed').toBe(200);
    const body = await response.json();
    expect(body.ok, 'GET /api/trash must return ok: true').toBe(true);
    expect(Array.isArray(body.entries), 'GET /api/trash must return an entries array').toBe(true);
    const entries = body.entries as Array<{ id: string; name: string; original_path: string }>;
    entryCount = entries.length;
    matchingEntry = entries.some((entry) => entry.id === id && entry.name === name
      && entry.original_path === `service-fixture/${name}`);
    expect(matchingEntry, 'GET /api/trash must contain the exact record returned by POST /do/rm').toBe(true);
    await expect(panel.getByText(name, { exact: true })).toBeVisible();
  } catch (error) {
    const dom = await page.evaluate((fileName) => {
      const drawers = Array.from(document.querySelectorAll('.MuiDrawer-root'));
      const fileText = Array.from(document.querySelectorAll('.MuiDrawer-root *'))
        .find((element) => element.childElementCount === 0 && element.textContent === fileName);
      return {
        drawerCount: drawers.length,
        hiddenDrawerCount: drawers.filter((element) => element.getAttribute('aria-hidden') === 'true').length,
        dialogCount: document.querySelectorAll('[role="dialog"]').length,
        fileTextPresent: Boolean(fileText),
        fileTextHiddenByAncestor: Boolean(fileText?.closest('[aria-hidden="true"]')),
        fileTextHasBounds: Boolean(fileText?.getClientRects().length)
      };
    }, name).catch(() => ({ unavailable: true }));
    const diagnostic = JSON.stringify({ responseStatus, entryCount, matchingEntry, dom });
    console.error('Recycle-bin service diagnostic:', diagnostic);
    if (process.env.GITHUB_ACTIONS === 'true') console.error(`::error title=Recycle-bin service diagnostic::${diagnostic}`);
    throw error;
  }
}

test.describe('Go service integration', () => {
  test.skip(!hasServiceConfiguration, 'requires PLAYWRIGHT_BASE_URL and ephemeral service test credentials');

  test('authenticates a protected deep link, navigates folders, and creates a folder', async ({ page }) => {
    const folderName = `playwright-created-${Date.now()}`;

    await page.goto('/d/service-fixture');
    await expect(page.getByRole('heading', { name: 'Welcome back' })).toBeVisible();
    await page.getByLabel('Username').fill(username!);
    await page.getByLabel('Password').fill(password!);
    await page.getByRole('checkbox', { name: 'Keep me signed in for 30 days' }).check();
    await page.getByRole('button', { name: 'Sign in' }).click();

    await expect(page).toHaveURL(/\/d\/service-fixture$/);
    const rememberedCookie = (await page.context().cookies()).find((cookie) => cookie.name === 'fileharbor_session');
    expect(Boolean(rememberedCookie?.httpOnly)).toBe(true);
    expect(rememberedCookie?.sameSite).toBe('Strict');
    expect((rememberedCookie?.expires ?? 0) - Date.now() / 1000).toBeGreaterThan(29 * 24 * 60 * 60);

    await expect(page.getByRole('button', { name: 'seed.txt', exact: true })).toBeVisible();

    await page.getByRole('link', { name: 'Root' }).click();
    await expect(page).toHaveURL(/\/$/);
    await page.getByRole('link', { name: 'service-fixture' }).click();
    await expect(page).toHaveURL(/\/d\/service-fixture$/);

    await page.getByRole('button', { name: 'New folder' }).click();
    const dialog = page.getByRole('dialog', { name: 'Create folder' });
    await dialog.getByLabel('Folder name').fill(folderName);
    await dialog.getByRole('button', { name: 'Confirm' }).click();
    await expect(page.getByRole('link', { name: folderName })).toBeVisible();

    await page.getByRole('button', { name: 'Settings' }).click();
    await page.getByRole('radio', { name: 'Graphite' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-fileharbor-accent', 'graphite');
    await page.getByRole('button', { name: 'Close' }).click();

    const reloadedListing = page.waitForResponse(response => new URL(response.url()).pathname === '/api/listing');
    await page.reload();
    const reloadResponse = await reloadedListing;
    expect(reloadResponse.status()).toBe(200);
    const reloadBody = await reloadResponse.json();
    expect(reloadBody.directory.path).toBe('service-fixture');
    expect(reloadBody.directory.entries.some((entry: { name: string }) => entry.name === folderName)).toBe(true);
    await expect(page.getByRole('link', { name: folderName })).toBeVisible();
    await page.getByRole('button', { name: 'Settings' }).click();
    await expect(page.getByRole('radio', { name: 'Graphite' })).toBeChecked();
    await page.getByRole('button', { name: 'Close' }).click();
    await expect(page.getByRole('link', { name: folderName })).toBeVisible();
  });

  test('moves a service file to the recycle bin, survives reload, and restores it', async ({ page }) => {
    const recycleBin = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Recycle bin' }) });
    const restoredName = `playwright-recycle-${Date.now()}.txt`;
    const recycleBinEntry = recycleBin.getByText(restoredName, { exact: true });
    await page.goto('/d/service-fixture');
    await expect(page.getByRole('heading', { name: 'Welcome back' })).toBeVisible();
    await page.getByLabel('Username').fill(username!);
    await page.getByLabel('Password').fill(password!);
    await page.getByRole('button', { name: 'Sign in' }).click();

    await page.getByRole('button', { name: 'New file' }).click();
    const newFileDialog = page.getByRole('dialog', { name: 'Create file' });
    await newFileDialog.getByLabel('File name').fill(restoredName);
    const createResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/do/newfile');
    await newFileDialog.getByRole('button', { name: 'Confirm' }).click();
    const created = await createResponse;
    expect(created.status()).toBe(200);
    expect(await created.json()).toMatchObject({ ok: true });
    await expect(page.getByRole('button', { name: restoredName, exact: true })).toBeVisible();
    await page.getByRole('button', { name: `Actions ${restoredName}` }).click();
    await page.getByRole('menuitem', { name: 'Move to recycle bin' }).click();
    const confirmation = page.getByRole('dialog', { name: `Move ${restoredName} to the recycle bin?` });
    const moveResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/do/rm');
    await confirmation.getByRole('button', { name: 'Move to recycle bin' }).click();
    const moved = await moveResponse;
    expect(moved.status()).toBe(200);
    const moveBody = await moved.json();
    expect(moveBody).toMatchObject({ ok: true, entry: { name: restoredName, original_path: `service-fixture/${restoredName}`, kind: 'file' } });
    expect(moveBody.entry.id).toMatch(/^[0-9a-f]{32}$/);
    const recycledID: string = moveBody.entry.id;
    await expect(confirmation).toHaveCount(0);
    // An exiting modal hides the workspace from role locators before deletion finishes.
    await expect(page.getByRole('button', { name: restoredName, exact: true, includeHidden: true })).toHaveCount(0);

    await test.step('list the moved record in the open recycle bin', async () => {
      await expectRecycledFile(page, recycleBin, restoredName, recycledID);
    });
    await page.reload();
    await test.step('list the same persisted record after reload', async () => {
      await expectRecycledFile(page, recycleBin, restoredName, recycledID);
    });
    const recycledRow = recycleBin.getByRole('listitem').filter({ has: page.getByText(restoredName, { exact: true }) });
    const restoreResponse = page.waitForResponse((response) => response.request().method() === 'POST' && /\/api\/trash\/[^/]+\/restore$/.test(new URL(response.url()).pathname));
    await recycledRow.getByRole('button', { name: 'Restore', exact: true }).click();
    const restored = await restoreResponse;
    expect(restored.status()).toBe(200);
    expect(await restored.json()).toMatchObject({ ok: true, path: `service-fixture/${restoredName}` });
    await expect(recycleBinEntry).toHaveCount(0);
    await recycleBin.getByRole('button', { name: 'Close', exact: true }).click();
    await expect(recycleBin).toHaveCount(0);
    await expect(page.getByRole('button', { name: restoredName, exact: true })).toBeVisible();
    await page.reload();
    await expect(page.getByRole('button', { name: restoredName, exact: true })).toBeVisible();
  });
  test('moves a service directory to the recycle bin, survives reload, and restores it', async ({ page }) => {
    const recycleBin = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Recycle bin' }) });
    const restoredName = `playwright-recycle-${Date.now()}`;
    const recycleBinEntry = recycleBin.getByText(restoredName, { exact: true });
    await page.goto('/d/service-fixture');
    await expect(page.getByRole('heading', { name: 'Welcome back' })).toBeVisible();
    await page.getByLabel('Username').fill(username!);
    await page.getByLabel('Password').fill(password!);
    await page.getByRole('button', { name: 'Sign in' }).click();

    await page.getByRole('button', { name: 'New folder' }).click();
    const newFileDialog = page.getByRole('dialog', { name: 'Create folder' });
    await newFileDialog.getByLabel('Folder name').fill(restoredName);
    const createResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/do/newdir');
    await newFileDialog.getByRole('button', { name: 'Confirm' }).click();
    const created = await createResponse;
    expect(created.status()).toBe(200);
    expect(await created.json()).toMatchObject({ ok: true });
    await expect(page.getByRole('link', { name: restoredName, exact: true })).toBeVisible();
    await page.getByRole('link', { name: restoredName, exact: true }).click();
    await page.getByRole('button', { name: 'New file' }).click();
    const childDialog = page.getByRole('dialog', { name: 'Create file' });
    await childDialog.getByLabel('File name').fill('child.txt');
    await childDialog.getByRole('button', { name: 'Confirm' }).click();
    await expect(page.getByRole('button', { name: 'child.txt', exact: true })).toBeVisible();
    await page.goto('/d/service-fixture');
    await page.getByRole('button', { name: `Actions ${restoredName}` }).click();
    await page.getByRole('menuitem', { name: 'Move to recycle bin' }).click();
    const confirmation = page.getByRole('dialog', { name: `Move ${restoredName} to the recycle bin?` });
    const moveResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/do/rm');
    await confirmation.getByRole('button', { name: 'Move to recycle bin' }).click();
    const moved = await moveResponse;
    expect(moved.status()).toBe(200);
    const moveBody = await moved.json();
    expect(moveBody).toMatchObject({ ok: true, entry: { name: restoredName, original_path: `service-fixture/${restoredName}`, kind: 'directory' } });
    expect(moveBody.entry.id).toMatch(/^[0-9a-f]{32}$/);
    const recycledID: string = moveBody.entry.id;
    await expect(confirmation).toHaveCount(0);
    // An exiting modal hides the workspace from role locators before deletion finishes.
    await expect(page.getByRole('link', { name: restoredName, exact: true, includeHidden: true })).toHaveCount(0);

    await test.step('list the moved record in the open recycle bin', async () => {
      await expectRecycledFile(page, recycleBin, restoredName, recycledID);
    });
    await page.reload();
    await test.step('list the same persisted record after reload', async () => {
      await expectRecycledFile(page, recycleBin, restoredName, recycledID);
    });
    const recycledRow = recycleBin.getByRole('listitem').filter({ has: page.getByText(restoredName, { exact: true }) });
    const restoreResponse = page.waitForResponse((response) => response.request().method() === 'POST' && /\/api\/trash\/[^/]+\/restore$/.test(new URL(response.url()).pathname));
    await recycledRow.getByRole('button', { name: 'Restore', exact: true }).click();
    const restored = await restoreResponse;
    expect(restored.status()).toBe(200);
    expect(await restored.json()).toMatchObject({ ok: true, path: `service-fixture/${restoredName}` });
    await expect(recycleBinEntry).toHaveCount(0);
    await recycleBin.getByRole('button', { name: 'Close', exact: true }).click();
    await expect(recycleBin).toHaveCount(0);
    await expect(page.getByRole('link', { name: restoredName, exact: true })).toBeVisible();
    await page.reload();
    await expect(page.getByRole('link', { name: restoredName, exact: true })).toBeVisible();
    await page.getByRole('link', { name: restoredName, exact: true }).click();
    await expect(page.getByRole('button', { name: 'child.txt', exact: true })).toBeVisible();
  });
});


test.describe('bounded read tools against Go service', () => {
  test.skip(!hasServiceConfiguration, 'requires ephemeral service credentials');
  test('searches descendants and previews a newly created archive without extraction', async ({ page }) => {
    await page.goto('/d/service-fixture');
    await page.getByLabel('Username').fill(username!);
    await page.getByLabel('Password').fill(password!);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(page.getByRole('button', { name: 'seed.txt', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Search folders', exact: true }).click();
    await page.getByLabel('Name contains').fill('seed');
    await page.getByLabel('Extension', { exact: true }).fill('txt');
    const searchResponse = page.waitForResponse(response => new URL(response.url()).pathname === '/api/search');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    expect((await searchResponse).status()).toBe(200);
    await expect(page.getByText('service-fixture/seed.txt', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Open containing folder', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Search folders' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'seed.txt', exact: true })).toBeVisible();

    const name = `preview-fixture-${Date.now()}`;
    await page.getByRole('button', { name: 'New folder', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Create folder' });
    await dialog.getByLabel('Folder name').fill(name);
    await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
    await page.getByRole('link', { name, exact: true }).click();
    await page.getByRole('button', { name: 'New file', exact: true }).click();
    const fileDialog = page.getByRole('dialog', { name: 'Create file' });
    await fileDialog.getByLabel('File name').fill('preview.txt');
    await fileDialog.getByRole('button', { name: 'Confirm', exact: true }).click();
    await expect(page.getByRole('button', { name: 'preview.txt', exact: true })).toBeVisible();
    await page.goto('/d/service-fixture');
    await page.getByRole('button', { name: `Actions ${name}`, exact: true }).click();
    const zipped = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/jobs');
    await page.getByRole('menuitem', { name: 'Archive', exact: true }).click();
    expect((await zipped).status()).toBe(202);
    await expect(page.getByText('Published: service-fixture/' + name + '.zip', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Close', exact: true }).click();
    await page.getByRole('button', { name: `Actions ${name}.zip`, exact: true }).click();
    const preview = page.waitForResponse(response => new URL(response.url()).pathname === '/api/archive/preview');
    await page.getByRole('menuitem', { name: 'Archive contents', exact: true }).click();
    const response = await preview;
    expect(response.status()).toBe(200);
    const body = await response.json();
    expect(body.preview.verification).toBe('metadata_only');
    expect(body.preview.complete).toBe(true);
    expect(body.preview.entries.some((entry: { name: string }) => entry.name.endsWith('preview.txt'))).toBe(true);
    await expect(page.getByText(/Payload integrity is not fully verified/)).toBeVisible();
    await page.getByRole('button', { name: 'Close', exact: true }).click();
    await page.getByRole('button', { name: `Actions ${name}.zip`, exact: true }).click();
    await page.getByRole('menuitem', { name: 'Extract', exact: true }).click();
    await expect(page.getByRole('radio', { name: 'New folder beside archive' })).toBeChecked();
    const output = `${name}-extracted`;
    await page.getByLabel('Folder name', { exact: true }).fill(output);
    await page.getByRole('button', { name: 'Confirm', exact: true }).click();
    await expect(page.getByText(`Published: service-fixture/${output}`, { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Close', exact: true }).click();
    await page.reload();
    await page.getByRole('button', { name: 'File tasks', exact: true }).click();
    await expect(page.getByText(`Published: service-fixture/${output}`, { exact: true })).toBeVisible();
  });
});

test.describe('shared favorites against Go service', () => {
  test.skip(!hasServiceConfiguration, 'requires ephemeral service credentials');
  test('shares favorite metadata with a second browser session', async ({ page, browser }) => {
    await page.goto('/d/service-fixture');
    await page.getByLabel('Username').fill(username!);
    await page.getByLabel('Password').fill(password!);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('button', { name: 'Favorites', exact: true }).click();
    await page.getByRole('textbox', { name: 'Display name' }).fill('Service fixture');
    const created = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/favorites');
    await page.getByRole('button', { name: 'Favorite this folder' }).click();
    expect((await created).status()).toBe(200);
    const context = await browser.newContext();
    try {
      const other = await context.newPage();
      await other.goto(new URL('/', page.url()).href);
      await other.getByLabel('Username').fill(username!);
      await other.getByLabel('Password').fill(password!);
      await other.getByRole('button', { name: 'Sign in', exact: true }).click();
      await other.getByRole('button', { name: 'Favorites', exact: true }).click();
      await expect(other.getByText('Service fixture', { exact: true })).toBeVisible();
      await other.getByRole('button', { name: 'Open', exact: true }).click();
      await expect(other).toHaveURL(/\/d\/service-fixture$/);
      await expect(other.getByRole('button', { name: 'seed.txt', exact: true })).toBeVisible();
    } finally { await context.close(); }
  });
});

test.describe('folder uploads against Go service', () => {
  test.skip(!hasServiceConfiguration, 'requires ephemeral service credentials');
  test('uploads equal filenames into separate nested folders and calculates size', async ({ page }) => {
    await page.goto('/d/service-fixture');
    await page.getByLabel('Username').fill(username!);
    await page.getByLabel('Password').fill(password!);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    const folder = `folder-upload-${Date.now()}`;
    await page.getByRole('button', { name: 'Upload', exact: true }).click();
    await page.locator('input[webkitdirectory]').evaluate((element, name) => {
      const transfer = new DataTransfer();
      for (const child of ['a', 'b']) {
        const file = new File(['data'], 'same.txt', { lastModified: 1 });
        Object.defineProperty(file, 'webkitRelativePath', { value: `${name}/${child}/same.txt` });
        transfer.items.add(file);
      }
      (element as HTMLInputElement).files = transfer.files;
      element.dispatchEvent(new Event('change', { bubbles: true }));
    }, folder);
    await expect(page.getByRole('status').filter({ hasText: 'All uploads are complete.' })).toBeVisible({ timeout: 20000 });
    await page.getByRole('button', { name: 'Close', exact: true }).click();
    await page.reload();
    await page.getByRole('button', { name: `Actions ${folder}`, exact: true }).click();
    await page.getByRole('menuitem', { name: 'Properties', exact: true }).click();
    const properties = page.locator('.MuiDrawer-paper').filter({ has: page.getByRole('heading', { name: 'Properties', exact: true }) });
    await properties.getByRole('button', { name: 'Calculate folder size' }).click();
    await expect(properties.getByText('8 B', { exact: true })).toBeVisible();
    await expect(properties.getByText('2 files, 2 folders', { exact: true })).toBeVisible();
    await properties.getByRole('button', { name: 'Close', exact: true }).click();
    await page.getByRole('link', { name: folder, exact: true }).click();
    for (const child of ['a', 'b']) {
      await page.getByRole('link', { name: child, exact: true }).click();
      await expect(page.getByRole('button', { name: 'same.txt', exact: true })).toBeVisible();
      await page.getByRole('link', { name: folder, exact: true }).click();
    }
  });
});
