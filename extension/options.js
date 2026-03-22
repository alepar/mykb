const serverInput = document.getElementById("server");
const saveButton = document.getElementById("save");
const statusDiv = document.getElementById("status");

browser.storage.local.get("server").then((result) => {
  serverInput.value = result.server || "http://localhost:9091";
});

saveButton.addEventListener("click", () => {
  const server = serverInput.value.replace(/\/+$/, "");
  browser.storage.local.set({ server }).then(() => {
    statusDiv.textContent = "Saved!";
    setTimeout(() => { statusDiv.textContent = ""; }, 2000);
  });
});
