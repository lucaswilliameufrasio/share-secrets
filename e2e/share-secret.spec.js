const { test, expect } = require('@playwright/test');

test('cria um link e consome o segredo uma única vez', async ({ page, context }) => {
  const secret = 'credencial de teste — não usar em produção';
  await page.goto('/');
  await page.locator('#secret').fill(secret);
  await page.locator('#create-button').click();

  const link = page.locator('#share-link');
  await expect(link).toHaveValue(/^http:\/\/127\.0\.0\.1:4173\/#.+\..+$/);
  await expect(page.locator('#secret')).toHaveValue('');

  const secretURL = await link.inputValue();
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
  await expect(recipient).toHaveURL('http://127.0.0.1:4173/');

  const secondRecipient = await context.newPage();
  await secondRecipient.goto(secretURL);
  await expect(secondRecipient.locator('#status')).toContainText('já foi usado');
  await expect(secondRecipient.locator('#opened-secret')).toBeHidden();
});
