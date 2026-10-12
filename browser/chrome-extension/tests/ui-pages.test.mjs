import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const asset=name=>readFileSync(new URL('../src/'+name,import.meta.url),'utf8');
const manifest=JSON.parse(readFileSync(new URL('../manifest.json',import.meta.url),'utf8'));

test('Chrome popup and full options page both exist, with independent scripts',()=>{
  assert.equal(manifest.action.default_popup,'src/popup.html');
  assert.equal(manifest.options_ui.page,'src/options.html');
  assert.equal(manifest.options_ui.open_in_tab,true);
  const popup=asset('popup.html'), options=asset('options.html');
  assert.match(popup,/src="\.\/popup\.js"/);
  assert.match(options,/src="\.\/options\.js"/);
  assert.match(popup,/id="quick-rules"/);
  assert.match(options,/id="pairing-rules"/);
});

test('popup only exposes safe operational shortcuts, not sensitive configuration',()=>{
  const popup=asset('popup.html'),js=asset('popup.js');
  for(const id of ['server','target','pair-site','site','local-agent-port','device',
                    'offer-rule','allow-overwrite']){
    assert.doesNotMatch(popup,new RegExp('id="'+id+'"'));
  }
  for(const id of ['quick-proxy','connect','refresh','quick-rules','open-options','message']){
    assert.match(popup,new RegExp('id="'+id+'"'));
  }
  assert.match(js,/chrome\.runtime\.openOptionsPage/);
  assert.match(js,/REQUEST_SESSION/);
  assert.match(js,/SEND_SESSION/);
  assert.match(js,/SYNC_THEN_OPEN/);
  assert.doesNotMatch(js,/CREATE_RULE|ACCEPT_RULE|ALLOW_OVERWRITE|REVOKE_RULE/);
});

test('independent options page retains every ID consumed by pairing and network UI',()=>{
  const html=asset('options.html'),js=asset('options.js');
  const used=[...js.matchAll(/\$\('([^']+)'\)/g)].map(m=>m[1]);
  const ids=[...html.matchAll(/\bid="([^"]+)"/g)].map(m=>m[1]);
  assert.equal(new Set(ids).size,ids.length,'options page IDs must remain unique');
  for(const id of new Set(used))
    assert.ok(ids.includes(id),'missing HTML control: '+id);
  for(const id of ['server','connect','local-agent-all','local-agent-port',
                    'site','sites','refresh-sites','add-site','target','pair-site',
                    'offer-rule','refresh-pairing','pairing-rules','message']){
    assert.ok(ids.includes(id),id);
  }
  assert.match(js,/chrome\.permissions\.request/);
  assert.match(js,/pairingCode: rule\.code/);
});

test('popup quick actions only apply to verified active rules and fail closed',()=>{
  const js=asset('popup.js');
  assert.match(js,/r\.status === 'active' && r\.valid && r\.policy\?\.siteOrigin/);
  assert.match(js,/rule\.lastTransfer\?\.state === 'PARTIAL'/);
  assert.match(js,/chrome\.runtime\.sendMessage/);
  assert.match(js,/currentWindow:true/);
  assert.match(js,/await refresh\(\{quiet:true\}\)/);
  assert.match(js,/sync\.disabled = paused/);
  assert.match(js,/open\.disabled = paused/);
});

test('full options navigation changes panels without removing pairing, networking, or IndexedDB consent',()=>{
  const html=asset('options.html'),code=asset('options.js'),css=asset('options.css');
  const panels=['server-section','proxy-section','sites-section','pairing-section','rules-section'];
  for(const id of panels){
    assert.match(html,new RegExp('id="'+id+'"'));
    assert.match(html,new RegExp('href="#'+id+'"'));
  }
  assert.match(code,/activateSettingsSection/);
  assert.match(code,/panel\.hidden=panel\.id!==selected/);
  assert.match(code,/hashchange/);
  assert.match(css,/\.settings-main>\.settings-section\[hidden\]/);
  assert.match(html,/IndexedDB/);
  assert.doesNotMatch(html,/A\/B|来源 A|接收 B/);
  assert.doesNotMatch(code,/来源 A|接收 B|A Agent|B Agent/);
});

test('popup displays role names rather than A/B device codes',()=>{
  const code=asset('popup.js');
  assert.match(code,/rule\.role === 'source' \? '发送端' : '接收端'/);
  assert.doesNotMatch(code,/来源 A|接收 B/);
});


test('one-time account-switch permission is accessible only through the Options UI',()=>{
  const options=asset('options.js');
  const worker=asset('service-worker.js');
  const session=asset('session-sync.js');
  assert.match(options,/允许本次覆盖现有登录数据/);
  assert.match(options,/同步成功后打开页面/);
  assert.match(options,/ALLOW_OVERWRITE.*allowed:true/);
  assert.match(options,/REQUEST_SESSION/);
  assert.match(options,/ALLOW_OVERWRITE.*allowed:false/);
  assert.doesNotMatch(options,/允许覆盖目标端 Cookie、Local\/Session Storage 与 IndexedDB（可能切换账号）/);
  assert.match(worker,/sender\.url!==chrome\.runtime\.getURL\('src\/options\.html'\)/);
  assert.match(worker,/await pruneOverwriteGrants\(\)/);
  assert.match(session,/consumeOneTimeOverwrite\(ruleId,ctx\.policy\.siteOrigin,envelope\.transferRequestId\)/);
  assert.match(options,/transferRequestId:requestId/);
  assert.match(worker,/sender\.url!==chrome\.runtime\.getURL\('src\/options\.html'\)/);
  assert.match(session,/encryptSnapshot\(ctx\.rule,ctx\.remote,sequence,payload,transferRequestId\)/);
  assert.match(session,/sourceRequested\.set\(ruleId,transferRequestId\)/);
  assert.match(session,/delete stored\[ruleId\]/);
  assert.match(session,/validOverwriteGrant\(candidate,siteOrigin,Date\.now\(\),requestId\)/);
  assert.doesNotMatch(session,/switches\[ruleId\]===true/);
});

test('MV3 alarm reconnect is not throttled by periodic session refresh',()=>{
  const worker=asset('service-worker.js'),session=asset('session-sync.js');
  assert.match(worker,/chrome\.alarms\.onAlarm/);
  assert.match(worker,/restoreActiveSubscriptions\(\)/);
  assert.match(session,/await connectBrowser\(\)/);
  assert.match(session,/shouldRetryConnection\(stored,settings\.serverOrigin,now\)/);
  assert.match(session,/nextReconnectDelay\(failures\)/);
  assert.ok(session.indexOf('await reconcilePendingDeliveries();')<
    session.indexOf('if(!force&&!shouldReconcile(last,now))return;'));
});

test('network binding invitation remains actionable while pending or revoked',()=>{
  const code=asset('options.js');
  assert.match(code,/rule\.network\?\.status==='offered'/);
  assert.match(code,/rule\.network\.status==='revoked'/);
  assert.match(code,/取消网络绑定邀请/);
  assert.match(code,/重新发起网络绑定邀请/);
  assert.match(code,/NETWORK_REVOKE/);
  assert.match(code,/NETWORK_OFFER/);
  assert.match(code,/NETWORK_EXITS/);
  assert.match(code,/已授权且在线的 Agent proxy\.exit 出口/);
  assert.doesNotMatch(code,/来源设备 Agent 已开启浏览器网络共享/);
});

test('server connection has one button and new pairing scope defaults all checked',()=>{
  const html=asset('options.html'),code=asset('options.js');
  assert.match(html,/id="connect" class="primary-button"/);
  assert.doesNotMatch(html,/id="save"|id="register"/);
  assert.ok(!code.includes("$('save')")&&!code.includes("$('register')"));
  assert.match(code,/type:'SAVE_SERVER'/);
  assert.match(code,/type:'REGISTER_BROWSER'/);
  assert.match(code,/type:'CONNECT_BROWSER'/);
  for(const id of ['sync-cookies','sync-local','sync-session','sync-indexeddb']){
    assert.match(html,new RegExp('id="'+id+'"[^>]*checked'));
  }
  assert.match(code,/dataTypes=\{allCookies:/);
  assert.match(code,/CREATE_RULE',siteOrigin,targetID,dataTypes/);
});

test('overwrite and open-after-success are one-shot checkbox options, not buttons',()=>{
  const code=asset('options.js'),popup=asset('popup.js');
  assert.match(code,/overwriteOnce\.type='checkbox'/);
  assert.match(code,/openAfter\.type='checkbox'/);
  assert.match(code,/overwriteOnce\.checked=false/);
  assert.match(code,/shouldOpen\?'SYNC_THEN_OPEN':'REQUEST_SESSION'/);
  assert.doesNotMatch(code,/const open = document\.createElement\('button'\)/);
  assert.doesNotMatch(code,/const overwrite = document\.createElement\('button'\)/);
  assert.match(popup,/openAfter\.type='checkbox'/);
  assert.doesNotMatch(popup,/el\('button','small-button outline-button','同步后打开'\)/);
});
