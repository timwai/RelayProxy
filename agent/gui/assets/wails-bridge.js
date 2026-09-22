(function () {
  'use strict';

  function invoke(method, ...args) {
    if (!globalThis.wails || !globalThis.wails.Call || typeof globalThis.wails.Call.ByName !== 'function') {
      return Promise.reject(new Error('Wails runtime is not ready'));
    }
    return globalThis.wails.Call.ByName('relayproxy/agent/gui.WailsService.' + method, ...args);
  }

  window.goOpenConnections = () => invoke('OpenConnections');
  window.goGetConnections = () => invoke('GetConnections');
  window.goGetStatus = () => invoke('GetStatus');
  window.goGetRemoteDesktopTargets = () => invoke('GetRemoteDesktopTargets');
  window.goConnectRemoteDesktop = (targetID, rawOptions) => invoke('ConnectRemoteDesktop', targetID, rawOptions);
  window.goDisconnectRemoteDesktop = () => invoke('DisconnectRemoteDesktop');
  window.goGetRemoteDesktopStatus = () => invoke('GetRemoteDesktopStatus');
  window.goGetRemoteDesktopFrame = () => invoke('GetRemoteDesktopFrame');
  window.goGetRemoteDesktopCursor = cursorID => invoke('GetRemoteDesktopCursor', cursorID || '');
  window.goGetRemoteDesktopClipboard = sequence => invoke('GetRemoteDesktopClipboard', sequence || 0);
  window.goSendRemoteDesktopClipboard = text => invoke('SendRemoteDesktopClipboard', text || '');
  window.goGetClipboardText = () => invoke('GetClipboardText');
  window.goSetClipboardText = text => invoke('SetClipboardText', text || '');
  window.goSendRemoteDesktopInput = raw => invoke('SendRemoteDesktopInput', raw);
  window.goRequestRemoteDesktopIDR = () => invoke('RequestRemoteDesktopIDR');
  window.goGetLogs = () => invoke('GetLogs');
  window.goClearLogs = () => invoke('ClearLogs');
  window.goGetConfig = () => invoke('GetConfig');
  window.goSaveConfig = raw => invoke('SaveConfig', raw);
  window.goReloadConfig = () => invoke('ReloadConfig');
  window.goSelectExit = exitID => invoke('SelectExit', exitID);
  window.goSetAutostart = enabled => invoke('SetAutostart', enabled);
  window.goSetTheme = theme => invoke('SetTheme', theme);
  window.goCopyClipboard = text => invoke('CopyClipboard', text);
  window.goOpenConfigDir = () => invoke('OpenConfigDir');
  window.goMinimizeWindow = () => invoke('MinimizeWindow');
  window.goRestart = () => invoke('Restart');
  window.goQuit = () => invoke('Quit');
})();
