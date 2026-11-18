// 在 Cursor 主进程里执行。选择器依赖 Cursor 的界面结构（3.23.23 上验证），升级后可能要改
(async (a) => {
  const { webContents } = require('electron');
  const w = webContents.getAllWebContents().find((c) => /workbench\.html/.test(c.getURL()));
  if (!w) throw new Error('no Cursor window');
  const page = (s) => w.executeJavaScript(s);
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const rows = `[...document.querySelectorAll('.agent-sidebar-cell[draggable=true]')].filter(e=>e.offsetParent)`;
  const titleOf = `(e=>e.innerText.split('\\n').map(s=>s.trim()).filter(Boolean)[0]||'')`;
  const input = `[...document.querySelectorAll('.aislash-editor-input[contenteditable=true]')].find(e=>e.offsetParent)`;

  const list = async () => JSON.parse(await page(`JSON.stringify(${rows}.map((e,i)=>{const l=e.innerText.split('\\n').map(s=>s.trim()).filter(Boolean);return {i,selected:e.dataset.selected==='true',busy:!!e.querySelector('.spinning-loader'),title:l[0]||'',sub:l.slice(1).join(' ').slice(0,80)}}))`));

  // 序号或关键字，关键字必须唯一，避免发错 Agent
  const resolve = async () => {
    const all = await list();
    const key = String(a.agent || '').trim();
    if (!key) throw new Error('agent is required: an index or a keyword from cursor_agents');
    if (/^\d+$/.test(key)) {
      if (!all[+key]) throw new Error(`no agent at index ${key}`);
      return all[+key];
    }
    const hit = all.filter((r) => (r.title + ' ' + r.sub).includes(key));
    if (hit.length !== 1) throw new Error(`keyword "${key}" matches ${hit.length} agents, need exactly 1`);
    return hit[0];
  };

  const remember = () => page(`(()=>{const r=${rows};const i=r.findIndex(e=>e.dataset.selected==='true');window.__ccPrev=i<0?null:{el:r[i],idx:i,title:${titleOf}(r[i])};return 0})()`);
  // 列表重绘后元素会换，退回按标题找；标题重复时只看原位置，新建 Agent 会把它往下挤一行
  // 新对话创建中会把选中抢回去，要连续两次确认选中才算切回
  const restore = async () => {
    const once = `(()=>{const p=window.__ccPrev;if(!p)return true;const r=${rows};let t=p.el.isConnected?p.el:null;if(!t){const s=r.filter(e=>${titleOf}(e)===p.title);t=s.length===1?s[0]:[r[p.idx],r[p.idx+1]].find(e=>e&&${titleOf}(e)===p.title)||null}if(!t||t.dataset.selected==='true')return true;t.click();return false})()`;
    let ok = 0;
    for (let t = 0; t < 15 && ok < 2; t++) {
      ok = (await page(once)) ? ok + 1 : 0;
      await sleep(300);
    }
    await page(`window.__ccPrev=null;0`);
  };
  const select = async (r) => {
    if (r.selected) return;
    await page(`${rows}[${r.i}].click();0`);
    for (let t = 0; t < 20; t++) {
      await sleep(150);
      if (await page(`${rows}[${r.i}]?.dataset.selected==='true'`)) return;
    }
    throw new Error('could not switch to the agent');
  };
  const inputText = () => page(`(()=>{const e=${input};return e?e.innerText.trim():null})()`);

  // Enter 在 Agent 运行时排队，Ctrl+Enter 立即送达
  const submit = async (text, now) => {
    await sleep(300);
    const cur = await inputText();
    if (cur === null) throw new Error('chat input not found; open the Agent panel in Cursor');
    if (cur) throw new Error(`input already has text, not sending so it is not overwritten: ${cur.slice(0, 60)}`);
    await page(`${input}.focus();0`);
    w.focus();
    await w.insertText(text);
    await sleep(300);
    if (!(await inputText())) throw new Error('text did not reach the input');
    const modifiers = now ? ['control'] : [];
    w.sendInputEvent({ type: 'keyDown', keyCode: 'Enter', modifiers });
    w.sendInputEvent({ type: 'keyUp', keyCode: 'Enter', modifiers });
    for (let t = 0; t < 20; t++) {
      await sleep(150);
      if (!(await inputText())) return;
    }
    throw new Error('input did not clear after Enter; the message may not be sent and is still in the input');
  };

  switch (a.cmd) {
    case 'agents':
      return JSON.stringify(await list());
    case 'read': {
      const r = await resolve();
      await remember();
      await select(r);
      let s = '';
      for (let t = 0; t < 20 && !s; t++) {
        await sleep(200);
        s = await page(`(()=>{const e=[...document.querySelectorAll('.composer-messages-container')].find(e=>e.offsetParent);return e?e.innerText:''})()`);
      }
      await restore();
      return JSON.stringify({ ...r, text: s.slice(-a.tail) });
    }
    case 'send': {
      const r = await resolve();
      if (r.busy && !a.mode) throw new Error(`agent ${r.i} is still running, not sent; pass mode=queue to send after it finishes or mode=now to deliver it right away`);
      await remember();
      await select(r);
      try { await submit(a.text, a.mode === 'now'); } finally { await restore(); }
      return JSON.stringify(r);
    }
    case 'new': {
      await remember();
      const ok = await page(`(()=>{const e=[...document.querySelectorAll('.agent-sidebar-cell')].find(e=>e.offsetParent&&e.innerText.trim().startsWith('New Agent'));if(!e)return false;e.click();return true})()`);
      if (!ok) throw new Error('New Agent button not found');
      await sleep(800);
      try { await submit(a.text); await sleep(800); } finally { await restore(); }
      await sleep(300);
      return JSON.stringify(await list());
    }
    default:
      throw new Error(`unknown command ${a.cmd}`);
  }
})
