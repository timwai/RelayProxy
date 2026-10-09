// Normalize fields actually returned by /sessions/active and /p2p/sessions.
// Keep these independent of React so they can be tested against legacy wire shapes.
export const pathNames={public_direct_quic:'Public Direct QUIC',p2p_quic:'P2P QUIC',relay_quic:'Relay QUIC',relay_tls:'Relay TLS'};
export function summarizeSession(session, devices=[]) {
  const findName=id=>devices.find(d=>d.id===id)?.name || id || '—';
  const peer=session.peerDiagnostics?.payload?.status || {};
  const relay=session.tunnelDiagnostics?.quic;
  const remoteRelay=peer.tunnelDiagnostics?.quic;
  const directQuic=peer.directQuic || {};
  return {
    client:session.clientDeviceName||findName(session.clientDeviceId),
    clientId:session.clientDeviceId||'',
    role:session.mode||'—',
    exit:session.exitDeviceId?findName(session.exitDeviceId):'未指定',
    transport:session.transport||'—',
    streams:session.activeStreams??0,
    bytesUp:session.bytesUp||0,
    bytesDown:session.bytesDown||0,
    relayRttMs:relay?.smoothed_rtt_ms,
    relayLost:relay?.sent_packets_lost,
    remoteRelayRttMs:remoteRelay?.smoothed_rtt_ms,
    remoteRelayLost:remoteRelay?.sent_packets_lost,
    directPath:peer.directPath ? pathNames[peer.directPath]||peer.directPath : '',
    directState:peer.directState||'',
    directRttMs:peer.directRttMs||0,
    directEndpoint:peer.directEndpoint||'',
    fallbackCount:peer.directFallbackCount||0,
    directBytesUp:peer.directBytesUp||0,
    directBytesDown:peer.directBytesDown||0,
    directError:peer.directError||'',
    directSendBps:directQuic.send_bps||0,
    directReceiveBps:directQuic.receive_bps||0,
    directPacketLossPct:directQuic.sent_packet_loss_pct||0,
    directRttDeviationMs:directQuic.rtt_deviation_ms||0,
    directGso:directQuic.gso||false,
    directUdpReadBufferBytes:directQuic.udp_read_buffer_bytes||0,
    directUdpWriteBufferBytes:directQuic.udp_write_buffer_bytes||0
  };
}
export function summarizePathReport(r) {
  if(!r)return {name:'等待报告',detail:'',reason:''};
  const detail=[];
  if(r.rttMs>0)detail.push('RTT '+r.rttMs+' ms');
  if(r.bytesUp>0||r.bytesDown>0)detail.push('↑ '+r.bytesUp+' B · ↓ '+r.bytesDown+' B');
  if(r.fallbackCount>0)detail.push('回退 '+r.fallbackCount+' 次');
  if(r.candidateSummary)detail.push(r.candidateSummary);
  return {name:pathNames[r.path]||r.path||(r.reason?'已降级':'等待报告'),detail:detail.join(' · '),reason:r.reason||''};
}