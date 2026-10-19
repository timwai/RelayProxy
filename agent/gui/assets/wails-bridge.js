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
  window.goGetFileShares = () => invoke('GetFileShares');
  window.goGetFileShareCandidates = () => invoke('GetFileShareCandidates');
  window.goGetFileDirectStatus = () => invoke('GetFileDirectStatus');
  window.goPrewarmRemoteFileDirect = (deviceID, shareID) => invoke('PrewarmRemoteFileDirect', deviceID, shareID);
  window.goGetFileSharePeers = () => invoke('GetFileSharePeers');
  window.goGetRemoteFileShares = deviceID => invoke('GetRemoteFileShares', deviceID);
  window.goGetRemoteFileEntries = (deviceID, shareID, relativePath) => invoke('GetRemoteFileEntries', deviceID, shareID, relativePath);
  window.goMutateRemoteFile = (deviceID, shareID, action, relativePath, destination) => invoke('MutateRemoteFile', deviceID, shareID, action, relativePath, destination);
  window.goOpenFileBrowserWindow = (deviceID, shareID, path) => invoke('OpenFileBrowserWindow', deviceID, shareID, path);
  window.goBeginFileUpload = (deviceID, shareID, relativePath, size) => invoke('BeginFileUpload', deviceID, shareID, relativePath, size);
  window.goAppendFileUploadChunk = (id, chunk) => invoke('AppendFileUploadChunk', id, chunk);
  window.goCommitFileUpload = id => invoke('CommitFileUpload', id);
  window.goDiscardFileUpload = id => invoke('DiscardFileUpload', id);
  window.goStartFileDownload = (deviceID, shareID, relativePath) => invoke('StartFileDownload', deviceID, shareID, relativePath);
  window.goChooseShareFolder = () => invoke('ChooseShareFolder');
  window.goChooseAndStartFileDownload = (deviceID, shareID, relativePath) => invoke('ChooseAndStartFileDownload', deviceID, shareID, relativePath);
  window.goChooseAndResumeFileDownload = id => invoke('ChooseAndResumeFileDownload', id);
  window.goGetFileTransfers = () => invoke('GetFileTransfers');
  window.goCancelFileDownload = id => invoke('CancelFileDownload', id);
  window.goClearFileTransfer = id => invoke('ClearFileTransfer', id);
  window.goClearInactiveFileTransfers = () => invoke('ClearInactiveFileTransfers');
  window.goResumeFileDownload = id => invoke('ResumeFileDownload', id);
  window.goSaveFileShares = state => invoke('SaveFileShares', state);
  window.goGetStatus = () => invoke('GetStatus');
  window.goGetDiagnostics = () => invoke('GetDiagnostics');
  window.goRunSpeedTest = (exitID, durationSeconds) => invoke('RunSpeedTest', exitID, durationSeconds);
  window.goTestCustomExit = (exitID) => invoke('TestCustomExit', exitID);
  window.goProbeDNS = () => invoke('ProbeDNS');
  window.goGetProxyExits = () => invoke('GetProxyExits');
  window.goGetRoutingSubscriptions = () => invoke('GetRoutingSubscriptions');
  window.goGetRDPTargets = () => invoke('GetRDPTargets');
  window.goConnectRDP = (targetID, autoLaunch) => invoke('ConnectRDP', targetID, autoLaunch);
  window.goDisconnectRDP = () => invoke('DisconnectRDP');
  window.goDisconnectRDPTarget = (targetID) => invoke('DisconnectRDPTarget', targetID);
  window.goGetLogs = () => invoke('GetLogs');
  window.goGetMessages = () => invoke('GetMessages');
  window.goClearMessages = () => invoke('ClearMessages');
  window.goHideVerificationPopup = () => invoke('HideVerificationPopup');
  window.goClearLogs = () => invoke('ClearLogs');
  window.goGetConfig = () => invoke('GetConfig');
  window.goSaveConfig = raw => invoke('SaveConfig', raw);
  window.goReloadConfig = () => invoke('ReloadConfig');
  window.goSelectExit = exitID => invoke('SelectExit', exitID);
  window.goGetNetworkCapabilities = () => invoke('GetNetworkCapabilities');
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
