// Keep multiline rule fields as raw strings while the editor is open.
// Normalizing on every keystroke removes empty lines and makes Enter unusable.
export const RULE_LIST_KEYS = ['processes', 'targets', 'ports', 'protocols'];

export function ruleListText(value) {
  return Array.isArray(value) ? value.join('\n') : String(value ?? '');
}

export function normalizeRuleLists(rule) {
  const result = {...rule};
  for (const key of RULE_LIST_KEYS) {
    result[key] = ruleListText(rule[key]).split(/\r?\n/).map(line => line.trim()).filter(Boolean);
  }
  return result;
}
