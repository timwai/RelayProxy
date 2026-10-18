// Traffic snapshots retain stable device IDs. Resolve names only at render time,
// so existing records reflect device renames without rewriting audit data.
export function connectionPathLabel(connection = {}, exits = []) {
  const path = String(connection?.path || '').trim();
  const action = String(connection?.action || '').toUpperCase();
  const exitID = String(connection?.exit_id || connection?.exitId || '').trim();
  if (action !== 'PROXY' || !exitID) return path || action || '—';

  const exit = (Array.isArray(exits) ? exits : []).find(item =>
    String(item?.deviceId || item?.id || '') === exitID);
  const name = String(exit?.name || exit?.deviceName || '').trim() || exitID;
  // Older traffic formats can already contain "PROXY · <device ID>".
  if (path.includes(exitID)) return path.split(exitID).join(name);
  return (path && path.toUpperCase() !== 'PROXY' ? path : 'PROXY') + ' · ' + name;
}
