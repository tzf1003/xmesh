document.querySelector('[data-testid="nav-settings"]').onclick = () => {
  document.querySelector('[data-testid="settings"]').hidden = false;
};
document.querySelector('[data-testid="review"]').onclick = () =>
  document.querySelector("dialog").showModal();
document.querySelector('[data-testid="confirm-ok"]').onclick = () => {
  document.querySelector('[data-testid="applied"]').textContent =
    `Applied ${document.querySelector("input").value}`;
  document.querySelector("dialog").close();
};
