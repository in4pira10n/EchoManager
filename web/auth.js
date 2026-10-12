(function () {
  function $(id) { return document.getElementById(id); }

  function preventZoom(event) { event.preventDefault(); }
  ["gesturestart", "gesturechange", "gestureend"].forEach((name) => {
    document.addEventListener(name, preventZoom, { passive: false });
  });
  document.addEventListener("touchmove", (event) => {
    if (event.touches && event.touches.length > 1) event.preventDefault();
  }, { passive: false });
  document.addEventListener("dblclick", preventZoom, { passive: false });

  const page = document.body.dataset.authPage;
  const submit = $("auth-submit");
  const error = $("auth-error");
  const modal = $("admin-auth-modal");
  const modalError = $("admin-auth-error");
  const adminUsername = $("admin-username");
  const adminPassword = $("admin-password");
  const confirmButton = $("admin-auth-confirm");

  function registrationFieldsAreValid() {
    const username = $("username").value.trim();
    const password = $("password").value;
    const confirmation = $("confirm-password").value;
    if (!username || !password || !confirmation) {
      error.textContent = "请完整填写注册信息。";
      return false;
    }
    if (password !== confirmation) {
      error.textContent = "两次输入的密码不一致。";
      return false;
    }
    error.textContent = "";
    return true;
  }

  function setModalOpen(open) {
    if (!modal) return;
    modal.hidden = !open;
    document.body.classList.toggle("dialog-open", open);
    if (open) {
      modalError.textContent = "";
    }
  }

  async function requestAuth() {
    const isRegister = page === "register";
    const payload = {
      username: $("username").value.trim(),
      password: $("password").value
    };
    if (isRegister) {
      payload.confirm_password = $("confirm-password").value;
      payload.admin_username = adminUsername.value.trim();
      payload.admin_password = adminPassword.value;
      if (!payload.admin_username || !payload.admin_password) {
        modalError.textContent = "请输入现有管理员用户名和密码。";
        return;
      }
    }

    submit.disabled = true;
    if (confirmButton) confirmButton.disabled = true;
    error.textContent = "";
    if (modalError) modalError.textContent = "";

    try {
      const response = await fetch(`/api/auth/${page}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify(payload)
      });
      const result = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(result.error || "请求失败。");
      location.assign(result.redirect || "/login");
    } catch (requestError) {
      if (isRegister) modalError.textContent = requestError.message || "网络错误。";
      else error.textContent = requestError.message || "网络错误。";
      submit.disabled = false;
      if (confirmButton) confirmButton.disabled = false;
    }
  }

  function begin() {
    if (page !== "register") {
      requestAuth();
      return;
    }
    if (!registrationFieldsAreValid()) return;
    setModalOpen(true);
  }

  submit.addEventListener("click", begin);
  if (confirmButton) confirmButton.addEventListener("click", requestAuth);
  const cancelButton = $("admin-auth-cancel");
  if (cancelButton) cancelButton.addEventListener("click", () => setModalOpen(false));
  if (modal) {
    modal.addEventListener("click", (event) => {
      if (event.target === modal) setModalOpen(false);
    });
  }
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && modal && !modal.hidden) {
      setModalOpen(false);
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      if (modal && !modal.hidden) requestAuth();
      else begin();
    }
  });
})();
