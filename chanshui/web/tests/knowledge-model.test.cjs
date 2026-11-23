const test = require('node:test');
const assert = require('node:assert/strict');
const React = require('react');
const { renderToStaticMarkup } = require('react-dom/server');
const appRequire = require('./app-require.cjs');

test('型号核对显示逐题结论且不显示公开截止日期对照', () => {
  const knowledge = {
    status: 'matched', label: '符合 GPT-6.1 Sol', reason: '4 道证明题答对 4 道，本项不计分。',
    boundary_review: { method: 'model_group', status: 'complete', reason: 'complete' },
    rows: [
      { id: 'a', event: '证明事件', happened_at: '2026-02-13', kind: 'know', ok: true, unsure: false, answer: 'C' },
      { id: 'b', event: '更新事件', happened_at: '2026-02-20', kind: 'unknown', ok: false, unsure: true, answer: 'D' },
      { id: 'c', event: '另一事件', happened_at: '2026-02-21', kind: 'wrong', ok: false, unsure: false, answer: 'A' },
    ],
  };
  const { knowledgeDateComparison } = appRequire('@/lib/knowledge-dates');
  assert.equal(knowledgeDateComparison('gpt-6.1-sol', knowledge), undefined);
  const ResultCards = appRequire('@/components/ResultCards').default;
  const html = renderToStaticMarkup(React.createElement(ResultCards, { model: 'gpt-6.1-sol', verdict: { knowledge } }));
  assert.match(html, /知识检测按型号核对/);
  assert.match(html, /符合 GPT-6.1 Sol/);
  assert.match(html, /答对/);
  assert.match(html, /不知道/);
  assert.match(html, /答错/);
  assert.doesNotMatch(html, /公开知识截止日期为/);
});

test('旧跨年报告仍显示原公开日期说明', () => {
  const { knowledgeDateComparison } = appRequire('@/lib/knowledge-dates');
  const text = knowledgeDateComparison('gpt-6.1-sol', { status: 'unknown', boundary_review: { method: 'cross_year', status: 'unconfirmed', reason: 'no_anchor' }, rows: [] });
  assert.match(text, /公开知识截止日期为 2026-04-30/);
});
