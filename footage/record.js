// Records footage of a real heedle up: drawdown with real runs, and the
// same runs in Temporal's own Web UI. Run by .github/workflows/footage.yml.
const { chromium } = require('playwright');
const fs = require('fs');
const OUT = process.env.OUT || 'out';
(async () => {
  const b = await chromium.launch();
  const shoot = async (name, fn) => {
    const dir = `${OUT}/${name}`;
    const ctx = await b.newContext({ viewport: { width: 1600, height: 900 }, recordVideo: { dir, size: { width: 1600, height: 900 } } });
    const p = await ctx.newPage();
    const t0 = Date.now(); const marks = {};
    const mark = (k) => { marks[k] = (Date.now() - t0) / 1000; };
    try { await fn(p, mark); } catch (e) { console.log(name, 'failed:', e.message); await p.screenshot({ path: `${dir}/failed.png` }); }
    await p.screenshot({ path: `${dir}/last.png` });
    await ctx.close();
    fs.writeFileSync(`${dir}/marks.json`, JSON.stringify(marks));
  };
  const W = (p, ms) => p.waitForTimeout(ms);

  // drawdown: the woven grid of real runs, a run, its provenance, then
  // straight into Temporal's history of that same run.
  await shoot('drawdown', async (p, mark) => {
    await p.goto('http://127.0.0.1:8090/'); await W(p, 3000); mark('home');
    const cell = p.locator('.weave .cell').nth(2); await cell.hover(); await W(p, 2500);
    await p.locator('.weave .selvedge').last().hover(); await W(p, 2500);
    await p.mouse.move(10, 10); await W(p, 800);
    mark('run');
    await p.locator('tr.click', { hasText: 'Completed' }).first().click(); await W(p, 3000);
    await p.evaluate(() => window.scrollTo({ top: 300, behavior: 'smooth' })); await W(p, 3000);
    await p.evaluate(() => window.scrollTo({ top: 0, behavior: 'smooth' })); await W(p, 1500);
    mark('temporal');
    const href = await p.locator('a', { hasText: 'Open in Temporal Web UI' }).getAttribute('href');
    await p.goto(href); await W(p, 6000);
    await p.mouse.wheel(0, 500); await W(p, 3000);
    await p.mouse.wheel(0, 700); await W(p, 3000);
    mark('end');
  });

  // Temporal's own list of every heedle run, then one run's history.
  await shoot('temporal', async (p, mark) => {
    await p.goto('http://127.0.0.1:8233/namespaces/default/workflows'); await W(p, 5000); mark('list');
    await p.locator('a[href*="/workflows/loom-"]').first().click(); await W(p, 6000); mark('history');
    await p.mouse.wheel(0, 600); await W(p, 3500);
    mark('end');
  });
  await b.close();
})();
