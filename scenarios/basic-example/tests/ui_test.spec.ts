import { test, expect } from '@playwright/test';

test.describe('Test UI', () => {
  test('page loads with heading', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1')).toHaveText('Test UI');
  });

  test('Fetch API button is visible', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('button', { name: 'Fetch API' })).toBeVisible();
  });

  test('clicking Fetch API populates output with 2 posts', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: 'Fetch API' }).click();

    const output = page.locator('pre#output');
    await expect(output).not.toBeEmpty();

    const text = await output.textContent();
    const posts = JSON.parse(text ?? '[]');

    expect(Array.isArray(posts)).toBe(true);
    expect(posts).toHaveLength(2);
  });

  test('rendered JSON contains expected post data', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: 'Fetch API' }).click();

    const output = page.locator('pre#output');
    await expect(output).not.toBeEmpty();

    const text = await output.textContent();
    const posts: Array<{ id: number; title: string; body: string }> = JSON.parse(text ?? '[]');

    // First post
    expect(posts[0]).toMatchObject({ id: 1, title: 'hello', body: 'world' });

    // Second post
    expect(posts[1]).toMatchObject({ id: 2, title: 'kube', body: 'test api' });

    // Every post has the required fields
    for (const post of posts) {
      expect(post).toHaveProperty('id');
      expect(post).toHaveProperty('title');
      expect(post).toHaveProperty('body');
    }
  });
});
