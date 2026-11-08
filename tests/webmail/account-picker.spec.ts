import { randomUUID } from 'node:crypto';
import { test as base, expect, type Page } from '@playwright/test';

type Account = {
  id: string;
  name: string;
  username: string;
  password: string;
  api_base: string;
  created: string;
};

type Mailboxes = {
  create: (label: string) => Promise<Account>;
  remove: (account: Account) => Promise<void>;
  seed: (account: Account, subject: string) => Promise<void>;
};

// Each test owns its accounts. Playwright owns and closes the page/context;
// this fixture also ends the server-side Alps session and removes test data.
const test = base.extend<{ mailboxes: Mailboxes }>({
  mailboxes: async ({ request, context, baseURL }, use) => {
    const api = (process.env.MAILARKY_HTTP_URL || 'http://localhost:8026').replace(/\/$/, '');
    const owned = new Map<string, Account>();
    const remove = async (account: Account) => {
      const response = await request.delete(api + account.api_base);
      expect([204, 404]).toContain(response.status());
      owned.delete(account.id);
    };
    try {
      await use({
        create: async label => {
          const suffix = randomUUID();
          const response = await request.post(api + '/api/v1/accounts', {
            data: { name: `${label} ${suffix}`, username: `picker-${suffix}@example.test` },
          });
          expect(response.status()).toBe(201);
          const account: Account = await response.json();
          owned.set(account.id, account);
          return account;
        },
        remove,
        seed: async (account, subject) => {
          const response = await request.post(api + account.api_base + '/messages', {
            data: { folder: 'INBOX', from: 'sender@example.test', to: [account.username], subject, body: 'Browser test fixture.' },
          });
          expect(response.status()).toBe(201);
        },
      });
    } finally {
      try {
        const response = await context.request.delete(new URL('/session', baseURL).href);
        expect(response.ok()).toBeTruthy();
      } finally {
        const results = await Promise.allSettled([...owned.values()].map(remove));
        for (const result of results) {
          if (result.status === 'rejected') throw result.reason;
        }
      }
    }
  },
});

const accountButton = (page: Page, account: Account) =>
  page.getByRole('button', { name: `Open ${account.name} in Alps`, exact: true });

test('lists accounts, searches names and usernames, and fits the viewport', async ({ page, mailboxes }) => {
  const one = await mailboxes.create('Checkout');
  const literal = await mailboxes.create('<img src=x onerror=alert(1)>');
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Open an account' })).toBeVisible();
  await expect(page.getByText('Default', { exact: true })).toBeVisible();
  await expect(accountButton(page, one)).toBeVisible();
  await expect(accountButton(page, literal)).toBeVisible();
  await expect(page.getByRole('list', { name: 'Test accounts' }).locator('img')).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();

  const search = page.getByRole('searchbox', { name: 'Search accounts' });
  await search.fill(one.name.toUpperCase());
  await expect(accountButton(page, one)).toBeVisible();
  await expect(accountButton(page, literal)).toHaveCount(0);
  await search.fill(literal.username);
  await expect(accountButton(page, literal)).toBeVisible();
  await expect(accountButton(page, one)).toHaveCount(0);
  await search.fill(`absent-${randomUUID()}`);
  await expect(page.getByText('No matching accounts')).toBeVisible();
  await expect(page.getByRole('list', { name: 'Test accounts' }).getByRole('button')).toHaveCount(0);
});

test('automatically picks up created and deleted accounts', async ({ page, mailboxes }) => {
  await page.goto('/');
  await expect(page.getByText('Default', { exact: true })).toBeVisible();
  const live = await mailboxes.create('Live account');
  await expect(accountButton(page, live)).toBeVisible();
  await mailboxes.remove(live);
  await expect(accountButton(page, live)).toHaveCount(0);
});

test('signs in with the keyboard and switches to an isolated mailbox', async ({ page, mailboxes }) => {
  const one = await mailboxes.create('First inbox');
  const two = await mailboxes.create('Second inbox');
  const firstSubject = `First message ${one.id}`;
  const secondSubject = `Second message ${two.id}`;
  await mailboxes.seed(one, firstSubject);
  await mailboxes.seed(two, secondSubject);
  await page.goto('/');
  await accountButton(page, one).focus();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/alps#\/mailbox\/INBOX$/);
  await expect(page.getByText(firstSubject, { exact: true })).toBeVisible();
  await expect(page.getByText(secondSubject, { exact: true })).toHaveCount(0);

  await page.goto('/');
  await accountButton(page, two).click();
  await expect(page).toHaveURL(/\/alps#\/mailbox\/INBOX$/);
  await expect(page.getByText(secondSubject, { exact: true })).toBeVisible();
  await expect(page.getByText(firstSubject, { exact: true })).toHaveCount(0);
});

test('returns to account selection after Alps sign-out', async ({ page, context, mailboxes }) => {
  const account = await mailboxes.create('Logout');
  await page.goto('/');
  await accountButton(page, account).click();
  await page.getByRole('button', { name: 'Profile options' }).click();
  await page.getByRole('button', { name: 'Sign Out', exact: true }).click();
  await expect(page).toHaveURL(new URL('/', page.url()).href);
  await expect(page.getByRole('heading', { name: 'Open an account' })).toBeVisible();
  await expect(accountButton(page, account)).toBeEnabled();
  expect((await context.cookies()).find(cookie => cookie.name === 'alps_logged_in')?.value).not.toBe('1');
  // Returning to the picker must not leave the browser unable to sign in again.
  await accountButton(page, account).click();
  await expect(page.getByRole('button', { name: 'Profile options' })).toBeVisible();
});

test('handles an account deleted between listing and selection', async ({ page, mailboxes }) => {
  const stale = await mailboxes.create('Deleted account');
  const other = await mailboxes.create('Still available');
  // Hold the displayed list steady while the real account is deleted, as can
  // happen between polls when a test harness tears down its fixture.
  await page.route('**/mailarky/accounts', route => route.fulfill({ json: [stale, other] }));
  await page.goto('/');
  await expect(accountButton(page, stale)).toBeVisible();
  await mailboxes.remove(stale);
  await accountButton(page, stale).click();
  await expect(page.getByRole('alert')).toContainText('This account no longer exists');
  await expect(accountButton(page, stale)).toHaveCount(0);
  await expect(accountButton(page, other)).toBeEnabled();
  await page.unroute('**/mailarky/accounts');
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(accountButton(page, stale)).toHaveCount(0);
  await accountButton(page, other).click();
  await expect(page.getByRole('button', { name: 'Profile options' })).toBeVisible();
});

test('recovers from a failed account list request', async ({ page, mailboxes }) => {
  const account = await mailboxes.create('Recovery');
  await page.route('**/mailarky/accounts', route => route.fulfill({ status: 502, json: { error: 'Mailarky is unavailable.' } }));
  await page.goto('/');
  await expect(page.getByRole('alert')).toContainText('Mailarky is unavailable.');
  await expect(page.getByText('Could not load accounts', { exact: true })).toBeVisible();
  await page.unroute('**/mailarky/accounts');
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(accountButton(page, account)).toBeVisible();
  await expect(page.getByRole('alert')).toBeHidden();
});

test('keeps password login available and returns to the picker on sign-out', async ({ page, mailboxes }) => {
  const account = await mailboxes.create('Password login');
  await page.goto('/');
  await page.getByRole('link', { name: 'Sign in with a password' }).click();
  await expect(page).toHaveURL(/\/alps#\/login$/);
  await page.locator('input[name="username"]').fill(account.username);
  await page.locator('input[name="password"]').fill(account.password);
  await page.locator('button[type="submit"]').click();
  await page.getByRole('button', { name: 'Profile options' }).click();
  await page.getByRole('button', { name: 'Sign Out', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Open an account' })).toBeVisible();
});
