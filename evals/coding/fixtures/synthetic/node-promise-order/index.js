// fetchOne resolves to a string after a delay INVERSELY proportional to id, so
// later ids resolve FIRST — exposing any ordering bug.
function fetchOne(id) {
  return new Promise((res) => setTimeout(() => res('item-' + id), (10 - id) * 5));
}

// fetchAll should return results IN INPUT ORDER. BUG: it pushes each result as
// it resolves, so the output order follows completion order, not input order.
async function fetchAll(ids) {
  const out = [];
  await Promise.all(
    ids.map(async (id) => {
      const r = await fetchOne(id);
      out.push(r);
    })
  );
  return out;
}

module.exports = { fetchAll, fetchOne };
