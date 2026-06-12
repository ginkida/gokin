'use strict';

// nextDelay returns the retry delay in ms for the given attempt (1-based):
// exponential growth from `base`, capped at `cap`.
function nextDelay(attempt, base, cap) {
  const delay = base * attempt;
  return Math.min(delay, cap);
}

module.exports = { nextDelay };
