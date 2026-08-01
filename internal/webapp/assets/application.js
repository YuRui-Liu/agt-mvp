"use strict";

document.addEventListener("DOMContentLoaded", () => {
  const form = document.getElementById("applicationForm");
  const status = document.getElementById("applicationStatus");
  const error = document.getElementById("applicationError");
  const button = document.getElementById("submitApplication");
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    button.disabled = true;
    error.hidden = true;
    status.textContent = "正在提交 Mock 投递…";
    try {
      const response = await fetch("/api/applications", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify({
          name: document.getElementById("candidateName").value,
          email: document.getElementById("candidateEmail").value,
          position: document.getElementById("candidatePosition").value
        })
      });
      const body = await response.json();
      if (!response.ok) throw new Error(body.message || "投递未完成。");
      status.textContent = "模拟投递已接收，完整体验闭环完成。";
      form.querySelectorAll("input").forEach((input) => { input.disabled = true; });
    } catch (cause) {
      error.textContent = cause.message;
      error.hidden = false;
      error.focus();
      status.textContent = "投递未完成，可重试。";
      button.disabled = false;
    }
  });
});
