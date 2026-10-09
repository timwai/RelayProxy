import React from 'react';

/** Group the display name and stable identifier, never join them inline. */
export function DeviceLabel({name, id}) {
  const displayName = String(name || '').trim();
  const displayId = String(id || '').trim();
  const primary = displayName || displayId || '未命名设备';
  const secondary = displayName && displayId && displayId !== displayName ? displayId : '';
  return React.createElement('span', {className:'device-label'},
    React.createElement('strong', {className:'device-label__name'}, primary),
    secondary ? React.createElement('small', {className:'device-label__id mono', title:displayId}, secondary) : null
  );
}
