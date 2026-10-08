import test from 'node:test';
import assert from 'node:assert/strict';
import {normalizeRuleLists,ruleListText} from './ruleLines.js';

test('routing rule textarea preserves trailing and repeated newlines while editing', () => {
  const draft = {
    name: 'Multiline',
    processes: 'chrome.exe\n',
    targets: 'example.com\n\nexample.org',
    ports: '443\n',
    protocols: 'tcp\nudp\n'
  };
  assert.equal(ruleListText(draft.processes), 'chrome.exe\n');
  assert.equal(ruleListText(draft.targets), 'example.com\n\nexample.org');

  const saved = normalizeRuleLists(draft);
  assert.deepEqual(saved.processes, ['chrome.exe']);
  assert.deepEqual(saved.targets, ['example.com', 'example.org']);
  assert.deepEqual(saved.ports, ['443']);
  assert.deepEqual(saved.protocols, ['tcp', 'udp']);
  assert.equal(draft.processes, 'chrome.exe\n', 'normalizing must not modify the open draft');
});

test('routing rule lists loaded as arrays remain arrays after saving', () => {
  const rule = {name: 'Existing', processes: ['a.exe', 'b.exe'], targets: [], ports: ['80'], protocols: ['tcp']};
  assert.equal(ruleListText(rule.processes), 'a.exe\nb.exe');
  assert.equal(ruleListText(rule.targets), '');
  assert.deepEqual(normalizeRuleLists(rule), rule);
  assert.notStrictEqual(normalizeRuleLists(rule), rule);
});
