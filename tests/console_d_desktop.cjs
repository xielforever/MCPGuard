'use strict';
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {chromium} = require(process.env.MCPGUARD_PLAYWRIGHT);
const root = path.resolve(__dirname, '..');
const cfg = JSON.parse(fs.readFileSync(path.join(root, '.dev/console-d-ui-2026-10-29/private-browser.json'), 'utf8'));
const origin = 'http://' + cfg.MCPGUARD_LISTEN;
const out = path.join(root, '.dev/console-d-ui-2026-10-29/desktop-runs', new Date().toISOString().replaceAll(/[:.]/g, '-'));
fs.mkdirSync(out, {recursive: true});
const checks = [], screenshots = [], errors = [];
let browser;
function check(value, label) { assert.ok(value, label); checks.push(label); }
(async () => {
  browser = await chromium.launch({headless: true, executablePath: process.env.MCPGUARD_BROWSER, args: ['--no-proxy-server']});
  const context = await browser.newContext({viewport: {width: 1600, height: 1100}, reducedMotion: 'reduce'});
  const page = await context.newPage(); page.on('pageerror', error => errors.push(error.message));
  await page.goto(origin + '/web/'); await page.locator('#login').waitFor({state: 'visible'});
  await page.locator('#username').fill('visitor'); await page.locator('#password').fill(cfg.console_secrets.visitor); await page.locator('#loginSubmit').click();
  await page.locator('#workspace').waitFor({state: 'visible'});
  const current = await (await context.request.get(origin + '/console/api/state')).json();
  const actionID = current.actions.find(action => action.toolName === 'pg.update_demo_note')?.actionId;
  assert.ok(actionID, 'real action history available for desktop review');
  const names = ['操作与审批', '资源', '规则', '用户与权限', '本人审计', '通知与设置'];
  for (const width of [1920, 1600, 1440, 1280]) {
    await page.setViewportSize({width, height: 1100});
    for (const expanded of [false, true]) {
      if (await page.locator('#navToggle').getAttribute('aria-expanded') !== String(expanded)) await page.locator('#navToggle').click();
      for (const [index, name] of names.entries()) {
        await page.locator('#nav').getByRole('button', {name, exact: true}).click();
        if (index === 0) { await page.locator('.queue-item[data-id="' + actionID + '"]').click(); await page.locator('#requestTitle').waitFor(); }
        check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `desktop layout fits ${width}px ${expanded ? 'expanded' : 'compact'}: ${name}`);
        if (index === 0) check(await page.locator('.request-plan').evaluate(element => element.getBoundingClientRect().width >= 560), `review area preserves readable width ${width}px ${expanded ? 'expanded' : 'compact'}`);
        if (width === 1600 && !expanded) {
          const filename = 'desktop-' + index + '.png'; await page.screenshot({path: path.join(out, filename), fullPage: true}); screenshots.push(filename);
        }
        if (index === 0 && width === 1280) { const filename = 'review-1280-' + (expanded ? 'expanded' : 'compact') + '.png'; await page.screenshot({path: path.join(out, filename), fullPage: true}); screenshots.push(filename); }
      }
    }
  }
  await page.locator('#nav').getByRole('button', {name: '资源', exact: true}).click();
  await page.locator('#refresh').focus(); await page.keyboard.press('Tab'); await page.keyboard.press('Shift+Tab');
  const focus = await page.locator('#refresh').evaluate(element => ({outline: getComputedStyle(element).outlineWidth, offset: getComputedStyle(element).outlineOffset}));
  check(focus.outline === '3px' && focus.offset === '4px', 'desktop keyboard focus uses the canonical visible ring');
  check(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--mg-color-primary').trim()) === '#83f2ed', 'runtime desktop uses the approved D primary token');
  check(errors.length === 0, 'desktop viewport and navigation changes produce no JavaScript exceptions');
  await page.locator('#logout').click(); await page.locator('#login').waitFor({state: 'visible'});
  check(await page.locator('#content').textContent() === '', 'desktop review session exits with private content cleared');
  await context.close();
  const report = {kind: 'CONSOLE_D_DESKTOP_FINAL', userScope: 'DESKTOP_ONLY', passed: checks.length, checks, screenshots, browserErrors: errors, readOnlyBusinessValidation: true};
  fs.writeFileSync(path.join(out, 'report.json'), JSON.stringify(report, null, 2) + '\n');
  process.stdout.write(JSON.stringify({status: 'PASS', checks: checks.length, screenshots: screenshots.length, evidence: out}) + '\n');
})().catch(error => {
  fs.writeFileSync(path.join(out, 'failure.json'), JSON.stringify({status: 'FAIL', checks, screenshots, message: error.message}, null, 2) + '\n');
  process.stderr.write(error.stack + '\n'); process.exitCode = 1;
}).finally(async () => { if (browser) await browser.close(); });
