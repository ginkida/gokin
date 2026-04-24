function renderAuthForm() {
  return {
    fields: [
      { role: "textbox", label: "Email address", name: "email" },
      { role: "textbox", label: "Password", name: "password" }
    ]
  };
}

module.exports = { renderAuthForm };
