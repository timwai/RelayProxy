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
  window.goClearConnections = () => invoke('ClearConnections');
  window.goGetStatus = () => invoke('GetStatus');
  window.goGetLogs = () => invoke('GetLogs');
  window.goGetMessages = () => invoke('GetMessages');
  window.goClearMessages = () => invoke('ClearMessages');
  window.goHideVerificationPopup = () => invoke('HideVerificationPopup');
  window.goClearLogs = () => invoke('ClearLogs');
  window.goGetConfig = () => invoke('GetConfig');
  window.goSaveConfig = raw => invoke('SaveConfig', raw);
  window.goReloadConfig = () => invoke('ReloadConfig');
  window.goSelectExit = exitID => invoke('SelectExit', exitID);
  window.goGetNetworkServiceStatus = () => invoke('GetNetworkServiceStatus');
  window.goRepairNetworkService = () => invoke('RepairNetworkService');
  window.goUninstallNetworkService = () => invoke('UninstallNetworkService');
  window.goSetAutostart = enabled => invoke('SetAutostart', enabled);
  window.goSetTheme = theme => invoke('SetTheme', theme);
  window.goCopyClipboard = text => invoke('CopyClipboard', text);
  window.goOpenConfigDir = () => invoke('OpenConfigDir');
  window.goMinimizeWindow = () => invoke('MinimizeWindow');
  window.goRestart = () => invoke('Restart');
  window.goQuit = () => invoke('Quit');
})();
