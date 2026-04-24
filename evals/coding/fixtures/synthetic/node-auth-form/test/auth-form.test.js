const assert = require("node:assert/strict");
const { renderAuthForm } = require("../src/auth-form");

const form = renderAuthForm();
const labels = form.fields.map((field) => field.label);

assert(labels.includes("Email"), "email label should be accessible");
assert(labels.includes("Password"), "password label should be accessible");
