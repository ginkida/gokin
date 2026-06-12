'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { nextDelay } = require('../src/backoff');

test('grows exponentially from base', () => {
  assert.strictEqual(nextDelay(1, 100, 60000), 100);
  assert.strictEqual(nextDelay(2, 100, 60000), 200);
  assert.strictEqual(nextDelay(3, 100, 60000), 400);
  assert.strictEqual(nextDelay(4, 100, 60000), 800);
});

test('caps at the configured maximum', () => {
  assert.strictEqual(nextDelay(20, 100, 60000), 60000);
});
