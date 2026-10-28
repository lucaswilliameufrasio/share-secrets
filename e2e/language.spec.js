const { test, expect } = require('@playwright/test');

test('detecta português pelo navegador e permite trocar para inglês', async ({ page }) => {
  await page.goto('/');

  await expect(page.locator('html')).toHaveAttribute('lang', 'pt-BR');
  await expect(page.locator('h1')).toContainText('Um segredo.');
  await page.locator('#create-button').click();
  await expect(page.locator('#status')).toHaveText('Digite o segredo antes de criar o link.');

  await page.locator('#language-select').selectOption('en');
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page).toHaveTitle('Share a secret');
  await expect(page.locator('#create-button')).toHaveText('Create one-time link');
  await expect(page.locator('#status')).toHaveText('Enter the secret before creating a link.');

  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('#create-button')).toHaveText('Create one-time link');
});

test('permite trocar de inglês para português sem perder o conteúdo digitado', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('share-secrets-language', 'en');
  });
  await page.goto('/');
  await page.locator('#secret').fill('conteúdo de teste');

  await page.locator('#language-select').selectOption('pt-BR');

  await expect(page.locator('html')).toHaveAttribute('lang', 'pt-BR');
  await expect(page.locator('#create-button')).toHaveText('Criar link descartável');
  await expect(page.locator('#secret')).toHaveValue('conteúdo de teste');
});
