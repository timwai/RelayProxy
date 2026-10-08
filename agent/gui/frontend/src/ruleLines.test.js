import test from 'node:test';
import assert from 'node:assert/strict';
import {dropTargetIndex,moveRule,normalizeRuleLists,protocolChoice,protocolsFromChoice,ruleListText,splitRuleList} from './ruleLines.js';

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

test('rule lists accept commas and semicolons (ASCII and full-width) as separators', () => {
  assert.deepEqual(splitRuleList('chrome.exe, firefox.exe;edge.exe，msedge.exe；ie.exe\nopera.exe'),
    ['chrome.exe', 'firefox.exe', 'edge.exe', 'msedge.exe', 'ie.exe', 'opera.exe']);
  assert.deepEqual(splitRuleList(' 80 ,, 443 ; ; \n'), ['80', '443']);
  assert.deepEqual(splitRuleList('10.0.0.0/8, example.com'), ['10.0.0.0/8', 'example.com'], 'CIDR slashes survive');
  const saved = normalizeRuleLists({processes: 'a.exe, b.exe', targets: '', ports: ['80, 443'], protocols: ['tcp']});
  assert.deepEqual(saved.processes, ['a.exe', 'b.exe']);
  assert.deepEqual(saved.ports, ['80', '443'], 'arrays containing separators are split too');
  assert.deepEqual(saved.targets, []);
});

test('protocol dropdown maps to and from the tcp/udp list', () => {
  assert.equal(protocolChoice(['tcp', 'udp']), 'both');
  assert.equal(protocolChoice([]), 'both', 'empty means both for the routing engine');
  assert.equal(protocolChoice(undefined), 'both');
  assert.equal(protocolChoice(['TCP']), 'tcp');
  assert.equal(protocolChoice('udp'), 'udp', 'legacy string drafts still resolve');
  assert.deepEqual(protocolsFromChoice('both'), ['tcp', 'udp']);
  assert.deepEqual(protocolsFromChoice('tcp'), ['tcp']);
  assert.deepEqual(protocolsFromChoice('udp'), ['udp']);
  for (const choice of ['both', 'tcp', 'udp']) assert.equal(protocolChoice(protocolsFromChoice(choice)), choice);
});

test('drag reordering moves a rule without losing or duplicating entries', () => {
  const rules = ['a', 'b', 'c', 'd'];
  assert.deepEqual(moveRule(rules, 0, 2), ['b', 'c', 'a', 'd']);
  assert.deepEqual(moveRule(rules, 3, 0), ['d', 'a', 'b', 'c']);
  assert.deepEqual(moveRule(rules, 1, 1), rules);
  assert.notStrictEqual(moveRule(rules, 1, 1), rules, 'always returns a copy');
  assert.deepEqual(moveRule(rules, 9, 0), rules, 'out-of-range indexes are ignored');
  assert.deepEqual(rules, ['a', 'b', 'c', 'd'], 'input is not mutated');
});

test('drop position maps to the slot the user pointed at', () => {
  // dragging row 0 and dropping on the lower half of row 2 lands after c
  assert.deepEqual(moveRule(['a', 'b', 'c', 'd'], 0, dropTargetIndex(0, 2, true)), ['b', 'c', 'a', 'd']);
  // upper half of row 2 lands before c
  assert.deepEqual(moveRule(['a', 'b', 'c', 'd'], 0, dropTargetIndex(0, 2, false)), ['b', 'a', 'c', 'd']);
  // dragging upwards: row 3 onto the upper half of row 1 lands before b
  assert.deepEqual(moveRule(['a', 'b', 'c', 'd'], 3, dropTargetIndex(3, 1, false)), ['a', 'd', 'b', 'c']);
  // lower half of row 1 lands after b
  assert.deepEqual(moveRule(['a', 'b', 'c', 'd'], 3, dropTargetIndex(3, 1, true)), ['a', 'b', 'd', 'c']);
  // dropping a row onto itself is a no-op either way
  assert.deepEqual(moveRule(['a', 'b', 'c'], 1, dropTargetIndex(1, 1, true)), ['a', 'b', 'c']);
  assert.deepEqual(moveRule(['a', 'b', 'c'], 1, dropTargetIndex(1, 1, false)), ['a', 'b', 'c']);
});
