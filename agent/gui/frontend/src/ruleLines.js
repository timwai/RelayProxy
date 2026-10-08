// Keep multiline rule fields as raw strings while the editor is open.
// Normalizing on every keystroke removes empty lines and makes Enter unusable.
export const RULE_LIST_KEYS = ['processes', 'targets', 'ports', 'protocols'];

// Items may be separated by newlines, commas or semicolons (ASCII or full-width),
// so "chrome.exe, firefox.exe; edge.exe" and one-per-line both work.
export const RULE_LIST_SEPARATORS = /[\r\n,，;；]+/;

export function ruleListText(value) {
  return Array.isArray(value) ? value.join('\n') : String(value ?? '');
}

export function splitRuleList(value) {
  return ruleListText(value).split(RULE_LIST_SEPARATORS).map(item => item.trim()).filter(Boolean);
}

export function normalizeRuleLists(rule) {
  const result = {...rule};
  for (const key of RULE_LIST_KEYS) {
    result[key] = splitRuleList(rule[key]);
  }
  return result;
}

// The routing engine only knows tcp and udp; the editor exposes that as a
// three-way choice instead of free text.
export const PROTOCOL_CHOICES = [
  ['both', 'TCP + UDP'],
  ['tcp', '仅 TCP'],
  ['udp', '仅 UDP']
];

export function protocolChoice(protocols) {
  const set = new Set(splitRuleList(protocols).map(p => p.toLowerCase()));
  const tcp = set.has('tcp'), udp = set.has('udp');
  if (tcp && !udp) return 'tcp';
  if (udp && !tcp) return 'udp';
  return 'both';
}

export function protocolsFromChoice(choice) {
  if (choice === 'tcp') return ['tcp'];
  if (choice === 'udp') return ['udp'];
  return ['tcp', 'udp'];
}

// Move one rule to a new position; `from`/`to` are indexes into `rules` after
// removal semantics (`to` is where the item lands in the resulting array).
export function moveRule(rules, from, to) {
  const list = Array.isArray(rules) ? [...rules] : [];
  if (from < 0 || from >= list.length || to < 0 || to >= list.length || from === to) return list;
  const [item] = list.splice(from, 1);
  list.splice(to, 0, item);
  return list;
}

// Translate a drop onto row `index` (upper or lower half) into a target index
// for moveRule, accounting for the dragged row leaving its old slot.
export function dropTargetIndex(from, index, after) {
  let to = after ? index + 1 : index;
  if (from < to) to -= 1;
  return to;
}
