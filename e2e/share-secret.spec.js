const { test, expect } = require('@playwright/test');

const port = process.env.PORT || '4173';

test('cria um link e consome o segredo uma única vez', async ({ page, context }) => {
  const secret = 'credencial de teste — não usar em produção';
  await page.clock.install();
  await page.goto('/');
  await page.locator('#secret').fill(secret);
  await page.locator('#ttl-minutes').fill('5');
  await page.locator('#create-button').click();

  const link = page.locator('#share-link');
  await expect(link).toHaveValue(new RegExp(`^http://127\\.0\\.0\\.1:${port}/#.+\\..+$`));
  await expect(page.locator('#secret')).toHaveValue('');
  await expect(page.locator('#tracked-links .tracked-link-title')).toContainText('Disponível');

  const secretURL = await link.inputValue();
  const persistedTracking = await page.evaluate(() => JSON.parse(localStorage.getItem('share-secrets-tracked-links')));
  const decryptionKey = new URL(secretURL).hash.slice(1).split('.')[1];
  expect(JSON.stringify(persistedTracking)).not.toContain(secretURL);
  expect(JSON.stringify(persistedTracking)).not.toContain(secret);
  expect(JSON.stringify(persistedTracking)).not.toContain(decryptionKey);
  const corruptedURL = new URL(secretURL);
  const [id, key] = corruptedURL.hash.slice(1).split('.');
  const replacement = key[0] === 'A' ? 'B' : 'A';
  corruptedURL.hash = `${id}.${replacement}${key.slice(1)}`;

  const badLinkPage = await context.newPage();
  await badLinkPage.goto(corruptedURL.toString());
  await expect(badLinkPage.locator('#status')).toContainText('continua disponível para tentar novamente');
  await expect(badLinkPage.locator('#opened-secret')).toBeHidden();

  const recipient = await context.newPage();
  await recipient.goto(secretURL);
  await expect(recipient.locator('#opened-secret')).toHaveText(secret);
  await expect(recipient.locator('#opened-secret')).toBeVisible();
  await expect(recipient).toHaveURL(`http://127.0.0.1:${port}/`);
  await expect(page.locator('#refresh-links')).toBeVisible();
  await page.clock.fastForward(30_000);
  await expect(page.locator('#tracked-links .tracked-link-title')).toContainText('Usado');
  await page.locator('#refresh-links').click();
  await expect(page.locator('#tracked-links .tracked-link-title')).toContainText('Usado');

  const secondRecipient = await context.newPage();
  await secondRecipient.goto(secretURL);
  await expect(secondRecipient.locator('#status')).toContainText('já foi usado');
  await expect(secondRecipient.locator('#opened-secret')).toBeHidden();
});
