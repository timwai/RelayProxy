import test from 'node:test';
import assert from 'node:assert/strict';
import {cookieAccessHosts,cookieAccessOrigins} from '../src/cookie-access.js';

test('HTTPS cookie access includes HTTP while HTTP consent never grants HTTPS',()=>{
  assert.deepEqual(cookieAccessOrigins('https://10.99.32.130:30009'),
    ['https://10.99.32.130/*','http://10.99.32.130/*']);
  assert.deepEqual(cookieAccessOrigins('http://10.99.32.130:30009'),['http://10.99.32.130/*']);
  assert.deepEqual(cookieAccessHosts('https://127.0.0.1:30009'),['127.0.0.1']);
  assert.deepEqual(cookieAccessHosts('https://[::1]:30009'),['[::1]']);
});

test('parent Cookie permissions use exact ancestor hosts without sibling or bare TLD grants',()=>{
  assert.deepEqual(cookieAccessOrigins('https://app.example.com'),[
    'https://app.example.com/*','http://app.example.com/*',
    'https://example.com/*','http://example.com/*'
  ]);
  assert.deepEqual(cookieAccessHosts('https://app.example.co.uk'),
    ['app.example.co.uk','example.co.uk','co.uk']);
  assert.deepEqual(cookieAccessHosts('http://localhost:30009'),['localhost']);
  assert.throws(()=>cookieAccessOrigins('file:///tmp/site'),/HTTP\/HTTPS/);
});
