// All path measurements in this module are scoped to the EXACT speed-test
// exit ID. Never attribute global Agent status.directPath to a peer device.
export function classifyDeviceTestPath(value){
 const raw=String(value||'').toLowerCase().replace(/[ -]/g,'_');
 if(/public[_]?direct|direct_(quic|tls|tcp)/.test(raw))return 'public';
 if(/p2p|hole[_]?punch/.test(raw))return 'p2p';
 if(/relay|^quic$|^tcp$/.test(raw))return 'relay';
 return 'unknown';
}

export function deviceSpeedLinkEvidence(exitID,exit,result){
 const publicInfo=exit?.direct?.public||{};
 const endpoints=Array.isArray(publicInfo.endpoints)?publicInfo.endpoints:[];
 const verifiedEndpoints=endpoints.filter(x=>x&&typeof x==='object'&&x.verified===true);
 const verifiedTest=!!exitID&&!!result&&!result.error&&result.exitId===exitID;
 const upload=verifiedTest?(result.upload||result.up||{}):{};
 const download=verifiedTest?(result.download||result.down||{}):{};
 const paths=verifiedTest?[upload.path,download.path].filter(Boolean):[];
 const kinds=new Set(paths.map(classifyDeviceTestPath));
 return {
  verifiedTest,
  paths,
  uploadPath:upload.path||'',
  downloadPath:download.path||'',
  quicUpload:upload.quic||null,
  quicDownload:download.quic||null,
  usedPublic:kinds.has('public'),
  usedP2P:kinds.has('p2p'),
  usedRelay:kinds.has('relay'),
  publicAdvertised:publicInfo.available===true,
  verifiedEndpointCount:verifiedEndpoints.length,
 };
}
