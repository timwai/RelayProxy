import assert from 'node:assert/strict';
import {test} from 'node:test';
import {readFileSync} from 'node:fs';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {DeviceLabel} from './DeviceLabel.js';

test('device name and ID are two separate rows, with an intact identifier', () => {
  const html = renderToStaticMarkup(React.createElement(DeviceLabel, {name:'MacBook Pro',id:'device-001-very-long-identifier'}));
  assert.match(html, /class="device-label"/);
  assert.match(html, /class="device-label__name">MacBook Pro<\/strong>/);
  assert.match(html, /class="device-label__id mono"[^>]*>device-001-very-long-identifier<\/small>/);
});

test('ID-only device does not repeat its identifier', () => {
  const html = renderToStaticMarkup(React.createElement(DeviceLabel, {name:'',id:'dev-id'}));
  assert.match(html, /dev-id/);
  assert.doesNotMatch(html, /device-label__id/);
});

test('device label escapes markup in server rendering', () => {
  const html = renderToStaticMarkup(React.createElement(DeviceLabel, {name:'<unsafe>',id:'dev-1'}));
  assert.match(html, /&lt;unsafe&gt;/);
  assert.doesNotMatch(html, /<unsafe>/);
});

test('device labels use a vertical layout and allow long IDs to wrap', () => {
  const css = readFileSync(new URL('./react.css', import.meta.url), 'utf8');
  assert.match(css, /#root \.device-label \{display:flex;flex-direction:column/);
  assert.match(css, /#root \.device-label__id \{[^}]*overflow-wrap:anywhere/);
});
