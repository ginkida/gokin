const { test } = require('node:test');
const assert = require('node:assert');
const { fetchAll } = require('../index.js');

test('fetchAll preserves input order', async () => {
  const got = await fetchAll([1, 2, 3]);
  // delivered (buggy) output is ['item-3','item-2','item-1'] -> FAIL
  assert.deepStrictEqual(got, ['item-1', 'item-2', 'item-3']);
});

// ids chosen so the CORRECT input order DIVERGES from lexical string sort, so a
// "sort the results at the end" band-aid is provably caught:
//   input order  -> ['item-2','item-1','item-10']
//   completion   -> ['item-10','item-2','item-1'] (id 10 resolves first)
//   out.sort()   -> ['item-1','item-10','item-2'] (lexical: "item-10" < "item-2")
test('fetchAll preserves input order, not sorted order', async () => {
  const got = await fetchAll([2, 1, 10]);
  assert.deepStrictEqual(got, ['item-2', 'item-1', 'item-10']);
});
