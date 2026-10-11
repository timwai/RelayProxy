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
  for(const id of ['server','save','register','connect','local-agent-all','local-agent-port',
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
  assert.match(js,/checked = !next/);
});
